# Backup Wizard + Scheduled Backups Rail Tab Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn the TUI Backup view into a three-step wizard (Location → Schedule → Confirm) and promote the existing "Scheduled backups" jobs view onto the rail directly under Backup.

**Architecture:** `BackupView` (internal/tui/backup.go) keeps its value-typed Bubbletea model and stage enum; the single `backupConfigure` stage becomes three stages, each backed by a small focused struct in its own file (`scheduleForm` in backup_schedule.go, `confirmControls` in backup_confirm.go) that owns its widgets, tab cycle, focus/blur and rendering. The existing `installRepeat` (backup_repeat.go) is generalized to take the name + schedule the wizard resolved. The rail change is a registry reorder in `NewApp` plus launcher/help/doc edits.

**Tech Stack:** Go 1.27, Bubbletea + bubbles (`textinput`, `cursor`), lipgloss, `internal/ui` theme helpers (`SelectRow`, `FieldBox`, `ActionLine`), `internal/policy` (cadence constants, `NormalizeSchedule`, `Validate`, `NextRun`), `internal/scheduler`, `internal/config` (`config.Update`).

Spec: `docs/superpowers/specs/2026-09-02-backup-wizard-and-scheduled-tab-design.md`.

## Global Constraints

- **TDD.** Failing test first, watch it fail for the right reason, minimal implementation, pass, commit. Run tests as `go test -race ./internal/tui/ -run '<Name>'` while iterating; run `go test -race ./...` once before the final push.
- **Import direction:** `internal/tui` must never import `internal/cli`.
- **Field focus rules (CLAUDE.md "TUI specifics"):** every text field renders through `boxedField`; a `Focus()` call's own returned cmd is what you return (never `textinput.Blink`); assign it to a local before returning; leaving a stage blurs its fields; `viewHiddenMsg` blurs everything; `viewShownMsg` re-focuses only the current stage's default field; constructors and `Init` focus nothing; route `cursor.BlinkMsg` to whichever field is `Focused()`. Size inputs with `ui.FieldBoxOverhead` subtracted.
- **Selection is a glyph.** Selectable rows go through `ui.SelectRow`. Never wrap an already-styled string in another style.
- **No modal for the backup gate.** The Confirm stage is the gate. `App` keeps its modal machinery for other views.
- **Scheduled confirm order:** install policy + timer FIRST, then start the backup; an install failure stays on Confirm and runs nothing.
- **Config edits use `config.Update`** (never `config.Write`) so env overrides are not persisted.
- **Wording:** picker button `▸ choose the current directory`; verb `choose <basename>`; wizard header left `New backup`, right `Step N of 3`; stage titles `Location`, `Schedule`, `Confirm`; the cadence list's first row is `one-shot`.
- Before claiming done: `go build ./...`, `go vet ./...`, `gofmt -l cmd internal` empty, `golangci-lint run ./...` clean, `go mod tidy -diff` empty, `git diff --check` clean. Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.

---

## File structure

| File | Responsibility |
| --- | --- |
| `internal/tui/app.go` | Rail registry: move `jobs` into the visible section under `backup`. |
| `internal/tui/settings.go`, `help.go` | Drop the Settings launcher; help descriptions for `jobs` and `settings`. |
| `internal/cli/ui.go` | `sentra ui` long help names seven views. |
| `internal/tui/dirpicker.go` | Button label / verb wording (`choose`). |
| `internal/tui/backup_schedule.go` (new) | `scheduleForm`: cadence list, name/time fields, weekday row, tab cycle, validation, prefill uniquify, render. |
| `internal/tui/backup_confirm.go` (new) | `confirmControls`: tag field + rescan toggle, tab cycle, render; `BackupView.confirmSummary`. |
| `internal/tui/backup_repeat.go` | `installRepeat(root, name, schedule, tag)`; `repeatPolicyName` stays; `nextRepeat` deleted. |
| `internal/tui/backup.go` | Stage machine, header, key routing per stage, chat seeding, running/done. |
| Tests | `backup_schedule_test.go`, `backup_confirm_test.go` (new); `backup_test.go`, `backup_repeat_test.go`, `fieldfocus_test.go`, `dirpicker_test.go`, `rail_test.go`, `settings_test.go`, `jobs_test.go`, `app_test.go` (edited). |
| Docs | README.md, docs/QUICKSTART.md, AGENTS.md, CLAUDE.md, docs/architecture.md. |

---

### Task 1: Scheduled backups onto the rail

**Files:**
- Modify: `internal/tui/app.go:299-350` (views slice, categories, hiddenFromRail), `internal/tui/app.go:1136-1139` (digit comment)
- Modify: `internal/tui/settings.go:60-66`, `internal/tui/help.go:47-54`, `internal/cli/ui.go:102`
- Test: `internal/tui/rail_test.go`, `internal/tui/settings_test.go:395-425`, `internal/tui/jobs_test.go:447-458`, `internal/tui/app_test.go:984-1000`

**Interfaces:**
- Produces: view id `jobs` at registry index 2 (rail order `dashboard, backup, jobs, snapshots, maintenance, settings, help`); no Settings `entryNavigate` with `targetID: "jobs"`.

- [ ] **Step 1: Rewrite the rail tests to expect seven views**

In `internal/tui/rail_test.go` replace `TestApp_RailShowsExactlySixViews` and the `want` list, drop `"jobs"` from `TestApp_DemotedViewsStayRoutable`, and change the digit test:

```go
// The rail contract: exactly seven destinations, in this order — the daily
// loop (backup, its schedules, snapshots) plus the three hubs. Everything
// else is hidden from the rail/palette and launched from a parent.
func TestApp_RailShowsExactlySevenViews(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	want := []string{"dashboard", "backup", "jobs", "snapshots", "maintenance", "settings", "help"}
	got := app.registry.Commands()
	if len(got) != len(want) {
		ids := make([]string, len(got))
		for i, c := range got {
			ids[i] = c.ID
		}
		t.Fatalf("registry has %d commands %v, want %v", len(got), ids, want)
	}
	for i, c := range got {
		if c.ID != want[i] {
			t.Errorf("rail[%d] = %q, want %q", i, c.ID, want[i])
		}
	}
}
```

`TestApp_DemotedViewsStayRoutable` list becomes:
```go
	for _, id := range []string{
		"diff", "restore", "check", "prune", "sync", "doctor",
		"recovery-kit", "password", "setup",
	} {
```

`TestApp_NumberKeysSkipHiddenViews`: press `'8'` (must be a no-op), then `'3'` must land on `"jobs"`, then `'5'` on `"maintenance"`:
```go
	m, _ := sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if got := m.(App); got.views[got.active].id != app.views[app.active].id {
		t.Fatalf("number key 8 jumped to hidden view %q", got.views[got.active].id)
	}
	m, _ = sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if got := m.(App); got.views[got.active].id != "jobs" {
		t.Fatalf("number key 3 = %q, want jobs", got.views[got.active].id)
	}
	m, _ = sized.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
	if got := m.(App); got.views[got.active].id != "maintenance" {
		t.Fatalf("number key 5 = %q, want maintenance", got.views[got.active].id)
	}
```

In `internal/tui/jobs_test.go` replace `TestJobs_RegisteredHiddenAndRoutable` with:
```go
// Scheduled backups is a rail destination now — directly under Backup, so
// the thing you just scheduled is one row away.
func TestJobs_OnTheRailUnderBackup(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	cmds := app.registry.Commands()
	if len(cmds) < 3 || cmds[1].ID != "backup" || cmds[2].ID != "jobs" {
		ids := make([]string, len(cmds))
		for i, c := range cmds {
			ids[i] = c.ID
		}
		t.Fatalf("rail = %v, want jobs at index 2 under backup", ids)
	}
	if cmds[2].Title != "Scheduled backups" {
		t.Errorf("rail title = %q, want Scheduled backups", cmds[2].Title)
	}
}
```
(If `Command` has no `Title` field, check `internal/tui/registry.go` for the field name that carries the view title and use that.)

In `internal/tui/settings_test.go`: the `want` list at ~line 399 becomes `[]string{"setup", "password", "recovery-kit"}`; replace `TestSettings_JobsRowRoutes` with:
```go
// Scheduled backups moved onto the rail; Settings must not keep a second
// route to it (two launchers for one visible view is clutter), nor either
// of the older Policies/Schedule ids.
func TestSettings_NoJobsLauncher(t *testing.T) {
	v := NewSettingsView(Deps{})
	for _, e := range v.entries {
		if e.kind == entryNavigate && (e.targetID == "jobs" || e.targetID == "policies" || e.targetID == "schedule") {
			t.Fatalf("settings must not carry a launcher for %q", e.targetID)
		}
	}
}
```
Also update the message at settings_test.go:133 to `"views = %d, want 18 (seven rail views + eleven hidden)"` and the comment above `TestApp_AllViewsRegistered` in app_test.go to say "seven-view rail"; reorder its `want` slice so `"jobs"` follows `"backup"` (membership only, order is cosmetic).

