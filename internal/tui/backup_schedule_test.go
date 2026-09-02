package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

func keyDown() tea.KeyMsg  { return tea.KeyMsg{Type: tea.KeyDown} }
func keyTab() tea.KeyMsg   { return tea.KeyMsg{Type: tea.KeyTab} }
func keyRight() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRight} }

// pressDown moves the cadence list n rows.
func pressDown(f scheduleForm, n int) scheduleForm {
	for i := 0; i < n; i++ {
		f, _ = f.update(keyDown())
	}
	return f
}

func TestScheduleForm_DefaultsToOneShotWithNoFields(t *testing.T) {
	f := newScheduleForm("/tmp/docs", nil)
	if !f.oneShot() {
		t.Fatal("a fresh form must default to one-shot")
	}
	if got := f.controls(); len(got) != 1 || got[0] != schedCadence {
		t.Fatalf("one-shot controls = %v, want [cadence]", got)
	}
	name, sched, _, err := f.build()
	if err != nil || name != "" || sched.Cadence != "" {
		t.Fatalf("one-shot build = (%q, %+v, %v), want empty and nil", name, sched, err)
	}
	if f.describe() != "one-shot" {
		t.Errorf("describe = %q", f.describe())
	}
}

// The visible controls follow the cadence: hourly needs no time, weekly adds
// a weekday, daily/monthly take a time.
func TestScheduleForm_ControlsFollowCadence(t *testing.T) {
	cases := []struct {
		downs int
		want  []scheduleControl
	}{
		{1, []scheduleControl{schedCadence, schedName}},                        // hourly
		{2, []scheduleControl{schedCadence, schedName, schedAt}},               // daily
		{3, []scheduleControl{schedCadence, schedName, schedAt, schedWeekday}}, // weekly
		{4, []scheduleControl{schedCadence, schedName, schedAt}},               // monthly
	}
	for _, c := range cases {
		f := pressDown(newScheduleForm("/tmp/docs", nil), c.downs)
		got := f.controls()
		if len(got) != len(c.want) {
			t.Fatalf("%s: controls = %v, want %v", f.cadence(), got, c.want)
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Fatalf("%s: controls = %v, want %v", f.cadence(), got, c.want)
			}
		}
	}
}

func TestScheduleForm_BuildsEachCadence(t *testing.T) {
	cases := []struct {
		downs    int
		cadence  string
		at, wday string
		describe string
	}{
		{1, policycfg.CadenceHourly, "", "", "hourly"},
		{2, policycfg.CadenceDaily, "02:00", "", "daily at 02:00"},
		{3, policycfg.CadenceWeekly, "02:00", "sun", "weekly on sun at 02:00"},
		{4, policycfg.CadenceMonthly, "02:00", "", "monthly on the 1st at 02:00"},
	}
	for _, c := range cases {
		f := pressDown(newScheduleForm("/tmp/docs", nil), c.downs)
		name, sched, reuses, err := f.build()
		if err != nil {
			t.Fatalf("%s: build error %v", c.cadence, err)
		}
		if name != "docs" || reuses {
			t.Errorf("%s: name = %q reuses=%v, want docs/false", c.cadence, name, reuses)
		}
		if sched.Cadence != c.cadence || sched.At != c.at || sched.Weekday != c.wday {
			t.Errorf("%s: schedule = %+v", c.cadence, sched)
		}
		if f.describe() != c.describe {
			t.Errorf("%s: describe = %q, want %q", c.cadence, f.describe(), c.describe)
		}
	}
}

