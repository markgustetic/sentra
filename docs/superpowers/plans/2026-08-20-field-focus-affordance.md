# Field Focus Affordance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every focused TUI text field renders inside a rounded aqua box and blinks its cursor; unfocused fields look as they do today.

**Architecture:** One `ui.FieldBox` style in `internal/ui/theme.go`; each view applies it to its focused field's `textinput.View()` and gains two mechanical additions — `textinput.Blink` returned at every focus transition, and `cursor.BlinkMsg` routed to the focused input in `Update`. Palette and modal inputs keep their chrome and gain blink only. Goldens regenerate deliberately at the end.

**Tech Stack:** Go 1.25, Bubbletea, bubbles (`textinput`, `cursor`), lipgloss.

**Spec:** `docs/superpowers/specs/2026-08-20-field-focus-affordance-design.md`

## Global Constraints

- TDD per view: failing test first (box-delta and blink-cmd), watch it fail, minimal wiring, pass, commit.
- The box marks ONLY the focused field. The border is the focus glyph — never rely on color alone.
- Never wrap an already-styled string INLINE; `FieldBox` border wrapping is safe (block-level, `ModalBox` precedent).
- Width rule: any call site assigning `input.Width` from pane width subtracts 4 (2 border + 2 padding columns) so a boxed field never wraps.
- Blink assertions execute the returned command and look for `cursor.BlinkMsg` (possibly inside a `tea.BatchMsg`); blink *timing* is untested by design.
- Goldens (`internal/tui/testdata/`) regenerate ONLY in Task 5, with a diff eyeball; never regenerate to paper over overflow/wrapping.
- Commit per task; `git add` only named files. `-race` only the changed package while iterating; the full gate runs once in Task 5.
- The rule-test shape for boxes is the corner-count delta: focusing a field adds exactly one `╭` to the view's render.

---

### Task 1: `ui.FieldBox` + shared test helpers

**Files:**
- Modify: `internal/ui/theme.go` (beside `PanelFocused`, ~line 66)
- Test: `internal/ui/theme_test.go` (create if absent), `internal/tui/fieldfocus_test.go` (create)

**Interfaces:**
- Consumes: `AccentAqua` (theme.go).
- Produces: `ui.FieldBox` (a `lipgloss.Style`) — every later task renders focused fields through it; `assertBlinkCmd(t *testing.T, cmd tea.Cmd)` and `boxCount(s string) int` in `internal/tui/fieldfocus_test.go` — every later task's tests use them.

- [ ] **Step 1: Write the failing tests**

In `internal/ui/theme_test.go` (match the package's existing test file if one exists; else `package ui`):

```go
func TestFieldBox_RendersRoundedFrame(t *testing.T) {
	out := FieldBox.Render("hello")
	for _, want := range []string{"╭", "╰", "hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("FieldBox output missing %q:\n%s", want, out)
		}
	}
}
```

Create `internal/tui/fieldfocus_test.go`:

```go
package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"
)

// assertBlinkCmd fails t unless cmd (possibly a batch) yields at least
// one cursor.BlinkMsg when executed. Blink TIMING is untestable; the
// contract under test is that a focus transition schedules the blink.
func assertBlinkCmd(t *testing.T, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a blink command, got nil")
	}
	if !yieldsBlink(cmd()) {
		t.Fatal("command did not yield cursor.BlinkMsg")
	}
}

func yieldsBlink(msg tea.Msg) bool {
	switch m := msg.(type) {
	case cursor.BlinkMsg:
		return true
	case tea.BatchMsg:
		for _, c := range m {
			if c != nil && yieldsBlink(c()) {
				return true
			}
		}
	}
	return false
}

// boxCount counts FieldBox frames in a render via the top-left corner.
// The rule every view test asserts: focusing a field adds exactly one.
func boxCount(s string) int { return strings.Count(s, "╭") }
```

(The helper file has no tests of its own; it compiles with the package. The `ui` test is the failing one.)

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ui/ -run TestFieldBox -v`
Expected: FAIL to compile — "undefined: FieldBox"

- [ ] **Step 3: Implement**

In `internal/ui/theme.go`, directly after `PanelFocused`:

```go
	// FieldBox frames the one text field currently accepting input. The
	// border is the focus affordance itself — a glyph, visible without
	// color — so views must apply it only to the focused field. Padding
	// matches Panel so boxed fields align with panel content.
	FieldBox = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).
			BorderForeground(AccentAqua).Padding(0, 1)
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/ui/ && go build ./internal/tui/`
Expected: PASS; tui compiles with the new helper file.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/theme.go internal/ui/theme_test.go internal/tui/fieldfocus_test.go
git commit -m "feat(ui): FieldBox style + shared focus-affordance test helpers"
```

