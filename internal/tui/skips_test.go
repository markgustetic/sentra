package tui

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// denySubdir creates root/<name> holding one file and chmods the
// directory 0o000 so its listing is denied — a TCC-protected folder in
// miniature. Skips where mode bits cannot deny.
func denySubdir(t *testing.T, root, name string) {
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
}

// runWizardBackup drives the wizard's own startOpMsg for src and
// returns the done screen. The route is the one
// TestBackupWizard_TagReachesTheSnapshot takes, so the summary under
// test is the one the operator sees.
func runWizardBackup(t *testing.T, src string) (BackupView, backupDoneMsg) {
	t.Helper()
	r := newFlowRepo(t)
	v, _ := toConfirm(t, toSchedule(t, backupAtRepo(t, r, src)))
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	for _, msg := range execCmds(t, cmd) {
		start, ok := msg.(startOpMsg)
		if !ok {
			continue
		}
		done := start.run(context.Background()).(backupDoneMsg)
		if done.err != nil {
			t.Fatalf("backup failed: %v", done.err)
		}
		m, _ = v.Update(done)
		return m.(BackupView), done
	}
	t.Fatal("no startOpMsg emitted")
	return BackupView{}, backupDoneMsg{}
}

// TestBackupWizard_DoneCountsSkippedFolders: a backup that dropped a
// denied folder says so on the done screen, and a clean one does not
// — the line is a warning, and a permanent "skipped 0" would teach the
// eye to pass over the one that matters.
func TestBackupWizard_DoneCountsSkippedFolders(t *testing.T) {
	cases := []struct {
		name string
		deny bool
	}{
		{"denied subdir is counted", true},
		{"clean tree says nothing", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := t.TempDir()
			if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
				t.Fatal(err)
			}
			if tc.deny {
				denySubdir(t, src, "locked")
			}
			v, done := runWizardBackup(t, src)
			out := v.View()
			if tc.deny {
				if done.info.Stats.Skipped != 1 {
					t.Fatalf("Stats.Skipped = %d, want 1", done.info.Stats.Skipped)
				}
				if !strings.Contains(out, "skipped   1") {
					t.Errorf("done screen should count the skipped folder:\n%s", out)
				}
				return
			}
			if strings.Contains(out, "skipped") {
				t.Errorf("clean backup must not mention skips:\n%s", out)
			}
		})
	}
}

// TestJobs_RunDoneCountsSkippedFolders: the job-run summary sums the
// skips across the policy's paths, so an unattended job that silently
// lost a folder is visible the next time the operator opens the view.
func TestJobs_RunDoneCountsSkippedFolders(t *testing.T) {
	deps, _, root := jobsDepsWithRepo(t)
	denySubdir(t, root, "locked")
	v := newJobsForTest(t, deps)
	v.tbl.SetCursor(0)
	v2, _ := pressJobsKey(v, 'r')
	m, cmd := v2.Update(confirmedMsg{id: jobRunConfirmID})
	v3 := m.(JobsView)
	var start startOpMsg
	var found bool
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok {
			start, found = s, true
		}
	}
	if !found {
		t.Fatal("confirmed run must emit a startOpMsg")
	}
	done, ok := start.run(context.Background()).(policyRunDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("run = %#v", done)
	}
	if done.skipped != 1 {
		t.Fatalf("skipped = %d, want 1", done.skipped)
	}
	m2, _ := v3.Update(done)
	out := m2.(JobsView).View()
	if !strings.Contains(out, "skipped    1") {
		t.Errorf("run-done screen should count the skipped folder:\n%s", out)
	}
}
