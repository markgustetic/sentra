package atomicfile

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirEntries returns the names in dir, for the "nothing left behind"
// assertions every leg of Write makes: a temp file that outlives a write
// — successful or not — is the bug this package exists to prevent.
func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// TestWrite_ReplacesContentAtPerm is the happy path: the body lands whole
// at the requested mode, a second Write over the file replaces it, and no
// temp file survives either write.
func TestWrite_ReplacesContentAtPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	for _, body := range []string{"first\n", "second, longer body\n"} {
		if err := Write(path, []byte(body), 0o600); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != body {
			t.Errorf("content = %q, want %q", got, body)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %o, want 600", perm)
		}
		if names := dirEntries(t, dir); len(names) != 1 || names[0] != "target" {
			t.Errorf("dir holds %v, want only target", names)
		}
	}
}

// TestWrite_TightensLoosePerm pins that perm applies to the replacement even
// when the previous file was more permissive: the credentials and config
// writers both promise 0600 on a file the operator may have created 0644.
func TestWrite_TightensLoosePerm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil { //nolint:gosec // deliberately loose: Write must replace it with 0600
		t.Fatal(err)
	}
	if err := Write(path, []byte("new"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("perm = %o, want 600", perm)
	}
}

// TestWriteFrom_FailedWriteLeavesPreviousFileIntact is the crash-safety
// rule: the target is replaced only by a fully written temp file, so a
// writer that fails midway can never leave a truncated or empty target.
// For sentra.yaml an empty file is the worst case — it still counts as
// configured yet loads as bucket "" — and for the credentials file it
// strands every other profile. The failure must also clean up its temp
// file.
func TestWriteFrom_FailedWriteLeavesPreviousFileIntact(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	before := []byte("complete previous content\n")
	if err := os.WriteFile(path, before, 0o600); err != nil {
		t.Fatal(err)
	}

	boom := errors.New("disk full")
	err := writeFrom(path, 0o600, func(w io.Writer) error {
		if _, err := io.WriteString(w, "partial"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("writeFrom error = %v, want %v", err, boom)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name the target %s", err, path)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read target after failed write: %v", err)
	}
	if string(after) != string(before) {
		t.Errorf("a failed write disturbed the target:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Errorf("failed write left temp files behind: %v", names)
	}
}

// TestWrite_FailedRenameCleansUp covers the other failure leg: the temp
// file was fully written but could not be renamed into place (here because
// the target is a directory). The temp file must not be abandoned.
func TestWrite_FailedRenameCleansUp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "target")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	err := Write(path, []byte("body\n"), 0o600)
	if err == nil {
		t.Fatal("Write over a directory succeeded, want an error")
	}
	if names := dirEntries(t, dir); len(names) != 1 || names[0] != "target" {
		t.Errorf("failed rename left temp files behind: %v", names)
	}
}

// TestWrite_MissingDirFails pins that Write does not invent the target's
// parent: creating directories is the caller's decision (config creates
// ~/.config/sentra private; a typo'd path must not gain a directory).
func TestWrite_MissingDirFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "target")
	if err := Write(path, []byte("body\n"), 0o600); err == nil {
		t.Fatal("Write into a missing directory succeeded, want an error")
	}
	if _, err := os.Lstat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Write created the missing parent directory (lstat err = %v)", err)
	}
}

// TestWrite_WritesThroughSymlink is the dotfiles rule: when the target is
// a symlink into a managed directory (stow, chezmoi, a plain `ln -s` into a
// dotfiles repo), Write must update the link's target and leave the link
// standing. Renaming the temp file over the link path would replace the
// link with a regular file — silently severing the operator's dotfiles on
// every rewrite — which is exactly what a plain os.WriteFile never did. No
// temp file may be left in either directory.
func TestWrite_WritesThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	realDir := filepath.Join(dir, "real")
	if err := os.Mkdir(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "target")
	if err := os.WriteFile(target, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if err := Write(link, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("Write through symlink: %v", err)
	}

	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("Write replaced the symlink with a %v; the dotfiles link is severed", fi.Mode())
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Errorf("link target does not hold the new body: %q", got)
	}
	if names := dirEntries(t, dir); len(names) != 2 {
		t.Errorf("link dir holds %v after Write", names)
	}
	if names := dirEntries(t, realDir); len(names) != 1 || names[0] != "target" {
		t.Errorf("target dir holds %v after Write", names)
	}
}

// TestWrite_DanglingSymlinkFails pins the other half of the rule: a link
// whose target is missing is neither a fresh file nor a file to write
// through. Creating a regular file at the link's own path would sever it
// just like the rename did, and inventing the target's parent directory
// would write somewhere the operator never named. The only honest outcome
// is a clear error naming the link, with the link left as it was.
func TestWrite_DanglingSymlinkFails(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "link")
	if err := os.Symlink(filepath.Join(dir, "missing", "target"), link); err != nil {
		t.Fatal(err)
	}
	err := Write(link, []byte("body\n"), 0o600)
	if err == nil {
		t.Fatal("Write through a dangling symlink succeeded, want an error")
	}
	if !strings.Contains(err.Error(), link) {
		t.Errorf("error %q does not name the link %s", err, link)
	}
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Errorf("failed Write replaced the dangling symlink with a %v", fi.Mode())
	}
	if names := dirEntries(t, dir); len(names) != 1 {
		t.Errorf("failed Write left entries behind: %v", names)
	}
}