---

### Task 2: Single-field views + blink-only chrome views

**Files:**
- Modify: `internal/tui/unlock.go`, `internal/tui/snapshots.go`, `internal/tui/recoverykit.go`, `internal/tui/backup.go`, `internal/tui/palette.go`, `internal/tui/modal.go`
- Test: `internal/tui/unlock_test.go`, `internal/tui/snapshots_test.go`, `internal/tui/recoverykit_test.go`, `internal/tui/backup_test.go`, `internal/tui/palette_test.go`, `internal/tui/modal_test.go`

**Interfaces:**
- Consumes: `ui.FieldBox`, `assertBlinkCmd`, `boxCount` (Task 1); `textinput.Blink`; `cursor.BlinkMsg`.
- Produces: nothing new — pattern established for Tasks 3–4.

The three mechanical patterns, applied per the checklist below. **Box** (in `View()`, wherever the input renders):

```go
	field := v.input.View()
	if v.input.Focused() {
		field = ui.FieldBox.Render(field)
	}
	// then print `field` exactly where v.input.View() printed before
```

**Blink on focus** (at each `Focus()` transition): return `textinput.Blink` as the cmd (or `tea.Batch(existing, textinput.Blink)` when a cmd already returns). For views that are CONSTRUCTED focused and landed on (unlock), `Init()` returns `textinput.Blink` instead of `nil`. If an App-level check shows the active landing view's `Init` is never invoked by `App.Init`, wire that propagation in `app.go` (authorized for this task) — verify with the unlock app-level test below before assuming.

**Blink routing** (in `Update`, alongside the existing message cases; add the `cursor` import):

```go
	case cursor.BlinkMsg:
		if v.input.Focused() {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(msg)
			return v, cmd
		}
		return v, nil
```

- [ ] **Step 1: Write the failing tests (all six views)**

Append to each view's test file. Exact code for unlock (`unlock_test.go`) — the others repeat this shape with their own constructors/focus triggers from the checklist table:

```go
// The box is the focus glyph: the unlock field is always focused, so its
// render always carries exactly one FieldBox frame.
func TestUnlock_FocusedFieldIsBoxed(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	if got := boxCount(v.View()); got != 1 {
		t.Fatalf("focused unlock field: boxCount = %d, want 1", got)
	}
}

// Landing on unlock must start the cursor blinking.
func TestUnlock_InitSchedulesBlink(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	assertBlinkCmd(t, v.Init())
}

// Blink ticks must reach the focused input so the schedule continues.
func TestUnlock_RoutesBlinkTicks(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	_, cmd := v.Update(cursor.BlinkMsg{})
	if cmd == nil {
		t.Fatal("blink tick was not routed to the focused input")
	}
}
```

Per-view checklist (each gets the three tests above, adapted; "trigger" = how the field gains focus in the test):

