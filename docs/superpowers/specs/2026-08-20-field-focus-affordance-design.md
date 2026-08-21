# Field focus affordance: a box and a blinking cursor on the active text field

**Date:** 2026-08-20
**Status:** Approved

## Problem

TUI text fields give almost no signal that they are waiting for input. No
view ever returns `textinput.Blink` or routes `cursor.BlinkMsg`, so cursors
render static; fields sit inline with no frame. Operators can't tell at a
glance which field — if any — is listening.

## Decision

Two affordances, applied uniformly (approach approved by Mark: box the
ACTIVE field only, not every field):

1. **Box:** the focused text field renders inside a rounded-border box —
   `AccentAqua` border (matching `PanelFocused`), `Padding(0, 1)` — via a
   new shared `internal/ui` helper. Unfocused fields keep their current
   rendering; the box itself is the focus marker. Border characters are
   glyphs, so the affordance survives `NO_COLOR` and renders under the
   lipgloss Ascii profile used by tests (the house "selection is a glyph,
   not a color" rule).
2. **Blink:** every focus transition returns `textinput.Blink`, and views
   forward `cursor.BlinkMsg` to the focused input in `Update`, so the
   cursor actually blinks in a live terminal.

Exception: inputs already presented inside dedicated chrome — the command
palette and modal text prompts — keep their existing chrome (no second
box) and gain only the blink wiring.

## Design

### Helper — `internal/ui`

Beside `Panel`/`PanelFocused` in the theme:

```go
// FieldBox frames the one text field currently accepting input. The
// border is the focus affordance itself — a glyph, visible without
// color — so views must apply it only to the focused field.
var FieldBox = lipgloss.NewStyle().
    Border(lipgloss.RoundedBorder()).
    BorderForeground(AccentAqua).
    Padding(0, 1)
```

Views call `ui.FieldBox.Render(field.View())` for the focused field only.
Lipgloss draws border characters as separate styled runs per line, so
wrapping a styled `textinput.View()` does not trip the "never wrap an
already-styled string" inline-reset bug (precedent: `ModalBox` already
wraps styled modal content).

**Width rule:** a call site that sizes its input near the content width
must subtract the box overhead (2 border + 2 padding columns) from the
input's `Width` so the boxed field never wraps or overflows the pane.

### Blink plumbing — per view

Two mechanical additions per view with text fields:

- Every `Focus()` transition returns `textinput.Blink` as (part of) its
  `tea.Cmd` — including `Init()` for views that land already focused
  (unlock, the wizard's focused field, snapshots' filter when opened,
  palette, modal input).
- `Update` forwards `cursor.BlinkMsg` to the currently focused input
  (`case cursor.BlinkMsg:` routing to `field.Update(msg)`), returning the
  input's command so the blink keeps scheduling itself.

### Site inventory (all must comply)

| File | Fields | Box | Blink |
|---|---|---|---|
| `unlock.go` | passphrase | yes | yes |
| `setup_wizard.go` | S3 detail fields + passphrase/confirm | focused field only | yes |
| `backup.go` | tag | yes | yes |
| `password.go` | new / confirm | focused one | yes |
| `policies.go` | name / path / tags / schedule | focused one | yes |
| `restore.go` | dest / scope | focused one | yes |
| `sync.go` | path / refs | focused one | yes |
| `snapshots.go` | filter | yes | yes |
| `recoverykit.go` | output path | yes | yes |
| `connect.go` | (none — no fields; listed for completeness) | — | — |
| `palette.go` | query | existing chrome only | yes |
| `modal.go` | text prompt | existing chrome (`ModalBox`) only | yes |

### Explicitly unchanged

- Non-text focus affordances (`SelectRow`'s `▍`, pane borders) stay as
  they are.
- No behavior change to key routing, `CapturesText`, or focus order —
  this is rendering + cursor liveness only.

## Testing (TDD)

- **Helper:** one test asserting `FieldBox.Render` output contains the
  rounded corner (`╭`) and the inner text.
- **Per view, rule-style:** for each multi-field form, a table test that
  the focused field's render contains `╭` and the unfocused fields' do
  not; for single-field views, focused-shows-box (and, where the field
  can be unfocused, unfocused-hides-box).
- **Blink:** focus transitions return a non-nil cmd whose message
  (possibly nested in a batch) includes `cursor.BlinkMsg` — assert by
  executing the cmd and type-switching. At minimum one such test per
  view; the helper for unwrapping batched cmds may live in a shared
  test file.
- **Goldens:** the existing golden/smoke renders will legitimately change
  where boxes appear. Regenerate deliberately, diff-eyeball that only the
  intended framing changed, and note the regeneration in the commit
  message. A golden must never be regenerated to paper over an unintended
  layout break (overflow, wrapping) — the width rule above is what the
  eyeball is checking.

## Documentation

CLAUDE.md, TUI specifics: one bullet — the focused text field renders
through `ui.FieldBox` and every focus transition emits `textinput.Blink`;
color-only focus signals are forbidden (the box is the glyph).

## Risks / notes

- Boxes add 2 rows per focused field; dense forms (wizard, policies)
  must still fit the standard 80×24 frame — the golden eyeball covers
  this.
- `cursor.BlinkMsg` routing must reach only the focused input; feeding it
  to every field is harmless (unfocused inputs ignore it) but the spec's
  intent is focused-only for clarity.
- Blink timing is untestable and untested by design; tests pin the
  command emission, not the schedule.