- [ ] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/tui/ -run 'TestApp_RailShowsExactlySevenViews|TestApp_NumberKeysSkipHiddenViews|TestJobs_OnTheRailUnderBackup|TestSettings_NoJobsLauncher|TestSettings' -v`
Expected: FAIL — registry has 6 commands; number key 3 = snapshots; settings carries a launcher for "jobs".

- [ ] **Step 3: Move `jobs` onto the rail**

In `internal/tui/app.go` views slice, move the `jobs` line up and comment it:
```go
		{id: "backup", model: NewBackupView(deps)},
		// Scheduled backups sits directly under Backup: the wizard's schedule
		// step lands here, so what you just scheduled is one row away.
		{id: "jobs", model: NewJobsView(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
```
Delete the `{id: "jobs", ...}` line from the hidden tail. Categories:
```go
	categories := map[string]string{
		"backup": "Operations", "jobs": "Operations", "maintenance": "Operations",
		"settings": "Settings",
	}
```
Remove `"jobs": true,` from `hiddenFromRail` and fix the comment there (`recovery-kit/password/setup in Settings`). Update the comments at app.go:299-304 ("seven visible destinations") and :1136-1139 ("8 with a seven-view rail is a no-op, not a teleport to whatever hides at index 7").

In `internal/tui/settings.go` delete the `Scheduled backups` entry line. In `internal/tui/help.go`:
```go
	"backup":      "Snapshot a folder into the repository",
	"jobs":        "Scheduled backups: cadence, next run, edit, run now",
	"snapshots":   "Browse snapshots, inspect, diff, and restore",
	"maintenance": "Check, prune, sync, and doctor in one place",
	"settings":    "Configuration and recovery",
```
In `internal/cli/ui.go:102`:
```go
		Long:          "Open the full-screen TUI: a seven-view rail (Dashboard, Backup, Scheduled backups, Snapshots, Maintenance, Settings, Help). Digits 1-7 jump to a view, ctrl+p opens the command palette, q quits.",
```

- [ ] **Step 4: Run the whole tui package + cli**

Run: `go test -race ./internal/tui/ ./internal/cli/`
Expected: PASS. If `help_test.go` or `app_fuzz_test.go` fail on a count, read the failure — the fuzz test derives the rail bound from `app.sidebar.list.Items()` so it should self-adjust; a help test asserting every registry command has a description passes because `jobs` now has one.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/settings.go internal/tui/help.go internal/cli/ui.go internal/tui/rail_test.go internal/tui/settings_test.go internal/tui/jobs_test.go internal/tui/app_test.go
git commit -m "tui: put Scheduled backups on the rail under Backup

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: Picker wording — the button chooses, it no longer starts

**Files:**
- Modify: `internal/tui/dirpicker.go:300-304` (enterVerb), `:353` (button label)
- Test: `internal/tui/dirpicker_test.go:150-158`, `internal/tui/backup_test.go:482-495`

**Interfaces:**
- Produces: `dirPicker.enterVerb()` returns `"choose " + filepath.Base(cwd)` on the button; button row text `▸ choose the current directory`.

- [ ] **Step 1: Update the two verb tests**

`dirpicker_test.go` ~155:
```go
	if got, want := p.enterVerb(), "choose "+filepath.Base(root); got != want {
		t.Errorf("on the Start button, verb = %q, want %q", got, want)
	}
```
`backup_test.go` `TestBackupStartButtonFooterSaysStart` → rename `TestBackupStartButtonFooterSaysChoose`, `want := "choose " + filepath.Base(root)`. Add to `TestDirPickerEnterVerbNamesWhatEnterActuallyDoes` (after the verb checks):
```go
	if !strings.Contains(p.View(true), "▸ choose the current directory") {
		t.Errorf("button must read 'choose the current directory':\n%s", p.View(true))
	}
```

- [ ] **Step 2: Run, expect failure**

Run: `go test ./internal/tui/ -run 'TestDirPickerEnterVerb|TestBackupStartButtonFooter' -v`
Expected: FAIL with `verb = "start the backup of …", want "choose …"`.

- [ ] **Step 3: Change the wording**

`dirpicker.go` enterVerb:
```go
	if p.onStart() {
		// The button commits the browsed directory to the caller — for the
		// backup wizard that means choosing it and moving to the next step,
		// not starting anything.
		return "choose " + filepath.Base(p.cwd)
	}
```
and the View line: `p.clipRow("▸ choose the current directory")`. Update the comments at dirpicker.go:24-30 that say "backing up the current directory" to "choosing the current directory".

- [ ] **Step 4: Run, expect pass** — `go test -race ./internal/tui/ -run 'DirPicker|Backup'`

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dirpicker.go internal/tui/dirpicker_test.go internal/tui/backup_test.go
git commit -m "tui: the picker button chooses a directory, it does not start a backup

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: `scheduleForm` — the Schedule step's model

**Files:**
- Create: `internal/tui/backup_schedule.go`
- Create: `internal/tui/backup_schedule_test.go`

**Interfaces:**
- Consumes: `boxedField(textinput.Model) string` (exists, internal/tui), `ui.SelectRow`, `ui.FieldBoxOverhead`, `repeatPolicyName(base string) string` (backup_repeat.go), `policycfg.Cadence*`, `policycfg.NormalizeSchedule`, `policycfg.Validate(name, config.PolicyConfig) error`.
- Produces (used by Task 5):
  ```go
  const scheduleOneShot = "one-shot"
  type scheduleControl int // schedCadence, schedName, schedAt, schedWeekday
  type scheduleForm struct { … }
  func newScheduleForm(dir string, policies map[string]config.PolicyConfig) scheduleForm
  func (f scheduleForm) oneShot() bool
  func (f scheduleForm) cadence() string           // one of scheduleCadences
  func (f scheduleForm) controls() []scheduleControl // visible, in tab order
  func (f scheduleForm) capturesText() bool         // name/at focused
  func (f scheduleForm) consumesArrows() bool       // cadence list focused
  func (f *scheduleForm) refocus() tea.Cmd
  func (f *scheduleForm) blur()
  func (f scheduleForm) update(msg tea.KeyMsg) (scheduleForm, tea.Cmd) // everything but enter/esc
  func (f scheduleForm) build() (name string, sched config.PolicySchedule, reuses bool, err error)
  func (f scheduleForm) describe() string           // "one-shot" | "hourly" | "daily at 02:00" | "weekly on sun at 02:00" | "monthly on the 1st at 02:00"
  func (f scheduleForm) setWidth(interior int)
  func (f scheduleForm) view() string
  func uniquePolicyName(base, dir string, policies map[string]config.PolicyConfig) string
  ```

- [ ] **Step 1: Write the failing tests**

`internal/tui/backup_schedule_test.go`:
```go
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
		{1, []scheduleControl{schedCadence, schedName}},                          // hourly
		{2, []scheduleControl{schedCadence, schedName, schedAt}},                 // daily
		{3, []scheduleControl{schedCadence, schedName, schedAt, schedWeekday}},   // weekly
		{4, []scheduleControl{schedCadence, schedName, schedAt}},                 // monthly
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
	f, _ = f.update(keyTab())                             // name
	f, _ = f.update(keyTab())                             // at
	f, _ = f.update(keyTab())                             // weekday
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
```

- [ ] **Step 2: Run to see them fail to compile**

Run: `go test ./internal/tui/ -run 'TestScheduleForm|TestUniquePolicyName' 2>&1 | head`
Expected: `undefined: newScheduleForm` (and friends).

- [ ] **Step 3: Implement `backup_schedule.go`**

```go
package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/ui"
)

// scheduleOneShot is the wizard's word for "no policy": the first row of
// the cadence list. It never reaches disk — build() returns an empty
// schedule for it and the confirm step installs nothing.
const scheduleOneShot = "one-shot"

// scheduleCadences is the Schedule step's list, top to bottom. manual is
// deliberately absent: a policy that never fires on its own is what the
// Scheduled backups tab's add form is for, not a backup you are taking now.
var scheduleCadences = []string{
	scheduleOneShot,
	policycfg.CadenceHourly,
	policycfg.CadenceDaily,
	policycfg.CadenceWeekly,
	policycfg.CadenceMonthly,
}

// scheduleWeekdays are the policy package's weekday tokens, in ←/→ order.
var scheduleWeekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// scheduleControl names which control of the Schedule step owns the keyboard.
type scheduleControl int

const (
	schedCadence scheduleControl = iota
	schedName
	schedAt
	schedWeekday
)

// scheduleForm is the Schedule step's state: a cadence list plus the fields
// that cadence needs. It owns its widgets' focus so the step flag and
// Focused() cannot disagree (refocus/blur are the only paths).
type scheduleForm struct {
	dir      string
	policies map[string]config.PolicyConfig
	cadence  int // index into scheduleCadences
	name     textinput.Model
	at       textinput.Model
	weekday  int // index into scheduleWeekdays
	focus    scheduleControl
	err      string
}

// newScheduleForm builds the step for dir. Nothing is focused: the caller
// focuses on stage entry (constructors and Init focus nothing). The name is
// prefilled with the sanitized basename, uniquified against policies so the
// default can never collide with a policy pointing elsewhere.
func newScheduleForm(dir string, policies map[string]config.PolicyConfig) scheduleForm {
	name := textinput.New()
	name.Prompt = "name>  "
	name.Placeholder = "policy name"
	name.SetValue(uniquePolicyName(repeatPolicyName(filepath.Base(dir)), dir, policies))
	at := textinput.New()
	at.Prompt = "time>  "
	at.Placeholder = "HH:MM"
	at.SetValue("02:00")
	return scheduleForm{
		dir:      dir,
		policies: policies,
		name:     name,
		at:       at,
		weekday:  len(scheduleWeekdays) - 1, // sun, matching the old ctrl+e default
	}
}

// uniquePolicyName returns base, or base-2, base-3, … — the first name that
// is free or already backs up exactly dir (that one is reused, not renamed).
func uniquePolicyName(base, dir string, policies map[string]config.PolicyConfig) string {
	name := base
	for i := 2; ; i++ {
		existing, exists := policies[name]
		if !exists || (len(existing.Paths) == 1 && existing.Paths[0] == dir) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func (f scheduleForm) cadence() string { return scheduleCadences[f.cadence] }
func (f scheduleForm) oneShot() bool   { return f.cadence() == scheduleOneShot }

// controls lists the visible controls in tab order. One-shot shows only the
// list; hourly adds the name; daily/monthly add the time; weekly adds the
// weekday too.
func (f scheduleForm) controls() []scheduleControl {
	c := []scheduleControl{schedCadence}
	switch f.cadence() {
	case scheduleOneShot:
	case policycfg.CadenceHourly:
		c = append(c, schedName)
	case policycfg.CadenceWeekly:
		c = append(c, schedName, schedAt, schedWeekday)
	default:
		c = append(c, schedName, schedAt)
	}
	return c
}

func (f scheduleForm) capturesText() bool  { return f.focus == schedName || f.focus == schedAt }
func (f scheduleForm) consumesArrows() bool { return f.focus == schedCadence }

// refocus blurs both fields, focuses the one f.focus names, and returns its
// Focus() cmd — nil when the keyboard is on the list or the weekday row,
// which have no cursor to blink.
func (f *scheduleForm) refocus() tea.Cmd {
	f.blur()
	switch f.focus {
	case schedName:
		return f.name.Focus()
	case schedAt:
		return f.at.Focus()
	}
	return nil
}

func (f *scheduleForm) blur() {
	f.name.Blur()
	f.at.Blur()
}

// update handles every key but enter and esc, which belong to the wizard's
// stage machine. Tab cycles the visible controls; ↑↓ move the list (and
// re-clamp focus, since the visible set may shrink); ←/→ cycle the weekday;
// everything else types into the focused field.
func (f scheduleForm) update(msg tea.KeyMsg) (scheduleForm, tea.Cmd) {
	f.err = ""
	switch {
	case msg.Type == tea.KeyTab:
		visible := f.controls()
		next := 0
		for i, c := range visible {
			if c == f.focus {
				next = (i + 1) % len(visible)
			}
		}
		f.focus = visible[next]
		cmd := f.refocus()
		return f, cmd
	case f.focus == schedCadence && msg.Type == tea.KeyUp:
		f.cadence = max(f.cadence-1, 0)
		return f, nil
	case f.focus == schedCadence && msg.Type == tea.KeyDown:
		f.cadence = min(f.cadence+1, len(scheduleCadences)-1)
		return f, nil
	case f.focus == schedWeekday && msg.Type == tea.KeyRight:
		f.weekday = (f.weekday + 1) % len(scheduleWeekdays)
		return f, nil
	case f.focus == schedWeekday && msg.Type == tea.KeyLeft:
		f.weekday = (f.weekday + len(scheduleWeekdays) - 1) % len(scheduleWeekdays)
		return f, nil
	case f.focus == schedName:
		var cmd tea.Cmd
		f.name, cmd = f.name.Update(msg)
		return f, cmd
	case f.focus == schedAt:
		var cmd tea.Cmd
		f.at, cmd = f.at.Update(msg)
		return f, cmd
	}
	return f, nil
}

// schedule assembles the PolicySchedule the current controls describe.
func (f scheduleForm) schedule() config.PolicySchedule {
	s := config.PolicySchedule{Cadence: f.cadence()}
	switch f.cadence() {
	case policycfg.CadenceDaily, policycfg.CadenceMonthly:
		s.At = strings.TrimSpace(f.at.Value())
	case policycfg.CadenceWeekly:
		s.At = strings.TrimSpace(f.at.Value())
		s.Weekday = scheduleWeekdays[f.weekday]
	}
	return policycfg.NormalizeSchedule(s)
}

// build validates the step. One-shot yields empties and no error. Otherwise
// the name and schedule go through policy.Validate (name rules, HH:MM,
// weekday), then the wizard's own rule: a name that exists for a DIFFERENT
// directory is refused naming that directory; the same directory is reused
// (reuses=true) so the confirm step can say "updates policy".
func (f scheduleForm) build() (name string, sched config.PolicySchedule, reuses bool, err error) {
	if f.oneShot() {
		return "", config.PolicySchedule{}, false, nil
	}
	name = strings.TrimSpace(f.name.Value())
	sched = f.schedule()
	p := config.PolicyConfig{Paths: []string{f.dir}, Schedule: sched}
	if err := policycfg.Validate(name, p); err != nil {
		return "", config.PolicySchedule{}, false, err
	}
	if existing, ok := f.policies[name]; ok {
		if len(existing.Paths) == 1 && existing.Paths[0] == f.dir {
			return name, sched, true, nil
		}
		return "", config.PolicySchedule{}, false,
			fmt.Errorf("policy %q already backs up %s — choose another name", name, strings.Join(existing.Paths, ", "))
	}
	return name, sched, false, nil
}

// describe renders the cadence for the confirm summary and the done screen.
func (f scheduleForm) describe() string {
	s := f.schedule()
	switch f.cadence() {
	case scheduleOneShot:
		return scheduleOneShot
	case policycfg.CadenceHourly:
		return "hourly"
	case policycfg.CadenceWeekly:
		return fmt.Sprintf("weekly on %s at %s", s.Weekday, s.At)
	case policycfg.CadenceMonthly:
		return "monthly on the 1st at " + s.At
	default:
		return "daily at " + s.At
	}
}

// setWidth sizes both fields from the pane interior, reserving the box.
func (f *scheduleForm) setWidth(interior int) {
	w := max(interior-7-1-ui.FieldBoxOverhead, 10) // 7-cell prompt, 1 cursor
	f.name.Width = w
	f.at.Width = w
}

func (f scheduleForm) view() string {
	var b strings.Builder
	b.WriteString(ui.Muted.Render("How often should this directory be backed up?"))
	b.WriteString("\n\n")
	for i, c := range scheduleCadences {
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(f.focus == schedCadence && i == f.cadence, "  "+c))
	}
	for _, c := range f.controls() {
		switch c {
		case schedName:
			fmt.Fprintf(&b, "\n%s", boxedField(f.name))
		case schedAt:
			fmt.Fprintf(&b, "\n%s", boxedField(f.at))
		case schedWeekday:
			fmt.Fprintf(&b, "\n%s", ui.SelectRow(f.focus == schedWeekday,
				"  weekday: "+scheduleWeekdays[f.weekday]+"   (←/→ to change)"))
		}
	}
	if f.err != "" {
		fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(f.err))
	}
	return b.String()
}
```

Check `boxedField`'s exact signature in `internal/tui` (grep `func boxedField`) and match it.

- [ ] **Step 4: Run the tests**

Run: `go test -race ./internal/tui/ -run 'TestScheduleForm|TestUniquePolicyName' -v`
Expected: PASS. `go vet ./internal/tui/` clean (an unused-type warning cannot occur — tests reference it).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/backup_schedule.go internal/tui/backup_schedule_test.go
git commit -m "tui: scheduleForm — the backup wizard's schedule step model

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: `confirmControls` — the Confirm step's tag field and rescan toggle

**Files:**
- Create: `internal/tui/backup_confirm.go`
- Create: `internal/tui/backup_confirm_test.go`

**Interfaces:**
- Produces (used by Task 5):
  ```go
  type confirmControl int // confirmTag, confirmRescan
  type confirmControls struct { tag textinput.Model; rescan bool; focus confirmControl }
  func newConfirmControls() confirmControls
  func (c *confirmControls) refocus() tea.Cmd
  func (c *confirmControls) blur()
  func (c confirmControls) capturesText() bool
  func (c confirmControls) update(msg tea.KeyMsg) (confirmControls, tea.Cmd) // tab, space, text; not enter/esc
  func (c *confirmControls) setWidth(interior int)
  func (c confirmControls) view() string
  ```

- [ ] **Step 1: Failing tests** — `internal/tui/backup_confirm_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestConfirmControls_TabCyclesTagAndRescan(t *testing.T) {
	c := newConfirmControls()
	if c.tag.Focused() {
		t.Fatal("constructor must focus nothing")
	}
	cmd := c.refocus()
	if !c.tag.Focused() || cmd == nil || !c.capturesText() {
		t.Fatal("refocus on the tag control must focus the field and return its blink cmd")
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	if c.focus != confirmRescan || c.tag.Focused() || c.capturesText() {
		t.Fatalf("tab must move to the rescan row and blur the tag: focus=%v", c.focus)
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	if c.focus != confirmTag || !c.tag.Focused() {
		t.Fatal("tab must wrap back to the tag field, focused")
	}
}

func TestConfirmControls_SpaceTogglesRescanOnlyOnItsRow(t *testing.T) {
	c := newConfirmControls()
	c.refocus()
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace})
	if c.rescan {
		t.Fatal("space in the tag field is a character, not a toggle")
	}
	if c.tag.Value() != " " {
		t.Fatalf("space must type into the tag: %q", c.tag.Value())
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace})
	if !c.rescan {
		t.Fatal("space on the rescan row must arm it")
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace})
	if c.rescan {
		t.Fatal("second space must disarm it")
	}
}