func TestScheduleForm_WeekdayCyclesWithArrows(t *testing.T) {
	f := pressDown(newScheduleForm("/tmp/docs", nil), 3) // weekly
	f, _ = f.update(keyTab())                            // name
	f, _ = f.update(keyTab())                            // at
	f, _ = f.update(keyTab())                            // weekday
	if f.focus != schedWeekday {
		t.Fatalf("focus = %v, want weekday", f.focus)
	}
	f, _ = f.update(keyRight()) // sun → mon (wraps)
	_, sched, _, _ := f.build()
	if sched.Weekday != "mon" {
		t.Fatalf("weekday after → = %q, want mon", sched.Weekday)
	}
	f, _ = f.update(tea.KeyMsg{Type: tea.KeyLeft})
	_, sched, _, _ = f.build()
	if sched.Weekday != "sun" {
		t.Fatalf("weekday after ← = %q, want sun", sched.Weekday)
	}
}

func TestScheduleForm_TabWrapsBackToTheList(t *testing.T) {
	f := pressDown(newScheduleForm("/tmp/docs", nil), 2) // daily: list, name, at
	f, _ = f.update(keyTab())
	if f.focus != schedName || !f.name.Focused() || !f.capturesText() {
		t.Fatalf("after one tab focus=%v nameFocused=%v", f.focus, f.name.Focused())
	}
	f, _ = f.update(keyTab())
	if f.focus != schedAt || !f.at.Focused() || f.name.Focused() {
		t.Fatalf("after two tabs focus=%v", f.focus)
	}
	f, _ = f.update(keyTab())
	if f.focus != schedCadence || f.at.Focused() || f.capturesText() || !f.consumesArrows() {
		t.Fatalf("tab must wrap to the list and blur the fields; focus=%v", f.focus)
	}
}

func TestScheduleForm_BadTimeRefused(t *testing.T) {
	f := pressDown(newScheduleForm("/tmp/docs", nil), 2) // daily
	f.at.SetValue("2am")
	if _, _, _, err := f.build(); err == nil || !strings.Contains(err.Error(), "HH:MM") {
		t.Fatalf("bad time must be refused with the HH:MM hint, got %v", err)
	}
}

func TestScheduleForm_NameCollisionRefusedUnlessSameDir(t *testing.T) {
	policies := map[string]config.PolicyConfig{
		"docs": {Paths: []string{"/elsewhere"}},
		"pics": {Paths: []string{"/tmp/docs"}},
	}
	f := pressDown(newScheduleForm("/tmp/docs", policies), 2)
	f.name.SetValue("docs")
	if _, _, _, err := f.build(); err == nil || !strings.Contains(err.Error(), "/elsewhere") {
		t.Fatalf("a name owned by another directory must be refused naming it, got %v", err)
	}
	f.name.SetValue("pics")
	name, _, reuses, err := f.build()
	if err != nil || name != "pics" || !reuses {
		t.Fatalf("same-directory policy must be reused: name=%q reuses=%v err=%v", name, reuses, err)
	}
}

func TestUniquePolicyName(t *testing.T) {
	policies := map[string]config.PolicyConfig{
		"docs":   {Paths: []string{"/elsewhere"}},
		"docs-2": {Paths: []string{"/also/elsewhere"}},
		"mine":   {Paths: []string{"/tmp/mine"}},
	}
	if got := uniquePolicyName("docs", "/tmp/docs", policies); got != "docs-3" {
		t.Errorf("uniquePolicyName(docs) = %q, want docs-3", got)
	}
	if got := uniquePolicyName("mine", "/tmp/mine", policies); got != "mine" {
		t.Errorf("same-dir policy must keep its name, got %q", got)
	}
	if got := uniquePolicyName("fresh", "/tmp/fresh", policies); got != "fresh" {
		t.Errorf("free name must be kept, got %q", got)
	}
}

// The prefilled name is the sanitized basename, uniquified.
func TestScheduleForm_PrefillsUniqueName(t *testing.T) {
	policies := map[string]config.PolicyConfig{"my docs": {}, "my-docs": {Paths: []string{"/x"}}}
	f := pressDown(newScheduleForm("/tmp/my docs", policies), 2)
	if got := f.name.Value(); got != "my-docs-2" {
		t.Fatalf("prefilled name = %q, want my-docs-2", got)
	}
}

