# Backup wizard + Scheduled backups on the rail — design

Date: 2026-09-02. Status: approved.

## Goal

Two changes to the TUI, decided together because the second gives the first
somewhere to land:

1. **Taking a backup becomes a three-step wizard**: choose the location,
   optionally attach a recurring schedule, then confirm on a full screen.
   Today the Backup view is one screen (folder picker + tag field) with two
   undiscoverable chords — `ctrl+r` arms a full rescan and `ctrl+e` cycles a
   repeat cadence — and a small yes/no modal as the gate.
2. **Scheduled backups becomes a rail tab.** The jobs view (id `jobs`, title
   "Scheduled backups") already lists every policy with cadence, timer state,
   next and last run, drill-in, add/edit, run-now, install/uninstall and
   delete. It is hidden and launched from Settings; it moves onto the rail.

Decisions taken with the user: confirming a scheduled backup installs the
policy + OS timer **and runs the first backup now** (today's `ctrl+e`
semantics); the tag and rescan controls live on the **confirm screen**; the
tab sits **directly under Backup** and the Settings launcher is **removed**;
the schedule step offers **presets plus a time** (not the free-text
shorthand the jobs edit form takes). The `ctrl+r` / `ctrl+e` chords are
removed, not kept as aliases.

## Non-goals

- No generic wizard component. Three stages on `BackupView`'s existing
  state machine are enough; the setup wizard's engine solves a different
  problem (side-effecting AWS steps) and is not reused.
- The jobs view's `a` add form is unchanged: it is the way to add a
  scheduled backup that must *not* run right now.
- The CLI (`sentra backup`, `policy`, `schedule`) is untouched.
- README screenshots of the Backup view go stale; regenerating them is a
  follow-up (see the readme-screenshots procedure), not part of this change.

## Part 1 — the backup wizard

### Stages

`backupStage` becomes: `backupLocation` → `backupSchedule` → `backupConfirm`
→ `backupRunning` → `backupDone`. Running and Done are today's stages,
unchanged in behavior. `backupConfigure` is retired.

Every wizard stage renders a header that mirrors the setup wizard's
`wizardHeader`: `New backup` on the left, `Step N of 3` on the right in
`ui.Muted`, then the stage title in `ui.Primary` (`Location`, `Schedule`,
`Confirm`). The step count is what tells the operator where they are; no
extra breadcrumb.

Navigation contract, applied uniformly:

- **enter** validates the current stage and advances (on Confirm, it starts).
- **esc** steps back one stage on Schedule and Confirm (`ConsumesEscape`
  true there, and while running where it cancels). On Location esc belongs to
  the shell, as today: it is how the operator leaves the view.
- Leaving a stage **blurs every field it owns**; entering a stage that owns a
  field focuses that stage's default field and returns `Focus()`'s cmd. This
  is the `focusField`/`blurFields` pattern from CLAUDE.md, so the stage flag
  and `Focused()` cannot disagree. `viewShownMsg` re-focuses whatever the
  current stage owns; `viewHiddenMsg` blurs everything.
- `CapturesText()` is true exactly when a text field is focused (Schedule
  with the keyboard on name/time, Confirm with it on the tag field).
  `ConsumesArrows()` is true on Location (picker) and on Schedule while the
  cadence list has the keyboard. `ConsumesTab()` is true on Schedule and
  Confirm, where tab cycles that stage's controls; false on Location, which
  has a single control now that the tag field has moved.

### Step 1 — Location

The existing `dirPicker` with its cursor-following preview pane. Only the
wording changes: the top button reads `▸ choose the current directory` and
`enterVerb` on it says `choose <basename>`; folder rows keep `open` / `go up`.
Enter on the button runs the cheap validation `requestBackup` does today —
`os.Stat` says directory, `deps.Repo != nil` — renders the error inline on
failure, and on success records the directory in `pending` and enters the
Schedule stage. Right/left/backspace/↑↓ navigate as today.