func TestConfirmControls_ViewBoxesOnlyFocusedTagAndMarksRescan(t *testing.T) {
	c := newConfirmControls()
	if n := boxCount(c.view()); n != 0 {
		t.Fatalf("nothing focused: boxCount = %d", n)
	}
	if !strings.Contains(c.view(), "[ ] force a full rescan") {
		t.Fatalf("rescan row must render unchecked:\n%s", c.view())
	}
	c.refocus()
	if n := boxCount(c.view()); n != 1 {
		t.Fatalf("tag focused: boxCount = %d, want 1", n)
	}
	c, _ = c.update(tea.KeyMsg{Type: tea.KeyTab})
	c, _ = c.update(tea.KeyMsg{Type: tea.KeySpace})
	if !strings.Contains(c.view(), "[x] force a full rescan") || !strings.Contains(c.view(), "▍") {
		t.Fatalf("armed rescan row must render checked and glyph-selected:\n%s", c.view())
	}
}
```

- [ ] **Step 2: Run, expect `undefined: newConfirmControls`**

Run: `go test ./internal/tui/ -run TestConfirmControls 2>&1 | head -3`

- [ ] **Step 3: Implement `backup_confirm.go`**

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/ui"
)

// confirmControl names which control of the Confirm step owns the keyboard.
type confirmControl int

const (
	confirmTag confirmControl = iota
	confirmRescan
)

// confirmControls are the two per-run knobs the Confirm step offers beneath
// its summary: an optional tag and the force-rescan toggle. They replaced
// the old configure-screen tag field and the ctrl+r chord — the run's
// adjustments belong where the operator reads what the run will do.
type confirmControls struct {
	tag    textinput.Model
	rescan bool
	focus  confirmControl
}

// newConfirmControls focuses nothing; the stage entry calls refocus.
func newConfirmControls() confirmControls {
	tag := textinput.New()
	tag.Prompt = "tag>  "
	tag.Placeholder = "optional label"
	return confirmControls{tag: tag}
}

// refocus blurs the tag, re-focuses it if it owns the keyboard, and returns
// Focus()'s cmd (nil on the rescan row, which has no cursor).
func (c *confirmControls) refocus() tea.Cmd {
	c.tag.Blur()
	if c.focus == confirmTag {
		return c.tag.Focus()
	}
	return nil
}

func (c *confirmControls) blur()             { c.tag.Blur() }
func (c confirmControls) capturesText() bool { return c.focus == confirmTag && c.tag.Focused() }

// update handles tab (cycle), space on the rescan row (toggle), and typing
// into the tag. Enter and esc belong to the wizard.
func (c confirmControls) update(msg tea.KeyMsg) (confirmControls, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyTab:
		if c.focus == confirmTag {
			c.focus = confirmRescan
		} else {
			c.focus = confirmTag
		}
		cmd := c.refocus()
		return c, cmd
	case c.focus == confirmRescan:
		if msg.Type == tea.KeySpace {
			c.rescan = !c.rescan
		}
		return c, nil
	default:
		var cmd tea.Cmd
		c.tag, cmd = c.tag.Update(msg)
		return c, cmd
	}
}

// setWidth sizes the tag from the pane interior: 6-cell prompt, 1 cursor,
// and the box's cells reserved unconditionally so focusing never resizes.
func (c *confirmControls) setWidth(interior int) {
	c.tag.Width = max(interior-6-1-ui.FieldBoxOverhead, 10)
}

func (c confirmControls) view() string {
	var b strings.Builder
	b.WriteString(boxedField(c.tag))
	box := "[ ]"
	if c.rescan {
		box = "[x]"
	}
	fmt.Fprintf(&b, "\n%s", ui.SelectRow(c.focus == confirmRescan,
		fmt.Sprintf("  %s force a full rescan (every file re-read; space toggles)", box)))
	return b.String()
}
```

