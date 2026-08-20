# Config discovery: bare `sentra` opens the production repo from anywhere

**Date:** 2026-08-20
**Status:** Approved

## Problem

Every repo-facing command defaults `--config` to `sentra.yaml` relative to the
process cwd. The TUI is the default surface (bare `sentra` falls through to
`sentra ui`), so running `sentra` from any directory that does not contain a
`sentra.yaml` lands on the first-run setup wizard instead of the operator's
configured repo. There is no user-level config location at all. The operator
wants: type `sentra` anywhere, get the unlock gate / dashboard for the
production repo.

## Decision

Add config-path discovery with a user-level fallback (Approach A of three
considered; home-only and `SENTRA_CONFIG`-env rejected as breaking or
half-manual):

1. An explicit `--config <path>` bypasses discovery entirely.
2. Otherwise, if `./sentra.yaml` exists as a regular file, use it
   (project-local config keeps winning; fully backwards compatible).
3. Otherwise, use `$XDG_CONFIG_HOME/sentra/sentra.yaml`, where an unset or
   empty `XDG_CONFIG_HOME` defaults to `~/.config`. This is the gh-CLI
   convention: `~/.config` even on macOS, deliberately not
   `os.UserConfigDir()`'s `~/Library/Application Support`.

When neither file exists, the *resolved target* is the home path. A first run
from any directory therefore lands on the wizard, which persists
`~/.config/sentra/sentra.yaml` on completion; from then on bare `sentra`
anywhere opens unlock → dashboard against the production repo.

## Design

### Resolution helper — `internal/config`

One new function, `config.DiscoverPath() string` (exact name at implementer's
discretion), returning the resolved default path per the rule above. It lives
in `internal/config` because both surfaces must agree and `internal/tui` must
never import `internal/cli`. Behavior details:

- "Exists" means `os.Stat` succeeds and the entry is a regular file. A
  directory named `sentra.yaml` does not count.
- The function consults only the cwd and the XDG env var; it never errors.
  If the home directory cannot be determined, fall back to the cwd-relative
  name (current behavior) rather than failing.
- Returns a path; never reads or writes the file.

### Call sites — `internal/cli`

Commands resolve at RunE time: when `cmd.Flags().Changed("config")` is false,
replace the flag's default value with `config.DiscoverPath()`. Flag defaults
stay `configFileName` so help text and existing command wiring do not churn.
A small shared helper in `internal/cli` (e.g. `resolveConfigPath(cmd, cfgPath)`)
keeps the `Changed` check in one place.

Commands that get discovery: every command whose `--config` defaults to
`configFileName` — backup, restore, snapshots, ls, stats, check, diff, prune,
pin/unpin, policy, doctor, sync, passwd, agent (all subcommands),
recovery-kit, schedule (all subcommands), ui, setup — and therefore bare
`sentra` via `SetUIAsDefault`.

`sentra schedule install` already embeds `filepath.Abs(cfgPath)` in the cron
line it emits. With discovery resolving before `loadScheduledPolicy`, cron
entries automatically carry the resolved absolute path and never depend on
cron's cwd. A test pins this.

### Write path — wizard and drafts

`runUI` already threads one absolute config path (`absCfgPath`) into the TUI,
and the wizard, settings, policy, and schedule flows write back through it —
so the TUI needs no changes beyond receiving the discovered path.

What must change: writes must tolerate a parent directory that does not exist
yet (`~/.config/sentra/` on a fresh machine).

- `config.Write` gains `os.MkdirAll(filepath.Dir(path), 0o700)` before
  writing. Harmless for cwd writes (dir "." exists), required for the home
  path. File perms stay as they are today.
- The setup engine's draft write (the draft sits beside the config via
  `Engine.DraftPath`) gets the same treatment, so an interrupted first run
  from a random directory still leaves a resumable draft.

### Explicitly unchanged

- **`sentra init`** stays cwd-only. Its design doc is explicit that init
  never reaches outside cwd; it is the scripting/recovery surface, and a
  script that runs `init` should get a config exactly where it ran.
- **`sentra local`** passes its explicit `.sentra-local.yaml`; discovery never
  fires.
- Any directory containing `sentra.yaml` behaves exactly as today, including
  this repository's own leftover test config.
- `config.Load` / `config.Update` semantics (env-overlay resolution, rebase-on-
  disk rewrites) are untouched; only *which default path* they are handed
  changes.

### Observability

New in this change: `sentra doctor` prints the resolved config path it probed,
so "which config am I on?" has a first-class answer. Error messages already include the path via
`config.Load` wrapping; no change needed there.

## Testing (TDD — failing test first, per repo convention)

- **`internal/config`:** table-driven tests for `DiscoverPath`: cwd file
  present; absent; both cwd and home present (cwd wins); `XDG_CONFIG_HOME`
  set vs unset; `sentra.yaml` is a directory (skipped). Use `t.Chdir` +
  `t.Setenv`; never touch the real home.
- **`internal/config`:** `Write` creates missing parent directories with 0700.
- **`internal/cli`:** a command run with `--config` unset resolves to the home
  fallback; with `--config` explicitly set (even to the literal default
  value's path) discovery is bypassed.
- **`internal/cli` (ui_test):** first-run launch from a directory with no
  `sentra.yaml` routes to the wizard with `ConfigPath` = home path; a cwd
  config still routes to it as today.
- **`internal/cli` (schedule_test):** emitted cron line embeds the resolved
  absolute home path when no cwd config exists.

Per the "test the rule, not the case" convention, the discovery table must
cover the precedence rule itself, not one happy path.

## Documentation

- **AGENTS.md** (source of truth for the per-command contract): new "config
  resolution" section stating the three-step rule and the init/local
  exceptions.
- **CLAUDE.md**: one-line update in the quick reference.
- **README / QUICKSTART**: mention that `sentra` finds
  `~/.config/sentra/sentra.yaml` when no project-local config exists, and
  that first-run setup writes there.

## Risks / notes

- Two possible config locations is a support surface: doctor's printed path is
  the mitigation.
- Resolution happens once per process at RunE time; cwd cannot change
  mid-process, so there is no TOCTOU concern beyond the ordinary stat race.
- Existing operators with cwd configs see zero behavior change; the feature
  only adds a fallback where today there is an error/wizard.