### Step 2 — Schedule

Controls, top to bottom:

1. **Cadence list** — `one-shot` (default), `hourly`, `daily`, `weekly`,
   `monthly`; ↑↓ move, rows render through `ui.SelectRow` (the `▍` glyph).
   Values are the `policy.Cadence*` constants; `one-shot` is the wizard's
   word for "no policy" and never reaches disk. (`manual` is deliberately
   not offered: a manual policy is what the jobs view's add form is for.)
2. **Name** text field, shown only when cadence ≠ one-shot. Prefilled by
   `repeatPolicyName(filepath.Base(dir))` and then **uniquified at prefill
   time** against `deps.Config.Policies` — `name`, `name-2`, `name-3`, … —
   skipping names whose single path is *this* directory (those are reused).
   The operator may edit it.
3. **Time** text field (`HH:MM`, default `02:00`), shown for daily, weekly
   and monthly. Hidden for hourly, whose schedule must carry no `At`.
4. **Weekday** row, shown for weekly only: `←`/`→` cycle `mon`…`sun`, default
   `sun`, rendered as a selectable row (no text input; the seven tokens are
   the policy package's `validWeekday` set).

Tab cycles the visible controls in that order and wraps; the list is a
control too (so the keyboard can come back to it). Text fields render
through `boxedField` and blink only when focused; the list and weekday row
are glyph-selected. Sizing subtracts `ui.FieldBoxOverhead`.

Enter builds a `config.PolicyConfig{Paths: [dir], Schedule: {Cadence, At,
Weekday}}` through `policy.NormalizeSchedule` and runs `policy.Validate(name,
p)` (which validates the name and the schedule), then applies the
wizard-specific rule: a name that already exists in `deps.Config.Policies`
**for a different directory is refused** inline ("policy %q already backs up
%s — choose another name"); the same directory is allowed and the confirm
screen says it will be updated. Errors render inline in `ui.Danger` under
the form and enter does not advance. One-shot skips all of this.

### Step 3 — Confirm

A read-only summary block:

```
directory   /Users/mark/Documents
schedule    one-shot
```
or
```
directory   /Users/mark/Documents
schedule    daily at 02:00 as policy "Documents" — installs an OS timer
next run    Thu 2026-09-03 02:00
```
`next run` comes from `policy.NextRun(schedule, now)` (clock is a test seam
on the view, as in JobsView). When the policy already exists for this
directory the schedule line says `updates policy "Documents"` instead.

Then two controls, tab cycles between them:

- **Tag** text field (`tag>`, placeholder `optional label`), focused by
  default when the stage is entered.
- **Rescan** toggle row: `[ ] force a full rescan (every file re-read)`;
  `space` toggles it while it has the keyboard. Rendered as a selectable
  row.

Enter from either control confirms. There is no modal: this screen *is* the
gate the yes/no `ConfirmModal` used to be, and `backupConfirmID` /
`confirmedMsg` handling in the view goes away. On confirm:

1. Re-stat the directory (it can vanish between steps) — failure renders
   inline and stays.
2. If cadence ≠ one-shot, `installRepeat(dir, name, schedule)` — the current
   function generalized to take the name and schedule instead of reading
   `v.repeat`. Its `config.Update` closure keeps the on-disk collision check
   but **refuses** instead of uniquifying: the wizard already resolved the
   name, and silently renaming what the operator just confirmed would lie to
   them. A failure renders inline ("could not install the schedule: …") and
   stays on Confirm; the backup does not run (a confirmed *repeating* backup
   must never degrade to a one-shot).
3. `startBackup(dir)` with the tag and rescan flag — today's function,
   unchanged.

### Done

Today's result screen, plus: when a schedule was installed, a line
`policy "Documents" installed — next run Thu 2026-09-03 02:00`, and the
action line offers `s` to open the Scheduled backups tab (emits
`activateMsg{id: "jobs"}`) beside enter's "run another backup", which
resets to Location as `resetTo` does now.

### Chat overlay

`chatBackupMsg{dir, tag}` seeds the picker at `dir`, sets the tag, forces
cadence one-shot, and enters the **Confirm** stage directly — the confirm
screen replaces the modal as the human gate the chat contract requires
(CLAUDE.md: nothing mutates without a confirm). Ignored mid-flow (running /
done), as today.

### What is removed

`ctrl+r`, `ctrl+e`, `nextRepeat`, the `repeat`/`focus`(picker vs tag)
fields, the tag field on the picker screen, `backupConfirmID`, and the
`confirmedMsg` case in `BackupView.Update`. `App` keeps its generic modal
machinery (prune, jobs and quit still use it).

## Part 2 — Scheduled backups on the rail

- In `NewApp`'s `views` slice, `{id: "jobs", …}` moves from the hidden tail
  to the visible head, directly after `backup`. Its id stays `jobs`
  (activateMsg routes, tests and docs reference it). `categories["jobs"] =
  "Operations"`. It leaves `hiddenFromRail`.
- The rail is now seven destinations, in order: Dashboard, Backup,
  Scheduled backups, Snapshots, Maintenance, Settings, Help. Digits 1–7 jump;
  8 and 9 are no-ops.
- Settings loses its "Scheduled backups" `entryNavigate` row. Its help
  description becomes "Configuration and recovery"; `help.go` gains a
  description for `jobs` ("Scheduled backups: cadence, next run, edit, run
  now"). The `sentra ui` long help, README, QUICKSTART, AGENTS.md and
  CLAUDE.md say seven views and drop the `ctrl+e`/`ctrl+r` rows.
- JobsView itself is unchanged. It already declares its arrow/text/escape
  seams and has rows, so it is focusable from the rail like every other
  non-inert view.

## Testing

All TDD, table-driven where it fits, in `internal/tui`:

- **Stage machine**: enter on the picker button advances to Schedule; enter
  on a folder row does not; esc on Schedule/Confirm steps back; esc on
  Location is left to the shell (`ConsumesEscape` false).
- **Schedule validation**: one-shot skips validation; bad time refused; a
  name colliding with another directory's policy refused with the path in
  the message; the same directory accepted; prefill uniquifies (`docs` →
  `docs-2` when `docs` backs up elsewhere); hourly carries no `At`; weekly
  carries the chosen weekday.
- **Confirm**: renders directory/schedule/next-run; installs policy + timer
  then emits `startOpMsg` (order asserted through the existing repeat
  fixture); install failure blocks the run and stays on Confirm; the
  rescan toggle reaches `SnapshotOptions.ForceRescan`; the tag reaches
  `SnapshotOptions.Tag`; a vanished directory is refused.
- **Focus rules**: `fieldOwners()` in `fieldfocus_test.go` gains rows for
  Schedule (name, time) and Confirm (tag) so the cross-view box/blink/blur
  assertions cover them; tab cycles the visible controls only (no time field
  for hourly); leaving a stage blurs; `viewShownMsg` re-focuses the current
  stage's default field.
- **Chat**: `chatBackupMsg` lands on Confirm with dir + tag seeded and
  one-shot cadence.
- **Done**: `s` emits `activateMsg{id:"jobs"}`; enter resets to Location
  keeping the window size.
- **App-level**: keys through all three steps to `backupRunning` (replaces
  `TestApp_BackupConfirmationFlowEndToEnd` / `…EscCancels`); the shell's
  globals (`q`, digits) still work on Location and are captured on a
  focused text field.
- **Rail**: `TestApp_RailShowsExactlySixViews` becomes seven in the new
  order; digit `3` lands on `jobs`, `8` is a no-op; `jobs` leaves the
  demoted list; Settings has no `jobs` launcher; `TestApp_AllViewsRegistered`
  keeps 18 views (seven rail + eleven hidden).