- [ ] **Step 4: Run** — `go test -race ./internal/tui/ -run TestConfirmControls -v` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/backup_confirm.go internal/tui/backup_confirm_test.go
git commit -m "tui: confirmControls — the backup wizard's tag field and rescan toggle

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Rewire `BackupView` into the three-step wizard

This is the load-bearing task: the stage machine, key routing, focus seams, install-then-run, and the chat seed. It deletes the chords and the modal.

**Files:**
- Modify: `internal/tui/backup.go` (most of it), `internal/tui/backup_repeat.go` (`installRepeat` signature; delete `nextRepeat`)
- Test: rewrite `internal/tui/backup_test.go` (the tests listed in Step 1), rewrite `internal/tui/backup_repeat_test.go`

**Interfaces:**
- Consumes: `scheduleForm` (Task 3), `confirmControls` (Task 4), `dirPicker` (`activate`, `enterVerb`, `View`, `previewView`), `startOpMsg`/`opTick`/`opRejectedMsg`/`backupDoneMsg` (unchanged), `activateMsg{id}`, `chatBackupMsg{dir, tag}`, `viewShownMsg`/`viewHiddenMsg`, `policycfg.NextRun`.
- Produces:
  ```go
  const ( backupLocation backupStage = iota; backupSchedule; backupConfirm; backupRunning; backupDone )
  type BackupView struct {
      deps Deps; stage backupStage; picker dirPicker
      sched scheduleForm; confirm confirmControls
      pending string   // the chosen directory (set on leaving Location)
      pathErr, notice string
      installedName string; installedNext time.Time; installedNextOK bool // for Done
      now func() time.Time                       // test seam, default time.Now
      schedGOOS, schedHome, schedExe string
      reporter *opReporter; bar progress.Model; result backupDoneMsg; width, height int
  }
  func (v BackupView) installRepeat(root, name string, schedule config.PolicySchedule, tag string) error
  ```

- [ ] **Step 1: Replace the old tests**

In `backup_test.go` DELETE: `confirmModalFrom`, `TestBackupFlow_EnterOnStartButtonStarts`, `TestApp_BackupConfirmationFlowEndToEnd`, `TestApp_BackupConfirmationEscCancels`, `TestBackupFocusSeamsFollowTheFocusedControl`, `TestBackupFirstEnterRaisesConfirmation`, `TestBackupTagFieldEnterRaisesConfirmation`, `TestBackupFlow_RescanToggle`, `TestBackup_TagFieldIsBoxedOnlyWhenFocused`, `TestBackup_TabToTagFieldSchedulesBlink`, `TestBackup_RoutesBlinkTicksWhileTagFocused`, `TestBackup_StartingTheBackupBlursTheTagField`, `TestBackup_RejectedStartRefocusesTheTagField`. In `TestBackupFlow_VanishedFolderRefusesToStart` replace `backupConfigure` with `backupLocation`. Fix any other `backupConfigure` reference (`grep -n backupConfigure internal/tui/*_test.go`).

ADD to `backup_test.go` (imports already include `os`, `path/filepath`, `strings`, `testing`, `time`, `tea`, `repo`; add `"github.com/markgustetic/sentra/internal/config"` and `policycfg "github.com/markgustetic/sentra/internal/policy"` if used):

