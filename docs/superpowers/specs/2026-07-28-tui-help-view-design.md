# TUI Help view — design

**Date:** 2026-07-28
**Status:** approved, ready for implementation planning

## Problem

The nav rail lists 17 screens by title alone. A title tells you what a screen is
called, not what it does — "Check" vs "Doctor", or "Files" vs "Snapshots", are
indistinguishable to anyone who hasn't read the CLI docs. The `?` modal answers
"which keys work here", never "what is this screen for".

## Solution

Add an 18th navigable view, `Help`, at the bottom of the rail. It renders one row
per rail entry — title plus a one-line description of what that screen does — in
rail order. ↑↓ move a cursor; enter jumps to the highlighted screen, so it
doubles as a navigable directory rather than a static reference card.

## Architecture

### Placement

`HelpView` lives in `internal/tui/help.go` and registers **last** in `NewApp`'s
views slice as id `"help"`, category `"Views"`. Registration order is rail order,
so it lands at the bottom, and because the registry drives both the rail and the
`ctrl+p` palette, the palette entry comes for free.

This makes 19 views total: 18 navigable plus the hidden `unlock` startup gate.

### Descriptions

`Command` in `internal/tui/registry.go` gains a `Description string` field.
`NewApp` populates it from a `viewDescriptions` map keyed by view id, declared in
`help.go` — the same shape as the `categories` map already sitting beside the
registration loop.

The view renders `registry.Commands()` directly, so **help order and rail order
cannot drift**: they are the same list, read once, in registration order.

*Alternative considered:* a `Description() string` method on each view model,
mirroring the existing `Title()`. Rejected — 17 file edits carrying one string
each, and the completeness test below gives an equivalent guarantee for a
fraction of the churn.

### Description text

Kept under ~50 columns so a row fits the content pane at the 80×20 minimum
(`contentW` = 59, minus panel padding and the 4-column description indent).

| id | description |
|---|---|
| `dashboard` | Repo health, last snapshot, and size timeline |
| `backup` | Snapshot a folder into the repository |
| `snapshots` | Browse past snapshots and inspect their files |
| `files` | Latest snapshot's directory layout as a graph |
| `diff` | Compare two snapshots file by file |
| `check` | Verify repository integrity end to end |
| `doctor` | Diagnose config, AWS access, and repo health |
| `recovery-kit` | Print a non-secret kit for disaster recovery |
| `policies` | Manage named backup policies and run them |
| `schedule` | Install or remove OS scheduler entries |
| `agent` | Scan for backup risks and get recommendations |
| `restore` | Restore a snapshot to a chosen destination |
| `prune` | Apply retention and reclaim unused storage |
| `sync` | Replicate this repository to a second bucket |
| `password` | Rotate the repository passphrase |
| `settings` | Configuration summary and app preferences |
| `setup` | Re-run the first-run configuration wizard |
| `help` | What each screen in the rail does |

The list includes `Help` itself. That is one fewer special case than filtering it
out, and enter on it is a harmless re-activate of the current view.

### Rendering and behavior

The `SettingsView` entry list already does this job; `HelpView` follows its shape.

- Header line, then per entry: `ui.SelectRow(i == cursor, title)` followed by the
  description on the next line, indented 4 and styled `ui.Muted`. The label
  passed to `SelectRow` is **unstyled** — wrapping an already-styled string
  embeds an ANSI reset that terminates the outer style mid-line.
- `ConsumesArrows() bool { return true }` — the cursor is always present, so ↑↓
  belong to the view whenever it holds content focus.
- Enter emits `activateMsg{id}`; the shell already routes that to navigation.
- `ShortHelp()` advertises `↑↓ entry` and `⏎ open`.
- The view declares neither `InertContent` (it is interactive) nor
  `CapturesText` (it has no text input).

### Scrolling

18 entries × 2 lines plus a header far exceeds the content budget — `contentH` is
16 at the 80×20 minimum. The view tracks its height from the synthetic
`tea.WindowSizeMsg` the shell forwards, and renders a window over the entry list
that follows the cursor, clamped at both ends.

No `bubbles/viewport`: the cursor is the scroll driver, so a viewport would need
parallel key wiring and a second source of truth for the visible region.

### Data flow

None. `HelpView` performs no I/O, opens no repository, takes no op guard, and
reads nothing from `Deps` beyond construction symmetry with its sibling views.
The description table is static text compiled into the binary.

### Error handling

There are no failure modes to handle — no I/O, no config read, no repo access. A
description missing from the map would render as an empty second line; the
completeness test below makes that unreachable in a built binary.

## Testing

TDD: each test is written first and watched fail for the right reason.

| Level | What it pins |
|---|---|
| registry | `Command.Description` round-trips through `Add`/`Commands`; `NewApp` populates it for every registered command |
| completeness | every registered command has a non-empty description, **and** `viewDescriptions` has no keys that are not registered commands |
| view | cursor clamps at both ends; enter on entry *i* emits `activateMsg` with that command's id; rendered order equals `registry.Commands()` order |
| windowing | at `contentH` = 16 the rendered output stays within budget, and moving the cursor to the last entry keeps that entry visible |
| App | `"help"` is the last rail entry, and activating it switches the active view — a view-level test cannot catch key routing |

The completeness test is the anti-drift mechanism: it states the rule ("every
navigable view describes itself"), not an example, so a view added later cannot
ship without a description and a view removed later cannot leave a stale entry
behind.

## Documentation to update

Three sites carry the old view count and must move to 19 total / 18 navigable:

- `internal/tui/app.go` — `NewApp`'s doc comment ("all 18 views — 17 navigable
  commands plus the unlock startup gate")
- `CLAUDE.md` — "Every CLI capability is also operable from the TUI (18 views)"
- `README.md` — "a full-screen TUI — 18 views, a first-run wizard"

## Out of scope

- Changing or absorbing the `?` keys modal. It answers a different question and
  stays as it is.
- Per-view long-form help or a tutorial mode.
- Showing descriptions in the `ctrl+p` palette results. The field will be
  available on `Command` if that is ever wanted, but nothing renders it here.
