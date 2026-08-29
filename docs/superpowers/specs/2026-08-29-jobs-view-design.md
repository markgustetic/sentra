# Scheduled backups view ("jobs") — design

2026-08-29. Approved in conversation; supersedes the split Policies /
Schedule views.

## Problem

Scheduled backups are managed in two overlapping hidden views under
Settings. **Schedule** shows cadence and timer state (install/uninstall)
but no next-run time and no way to see what a job protects. **Policies**
lists/adds/runs/removes policies but cannot truly edit one (retype to
replace), cannot show what is backed up, and — a live bug — removing a
policy leaves its OS timer installed and orphaned, where it fails on
every fire (`sentra policy run <name>` on a missing policy). There is no
single answer to "what runs, when does it run next, and what does it
protect?"

## Decision summary

One new **JobsView** (title "Scheduled backups", registry id `jobs`)
replaces both views. `policies.go` and `schedule.go` are deleted; their
reusable parts (the policy form, the run flow, the scheduler seams)
move with the merge. Settings' two navigate rows become one. Deleting a
job stops the job only — policy removed from `sentra.yaml` **and** timer
uninstalled, snapshots untouched. Drill-in shows the newest snapshot's
file tree per path. `sentra policy remove` gets the same orphan fix.

## The list stage

A table, one row per named policy (manual ones included):

| Job | Schedule | Timer | Next run | Last run |

- **Schedule** — `policy.FormatScheduleSpec`, as today.
- **Timer** — `installed` / `not installed` / `—` (manual cadence),
  from `scheduler.Installed` via the existing GOOS/home/exe test seams.
- **Next run** — computed by the new pure helper (below). Shown only
  when the timer is installed; `not installed` and manual rows show `—`,
  so the view never promises a run that cannot fire.
- **Last run** — from the shared snapshot preload (no extra S3 list):
  the newest snapshot whose tag contains the token `policy:<name>`,
  falling back to the newest snapshot whose root matches one of the
  job's paths (compared after `~`/relative-path normalization to
  absolute, as the walker records roots). The fallback covers jobs
  created by the Backup view's
  ctrl+e repeat flow, whose first snapshot ran under the user's own tag
  before the timer ever fired. Rendered as a relative age ("2h ago").

Hydration mirrors the old views: config policies from `deps.Config`,
timer state from filesystem stats, snapshots from the shared preload.
`R` (capital) re-stats — `r` belongs to run-now, resolving the old
views' key collision (Schedule used `r` for refresh, Policies for run);
a per-row stat error folds into the notice line rather than aborting
the table. Reload also happens automatically after every action.

## List actions

- `enter` — drill in (below).
- `a` — add: the existing `policyForm`, unchanged.
- `e` — edit: the same form **pre-filled** from the selected policy.
  The name field is read-only in edit mode; rename = delete + re-add
  (timer files are keyed by name and snapshot tags carry it — renaming
  in place would orphan both). Saving rewrites via `config.Update`
  (rebased on disk; never persists `SENTRA_*` overrides), preserves
  config-authored Hooks, and — when the timer is installed and the
  schedule changed — re-renders and reinstalls the timer files in the
  same action, so an edited cadence takes effect without a second step.
  Editing an installed job's schedule to `manual` instead uninstalls
  the timer (manual cannot render a timer, and a manual job must not
  keep firing on the old cadence).
- `r` — run now: the existing run flow unchanged (op guard, hooks,
  typed confirm when prune mode is `apply`, simple confirm otherwise,
  validate-before-confirm so a corrupt prune mode can't hide behind the
  simple gate).
- `i` / `u` — install / uninstall the timer, behind the existing simple
  confirms.
- `d` — delete the job, behind one simple confirm whose body states the
  full effect: removes the policy from `sentra.yaml` AND uninstalls its
  timer files; existing snapshots are untouched and age out via
  retention. Simple (not typed) confirm: no snapshot data is at risk,
  and the inverse (re-add + install) restores the prior state.
  Uninstall runs even if the timer stat errored, and a timer-uninstall
  failure after a successful config edit is reported in the notice —
  the config edit is not rolled back (matching the CLI, where the two
  steps were always separable).

