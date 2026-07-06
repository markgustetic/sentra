package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

func scheduleTestDeps(t *testing.T, home string) Deps {
	t.Helper()
	cfg := config.Defaults()
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"~/Documents"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfg.Policies["docs"] = config.PolicyConfig{
		Paths:    []string{"~/docs"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	return Deps{Config: &cfg, ConfigPath: filepath.Join(home, "sentra.yaml")}
}

// newScheduleTestView builds a ScheduleView pinned to a temp home and a
// linux target so Install/Uninstall/Installed touch only the temp tree.
func newScheduleTestView(t *testing.T, home string) ScheduleView {
	t.Helper()
	v := NewScheduleView(scheduleTestDeps(t, home))
	v.osOverride = "linux"
	v.homeOverride = home
	v.exeOverride = "/usr/bin/sentra"
	v.reload()
	return v
}

func TestScheduleView_ListsPoliciesWithCadence(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	out := v.View()
	for _, want := range []string{"home", "daily@03:00", "docs", "manual", "not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule view missing %q:\n%s", want, out)
		}
	}
}

func TestScheduleView_InstallConfirmFlow(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	// Cursor is on the first policy ("docs" or "home" — rows are sorted).
	// Move to the "home" (daily) row so Install has a real schedule.
	v.selectPolicy("home")

	// Press 'i' → a confirm modal is requested (pushModalMsg), nothing on disk yet.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	msgs := execCmds(t, cmd)
	var pushed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed = true
			_ = pm
		}
	}
	if !pushed {
		t.Fatal("pressing i must request a confirm modal")
	}
	installed, err := scheduleInstalledFor(t, v, "home")
	if err != nil {
		t.Fatalf("installed check: %v", err)
	}
	if installed {
		t.Fatal("files written before confirmation")
	}

	// Confirm → files are written and the row flips to installed.
	m, cmd = v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	// The install command is a quick tea.Cmd returning scheduleDoneMsg.
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	installed, err = scheduleInstalledFor(t, v, "home")
	if err != nil {
		t.Fatalf("installed check post-confirm: %v", err)
	}
	if !installed {
		t.Fatal("files not written after confirmation")
	}
	if !strings.Contains(v.View(), "installed") {
		t.Errorf("view should reflect installed state:\n%s", v.View())
	}
}

func TestScheduleView_UninstallConfirmFlow(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	v.selectPolicy("home")

	// Install first (confirm path).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	_ = execCmds(t, cmd)
	m, cmd = v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "home"); !installed {
		t.Fatal("precondition: install failed")
	}

	// Press 'u' → confirm modal; confirm → files removed.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	v = m.(ScheduleView)
	m, cmd = v.Update(confirmedMsg{id: scheduleUninstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "home"); installed {
		t.Fatal("files still present after uninstall")
	}
}

func TestScheduleView_ManualPolicyInstallErrors(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	v.selectPolicy("docs") // manual cadence

	// Confirm an install for a manual policy: the run reports an error and
	// nothing is written.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	m, cmd := v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "docs"); installed {
		t.Fatal("manual policy should not install any files")
	}
	if v.notice == "" {
		t.Error("manual install should surface a notice")
	}
}

func TestScheduleView_NilConfigPlaceholder(t *testing.T) {
	v := NewScheduleView(Deps{})
	if !strings.Contains(v.View(), "no policies") {
		t.Errorf("empty-config view should show a placeholder:\n%s", v.View())
	}
}

// scheduleInstalledFor reports whether the named policy's files exist under
// the view's overridden home/OS.
func scheduleInstalledFor(t *testing.T, v ScheduleView, name string) (bool, error) {
	t.Helper()
	paths, err := schedulerPathsFor(v, name)
	if err != nil {
		return false, err
	}
	return schedulerInstalled(paths)
}