```go
// toSchedule walks a Location-stage view to the Schedule step by pressing
// enter on the button.
func toSchedule(t *testing.T, v BackupView) BackupView {
	t.Helper()
	m, _ := onStartButton(v).Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupSchedule {
		t.Fatalf("enter on the button: stage = %v, want backupSchedule (pathErr=%q)", got.stage, got.pathErr)
	}
	return got
}

// toConfirm walks on to the Confirm step (one-shot unless the caller moved
// the cadence first).
func toConfirm(t *testing.T, v BackupView) (BackupView, tea.Cmd) {
	t.Helper()
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(BackupView)
	if got.stage != backupConfirm {
		t.Fatalf("enter on the schedule step: stage = %v, want backupConfirm (err=%q)", got.stage, got.sched.err)
	}
	return got, cmd
}

// TestBackupWizard_StepsForwardAndBack: enter advances Location → Schedule →
// Confirm; esc steps back; Location's esc belongs to the shell.
func TestBackupWizard_StepsForwardAndBack(t *testing.T) {
	v := backupAt(t, tempTree(t))
	if v.stage != backupLocation || v.ConsumesEscape() {
		t.Fatal("a fresh view is on Location and leaves esc to the shell")
	}
	v = toSchedule(t, v)
	if !v.ConsumesEscape() || !v.ConsumesTab() || !v.ConsumesArrows() || v.CapturesText() {
		t.Fatal("Schedule with the list focused: esc/tab/arrows are ours, text is not captured")
	}
	if v.pending == "" {
		t.Fatal("leaving Location must record the chosen directory")
	}
	v, _ = toConfirm(t, v)
	if !v.ConsumesEscape() || !v.ConsumesTab() || v.ConsumesArrows() || !v.CapturesText() {
		t.Fatal("Confirm with the tag focused: esc/tab ours, text captured, arrows to the shell")
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(BackupView)
	if v.stage != backupSchedule || v.confirm.tag.Focused() {
		t.Fatalf("esc on Confirm → Schedule with the tag blurred; stage=%v", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(BackupView)
	if v.stage != backupLocation || v.sched.name.Focused() || v.sched.at.Focused() {
		t.Fatalf("esc on Schedule → Location with its fields blurred; stage=%v", v.stage)
	}
}

// Enter on a folder row navigates; only the button advances.
func TestBackupWizard_EnterOnFolderRowDoesNotAdvance(t *testing.T) {
	v := backupAt(t, tempTree(t))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // onto ".."
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.(BackupView); got.stage != backupLocation {
		t.Fatalf("enter on a folder row must navigate, not advance; stage=%v", got.stage)
	}
}

// The header names the step so the operator always knows where they are.
func TestBackupWizard_HeaderNamesTheStep(t *testing.T) {
	v := backupAt(t, tempTree(t))
	for _, want := range []string{"New backup", "Step 1 of 3", "Location"} {
		if !strings.Contains(v.View(), want) {
			t.Errorf("Location view lacks %q:\n%s", want, v.View())
		}
	}
	v = toSchedule(t, v)
	if !strings.Contains(v.View(), "Step 2 of 3") || !strings.Contains(v.View(), "Schedule") {
		t.Errorf("Schedule view lacks its header:\n%s", v.View())
	}
	v, _ = toConfirm(t, v)
	if !strings.Contains(v.View(), "Step 3 of 3") || !strings.Contains(v.View(), "Confirm") {
		t.Errorf("Confirm view lacks its header:\n%s", v.View())
	}
}

// Confirm's summary names the directory and the schedule; one-shot installs
// nothing and enter starts the op through the one-op guard with the seeded
// first tick.
func TestBackupWizard_OneShotConfirmStartsTheBackup(t *testing.T) {
	root := tempTree(t)
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, root)))
	if !strings.Contains(v.View(), root) || !strings.Contains(v.View(), "one-shot") {
		t.Fatalf("confirm summary must name the directory and one-shot:\n%s", v.View())
	}
	v.confirm.tag.SetValue("nightly")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if v.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning (pathErr=%q)", v.stage, v.pathErr)
	}
	if v.confirm.tag.Focused() {
		t.Error("starting must blur the tag field")
	}
	var foundStart, foundTick bool
	for _, msg := range execCmds(t, cmd) {
		switch mm := msg.(type) {
		case startOpMsg:
			foundStart = true
			if mm.name != "backup" {
				t.Errorf("op name = %q, want backup", mm.name)
			}
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart || !foundTick {
		t.Errorf("start=%v tick=%v; both must be batched", foundStart, foundTick)
	}
}

// The rescan toggle and the tag reach SnapshotOptions: prove it end to end by
// running the op and reading the snapshot back.
func TestBackupWizard_TagReachesTheSnapshot(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := toConfirm(t, toSchedule(t, backupAtRepo(t, r, src)))
	v.confirm.tag.SetValue("nightly")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	for _, msg := range execCmds(t, cmd) {
		if start, ok := msg.(startOpMsg); ok {
			done := start.run(context.Background()).(backupDoneMsg)
			if done.err != nil {
				t.Fatalf("backup failed: %v", done.err)
			}
			if done.info.Tag != "nightly" {
				t.Fatalf("snapshot tag = %q, want nightly", done.info.Tag)
			}
			return
		}
	}
	t.Fatal("no startOpMsg emitted")
}

func TestBackupWizard_RescanToggleReachesOptions(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})   // rescan row
	m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(BackupView)
	if !v.confirm.rescan || !strings.Contains(v.View(), "[x]") {
		t.Fatalf("space on the rescan row must arm it:\n%s", v.View())
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // enter from the rescan row confirms too
	if got := m.(BackupView); got.stage != backupRunning {
		t.Fatalf("enter on the rescan row must confirm; stage=%v", got.stage)
	}
}

// Focus seams on Schedule: tab onto the name field captures text, blinks,
// boxes; viewHiddenMsg blurs; viewShownMsg re-focuses the stage's field.
func TestBackupWizard_ScheduleFieldFocusSeams(t *testing.T) {
	v := toSchedule(t, backupAt(t, tempTree(t)))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // hourly: name appears
	v = m.(BackupView)
	v.sched.name.Cursor.BlinkSpeed = time.Millisecond
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(BackupView)
	assertBlinkCmd(t, cmd)
	if !v.CapturesText() || v.ConsumesArrows() || boxCount(v.View()) != 1 {
		t.Fatalf("name focused: captures=%v arrows=%v boxes=%d", v.CapturesText(), v.ConsumesArrows(), boxCount(v.View()))
	}
	m, _ = v.Update(viewHiddenMsg{})
	if got := m.(BackupView); got.sched.name.Focused() {
		t.Fatal("viewHiddenMsg must blur the schedule field")
	}
	m, showCmd := m.(BackupView).Update(viewShownMsg{})
	if got := m.(BackupView); !got.sched.name.Focused() {
		t.Fatal("viewShownMsg must re-focus the field the stage owns")
	}
	assertBlinkCmd(t, showCmd)
}

// A chat intent lands on Confirm with the directory and tag seeded, one-shot,
// and the tag focused — the confirm screen is the human gate the chat needs.
func TestBackupWizard_ChatIntentLandsOnConfirm(t *testing.T) {
	src := tempTree(t)
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, cmd := m.(BackupView).Update(chatBackupMsg{dir: src, tag: "from-chat"})
	v = m.(BackupView)
	if v.stage != backupConfirm || v.pending != src || v.confirm.tag.Value() != "from-chat" || !v.sched.oneShot() {
		t.Fatalf("chat seed: stage=%v pending=%q tag=%q oneShot=%v", v.stage, v.pending, v.confirm.tag.Value(), v.sched.oneShot())
	}
	if !v.confirm.tag.Focused() {
		t.Fatal("landing on Confirm focuses the tag")
	}
	assertBlinkCmd(t, cmd)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.(BackupView); got.stage != backupRunning {
		t.Fatalf("enter on the seeded Confirm starts; stage=%v", got.stage)
	}
}

// A refused start (another op running) returns to Confirm with the tag
// re-focused, so the operator can retry without re-walking the wizard.
func TestBackupWizard_RejectedStartReturnsToConfirm(t *testing.T) {
	v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m, cmd := m.(BackupView).Update(opRejectedMsg{name: "backup"})
	v = m.(BackupView)
	if v.stage != backupConfirm || !v.confirm.tag.Focused() || v.notice == "" {
		t.Fatalf("rejection: stage=%v tagFocused=%v notice=%q", v.stage, v.confirm.tag.Focused(), v.notice)
	}
	assertBlinkCmd(t, cmd)
}
```
Check `opRejectedMsg`'s field name (`name`) in ops.go before relying on it.

Rewrite `backup_repeat_test.go`: keep `repeatFixture` and `TestRepeatPolicyName_Sanitizes`; delete `TestBackup_CtrlECyclesRepeat`, `TestBackup_ConfirmBodyNamesRepeat`, `TestBackup_PolicyNameCollisionUniquified`; replace the two install tests:

```go
// atDailyConfirm walks the fixture to Confirm with daily@02:00 chosen for dir.
func atDailyConfirm(t *testing.T, v BackupView, dir string) BackupView {
	t.Helper()
	v.picker = newDirPicker(dir)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = toSchedule(t, m.(BackupView))
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown}) // hourly
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
```
(Look at the old `TestBackup_ScheduleFailureBlocksBackup` for how it forced the failure — reuse that mechanism if `plan9` does not make `scheduler.PathsFor` fail; check `internal/scheduler`.)