All config edits are config-only (no repo lock, no op guard); run-now
takes the op guard exactly as before.

## Drill-in (detail stage)

Summary block up top: paths, tags, schedule spec, timer state, next
run, last run, check/prune modes. Below it, the file tree of the newest
snapshot for the selected path — `repo.LoadSnapshot` on the same
snapshot the Last-run column resolved (per path), rendered with the
existing `buildDirTree`/`renderDirTree`, loaded by a read-only
`tea.Cmd` with the spinner pattern (no op guard). Scrollable; sized to
the pane like the snapshot detail view.

- Multiple paths: `←`/`→` (and `tab`) cycle which path's tree is shown;
  the summary names the active path and its snapshot id.
- A path with no matching snapshot shows "not backed up yet — run the
  job to take its first snapshot".
- `e` / `d` / `r` work here too (same handlers); `esc` returns to the
  list.

## NextRun

`policy.NextRun(s config.PolicySchedule, now time.Time) (time.Time, bool)`
— pure, in `internal/policy` (below both surfaces), `ok=false` for
manual. Semantics mirror what the renderers install, in `now`'s
location (launchd/systemd fire in local time):

- `hourly` → next top of hour (both renderers fire at minute 0).
- `daily@HH:MM` → today at HH:MM, or tomorrow if already past.
- `weekly@day:HH:MM` → next occurrence of that weekday at HH:MM
  (today counts if still ahead).
- `monthly@HH:MM` → the 1st of this month at HH:MM, else the 1st of
  next month (both renderers install day 1).

Computed next-trigger, not OS truth: launchd/systemd may fire late
(sleep, catch-up) — the column answers "when is it scheduled", which is
knowable and platform-neutral, rather than shelling out to
`launchctl`/`systemctl`.

## CLI parity and the orphan fix

- `sentra schedule status <name>` additionally prints the computed next
  run for an installed, non-manual schedule.
- `sentra policy remove <name>`: after the config edit, if the policy's
  timer files are installed, uninstall them and print that it did. An
  installed timer for a deleted policy can only ever fail; leaving it
  is never right. (The TUI delete and the CLI stay behaviorally
  aligned; AGENTS.md records the combined behavior.)

## Registry, routing, docs

- App registration: the `policies` and `schedule` entries are replaced
  by one hidden `jobs` entry; `hiddenFromRail` updated. The rail stays
  six views; `rail_test`'s registry pin changes accordingly.
- Palette derives from the registry, so it picks up the new entry for
  free; the old `policies`/`schedule` ids stop resolving (acceptable —
  they were reachable only via palette/registry, and the launcher is
  the documented path).
- Chat `open_view` tool description gains `jobs` in its example list.
- Docs: README (tour blurb, key table, TUI section launcher list),
  AGENTS.md surface-contract paragraph
  ("policies/schedule/…" → "jobs/…" launcher list, plus the
  `policy remove` behavior note), CLAUDE.md quick-reference sentence.

## Testing (TDD)

- `policy.NextRun`: table-driven per cadence, including already-past
  clock today, weekday wrap, month wrap, and manual → `ok=false`.
- View: registry/rail pins (`rail_test`), Settings navigate row routes
  to `jobs`, list renders all three timer states and `—` next-run for
  uninstalled/manual rows, last-run tag match + root fallback.
- Edit: form pre-fill, read-only name, save rewrites config and
  reinstalls the timer when schedule changed (via GOOS/home/exe seams);
  save without schedule change does not touch timer files; edit to
  `manual` uninstalls an installed timer.
- Delete: one confirm removes the policy from the on-disk config AND
  the timer files; snapshots untouched (assert the repo still lists
  them).
- Drill-in: tree renders from the in-memory repo's manifest; no-
  snapshot path shows the placeholder; path cycling.
- CLI: `policy remove` uninstalls installed timer files (regression for
  the orphan bug); `schedule status` prints next run.
- App-level key routing for the new view (per the "a view cannot test
  the shell" convention).

## Out of scope

- Deleting a job's snapshots from the delete flow (retention/prune own
  data deletion).
- Reading actual next-fire times from launchd/systemctl.
- Renaming a job in place.
- A Dashboard "next scheduled backup" line (possible follow-up).
