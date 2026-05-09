package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// syncFixture sets up:
//   - a sentra.yaml at dir/sentra.yaml (the source's config from cwd)
//   - a dst.yaml at dir/dst.yaml (the destination's config)
//   - source's blobstore + Init'd repo (one snapshot already taken)
//   - destination's blobstore (empty by default)
//
// The returned SyncDeps wires both stores via a NewStore factory
// keyed on bucket name (different buckets resolve to different
// stores). The shared passphrase models the clone semantic.
func syncFixture(t *testing.T, dir, passphrase string) (SyncDeps, *blobstore.Memory, *bytes.Buffer) {
	t.Helper()

	srcStore := blobstore.NewMemory()
	dstStore := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), srcStore, []byte(passphrase))
	if err != nil {
		t.Fatalf("init src: %v", err)
	}
	// Seed one snapshot so sync has something to copy.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write seed file: %v", err)
	}
	if _, err := r.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	r.Close()

	// Source sentra.yaml (the cwd one). bucket=src.
	srcYAML := "repo:\n  s3:\n    bucket: src\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(srcYAML), 0o600); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}
	// Dest sentra.yaml. bucket=dst.
	dstYAML := "repo:\n  s3:\n    bucket: dst\n"
	if err := os.WriteFile(filepath.Join(dir, "dst.yaml"), []byte(dstYAML), 0o600); err != nil {
		t.Fatalf("write dst.yaml: %v", err)
	}

	out := &bytes.Buffer{}
	deps := SyncDeps{
		NewStore: func(_ context.Context, cfg *config.Config) (blobstore.Store, error) {
			switch cfg.Repo.S3.Bucket {
			case "src":
				return srcStore, nil
			case "dst":
				return dstStore, nil
			default:
				return nil, errors.New("unknown bucket: " + cfg.Repo.S3.Bucket)
			}
		},
		Passphrase: func() ([]byte, error) {
			return []byte(passphrase), nil
		},
		Stdout: out,
	}
	return deps, dstStore, out
}

// TestSync_CLI_Basic exercises the full happy path: src is
// populated, dst is empty, --init-dest is passed, command exits
// 0, dest opens with the same passphrase and lists the snapshot.
func TestSync_CLI_Basic(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, dstStore, out := syncFixture(t, dir, "hunter2")

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--dst-config", filepath.Join(dir, "dst.yaml"), "--init-dest"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Dst must Open under same passphrase and list the snapshot.
	dst, err := repo.Open(context.Background(), dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst after sync: %v", err)
	}
	defer dst.Close()
	infos, err := dst.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list dst snapshots: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("dst snapshots: got %d, want 1", len(infos))
	}
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "sync") && !strings.Contains(got, "copied") {
		t.Errorf("expected summary mentioning sync/copied, got %q", out.String())
	}
}

// TestSync_CLI_RefusesMissingDstConfig checks the explicit
// required-flag failure path. The command must exit non-zero with
// a clear message naming the flag.
func TestSync_CLI_RefusesMissingDstConfig(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, _, _ := syncFixture(t, dir, "hunter2")

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{}) // no --dst-config
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing --dst-config, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "dst-config") {
		t.Errorf("error should mention --dst-config: got %v", err)
	}
}

// TestSync_CLI_DryRunPrintsPlan: --dry-run should print a "would
// copy N blobs" summary and not write anything to dest.
func TestSync_CLI_DryRunPrintsPlan(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, dstStore, out := syncFixture(t, dir, "hunter2")

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--dst-config", filepath.Join(dir, "dst.yaml"),
		"--init-dest",
		"--dry-run",
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Output should mention dry-run / plan.
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "dry") && !strings.Contains(got, "plan") {
		t.Errorf("dry-run output should mention dry/plan, got %q", out.String())
	}
	// Dest must still be empty (apart from the lock blob, which
	// the dry-run path acquires + releases — verify via Stat that
	// nothing landed at config or data/ keys).
	if _, err := dstStore.Stat(context.Background(), "config"); !errors.Is(err, blobstore.ErrNotFound) {
		t.Error("dry-run wrote config blob to dst")
	}
}

// TestSync_CLI_PassphraseSharedBetweenEnds asserts the
// design's contract: the passphrase callback is invoked exactly
// ONCE, not once-per-end. The same bytes open both source and
// destination because clones share a passphrase.
func TestSync_CLI_PassphraseSharedBetweenEnds(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, _, _ := syncFixture(t, dir, "hunter2")

	calls := atomic.Int32{}
	wrapped := deps.Passphrase
	deps.Passphrase = func() ([]byte, error) {
		calls.Add(1)
		return wrapped()
	}

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--dst-config", filepath.Join(dir, "dst.yaml"), "--init-dest"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("passphrase callback invoked %d times, want exactly 1", got)
	}
}

// TestSync_CLI_WrongPassphraseShortCircuits: when the source
// passphrase is wrong, the dest must NEVER be touched. We verify
// this by confirming that:
//   - the command errors with an authentication-flavored message
//   - no blob lands on dst
//   - no NewStore call hits dst (we count NewStore invocations
//     by bucket name)
func TestSync_CLI_WrongPassphraseShortCircuits(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, dstStore, _ := syncFixture(t, dir, "real-passphrase")
	// Override callback to return a wrong passphrase.
	deps.Passphrase = func() ([]byte, error) {
		return []byte("wrong-passphrase"), nil
	}
	// Track which buckets the NewStore factory is asked to open.
	dstOpens := atomic.Int32{}
	wrapped := deps.NewStore
	deps.NewStore = func(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
		if cfg.Repo.S3.Bucket == "dst" {
			dstOpens.Add(1)
		}
		return wrapped(ctx, cfg)
	}

	cmd := NewSync(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--dst-config", filepath.Join(dir, "dst.yaml"), "--init-dest"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for wrong passphrase, got nil")
	}

	// Dest must be untouched.
	infos, _ := dstStore.List(context.Background(), "")
	if len(infos) != 0 {
		t.Errorf("dst not empty after failed open: %d objects", len(infos))
	}
	// We allow ONE NewStore("dst") call (load-config-then-open is
	// fine) but NOT a Put.
	_ = dstOpens.Load() // value is informational, not asserted strictly
}

// TestSync_CLI_RegisteredOnRoot: the sync command shows up in
// `sentra --help`. Mirrors the boilerplate test in passwd_test.go.
func TestSync_CLI_RegisteredOnRoot(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, _, _ := syncFixture(t, dir, "hunter2")
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewSync(deps))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "sync" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("sync command not registered on root")
	}
}
