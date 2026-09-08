package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// Policy paths are stored absolute and run absolute. The timer that
// fires `policy run` inherits a working directory nobody chose (launchd
// starts jobs in `/`), so a path that still depends on the cwd at run
// time backs up the wrong tree — `--path .` under launchd was the root
// filesystem, and `~/x` failed every fire because sh never expanded it.

// TestPolicyAdd_PersistsAbsolutePaths: `policy add` resolves each --path
// against the operator's cwd and home at add time — the only moment
// those are the ones the operator meant — and persists the result.
func TestPolicyAdd_PersistsAbsolutePaths(t *testing.T) {
	// Symlink-resolved: os.Getwd answers /private/var for a /var TMPDIR.
	dir := realPath(t, t.TempDir())
	chDir(t, dir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	writePolicyConfigFile(t, dir, &cfg)

	out := &bytes.Buffer{}
	cmd := NewPolicy(PolicyDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"add", "home", "--path", ".", "--path", "~/Documents", "--path", "sub/../other"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{dir, filepath.Join(home, "Documents"), filepath.Join(dir, "other")}
	p := got.Policies["home"]
	if len(p.Paths) != len(want) {
		t.Fatalf("paths: got %v, want %v", p.Paths, want)
	}
	for i := range want {
		if p.Paths[i] != want[i] {
			t.Errorf("path %d: got %q, want %q", i, p.Paths[i], want[i])
		}
		if !filepath.IsAbs(p.Paths[i]) {
			t.Errorf("path %d persisted relative: %q", i, p.Paths[i])
		}
	}
}

// policyPathFixture writes a policy "home" whose single path is stored
// verbatim (as a hand-edited sentra.yaml would have it) into cfgDir,
// with an in-memory repo behind it, and returns deps plus the store.
func policyPathFixture(t *testing.T, cfgDir, storedPath string) (PolicyDeps, *blobstore.Memory) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{storedPath},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	writePolicyConfigFile(t, cfgDir, &cfg)

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	r.Close()
	deps := PolicyDeps{
		RepoDeps: RepoDeps{
			NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
			Stdout:     io.Discard,
		},
		Stderr: io.Discard,
	}
	return deps, store
}

func writeSourceFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// realPath resolves symlinks so a cwd-derived path (os.Getwd reports
// /private/var for a /var TMPDIR on macOS) compares equal to its origin.
func realPath(t *testing.T, p string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func onlySnapshotRoot(t *testing.T, store blobstore.Store) string {
	t.Helper()
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 1 {
		t.Fatalf("snapshots: got %d, want 1", len(snaps))
	}
	return snaps[0].Root
}

// TestPolicyRun_ResolvesTildePath: a pre-existing config that stored
// `~/x` (written before add resolved paths, or by hand) still runs —
// the tilde expands against the home directory at run time.
func TestPolicyRun_ResolvesTildePath(t *testing.T) {
	cfgDir := t.TempDir()
	chDir(t, cfgDir)
	home := t.TempDir()
	t.Setenv("HOME", home)
	src := filepath.Join(home, "x")
	writeSourceFile(t, src, "alpha")
	deps, store := policyPathFixture(t, cfgDir, "~/x")

	if err := runPolicyWith(t, deps, context.Background(), "run", "home", "--config", filepath.Join(cfgDir, "sentra.yaml")); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := onlySnapshotRoot(t, store); got != src {
		t.Fatalf("snapshot root: got %q, want %q", got, src)
	}
}

// TestPolicyRun_RelativePathAnchorsToConfigDir: the dangerous case. A
// relative path in the config must never resolve against the process
// cwd — that is `/` under launchd. It anchors to the directory of the
// sentra.yaml the policy came from, the one location that is stable
// across an interactive run and a timer fire.
func TestPolicyRun_RelativePathAnchorsToConfigDir(t *testing.T) {
	cfgDir := t.TempDir()
	elsewhere := t.TempDir()
	chDir(t, elsewhere)
	// A decoy "src" in the cwd: a cwd-relative resolution would find it
	// and the run would succeed against the wrong tree.
	writeSourceFile(t, filepath.Join(elsewhere, "src"), "decoy")
	want := filepath.Join(cfgDir, "src")
	writeSourceFile(t, want, "alpha")
	deps, store := policyPathFixture(t, cfgDir, "src")

	if err := runPolicyWith(t, deps, context.Background(), "run", "home", "--config", filepath.Join(cfgDir, "sentra.yaml")); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := onlySnapshotRoot(t, store); got != want {
		t.Fatalf("snapshot root: got %q, want %q (config dir), not the cwd's src", got, want)
	}
}
