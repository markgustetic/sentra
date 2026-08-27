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

// repeatFixture returns a configure-stage BackupView wired for a
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

// ctrl+e cycles the repeat cadence through the simple periods and back to
// off — a chord, like rescan's ctrl+r, because both configure-stage
// controls own plain runes.
func TestBackup_CtrlECyclesRepeat(t *testing.T) {
	v := NewBackupView(Deps{})
	want := []string{policycfg.CadenceDaily, policycfg.CadenceWeekly, policycfg.CadenceMonthly, ""}
	for _, w := range want {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
		v = m.(BackupView)
		if v.repeat != w {
			t.Fatalf("repeat = %q, want %q", v.repeat, w)
		}
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyCtrlE})
	if out := m.(BackupView).View(); !strings.Contains(out, "daily") {
		t.Fatalf("configure frame does not show the armed cadence:\n%s", out)
	}
}

// The confirmation gate must name what confirming installs — an operator
// never agrees to a standing schedule that was not spelled out.
func TestBackup_ConfirmBodyNamesRepeat(t *testing.T) {
	v, _, _ := repeatFixture(t)
	v.repeat = policycfg.CadenceWeekly
	dir := filepath.Join(t.TempDir(), "Projects")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	_, cmd := v.requestBackup(dir)
	if cmd == nil {
		t.Fatal("requestBackup produced no modal command")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("got %T, want pushModalMsg", cmd())
	}
	body := push.modal.View()
	for _, wantSub := range []string{"weekly", "Projects"} {
		if !strings.Contains(body, wantSub) {
			t.Errorf("confirm modal missing %q:\n%s", wantSub, body)
		}
	}
}

// Confirming with a cadence armed writes the policy into sentra.yaml,
// installs the OS scheduler files, and then starts the backup — in that
// order, so a failed install never leaves an unscheduled "repeating"
// backup that quietly ran once.
func TestBackup_ConfirmInstallsPolicyScheduleThenRuns(t *testing.T) {
	v, cfgPath, home := repeatFixture(t)
	v.repeat = policycfg.CadenceDaily
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v.tag.SetValue("nightly")
	v.pending = dir

	m, cmd := v.Update(confirmedMsg{id: backupConfirmID})
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
	if len(p.Paths) != 1 || p.Paths[0] != dir {
		t.Fatalf("policy paths = %v, want [%s]", p.Paths, dir)
	}
	if len(p.Tags) != 1 || p.Tags[0] != "nightly" {
		t.Fatalf("policy tags = %v, want [nightly]", p.Tags)
	}
	if p.Schedule.Cadence != policycfg.CadenceDaily {
		t.Fatalf("policy cadence = %q, want daily", p.Schedule.Cadence)
	}
	for _, f := range []string{"sentra-docs.service", "sentra-docs.timer"} {
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", f)); err != nil {
			t.Errorf("scheduler file %s not installed: %v", f, err)
		}
	}
	// In-memory coherence: the shared resolved config sees the policy too,
	// so the Schedule/Policies views list it without a relaunch.
	if _, ok := v.deps.Config.Policies["docs"]; !ok {
		t.Error("in-memory config missing the new policy")
	}
}

// A failed install blocks the backup: the operator asked for a REPEATING
// backup, and silently degrading to a one-shot would betray that.
func TestBackup_ScheduleFailureBlocksBackup(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)}) // no ConfigPath
	v.repeat = policycfg.CadenceDaily
	dir := t.TempDir()
	v.pending = dir
	m, cmd := v.Update(confirmedMsg{id: backupConfirmID})
	got := m.(BackupView)
	if got.stage != backupConfigure {
		t.Fatalf("stage = %v, want backupConfigure after a failed install", got.stage)
	}
	if cmd != nil {
		t.Fatal("backup must not start when the schedule install failed")
	}
	if got.pathErr == "" {
		t.Fatal("failed install must surface an error")
	}
}

// A policy name is derived from the directory basename; a clash with an
// existing policy pointing somewhere ELSE is uniquified, never clobbered.
// The same directory reuses its policy (cadence/tag refresh), keeping any
// config-authored hooks.
func TestBackup_PolicyNameCollisionUniquified(t *testing.T) {
	v, cfgPath, _ := repeatFixture(t)
	otherDir := t.TempDir()
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies = map[string]config.PolicyConfig{
			"docs": {Paths: []string{otherDir}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	v.repeat = policycfg.CadenceMonthly
	dir := filepath.Join(t.TempDir(), "docs")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	v.pending = dir
	m, _ := v.Update(confirmedMsg{id: backupConfirmID})
	if got := m.(BackupView); got.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning (pathErr=%q)", got.stage, got.pathErr)
	}

	onDisk, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if p := onDisk.Policies["docs"]; len(p.Paths) != 1 || p.Paths[0] != otherDir {
		t.Fatalf("existing policy clobbered: %v", p.Paths)
	}
	p2, ok := onDisk.Policies["docs-2"]
	if !ok {
		t.Fatalf("uniquified policy missing; policies = %v", onDisk.Policies)
	}
	if len(p2.Paths) != 1 || p2.Paths[0] != dir {
		t.Fatalf("docs-2 paths = %v, want [%s]", p2.Paths, dir)
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
