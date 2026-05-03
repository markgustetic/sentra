package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// initFixture builds an InitDeps that uses a fresh in-memory store
// and a static passphrase. The store is shared across the deps so
// tests can inspect it after the command runs.
func initFixture(t *testing.T, passphrase string) (InitDeps, *blobstore.Memory, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	out := &bytes.Buffer{}
	deps := InitDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) {
			return []byte(passphrase), nil
		},
		Stdout: out,
	}
	return deps, store, out
}

// chDir cds the test process into dir and restores the previous wd
// on cleanup. The init command works against the current directory,
// so tests need a stable cwd.
func chDir(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// TestInit_FreshDir creates sentra.yaml and the encrypted config blob
// in the injected store. After running, Open(memory, passphrase)
// should succeed.
func TestInit_FreshDir(t *testing.T) {
	chDir(t, t.TempDir())
	deps, store, _ := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// sentra.yaml must exist locally.
	if _, err := os.Stat("sentra.yaml"); err != nil {
		t.Fatalf("sentra.yaml not created: %v", err)
	}

	// The config blob must exist in the in-memory store, and it must
	// open with the passphrase we injected.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer r.Close()
}

// TestInit_RefusesExisting refuses to clobber a pre-existing
// sentra.yaml without --force. The on-disk repo state would be
// orphaned by re-init, so the safety guard is critical.
func TestInit_RefusesExisting(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte("repo: {}\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	deps, _, _ := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	errBuf := &bytes.Buffer{}
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on existing sentra.yaml, got nil")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "exists") && !strings.Contains(msg, "force") {
		t.Errorf("expected refusal mentioning exists/force, got %v", err)
	}
}

// TestInit_ForceOverwrites with --force replaces an existing
// sentra.yaml *and* re-bootstraps the repo. After force-init with a
// new passphrase, only the new passphrase should open the repo.
func TestInit_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte("# stale\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	deps, store, _ := initFixture(t, "newpass")

	cmd := NewInit(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Old passphrase must NOT open the new repo.
	if _, err := repo.Open(context.Background(), store, []byte("oldpass")); err == nil {
		t.Fatal("expected wrong-passphrase error opening with stale pass")
	} else if !errors.Is(err, repo.ErrWrongPassphrase) {
		t.Fatalf("expected ErrWrongPassphrase, got %v", err)
	}
	// New passphrase opens.
	r, err := repo.Open(context.Background(), store, []byte("newpass"))
	if err != nil {
		t.Fatalf("Open with new pass: %v", err)
	}
	r.Close()
}

// TestInit_PrintsSummary asserts the user gets some confirmation
// output. The exact wording is loose; the important thing is the
// run isn't silent.
func TestInit_PrintsSummary(t *testing.T) {
	chDir(t, t.TempDir())
	deps, _, out := initFixture(t, "hunter2")

	cmd := NewInit(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "init") &&
		!strings.Contains(strings.ToLower(got), "sentra.yaml") {
		t.Errorf("expected init summary in output, got %q", got)
	}
}

// TestInit_RegisteredOnRoot verifies the command shows up under the
// root command's children (so users see it in `sentra --help`).
func TestInit_RegisteredOnRoot(t *testing.T) {
	deps, _, _ := initFixture(t, "x")
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewInit(deps))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "init" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("init command not registered on root")
	}
}
