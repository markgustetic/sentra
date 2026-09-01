package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// launchDeps seeds the shared snapshot preload with two rows, so the
// snapshot-first launch hooks have a live selection to read.
func launchDeps() Deps {
	t0 := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	return Deps{preload: &snapshotPreload{snaps: []repo.SnapshotInfo{
		{ID: "snap-aaa", CreatedAt: t0, Tag: "nightly"},
		{ID: "snap-bbb", CreatedAt: t0.Add(time.Hour), Tag: "weekly"},
	}}}
}

// Restore left the rail because restoring starts from WHICH snapshot: r on
// the highlighted row hands that snapshot to the restore flow directly.
func TestSnapshots_RestoreKeyLaunchesSelected(t *testing.T) {
	s := NewSnapshots(Deps{}).SetSnapshots(launchDeps().preload.snaps)
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r on a selected snapshot produced no command")
	}
	got, ok := cmd().(launchRestoreMsg)
	if !ok {
		t.Fatalf("r produced %T, want launchRestoreMsg", cmd())
	}
	if got.snapID == "" {
		t.Fatal("launchRestoreMsg carried no snapshot id")
	}
}

// Diff's rail entry is gone the same way: d launches the compare flow with
// the highlighted snapshot pre-picked as side A.
func TestSnapshots_DiffKeyLaunchesSelected(t *testing.T) {
	s := NewSnapshots(Deps{}).SetSnapshots(launchDeps().preload.snaps)
	_, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("d on a selected snapshot produced no command")
	}
	got, ok := cmd().(launchDiffMsg)
	if !ok {
		t.Fatalf("d produced %T, want launchDiffMsg", cmd())
	}
	if got.snapID == "" {
		t.Fatal("launchDiffMsg carried no snapshot id")
	}
}

// With no snapshots there is nothing to hand over: the keys are inert.
func TestSnapshots_LaunchKeysInertWithoutRows(t *testing.T) {
	s := NewSnapshots(Deps{})
	for _, r := range []rune{'r', 'd'} {
		if _, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}); cmd != nil {
			t.Errorf("%q with no snapshots produced a command", string(r))
		}
	}
}

// A seeded launch skips the picker exactly the way choosing the row there
// would have: snapshot set, destination stage, dest field focused.
func TestRestoreView_LaunchSeedSkipsPicker(t *testing.T) {
	v := NewRestoreView(launchDeps())
	m, _ := v.Update(launchRestoreMsg{snapID: "snap-bbb"})
	got := m.(RestoreView)
	if got.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", got.stage)
	}
	if got.snapID != "snap-bbb" {
		t.Fatalf("snapID = %q, want snap-bbb", got.snapID)
	}
	if !got.dest.Focused() {
		t.Fatal("dest input must take focus, as the picker's enter does")
	}
}

// A launch must never clobber a flow already past the picker — a running
// restore keeps running; the operator just lands on its live screen.
func TestRestoreView_LaunchSeedIgnoredMidFlow(t *testing.T) {
	v := NewRestoreView(launchDeps())
	v.stage = restoreRunning
	m, _ := v.Update(launchRestoreMsg{snapID: "snap-bbb"})
	if got := m.(RestoreView); got.stage != restoreRunning {
		t.Fatalf("mid-flow seed changed stage to %v", got.stage)
	}
}

// Diff's seed pre-picks side A and lands on the B picker; relaunching from
// a stale rendered diff resets it the same way (diff holds no op state, so
// a reset is always safe).
func TestDiff_LaunchSeedPrePicksSideA(t *testing.T) {
	d := NewDiff(launchDeps())
	m, _ := d.Update(launchDiffMsg{snapID: "snap-aaa"})
	got := m.(Diff)
	if got.stage != diffPickB {
		t.Fatalf("stage = %v, want diffPickB", got.stage)
	}
	if got.idA != "snap-aaa" {
		t.Fatalf("idA = %q, want snap-aaa", got.idA)
	}

	got.stage = diffShow
	got.err = "stale failure"
	m, _ = got.Update(launchDiffMsg{snapID: "snap-bbb"})
	got = m.(Diff)
	if got.stage != diffPickB || got.idA != "snap-bbb" || got.err != "" {
		t.Fatalf("relaunch did not reset the compare flow: %+v", got.stage)
	}
}

// The shell half: a launch message seeds the hidden view AND activates it,
// so one keypress in Snapshots lands the operator inside the target flow.
func TestApp_LaunchMsgsRouteAndActivate(t *testing.T) {
	for _, tt := range []struct {
		msg  tea.Msg
		want string
	}{
		{launchRestoreMsg{snapID: "snap-aaa"}, "restore"},
		{launchDiffMsg{snapID: "snap-aaa"}, "diff"},
	} {
		app := NewApp(Deps{RepoName: "x"})
		sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
		m, _ := sized.(App).Update(tt.msg)
		got := m.(App)
		if got.views[got.active].id != tt.want {
			t.Fatalf("%T left active on %q, want %q", tt.msg, got.views[got.active].id, tt.want)
		}
	}
}