- [ ] **Step 2: Run, expect compile failures** — `go test ./internal/tui/ 2>&1 | head` → `undefined: backupSchedule`, `v.sched undefined`, …

- [ ] **Step 3: Rewrite `installRepeat`**

In `backup_repeat.go` delete `nextRepeat`; change the signature and body:
```go
// installRepeat persists policy `name` for root (path + tag + schedule)
// into sentra.yaml and installs the OS scheduler entry that runs it. The
// wizard's Schedule step resolved the name and cadence; this only writes
// them. The collision check inside config.Update's closure runs against the
// on-disk map (a concurrent edit can't be lost) and REFUSES a name owned by
// another directory rather than uniquifying: the operator just confirmed
// this name, and silently renaming it would lie to them. The same
// directory reuses its policy — cadence and tag refresh, config-authored
// hooks survive, mirroring `policy add --replace`.
func (v BackupView) installRepeat(root, name string, schedule config.PolicySchedule, tag string) error {
	if strings.TrimSpace(v.deps.ConfigPath) == "" {
		return fmt.Errorf("no config file to hold the policy — run setup first")
	}
	schedule = policycfg.NormalizeSchedule(schedule)
	var tags []string
	if tag = strings.TrimSpace(tag); tag != "" {
		tags = []string{tag}
	}
	err := config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if existing, exists := cfg.Policies[name]; exists &&
			!(len(existing.Paths) == 1 && existing.Paths[0] == root) {
			return fmt.Errorf("policy %q already backs up %s", name, strings.Join(existing.Paths, ", "))
		}
		p := cfg.Policies[name] // zero value when new; hooks survive when reused
		p.Paths = []string{root}
		p.Tags = tags
		p.Schedule = schedule
		cfg.Policies[name] = p
		return nil
	})
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(v.schedGOOS, v.schedHome, name)
	if err != nil {
		return err
	}
	exe, err := scheduler.Executable(v.schedExe)
	if err != nil {
		return err
	}
	files, err := scheduler.Render(paths, exe, v.deps.ConfigPath, name, schedule)
	if err != nil {
		return err
	}
	if err := scheduler.Install(files); err != nil {
		return err
	}
	// Mirror disk in the shared resolved config so the Scheduled backups
	// tab lists the policy without a relaunch.
	if v.deps.Config != nil {
		if v.deps.Config.Policies == nil {
			v.deps.Config.Policies = map[string]config.PolicyConfig{}
		}
		p := v.deps.Config.Policies[name]
		p.Paths = []string{root}
		p.Tags = tags
		p.Schedule = schedule
		v.deps.Config.Policies[name] = p
	}
	return nil
}
```
Drop the now-unused `path/filepath` import if nothing else in the file uses it (`repeatPolicyName` does not).

- [ ] **Step 4: Rewrite `backup.go`**

Replace the stage enum, the `backupFocus` type, `backupConfirmID`, and the struct:
```go
// backupStage is the wizard's position: three configure steps, then the run.
type backupStage int

const (
	backupLocation backupStage = iota
	backupSchedule
	backupConfirm
	backupRunning
	backupDone
)

// BackupView is the three-step backup wizard: Location (folder picker) →
// Schedule (one-shot or a cadence that becomes a named policy + OS timer) →
// Confirm (summary, tag, rescan; the gate) → running → done. Each
// configure step owns its widgets through a small struct so focus follows
// the stage: entering a stage focuses its default field, leaving it blurs.
type BackupView struct {
	deps    Deps
	stage   backupStage
	picker  dirPicker
	sched   scheduleForm
	confirm confirmControls
	pending string // the directory chosen on Location
	pathErr string
	notice  string // transient banner, e.g. after an op rejection

	// installedName/Next record the schedule Confirm installed, for the
	// done screen's "next run" line.
	installedName   string
	installedNext   time.Time
	installedNextOK bool

	// now pins the clock for next-run rendering; schedGOOS/schedHome/
	// schedExe are the scheduler seams (empty = production defaults).
	now                            func() time.Time
	schedGOOS, schedHome, schedExe string

	reporter *opReporter
	bar      progress.Model
	result   backupDoneMsg
	width    int
	height   int
}

func NewBackupView(deps Deps) BackupView {
	start, err := os.Getwd()
	if err != nil {
		start = ""
	}
	return BackupView{
		deps:    deps,
		picker:  newDirPicker(start),
		confirm: newConfirmControls(),
		now:     time.Now,
		bar:     progress.New(progress.WithDefaultGradient()),
	}
}
```
(`sched` is built on entering Schedule, from the chosen directory; the zero value is fine on Location. Add `"time"` and `policycfg "github.com/markgustetic/sentra/internal/policy"` and `"github.com/markgustetic/sentra/internal/config"` imports; drop `textinput` and `cursor` only if unused — `cursor.BlinkMsg` is still routed.)

Seams:
```go
func (v BackupView) CapturesText() bool {
	switch v.stage {
	case backupSchedule:
		return v.sched.capturesText()
	case backupConfirm:
		return v.confirm.capturesText()
	}
	return false
}

func (v BackupView) ConsumesArrows() bool {
	switch v.stage {
	case backupLocation:
		return true
	case backupSchedule:
		return v.sched.consumesArrows()
	}
	return false
}

func (v BackupView) ConsumesTab() bool { return v.stage == backupSchedule || v.stage == backupConfirm }

// ConsumesEscape on Schedule/Confirm (step back) and while running (cancel).
// On Location esc belongs to the shell — it is how the operator leaves.
func (v BackupView) ConsumesEscape() bool {
	return v.stage == backupSchedule || v.stage == backupConfirm || v.stage == backupRunning
}
```

`ShortHelp` per stage:
```go
	case backupLocation:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open/choose")),
			key.NewBinding(key.WithKeys("backspace"), key.WithHelp("bksp", "up a level")),
		}
	case backupSchedule:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "cadence")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case backupConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "tag/rescan")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "start")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case backupDone:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again")),
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "scheduled backups")),
		}
```
(The `s` binding is wired in Task 6; listing it here is harmless. If you prefer, add it in Task 6.)

Stage transitions (new helpers; every entry returns the focus cmd, every exit blurs):
```go
// enterSchedule leaves Location for Schedule: the picker's directory becomes
// pending and a fresh scheduleForm is built for it against the known
// policies. Nothing is focused — the list has the keyboard.
func (v BackupView) enterSchedule(dir string) (tea.Model, tea.Cmd) {
	v.pending = dir
	var policies map[string]config.PolicyConfig
	if v.deps.Config != nil {
		policies = v.deps.Config.Policies
	}
	v.sched = newScheduleForm(dir, policies)
	v.sched.setWidth(pickerContentWidth(v.width))
	v.stage = backupSchedule
	return v, nil
}

// enterConfirm leaves Schedule for Confirm with the tag field focused.
func (v BackupView) enterConfirm() (tea.Model, tea.Cmd) {
	v.sched.blur()
	v.stage = backupConfirm
	v.pathErr = ""
	v.confirm.focus = confirmTag
	cmd := v.confirm.refocus()
	return v, cmd
}

// backTo steps the wizard back one stage, blurring what it leaves.
func (v BackupView) backTo(stage backupStage) (tea.Model, tea.Cmd) {
	v.sched.blur()
	v.confirm.blur()
	v.pathErr = ""
	v.stage = stage
	if stage == backupSchedule {
		v.sched.err = ""
		v.sched.focus = schedCadence
	}
	return v, nil
}

// checkDir is the cheap validation both the Location step and Confirm run:
// stat says directory, and a repo is configured. The walker surfaces
// everything else.
func (v *BackupView) checkDir(root string) bool {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		v.pathErr = fmt.Sprintf("directory not found: %s", root)
		return false
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return false
	}
	v.pathErr = ""
	return true
}
```

