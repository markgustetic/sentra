package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

// notifyRecorder stands in for notify.ExecRunner and keeps each
// notification's subtitle and body — the trailing argv on every
// supported platform.
type notifyRecorder struct{ bodies []string }

func (n *notifyRecorder) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	n.bodies = append(n.bodies, strings.Join(args[len(args)-2:], " | "))
	return nil, nil
}

// TestBackupWizard_OneShotRunNotifies: the operator may have switched
// away from the terminal during a long run, so the one-shot backup
// announces itself like a policy run, named after its folder.
func TestBackupWizard_OneShotRunNotifies(t *testing.T) {
	r := newFlowRepo(t)
	src := filepath.Join(t.TempDir(), "photos")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &notifyRecorder{}
	v := NewBackupView(Deps{Repo: r, Notify: rec.run})
	v.picker = newDirPicker(src)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v, _ = toConfirm(t, toSchedule(t, m.(BackupView)))
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	for _, msg := range execCmds(t, cmd) {
		if start, ok := msg.(startOpMsg); ok {
			if done := start.run(context.Background()).(backupDoneMsg); done.err != nil {
				t.Fatalf("backup failed: %v", done.err)
			}
			if len(rec.bodies) != 1 || !strings.HasPrefix(rec.bodies[0], "Backup complete | photos: 1 files") {
				t.Fatalf("notification = %q, want the summary named after the folder", rec.bodies)
			}
			return
		}
	}
	t.Fatal("no startOpMsg emitted")
}

// TestJobs_RunNotifiesOnSuccessAndFailure: the TUI's policy run fires
// the same notification the CLI's does — from internal/policy, so the
// two cannot drift.
func TestJobs_RunNotifiesOnSuccessAndFailure(t *testing.T) {
	r := newFlowRepo(t)
	src := realTempDir(t)
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Defaults()
	rec := &notifyRecorder{}
	deps := Deps{Repo: r, Config: &cfg, Notify: rec.run}
	p := config.PolicyConfig{Paths: []string{src}, Schedule: config.PolicySchedule{Cadence: "manual"}}
	if done := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter()).run(context.Background()).(policyRunDoneMsg); done.err != nil {
		t.Fatalf("run: %v", done.err)
	}
	p.Hooks.Before = "exit 7"
	if done := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter()).run(context.Background()).(policyRunDoneMsg); done.err == nil {
		t.Fatal("failing before hook must fail the run")
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("want two notifications, got %q", rec.bodies)
	}
	if !strings.HasPrefix(rec.bodies[0], "Backup complete | job: 1 files") {
		t.Errorf("success notification = %q", rec.bodies[0])
	}
	if !strings.HasPrefix(rec.bodies[1], "Backup failed | job: ") {
		t.Errorf("failure notification = %q", rec.bodies[1])
	}

	cfg.Notify.DisableDesktop = true
	p.Hooks.Before = ""
	if done := buildPolicyRunOp(deps, "job-run", "job", p, newOpReporter()).run(context.Background()).(policyRunDoneMsg); done.err != nil {
		t.Fatalf("run: %v", done.err)
	}
	if len(rec.bodies) != 2 {
		t.Fatalf("disable_desktop must silence the run, got %q", rec.bodies)
	}
}

func TestSettings_ToggleNotifyPersists(t *testing.T) {
	v, path, cfg := settingsWithConfig(t)
	v = cursorTo(v, entryToggleNotify)
	if !strings.Contains(v.View(), "Desktop notifications   [on]") {
		t.Fatalf("notifications default on:\n%s", v.View())
	}

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, _ = runGuardedOp(t, m, cmd)
	v = m.(SettingsView)

	if !cfg.Notify.DisableDesktop {
		t.Error("toggling must flip the in-memory config after a successful write")
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.Notify.DisableDesktop {
		t.Error("toggling must persist disable_desktop to disk")
	}
	if !strings.Contains(v.View(), "Desktop notifications   [off]") {
		t.Errorf("view should show notifications as off:\n%s", v.View())
	}
}