| View | Constructor / trigger | Box test | Blink-on-focus test | Tick-routing test |
|---|---|---|---|---|
| unlock | `NewUnlockView(unlockDeps(t, ...))`, focused at construction | render has 1 box | `Init()` blinks | as above |
| snapshots | filter focuses on `/` key (snapshots.go:517) | before `/`: 0 boxes in the filter row; after: +1 | the `/` keypress's cmd blinks | route while filter focused |
| recoverykit | path field focuses at its stage (recoverykit.go:177) | +1 after the stage transition | that transition's cmd blinks | route while focused |
| backup | tag field focuses via its key (backup.go:222) | +1 after | that keypress's cmd blinks | route while focused |
| palette | focused at construction (palette.go:40) | NO box test — chrome exception | opening cmd/`Init` blinks | route |
| modal | text prompt focused at construction (modal.go:178) | NO box test — chrome exception | construction-site cmd blinks | route |

Notes: snapshots' filter row and backup's pane may already render `╭` from other chrome — that is why every box assertion is a DELTA (count before focus vs after), never an absolute, except views like unlock whose `View()` contains no other rounded borders (verify with the unfocused render first; if unlock's count-1 assertion is wrong because of existing chrome, switch it to the delta form too).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'FocusedFieldIsBoxed|SchedulesBlink|RoutesBlinkTicks|Boxed' 2>&1 | head -20`
Expected: FAIL — no boxes rendered, `Init()`/focus cmds are nil or lack BlinkMsg, ticks unrouted.

- [ ] **Step 3: Implement per the checklist**

Apply the three patterns to each of the six files at the sites named in the table (Focus() sites: unlock.go:71+106, snapshots.go:517, recoverykit.go:177, backup.go:222, palette.go:40, modal.go:178). Palette/modal: blink patterns only, no `FieldBox`. Where a `Width` is assigned to one of these inputs from pane width, subtract 4 per the Global Constraints.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run 'TestUnlock|TestSnapshots|TestRecovery|TestBackup|TestPalette|TestModal'` then the full package `go test -race ./internal/tui/`.
Expected: new tests PASS. Golden tests MAY fail here if a boxed field appears in a golden frame — inspect: if the failure is exactly the new box on a focused field, note it in the commit message and leave goldens for Task 5's deliberate regeneration by running the remaining suite with `-run` filters; if the failure shows wrapping/overflow, fix the width per the width rule instead.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/unlock.go internal/tui/snapshots.go internal/tui/recoverykit.go internal/tui/backup.go internal/tui/palette.go internal/tui/modal.go internal/tui/unlock_test.go internal/tui/snapshots_test.go internal/tui/recoverykit_test.go internal/tui/backup_test.go internal/tui/palette_test.go internal/tui/modal_test.go
git commit -m "feat(tui): focused-field box + cursor blink for single-field views"
```

---

### Task 3: Multi-field forms — password, restore, sync, policies

**Files:**
- Modify: `internal/tui/password.go`, `internal/tui/restore.go`, `internal/tui/sync.go`, `internal/tui/policies.go`
- Test: `internal/tui/password_test.go`, `internal/tui/restore_test.go`, `internal/tui/sync_test.go`, `internal/tui/policies_test.go`

**Interfaces:**
- Consumes: `ui.FieldBox`, `assertBlinkCmd`, `boxCount` (Task 1); the Task 2 patterns verbatim.
- Produces: nothing new.

Patterns are the same three as Task 2 (box in `View()` behind `Focused()`; `textinput.Blink` at focus transitions; `cursor.BlinkMsg` routed to the focused field — for multi-field views the routing case checks each field's `Focused()` and updates the one that is). Multi-field routing form:

```go
	case cursor.BlinkMsg:
		var cmd tea.Cmd
		switch {
		case v.dest.Focused():
			v.dest, cmd = v.dest.Update(msg)
		case v.scope.Focused():
			v.scope, cmd = v.scope.Update(msg)
		}
		return v, cmd
```

- [ ] **Step 1: Write the failing tests**

The rule test per form — exact structure for restore (`restore_test.go`); password (`newPass`/`confirmPass`, tab at password.go:169-172), sync (`dstPath`/`snapRefs`, tab at sync.go:215-217), and policies (`form.name/path/tags/schedule`, cycling at policies.go:247-253) repeat the shape with their own field names and focus keys. The rule under test: exactly ONE box, and it FOLLOWS focus — the delta over a fully-blurred render proves the box marks focus, not a fixed field position:

```go
func TestRestore_ExactlyOneBoxAndItFollowsFocus(t *testing.T) {
	v := ... // construct; enter the state where dest is focused (restore.go:197)
	base := ... // the same view with ALL fields blurred (call .Blur() on each, or capture pre-focus state)
	n := boxCount(base.View())
	focused := v.View()
	if boxCount(focused) != n+1 {
		t.Fatalf("focused form: boxCount = %d, want %d (+1 over blurred)", boxCount(focused), n+1)
	}
	tabbed, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // moves focus dest→scope per restore.go:215-218
	if boxCount(tabbed.(RestoreView).View()) != n+1 {
		t.Fatalf("box count changed when focus moved — box must follow focus, one at a time")
	}
	assertBlinkCmd(t, cmd) // the focus transition schedules the blink
	_, tick := tabbed.(RestoreView).Update(cursor.BlinkMsg{})
	if tick == nil {
		t.Fatal("blink tick not routed to the newly focused field")
	}
}
```

Adapt constructor/state entry per file from its existing tests (each of these four test files already constructs its view and drives focus — reuse those fixtures; the view type names are `PasswordView`, `RestoreView`, `SyncView`, `PoliciesView` — confirm by reading each file's constructor).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'ExactlyOneBox|BoxFollowsFocus' 2>&1 | head -20`
Expected: FAIL — zero boxes rendered, no blink cmds.

- [ ] **Step 3: Implement per the checklist**

Focus sites to carry `textinput.Blink`: password.go:82 (construction — the view's entry transition), 169, 172; restore.go:197, 215, 218; sync.go:94 (construction), 215, 217; policies.go:247, 249, 251, 253, 708. Box rendering in each `View()` behind `Focused()`. Width rule where `Width` is assigned.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run 'TestPassword|TestRestore|TestSync|TestPolicies'` then full `go test -race ./internal/tui/` (same golden-failure triage rule as Task 2 Step 4).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/password.go internal/tui/restore.go internal/tui/sync.go internal/tui/policies.go internal/tui/password_test.go internal/tui/restore_test.go internal/tui/sync_test.go internal/tui/policies_test.go
git commit -m "feat(tui): focused-field box + blink across multi-field forms"
```

---

### Task 4: Setup wizard

**Files:**
- Modify: `internal/tui/setup_wizard.go`
- Test: `internal/tui/setup_wizard_test.go`

**Interfaces:**
- Consumes: `ui.FieldBox`, `assertBlinkCmd`, `boxCount` (Task 1); Task 2/3 patterns verbatim.
- Produces: nothing new.

The wizard is the densest form: an S3-details field slice (`v.fields`, constructed at setup_wizard.go:242-252, focus cycling at :843) and the passphrase pair (`newPass`/`confirmPass`, focus sites :450, :554, :712, :715, :1121). Same three patterns:

- Box: in the stage renders, wrap the focused field's `View()` in `ui.FieldBox.Render` — the fields loop renders each `v.fields[i].View()`; box the one where `v.fields[i].Focused()`. Same for the passphrase stage's two inputs.
- Blink: every one of the six focus sites returns/batches `textinput.Blink`. The wizard's `Init`/landing: if the bucket field is focused at construction (setup_wizard.go:252), `Init()` must blink.
- Tick routing: one `cursor.BlinkMsg` case updating whichever field (details slice or passphrase pair) is focused.
- Width rule: the wizard sets field widths — subtract 4 where they track pane width.

- [ ] **Step 1: Write the failing tests**

Append to `setup_wizard_test.go`, reusing its existing constructor/stage-driving helpers (the file has extensive fixtures — mirror how existing tests reach the details stage and the passphrase stage):

```go
// One box, following focus through the details fields.
func TestSetupWizard_BoxFollowsDetailsFocus(t *testing.T) {
	v := ... // reach the S3-details stage via the file's existing helpers
	n := boxCount(v.View())
	moved, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // next field, setup_wizard.go:843
	if boxCount(moved.(SetupWizardView).View()) != n {
		t.Fatal("box count changed as focus moved — must stay exactly one")
	}
	assertBlinkCmd(t, cmd)
}

// Landing on the wizard starts the blink on the bucket field.
func TestSetupWizard_InitSchedulesBlink(t *testing.T) {
	v := ... // fresh wizard via existing constructor helper
	assertBlinkCmd(t, v.Init())
}

// Passphrase stage: entering it focuses newPass and blinks; the frame
// sits on the passphrase field, exactly one.
func TestSetupWizard_PassphraseStageBoxAndBlink(t *testing.T) {
	v, cmd := ... // drive to the passphrase entry stage (focus site setup_wizard.go:1121 or :450 per the file's flow helpers)
	assertBlinkCmd(t, cmd)
	if got := boxCount(v.View()); got < 1 {
		t.Fatalf("passphrase stage renders no field box")
	}
	_, tick := v.Update(cursor.BlinkMsg{})
	if tick == nil {
		t.Fatal("blink tick not routed on the passphrase stage")
	}
}
```

(Ellipses are constructor plumbing to be filled from the file's own existing test helpers — the assertions and message flow above are the required content and must appear as written.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSetupWizard_Box|TestSetupWizard_Init|TestSetupWizard_Passphrase' 2>&1 | head -20`
Expected: FAIL — no boxes, nil/blink-less cmds.

- [ ] **Step 3: Implement**

All six focus sites + fields-loop box + passphrase-stage box + one tick-routing case + `Init` blink + width rule.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run TestSetupWizard` then full `go test -race ./internal/tui/` (golden triage rule as before).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): focused-field box + blink in the setup wizard"
```

---

### Task 5: Goldens, docs, full gate

**Files:**
- Modify: `internal/tui/testdata/*` (regenerated), `CLAUDE.md`

**Interfaces:**
- Consumes: Tasks 1–4 finished.
- Produces: the shipped feature.

- [ ] **Step 1: Regenerate goldens deliberately**

```bash
go test ./internal/tui/ -run TestGolden -update
git diff --stat internal/tui/testdata/
git diff internal/tui/testdata/ | head -200
```

Eyeball the diff: every change must be a `FieldBox` frame appearing around a focused field (border rows/columns, shifted content). Any wrapping, truncation, or pane overflow is a WIDTH BUG — fix the offending `Width` assignment (subtract 4) and regenerate; never accept a broken layout into a golden.

- [ ] **Step 2: CLAUDE.md**

In the "TUI specifics" bullet list, add:

```markdown
- **Focused text fields are boxed and blink.** The one field accepting
  input renders through `ui.FieldBox` (the border is the focus glyph —
  color-only focus signals are invisible under NO_COLOR), every focus
  transition returns `textinput.Blink`, and the view routes
  `cursor.BlinkMsg` to the focused input. New fields must follow all
  three or they will look dead.
```

- [ ] **Step 3: Full gate**

```bash
GOFLAGS=-timeout=40m just check
go mod tidy -diff
git diff --check
```

Expected: clean (stale golangci cross-worktree cache → `golangci-lint cache clean` and retry).

- [ ] **Step 4: Commit**

```bash
git add internal/tui/testdata/ CLAUDE.md
git commit -m "chore(tui): regenerate goldens for field boxes; document the focus affordance"
```

(Push and `just install` happen post-merge, outside this plan.)