`Update` cases to change:
```go
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		if interior := pickerContentWidth(msg.Width); previewPaneWidth(interior) > 0 {
			v.picker.width = pickerColWidth
		} else {
			v.picker.width = interior
		}
		v.sched.setWidth(pickerContentWidth(msg.Width))
		v.confirm.setWidth(pickerContentWidth(msg.Width))
		return v, nil

	case opRejectedMsg:
		// Our start was refused; return to Confirm (not Location — the
		// operator's choices stand) with the tag re-focused for the retry.
		if v.stage == backupRunning && msg.name == "backup" {
			v.stage = backupConfirm
			v.notice = "another operation is in progress — try again when it finishes"
			cmd := v.confirm.refocus()
			return v, cmd
		}
		return v, nil

	case chatBackupMsg:
		// The chat's start_backup intent lands on Confirm — the same human
		// gate a hand-driven backup reaches — with the directory and tag
		// seeded and one-shot chosen. Ignored mid-flow.
		if v.stage == backupRunning || v.stage == backupDone {
			return v, nil
		}
		dir := strings.TrimSpace(msg.dir)
		if !v.checkDir(dir) {
			v.stage = backupLocation
			return v, nil
		}
		v.picker = newDirPicker(dir)
		m, _ := v.enterSchedule(dir)
		v = m.(BackupView)
		v.confirm = newConfirmControls()
		v.confirm.setWidth(pickerContentWidth(v.width))
		if msg.tag != "" {
			v.confirm.tag.SetValue(msg.tag)
		}
		return v.enterConfirm()

	case viewShownMsg:
		switch v.stage {
		case backupSchedule:
			cmd := v.sched.refocus()
			return v, cmd
		case backupConfirm:
			cmd := v.confirm.refocus()
			return v, cmd
		}
		return v, nil

	case viewHiddenMsg:
		v.sched.blur()
		v.confirm.blur()
		return v, nil

	case cursor.BlinkMsg:
		var cmd tea.Cmd
		switch {
		case v.sched.name.Focused():
			v.sched.name, cmd = v.sched.name.Update(msg)
		case v.sched.at.Focused():
			v.sched.at, cmd = v.sched.at.Update(msg)
		case v.confirm.tag.Focused():
			v.confirm.tag, cmd = v.confirm.tag.Update(msg)
		}
		return v, cmd
```
Delete the `confirmedMsg` case entirely.

`handleKey`:
```go
func (v BackupView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case backupRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	case backupDone:
		if msg.Type == tea.KeyEnter {
			return v.resetTo()
		}
		return v, nil

	case backupSchedule:
		v.notice = ""
		switch msg.Type {
		case tea.KeyEsc:
			return v.backTo(backupLocation)
		case tea.KeyEnter:
			if _, _, _, err := v.sched.build(); err != nil {
				v.sched.err = err.Error()
				return v, nil
			}
			return v.enterConfirm()
		}
		var cmd tea.Cmd
		v.sched, cmd = v.sched.update(msg)
		return v, cmd

	case backupConfirm:
		v.notice = ""
		switch msg.Type {
		case tea.KeyEsc:
			return v.backTo(backupSchedule)
		case tea.KeyEnter:
			return v.confirmRun()
		}
		var cmd tea.Cmd
		v.confirm, cmd = v.confirm.update(msg)
		return v, cmd

	default: // backupLocation
		v.notice = ""
		switch msg.Type {
		case tea.KeyUp:
			v.picker = v.picker.moveUp()
		case tea.KeyDown:
			v.picker = v.picker.moveDown()
		case tea.KeyBackspace, tea.KeyLeft:
			v.picker = v.picker.up()
		case tea.KeyRight:
			v.picker, _ = v.picker.activate()
		case tea.KeyEnter:
			var chosen string
			v.picker, chosen = v.picker.activate()
			if chosen == "" {
				return v, nil
			}
			chosen = strings.TrimSpace(chosen)
			if !v.checkDir(chosen) {
				return v, nil
			}
			return v.enterSchedule(chosen)
		}
		return v, nil
	}
}
```

`confirmRun` (in backup_confirm.go, alongside the controls) replaces `requestBackup` + the `confirmedMsg` branch:
```go
// confirmRun is the wizard's gate. Re-check the directory (it can vanish
// between steps), install the schedule if one was chosen — FIRST, so a
// failed install blocks the run rather than degrading a confirmed
// repeating backup to a one-shot — then start the op.
func (v BackupView) confirmRun() (tea.Model, tea.Cmd) {
	if !v.checkDir(v.pending) {
		return v, nil
	}
	v.installedName, v.installedNextOK = "", false
	if !v.sched.oneShot() {
		name, sched, _, err := v.sched.build()
		if err != nil {
			v.pathErr = err.Error()
			return v, nil
		}
		if err := v.installRepeat(v.pending, name, sched, v.confirm.tag.Value()); err != nil {
			v.pathErr = "could not install the schedule: " + err.Error()
			return v, nil
		}
		v.installedName = name
		v.installedNext, v.installedNextOK = policycfg.NextRun(sched, v.now())
	}
	return v.startBackup(v.pending)
}

// confirmSummary renders the read-only block above the controls.
func (v BackupView) confirmSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "directory   %s\n", v.fit(v.pending))
	if v.sched.oneShot() {
		b.WriteString("schedule    one-shot\n")
		return b.String()
	}
	name, sched, reuses, err := v.sched.build()
	if err != nil { // cannot happen: Schedule's enter validated; render honestly anyway
		fmt.Fprintf(&b, "schedule    %s\n", ui.Danger.Render(err.Error()))
		return b.String()
	}
	verb := "installs an OS timer"
	if reuses {
		verb = fmt.Sprintf("updates policy %q", name)
	}
	fmt.Fprintf(&b, "schedule    %s as policy %q — %s\n", v.sched.describe(), name, verb)
	if next, ok := policycfg.NextRun(sched, v.now()); ok {
		fmt.Fprintf(&b, "next run    %s\n", next.Format("Mon 2006-01-02 15:04"))
	}
	return b.String()
}
```
Delete `requestBackup`. In `startBackup`: read `tag := strings.TrimSpace(v.confirm.tag.Value())`, `rescan := v.confirm.rescan`, and replace `v.tag.Blur()` with `v.confirm.blur()`.

Header + `View`:
```go
// header mirrors the setup wizard's: what this is on the left, where you
// are on the right, then the step's title.
func (v BackupView) header(step int, title string) string {
	left := ui.Muted.Render("New backup")
	right := ui.Muted.Render(fmt.Sprintf("Step %d of 3", step))
	gap := max(pickerContentWidth(v.width)-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right + "\n\n" + ui.Primary.Render(title) + "\n"
}
```
`View` cases: running and done unchanged (done gains its extra line in Task 6); the configure stages:
```go
	case backupLocation:
		b.WriteString(v.header(1, "Location"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.fit(v.notice)))
		}
		pickerCol := v.picker.View(true)
		if paneW := previewPaneWidth(pickerContentWidth(v.width)); paneW > 0 {
			left := lipgloss.NewStyle().Width(pickerColWidth).Render(pickerCol)
			pickerCol = lipgloss.JoinHorizontal(lipgloss.Top,
				left, strings.Repeat(" ", previewGapWidth), v.picker.previewView(paneW))
		}
		fmt.Fprintf(&b, "\n%s", pickerCol)
		if v.pathErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.fit(v.pathErr)))
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine(v.picker.enterVerb(), "↑↓ move · → opens · bksp up · esc leaves"))

	case backupSchedule:
		b.WriteString(v.header(2, "Schedule"))
		fmt.Fprintf(&b, "\n%s", v.sched.view())
		fmt.Fprintf(&b, "\n\n%s", v.actionLine("continue to the summary", "tab next field · esc back"))

	case backupConfirm:
		b.WriteString(v.header(3, "Confirm"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.fit(v.notice)))
		}
		fmt.Fprintf(&b, "\n%s\n%s", v.confirmSummary(), v.confirm.view())
		if v.pathErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.fit(v.pathErr)))
		}
		verb := "start the backup of " + filepath.Base(v.pending)
		if !v.sched.oneShot() {
			verb = "install the schedule and " + verb
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine(verb, "tab tag/rescan · esc back"))
```
Keep the `default:` unreachable by listing all five stages explicitly.

- [ ] **Step 5: Run the backup tests**

Run: `go test -race ./internal/tui/ -run 'Backup|InstallRepeat|RepeatPolicyName' -v 2>&1 | grep -v '^=== RUN' | head -60`
Expected: PASS. Then `go test -race ./internal/tui/` — the `fieldfocus_test.go` `backup` row still references `v.tag` and `KeyTab` on Location; it fails to compile. Fix it in the next step (it belongs to this task because the build must be green at the commit).

- [ ] **Step 6: Update the `fieldOwners` rows**

In `fieldfocus_test.go` replace the `backup` entry with two:
```go
		{
			name: "backup, schedule step on the name field",
			focused: func(t *testing.T) tea.Model {
				v := toSchedule(t, backupAt(t, tempTree(t)))
				m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}) // hourly: name appears
				m, _ = m.(BackupView).Update(tea.KeyMsg{Type: tea.KeyTab})
				return m
			},
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(BackupView)
				do(&v.sched.name)
				do(&v.sched.at)
				do(&v.confirm.tag)
				return v
			},
		},
		{
			name: "backup, confirm step on the tag field",
			focused: func(t *testing.T) tea.Model {
				v, _ := toConfirm(t, toSchedule(t, backupAt(t, tempTree(t))))
				return v
			},
			fields: func(m tea.Model, do func(*textinput.Model)) tea.Model {
				v := m.(BackupView)
				do(&v.sched.name)
				do(&v.sched.at)
				do(&v.confirm.tag)
				return v
			},
		},
```
In `TestFieldFocus_ShownFocusesNothingOutsideAFieldStage` add after the picker case:
```go
		{"backup, schedule step on the cadence list", func(t *testing.T) tea.Model {
			return toSchedule(t, backupAt(t, tempTree(t)))
		}},
```

