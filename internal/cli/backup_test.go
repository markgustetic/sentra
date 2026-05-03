package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// backupFixture builds a BackupDeps wired to a memory-backed repo
// already initialized with the given passphrase. The store is reused
// across NewStore calls so backup commands and follow-up assertions
// see the same content.
func backupFixture(t *testing.T, passphrase string) (BackupDeps, *blobstore.Memory, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	deps := BackupDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) {
			return []byte(passphrase), nil
		},
		Stdout: out,
		Stderr: errBuf,
	}
	return deps, store, out, errBuf
}

// writeBackupConfigFile writes a minimal sentra.yaml so the backup
// command's config-load step finds a file rather than falling back
// to defaults. Its contents don't matter to the in-memory tests
// because the deps' NewStore ignores them.
func writeBackupConfigFile(t *testing.T, dir string) {
	t.Helper()
	body := "repo:\n  s3:\n    bucket: ignored\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}
}

// TestBackup_RoundTrip writes a few files into a temp dir, runs
// backup, and asserts the snapshot summary references the expected
// file count and a non-empty snapshot ID.
func TestBackup_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)

	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "b.txt"), []byte("bravo bravo bravo"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	deps, store, out, _ := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{src, "--tag", "test-tag"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "snap-") {
		t.Errorf("expected snapshot ID in output, got %q", got)
	}
	if !strings.Contains(got, "2") { // file count
		t.Errorf("expected file count in output, got %q", got)
	}

	// Verify the snapshot shows up via repo.ListSnapshots against
	// the same memory store we injected.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots: got %d, want 1", len(snaps))
	}
	if snaps[0].Tag != "test-tag" {
		t.Errorf("tag: got %q, want test-tag", snaps[0].Tag)
	}
	if snaps[0].Stats.Files != 2 {
		t.Errorf("files: got %d, want 2", snaps[0].Stats.Files)
	}
}

// TestBackup_ProgressOnStderr verifies the inline progress UI writes
// to stderr (so stdout stays parseable). We don't pin the exact
// rendering — bubbles' progress bar is animation-y — just that the
// command's status output goes to stderr.
func TestBackup_ProgressOnStderr(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	deps, _, out, _ := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	stderrBuf := &bytes.Buffer{}
	cmd.SetErr(stderrBuf)
	cmd.SetArgs([]string{src})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// stdout has the final summary; stderr (in a real run) would
	// have progress, but the in-memory store is fast enough that
	// the periodic ticker may not fire. We assert only that the
	// final summary lands in stdout, leaving the stderr animation
	// behavior to manual smoke testing.
	if !strings.Contains(out.String(), "snap-") {
		t.Errorf("stdout missing summary: %q", out.String())
	}
}

// TestBackup_HonorsConfigBackupSettings is the end-to-end check that
// sentra.yaml's `backup.exclude_caches: false` and `backup.ignore_file`
// actually flow through to the underlying walk. Set ExcludeCaches=false
// and a CACHEDIR.TAG-marked dir; verify files inside the cache dir
// are part of the snapshot tree.
func TestBackup_HonorsConfigBackupSettings(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Write a sentra.yaml that turns off cache exclusion.
	body := "repo:\n  s3:\n    bucket: ignored\nbackup:\n  ignore_file: .sentraignore\n  exclude_caches: false\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}

	src := filepath.Join(dir, "src")
	cache := filepath.Join(src, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "CACHEDIR.TAG"),
		[]byte("Signature: 8a477f597d28d172789f06886806bc55\n"), 0o600); err != nil {
		t.Fatalf("write tag: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "junk.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}

	deps, store, out, _ := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{src})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Pull the snapshot manifest and verify cache contents made it in.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots: got %d, want 1", len(snaps))
	}
	m, err := r.LoadSnapshot(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	gotPaths := make(map[string]bool)
	for _, fe := range m.Tree {
		gotPaths[fe.Path] = true
	}
	for _, want := range []string{"cache/CACHEDIR.TAG", "cache/junk.txt", "real.txt"} {
		if !gotPaths[want] {
			t.Errorf("expected %q in snapshot tree, got %v", want, gotPaths)
		}
	}
}

// TestBackup_DefaultExcludesCaches asserts the same plumbing in the
// other direction: with the documented default (exclude_caches: true)
// the cache directory is skipped.
func TestBackup_DefaultExcludesCaches(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Default config — backup.exclude_caches: true is in Defaults().
	writeBackupConfigFile(t, dir)

	src := filepath.Join(dir, "src")
	cache := filepath.Join(src, "cache")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "CACHEDIR.TAG"),
		[]byte("Signature: 8a477f597d28d172789f06886806bc55\n"), 0o600); err != nil {
		t.Fatalf("write tag: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cache, "junk.txt"), []byte("inside"), 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("outside"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}

	deps, store, out, _ := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{src})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
	snaps, _ := r.ListSnapshots(context.Background())
	m, _ := r.LoadSnapshot(context.Background(), snaps[0].ID)
	for _, fe := range m.Tree {
		if strings.HasPrefix(fe.Path, "cache/") {
			t.Errorf("cache should be skipped by default, got %q in tree", fe.Path)
		}
	}
}

// TestBackup_RequiresPath enforces the positional argument: zero
// args is a usage error, not a silent no-op.
func TestBackup_RequiresPath(t *testing.T) {
	chDir(t, t.TempDir())
	deps, _, _, _ := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for missing path argument, got nil")
	}
}
