package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// repeatFixture returns a Location-stage BackupView wired for a
// deterministic schedule install: a real repo, a real sentra.yaml, a temp
// home, and linux scheduler paths (fixed file names, no launchd).
func repeatFixture(t *testing.T) (BackupView, string, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "sentra.yaml")
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "b"
	if err := config.Write(cfgPath, cfg); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	v := NewBackupView(Deps{Repo: newFlowRepo(t), Config: loaded, ConfigPath: cfgPath})
	v.schedGOOS = "linux"
	v.schedHome = home
	v.schedExe = "/usr/bin/sentra"
	return v, cfgPath, home
}

// atDailyConfirm walks the fixture to Confirm with daily@02:00 chosen for dir.
func atDailyConfirm(t *testing.T, v BackupView, dir string) BackupView {
	t.Helper()
	v.picker = newDirPicker(dir)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = toSchedule(t, m.(BackupView))
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})              // hourly
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyDown}) // daily
	v, _ = toConfirm(t, m.(BackupView))
	return v
}

// Confirming a scheduled backup installs the policy + timer FIRST, then
// starts the run — a failed install never leaves an unscheduled
// "repeating" backup that quietly ran once.
func TestBackupWizard_ConfirmInstallsPolicyScheduleThenRuns(t *testing.T) {
	v, cfgPath, home := repeatFixture(t)
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v = atDailyConfirm(t, v, dir)
	if !strings.Contains(v.View(), `daily at 02:00 as policy "docs"`) || !strings.Contains(v.View(), "next run") {
		t.Fatalf("confirm summary must describe the schedule and next run:\n%s", v.View())
	}
	v.confirm.tag.SetValue("nightly")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning (pathErr=%q)", got.stage, got.pathErr)
	}
	if cmd == nil {
		t.Fatal("no backup op started")
	}
	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := onDisk.Policies["docs"]
	if !ok {
		t.Fatalf("policy 'docs' not written; policies = %v", onDisk.Policies)
	}
	if len(p.Paths) != 1 || p.Paths[0] != dir || len(p.Tags) != 1 || p.Tags[0] != "nightly" {
		t.Fatalf("policy = %+v", p)
	}
	if p.Schedule.Cadence != policycfg.CadenceDaily || p.Schedule.At != "02:00" {
		t.Fatalf("schedule = %+v", p.Schedule)
	}
	for _, f := range []string{"sentra-docs.service", "sentra-docs.timer"} {
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", f)); err != nil {
			t.Errorf("scheduler file %s not installed: %v", f, err)
		}
	}
	if _, ok := v.deps.Config.Policies["docs"]; !ok {
		t.Error("in-memory config missing the new policy")
	}
	if got.installedName != "docs" || !got.installedNextOK {
		t.Errorf("done-screen record: name=%q nextOK=%v", got.installedName, got.installedNextOK)
	}
}

func TestBackupWizard_ScheduleFailureBlocksBackup(t *testing.T) {
	v, _, _ := repeatFixture(t)
	v.schedGOOS = "plan9" // scheduler.PathsFor refuses an unsupported platform
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v = atDailyConfirm(t, v, dir)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupConfirm {
		t.Fatalf("stage = %v, want to stay on Confirm", got.stage)
	}
	if cmd != nil {
		t.Fatal("a failed install must not start the backup")
	}
	if !strings.Contains(got.View(), "could not install the schedule") {
		t.Errorf("view must surface the install error:\n%s", got.View())
	}
}

// installRepeat refuses a name the wizard did not resolve: an on-disk
// policy of that name pointing elsewhere is an error, never uniquified.
func TestInstallRepeat_RefusesForeignName(t *testing.T) {
	v, cfgPath, _ := repeatFixture(t)
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies = map[string]config.PolicyConfig{"docs": {Paths: []string{"/elsewhere"}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := v.installRepeat("/tmp/docs", "docs", config.PolicySchedule{Cadence: policycfg.CadenceDaily, At: "02:00"}, "")
	if err == nil || !strings.Contains(err.Error(), "/elsewhere") {
		t.Fatalf("want a collision error naming /elsewhere, got %v", err)
	}
}

// The name derivation is safe for config keys, scheduler labels, and
// filenames regardless of what the folder is called.
func TestRepeatPolicyName_Sanitizes(t *testing.T) {
	for in, want := range map[string]string{
		"docs":        "docs",
		"My Stuff!":   "My-Stuff",
		"...":         "backup",
		"2026 photos": "2026-photos",
	} {
		if got := repeatPolicyName(in); got != want {
			t.Errorf("repeatPolicyName(%q) = %q, want %q", in, got, want)
		}
		if got := repeatPolicyName(in); policycfg.ValidateName(got) != nil {
			t.Errorf("repeatPolicyName(%q) = %q fails ValidateName", in, got)
		}
	}
}