- [ ] **Step 7: Run the whole package, then vet/lint**

Run: `go test -race ./internal/tui/` → PASS. `go vet ./internal/tui/ && golangci-lint run ./internal/tui/` → clean. If `app_fuzz_test.go` invariants trip (e.g. "a Down key never does nothing" on Confirm), the fix is in the seams: Confirm must report `ConsumesArrows() == false` so the rail takes ↓ — already the case above.

- [ ] **Step 8: Commit**

```bash
git add internal/tui/backup.go internal/tui/backup_confirm.go internal/tui/backup_repeat.go internal/tui/backup_test.go internal/tui/backup_repeat_test.go internal/tui/fieldfocus_test.go
git commit -m "tui: backup is a three-step wizard — location, schedule, confirm

The single configure screen with its ctrl+e/ctrl+r chords and yes/no
modal becomes Location → Schedule → Confirm. Schedule offers one-shot,
hourly, daily, weekly, monthly with a time, weekday and editable policy
name; Confirm shows the summary and next run, takes the tag and rescan
toggle, and is the gate. A scheduled confirm installs the policy and
timer first, then runs. The chat intent lands on Confirm.

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Done screen extras and the App-level walk-through

**Files:**
- Modify: `internal/tui/backup.go` (done stage `View` + `handleKey`)
- Test: `internal/tui/backup_test.go`

**Interfaces:**
- Consumes: `activateMsg{id: "jobs"}` (app.go), `installedName/installedNext/installedNextOK` (Task 5).

- [ ] **Step 1: Failing tests**

```go
// Done after a scheduled run names the policy and next run, and `s` jumps
// to the Scheduled backups tab.
func TestBackupWizard_DoneOffersScheduledBackups(t *testing.T) {
	v := backupAt(t, tempTree(t))
	v.installedName = "docs"
	v.installedNext = time.Date(2026, 9, 3, 2, 0, 0, 0, time.UTC)
	v.installedNextOK = true
	m, _ := v.Update(backupDoneMsg{info: repo.SnapshotInfo{ID: "abc"}})
	v = m.(BackupView)
	for _, want := range []string{`policy "docs" installed`, "next run Thu 2026-09-03 02:00", "s scheduled backups"} {
		if !strings.Contains(v.View(), want) {
			t.Errorf("done view lacks %q:\n%s", want, v.View())
		}
	}
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	found := false
	for _, msg := range execCmds(t, cmd) {
		if a, ok := msg.(activateMsg); ok && a.id == "jobs" {
			found = true
		}
	}
	if !found {
		t.Fatal("'s' on Done must emit activateMsg{jobs}")
	}
}

// Keys through the whole wizard at App level: enter (Location) → enter
// (Schedule, one-shot) → enter (Confirm) → running, with no modal raised.
func TestApp_BackupWizardEndToEnd(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := NewApp(Deps{Repo: r, RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = m.(App)
	bi := -1
	for i, v := range app.views {
		if v.id == "backup" {
			bi = i
		}
	}
	app.active, app.focus = bi, focusContent
	bv := app.views[bi].model.(BackupView)
	bv.picker = newDirPicker(src)
	app.views[bi].model = bv

	press := func(k tea.KeyMsg) {
		m, cmd := app.Update(k)
		app = m.(App)
		for _, msg := range execCmds(t, cmd) {
			if _, blink := msg.(cursor.BlinkMsg); blink {
				continue
			}
			m, _ = app.Update(msg)
			app = m.(App)
		}
	}
	stage := func() backupStage { return app.views[bi].model.(BackupView).stage }

	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupSchedule {
		t.Fatalf("after enter on Location: stage = %v", stage())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupConfirm {
		t.Fatalf("after enter on Schedule: stage = %v", stage())
	}
	if len(app.modals) != 0 {
		t.Fatalf("the wizard must raise no modal, got %d", len(app.modals))
	}
	press(tea.KeyMsg{Type: tea.KeyEsc})
	if stage() != backupSchedule {
		t.Fatalf("esc on Confirm must step back, stage = %v", stage())
	}
	press(tea.KeyMsg{Type: tea.KeyEnter})
	press(tea.KeyMsg{Type: tea.KeyEnter})
	if stage() != backupRunning {
		t.Fatalf("after enter on Confirm: stage = %v", stage())
	}
}
```
Add `"github.com/charmbracelet/bubbles/cursor"` to the test imports. If `execCmds` blocks on a blink cmd (it executes cmds), drop `BlinkSpeed` on the fields first or use `drainTurn` from chat_test.go, which already skips `cursor.BlinkMsg` — read it and pick the one that fits.

- [ ] **Step 2: Run, expect failure** — `go test ./internal/tui/ -run 'TestBackupWizard_DoneOffers|TestApp_BackupWizardEndToEnd' -v`

- [ ] **Step 3: Implement**

`handleKey` done case:
```go
	case backupDone:
		switch {
		case msg.Type == tea.KeyEnter:
			return v.resetTo()
		case msg.Type == tea.KeyRunes && string(msg.Runes) == "s" && v.installedName != "":
			return v, func() tea.Msg { return activateMsg{id: "jobs"} }
		}
		return v, nil
```
`View` done case, after the stats block and before the action line:
```go
		if v.installedName != "" {
			line := fmt.Sprintf("policy %q installed", v.installedName)
			if v.installedNextOK {
				line += " — next run " + v.installedNext.Format("Mon 2006-01-02 15:04")
			}
			fmt.Fprintf(&b, "\n\n%s", ui.Success.Render(v.fit(line)))
		}
		secondary := ""
		if v.installedName != "" {
			secondary = "s scheduled backups"
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine("run another backup", secondary))
```
Keep `ShortHelp`'s `s` binding only when `v.installedName != ""` (build the slice conditionally).

- [ ] **Step 4: Run** — `go test -race ./internal/tui/` → PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/backup.go internal/tui/backup_test.go
git commit -m "tui: backup done screen names the installed schedule and jumps to it

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: Docs, full gate, reinstall

**Files:**
- Modify: `README.md:15,250-254,266`, `docs/QUICKSTART.md:334-341`, `AGENTS.md:203-208`, `CLAUDE.md:19`, `docs/architecture.md:202`
- Memory: `/Users/markgustetic/.claude/projects/-Users-markgustetic-Programming-portfolio-sentra/memory/tui-rewrite.md` (one line: the rail is SEVEN views; Backup is a 3-step wizard)

- [ ] **Step 1: Edit the docs**

README.md:15 → `a seven-view rail`. README.md:250-254:
```
otherwise **the dashboard**. The rail holds seven destinations — Dashboard,
Backup, Scheduled backups, Snapshots, Maintenance, Settings, Help — and the
occasional jobs live one keypress inside them: restore and diff launch from a
snapshot row, check/prune/sync/doctor from Maintenance, recovery-kit/
passphrase/setup from Settings. Backup is a three-step wizard: pick the
folder, pick a schedule (one-shot, or hourly/daily/weekly/monthly — that
installs a named policy plus a launchd/systemd timer), confirm.
```
README.md:266 row → `| \`enter\` · \`esc\` (in Backup) | Next wizard step · back a step |`. Update the `backup.png` alt text at README.md:58 to describe the wizard's Location step ("Backup wizard, step 1: folder picker with jump-to places and the live preview pane"). QUICKSTART.md:334-341: seven views, digits `1`-`7`, Settings "holds the recovery kit, passphrase rotation, and re-running setup", and replace the `ctrl+e` sentence with the wizard sentence above. AGENTS.md:203-208: "seven destinations (Dashboard, Backup, Scheduled backups, Snapshots, Maintenance, Settings, Help) … recovery-kit/passphrase/setup from Settings. The jobs view (id `jobs`, title "Scheduled backups") sits on the rail under Backup; the Backup view is a three-step wizard (Location → Schedule → Confirm) whose Schedule step writes a named policy and OS timer through the same `config.Update` + `scheduler` path `policy add`/`schedule install` use." CLAUDE.md:19: "seven-view rail (Dashboard, Backup, Scheduled backups, Snapshots, Maintenance, Settings, Help)". architecture.md:202: "The rail lists seven destinations".

- [ ] **Step 2: Full gate**

```bash
go build ./... && go vet ./... && gofmt -l cmd internal && GOFLAGS=-timeout=40m go test -race ./... && golangci-lint run ./... && go mod tidy -diff && git diff --check
```
Expected: all clean; `gofmt -l` prints nothing.

- [ ] **Step 3: Commit, reinstall, push**

```bash
git add README.md docs/QUICKSTART.md AGENTS.md CLAUDE.md docs/architecture.md
git commit -m "docs: seven-view rail and the backup wizard

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
just install
git push origin main
```
Then note the README `backup.png` screenshot is stale (regenerate per the readme-screenshots procedure) as a follow-up.
