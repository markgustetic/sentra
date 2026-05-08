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

// passwdFixture sets up an Init'd repo at `dir` with the given old
// passphrase, then returns a PasswdDeps wired to the same store, an
// out buffer, and the store handle for post-run inspection.
//
// Tests stage the repo (via direct repo.Init against the in-memory
// store) before invoking the CLI command — that way the CLI's own
// Open path is what we exercise, not Init.
func passwdFixture(t *testing.T, dir string, oldPass, newPass string) (PasswdDeps, *blobstore.Memory, *bytes.Buffer) {
	t.Helper()

	// Stage a sentra.yaml so config.Load doesn't error.
	yaml := "repo:\n  s3:\n    bucket: test-bucket\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(oldPass))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	r.Close()

	out := &bytes.Buffer{}
	deps := PasswdDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) {
			return []byte(oldPass), nil
		},
		NewPassphrase: func(_ string) ([]byte, error) {
			return []byte(newPass), nil
		},
		Stdout: out,
	}
	return deps, store, out
}

// TestPasswd_CLI_Basic is the end-to-end happy path: stub callbacks
// for old + new, run the command, assert the rotation actually took
// effect (old passphrase no longer Opens; new one does).
func TestPasswd_CLI_Basic(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, store, out := passwdFixture(t, dir, "old-pass-123", "new-pass-456")

	cmd := NewPasswd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Old must NOT Open.
	if _, err := repo.Open(context.Background(), store, []byte("old-pass-123")); !errors.Is(err, repo.ErrWrongPassphrase) {
		t.Errorf("Open(old): got %v, want ErrWrongPassphrase", err)
	}
	// New MUST Open.
	r, err := repo.Open(context.Background(), store, []byte("new-pass-456"))
	if err != nil {
		t.Fatalf("Open(new): %v", err)
	}
	r.Close()

	// Output should mention success.
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "rotat") && !strings.Contains(got, "done") {
		t.Errorf("expected success summary mentioning rotation, got %q", out.String())
	}
}

// TestPasswd_CLI_NewPassphraseFlag verifies --new-passphrase-file
// resolves the new passphrase from the file path. The test wires
// the deps' NewPassphrase callback to the same config.Resolve
// helper main.go would use, so the file resolution path
// (including 0600 enforcement from Phase 1) is exercised end to end.
func TestPasswd_CLI_NewPassphraseFlag(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Don't share passphrases via passwdFixture's stub; the new one
	// comes from the file.
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("from-callback"))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	r.Close()

	yaml := "repo:\n  s3:\n    bucket: test\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	// Stage the new passphrase as a 0600 file.
	newPassFile := filepath.Join(dir, "new.pass")
	if err := os.WriteFile(newPassFile, []byte("from-file-passphrase"), 0o600); err != nil {
		t.Fatalf("write pass file: %v", err)
	}

	deps := PasswdDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("from-callback"), nil },
		// Production wires this to config.Resolve with PassphraseFile;
		// mirror that here so the test exercises the real resolution.
		NewPassphrase: func(file string) ([]byte, error) {
			return config.Resolve(config.ResolveOptions{PassphraseFile: file})
		},
		Stdout: io.Discard,
	}

	cmd := NewPasswd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--new-passphrase-file", newPassFile})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	r2, err := repo.Open(context.Background(), store, []byte("from-file-passphrase"))
	if err != nil {
		t.Fatalf("Open with file passphrase: %v", err)
	}
	r2.Close()
}

// TestPasswd_CLI_NewPassphraseTooShort exercises the < 8-char
// floor (mirroring init's minPassphraseLen). The command must
// refuse without writing anything.
func TestPasswd_CLI_NewPassphraseTooShort(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, store, _ := passwdFixture(t, dir, "old-pass-123", "short")

	cmd := NewPasswd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for too-short new passphrase, got nil")
	}
	// Old passphrase should still work — no rotation happened.
	r, err := repo.Open(context.Background(), store, []byte("old-pass-123"))
	if err != nil {
		t.Fatalf("Open with old after refusal: %v", err)
	}
	r.Close()
}

// TestPasswd_CLI_NewMatchesOld covers the matching-passphrase
// case at the CLI boundary. Either the CLI or the repo layer
// must refuse — we accept either error path.
func TestPasswd_CLI_NewMatchesOld(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	// Both callbacks return the same bytes.
	deps, store, _ := passwdFixture(t, dir, "samesame123", "samesame123")

	cmd := NewPasswd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for matching passphrases, got nil")
	}
	// Old passphrase must still work — nothing was written.
	r, err := repo.Open(context.Background(), store, []byte("samesame123"))
	if err != nil {
		t.Fatalf("Open after refusal: %v", err)
	}
	r.Close()
}

// TestPasswd_CLI_OldPassphraseWrong: Open fails with the wrong
// old passphrase. The new-passphrase callback must NEVER be
// invoked — operators get a clean "wrong passphrase" without
// being prompted for the new one (which would imply they
// authenticated successfully).
func TestPasswd_CLI_OldPassphraseWrong(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("real-passphrase"))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	r.Close()

	yaml := "repo:\n  s3:\n    bucket: test\n"
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}

	newPassphraseCalled := false
	deps := PasswdDeps{
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("wrong-passphrase"), nil },
		NewPassphrase: func(_ string) ([]byte, error) {
			newPassphraseCalled = true
			return []byte("never-asked-for-this"), nil
		},
		Stdout: io.Discard,
	}

	cmd := NewPasswd(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected error for wrong old passphrase, got nil")
	}
	if newPassphraseCalled {
		t.Error("new-passphrase callback should not be invoked when Open fails")
	}
	// Real passphrase still works — repo state untouched.
	r2, err := repo.Open(context.Background(), store, []byte("real-passphrase"))
	if err != nil {
		t.Fatalf("Open with real passphrase: %v", err)
	}
	r2.Close()
}

// TestPasswd_CLI_RegisteredOnRoot ensures the command shows up under
// `sentra --help`. Mirrors TestInit_RegisteredOnRoot.
func TestPasswd_CLI_RegisteredOnRoot(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps, _, _ := passwdFixture(t, dir, "old", "newnewnew")
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewPasswd(deps))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "passwd" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("passwd command not registered on root")
	}
}