// Text fields box only when focused; the list rows carry the ▍ glyph.
func TestScheduleForm_ViewBoxesOnlyTheFocusedField(t *testing.T) {
	f := pressDown(newScheduleForm("/tmp/docs", nil), 2)
	if n := boxCount(f.view()); n != 0 {
		t.Fatalf("list focused: boxCount = %d, want 0", n)
	}
	if !strings.Contains(f.view(), "▍") {
		t.Fatal("the cadence list must mark its selection with the glyph")
	}
	f, _ = f.update(keyTab())
	if n := boxCount(f.view()); n != 1 {
		t.Fatalf("name focused: boxCount = %d, want 1", n)
	}
}

// TestScheduleForm_ChosenCadenceMarkedAfterTabbingOff pins the radio mark:
// ▍ means "the keyboard is here" and leaves the list with tab, so the chosen
// cadence needs its own glyph — ● on the chosen row, ○ on the rest — that
// stays put while focus sits on the name, time, or weekday row. A glyph, not
// a color: the Ascii profile emits no ANSI, so this is what NO_COLOR sees.
func TestScheduleForm_ChosenCadenceMarkedAfterTabbingOff(t *testing.T) {
	assertChosen := func(t *testing.T, view, cadence string) {
		t.Helper()
		if !strings.Contains(view, "● "+cadence) {
			t.Fatalf("chosen row %q not marked ●:\n%s", cadence, view)
		}
		if n := strings.Count(view, "●"); n != 1 {
			t.Fatalf("● count = %d, want exactly one:\n%s", n, view)
		}
		if n := strings.Count(view, "○"); n != len(scheduleCadences)-1 {
			t.Fatalf("○ count = %d, want %d:\n%s", n, len(scheduleCadences)-1, view)
		}
	}
	f := pressDown(newScheduleForm("/tmp/docs", nil), 3) // weekly: name, time, weekday
	assertChosen(t, f.view(), policycfg.CadenceWeekly)

	// Tab walks name → time → weekday; the ▍ leaves the list on the first
	// tab and lands on the weekday row on the third. The ● never moves.
	for i := 1; i <= 3; i++ {
		f, _ = f.update(keyTab())
		view := f.view()
		assertChosen(t, view, policycfg.CadenceWeekly)
		if strings.Contains(view, "▍ ● ") || strings.Contains(view, "▍ ○ ") {
			t.Fatalf("tab %d: focus glyph still on a cadence row:\n%s", i, view)
		}
	}
	if !strings.Contains(f.view(), "▍") {
		t.Fatal("third tab should put the focus glyph on the weekday row")
	}
}

// The weekday row's control is ←/→, so ↑/↓ there are the classic slip. The
// row must own them (consumesArrows true) and let them do nothing rather
// than reporting false and having the shell hand them to the nav rail, whose
// live preview would switch the active view mid-wizard.
func TestScheduleForm_WeekdayRowOwnsVerticalArrows(t *testing.T) {
	f := pressDown(newScheduleForm("/tmp/docs", nil), 3) // weekly
	for range 3 {
		f, _ = f.update(keyTab()) // name, at, weekday
	}
	if f.focus != schedWeekday {
		t.Fatalf("focus = %v, want weekday", f.focus)
	}
	if !f.consumesArrows() {
		t.Fatal("the weekday row must report that it consumes ↑/↓")
	}
	for _, k := range []tea.KeyMsg{{Type: tea.KeyDown}, {Type: tea.KeyUp}} {
		got, _ := f.update(k)
		if got.focus != schedWeekday || got.weekday != f.weekday || got.cadenceIdx != f.cadenceIdx {
			t.Fatalf("%s on the weekday row changed the form: focus=%v weekday=%d cadence=%d",
				k, got.focus, got.weekday, got.cadenceIdx)
		}
	}
}
