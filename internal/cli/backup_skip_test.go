package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// denySubdir creates root/<name> holding one file and chmods the
// directory 0o000 so its listing is denied — a TCC-protected folder in
// miniature. Returns the symlink-resolved path, which is the spelling
// the walk (started from repo.ResolveRoot's root) reports. Skips where
// mode bits cannot deny.
func denySubdir(t *testing.T, root, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod permission denial is not modeled on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod cannot deny access")
	}
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hidden.txt"), []byte("unreachable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// skipSource lays out <dir>/src with one readable file and one denied
// subdirectory and returns (src, denied path).
func skipSource(t *testing.T, dir string) (string, string) {
	t.Helper()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	return src, denySubdir(t, src, "locked")
}

// TestBackup_ReportsDeniedSubdir pins the human-readable contract: a
// backup that dropped a denied folder names it on stderr as it
// happens (beside the progress bar) and counts it in the stdout
// summary. Silence here is the bug — the snapshot succeeded, so an
// operator who was not told has no reason to look.
func TestBackup_ReportsDeniedSubdir(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)
	src, denied := skipSource(t, dir)

	deps, _, out, errBuf := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{src})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	wantLine := "skipped " + denied + ": permission denied"
	if got := errBuf.String(); strings.Count(got, wantLine) != 1 {
		t.Errorf("stderr should carry %q exactly once:\n%s", wantLine, got)
	}
	if got := out.String(); !strings.Contains(got, "skipped:   1") {
		t.Errorf("stdout summary should count the skip:\n%s", got)
	}
}

// TestBackup_NoSkipsPrintsNoSkipLine: the count line is a warning,
// present only when there is something to warn about.
func TestBackup_NoSkipsPrintsNoSkipLine(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	deps, _, out, errBuf := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{src})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(out.String(), "skipped") || strings.Contains(errBuf.String(), "skipped") {
		t.Errorf("clean tree must not mention skips:\nstdout %q\nstderr %q", out.String(), errBuf.String())
	}
}

// TestBackup_JSONReportsSkipped: the machine surface carries the count
// too, and the skip lines stay off stdout so the JSON remains parseable.
func TestBackup_JSONReportsSkipped(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)
	src, denied := skipSource(t, dir)

	deps, _, out, errBuf := backupFixture(t, "hunter2")
	cmd := NewBackup(deps)
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	cmd.SetArgs([]string{src, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var row struct {
		Files   int `json:"files"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(out.Bytes(), &row); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if row.Files != 1 || row.Skipped != 1 {
		t.Errorf("row = %+v, want files=1 skipped=1", row)
	}
	if !strings.Contains(errBuf.String(), "skipped "+denied) {
		t.Errorf("skip line should still reach stderr under --json:\n%s", errBuf.String())
	}
}

// TestBackupPlanApply_ReportDeniedSubdir: the plan file cannot carry
// the callback, so both `backup plan` and `backup apply` re-wire it —
// the reviewer sees the omission when the plan is written, and again
// (once, not once per apply walk) when it is applied.
func TestBackupPlanApply_ReportDeniedSubdir(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, dir)
	src, denied := skipSource(t, dir)
	planPath := filepath.Join(dir, "backup-plan.json")
	wantLine := "skipped " + denied + ": permission denied"

	deps, _, out, errBuf := backupFixture(t, "hunter2")
	planCmd := NewBackup(deps)
	planCmd.SetOut(out)
	planCmd.SetErr(errBuf)
	planCmd.SetArgs([]string{"plan", src, "--out", planPath})
	if err := planCmd.Execute(); err != nil {
		t.Fatalf("plan execute: %v", err)
	}
	if got := errBuf.String(); strings.Count(got, wantLine) != 1 {
		t.Errorf("plan stderr should carry %q once:\n%s", wantLine, got)
	}
	out.Reset()
	errBuf.Reset()

	applyCmd := NewBackup(deps)
	applyCmd.SetOut(out)
	applyCmd.SetErr(errBuf)
	applyCmd.SetArgs([]string{"apply", planPath, "--yes"})
	if err := applyCmd.Execute(); err != nil {
		t.Fatalf("apply execute: %v", err)
	}
	if got := errBuf.String(); strings.Count(got, wantLine) != 1 {
		t.Errorf("apply stderr should carry %q exactly once:\n%s", wantLine, got)
	}
	if got := out.String(); !strings.Contains(got, "skipped:   1") {
		t.Errorf("apply summary should count the skip:\n%s", got)
	}
}

// TestPolicyRun_ReportsDeniedSubdir: an unattended run's only voice is
// its log, so `policy run` names the skipped folder and carries the
// count on the per-snapshot summary line.
func TestPolicyRun_ReportsDeniedSubdir(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	src, denied := skipSource(t, dir)

	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	writePolicyConfigFile(t, dir, &cfg)

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	r.Close()

	out := &bytes.Buffer{}
	deps := PolicyDeps{
		RepoDeps: RepoDeps{
			NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
			Stdout:     out,
		},
		Stderr: io.Discard,
	}
	cmd := NewPolicy(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run", "home"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	wantLine := "skipped " + denied + ": permission denied"
	if strings.Count(got, wantLine) != 1 {
		t.Errorf("output should carry %q once:\n%s", wantLine, got)
	}
	if !strings.Contains(got, "1 files, 1 skipped") {
		t.Errorf("snapshot line should carry the skip count:\n%s", got)
	}
}

// A note printed between bar frames must erase the frame it replaces:
// frames never clear to end of line (they are all the same width), so a
// narrower note would otherwise leave the bar's tail beside the message.
func TestProgressPainter_NoteClearsTheFrame(t *testing.T) {
	var buf bytes.Buffer
	pp := startProgressPainter(&buf, ui.NewByteProgress(100))
	pp.note("skipped /x: permission denied")
	pp.stop()
	if !strings.Contains(buf.String(), "\r\x1b[Kskipped /x: permission denied\n") {
		t.Fatalf("note did not clear to end of line before printing:\n%q", buf.String())
	}
}

// skipLine must never dereference a nil error; the walker always passes
// one today, but the line's own contract says the reason may widen.
func TestSkipLine_NilError(t *testing.T) {
	if got := skipLine("/x", nil); got != "skipped /x: skipped" {
		t.Fatalf("skipLine(nil) = %q", got)
	}
}
