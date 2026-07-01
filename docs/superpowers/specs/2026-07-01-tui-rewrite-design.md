# TUI Rewrite — Design

**Date:** 2026-07-01
**Status:** Design approved (all sections); pending spec review.

## Goal

Rewrite `internal/tui` into a complete, visually polished frontend for sentra:
every CLI capability operable from the TUI, a first-run setup wizard inside the
TUI, and full use of the Charm ecosystem (bubbletea, bubbles, lipgloss, huh,
harmonica). The scriptable CLI surface is preserved unchanged.

## Decisions (approved during brainstorming)

| Decision | Choice |
|---|---|
| Setup fate | **TUI-first, CLI kept** — TUI gains a built-in setup wizard; `sentra setup` CLI wizard and headless subcommands stay untouched |
| Function scope | **Everything** — all 12 operations (below), destructive ones confirmation-gated |
| Dependencies | **Latest stable v1 Charm line** (bubbletea v1.x, bubbles, lipgloss v1.x, huh); no v2 migration |
| Navigation | **Sidebar rail + `ctrl+p` command palette** (hybrid) |
| Visual direction | **Charm expressive** — rounded borders, violet/pink/aqua adaptive palette, gradient title bar, animated progress/spinners |
| Execution strategy | **Phased in-place** — 3 phases, each landing fully green and usable on `main` |

## Non-goals / out of scope

- Bubbletea v2 migration.
- Removing or altering any CLI command, flag, or output (AGENTS.md contracts
  stand; the CLI `huh` wizard remains the scriptable setup path).
- New repo-layer features. `internal/repo`, `internal/agent`,
  `internal/blobstore`, `internal/config` are consumed, not modified.
  The only non-TUI code change is the setup-engine extraction described
  below, which moves existing `internal/cli` code without behavior change.
- Theme customization settings (accent picker etc.). Adaptive light/dark and
  `NO_COLOR` support only.

## Architecture

### Shell + views (Phase 1)

`internal/tui` is rebuilt around a shell that owns global concerns; views are
small, isolated models behind a common contract.

- **`App` (root model):** layout (sidebar rail left, content pane right,
  status bar bottom), view routing, window size broadcast, theme, global
  keymap, overlay rendering (palette, modal stack), and the
  one-mutating-operation-at-a-time guard.
- **Command registry:** single source of truth `{name, category, keybinding,
  target view or action}`. Drives both the sidebar and the palette so they
  never drift. Views/actions register at App construction.
- **Sidebar rail:** persistent nav listing every view, with badge support
  (e.g. agent findings count). Built on `bubbles/list`.
- **Command palette:** `ctrl+p` overlay — `textinput` + fuzzy-filtered
  registry entries. Enter opens the view or launches the action.
- **Status bar:** contextual key hints via `bubbles/help` + `bubbles/key`,
  fed by the active view's keymap; shows the global "operation running"
  indicator and repo identity.
- **Modal stack:** confirmations and error dialogs as lipgloss-layered
  overlays; the shell owns push/pop and input capture.
- **View contract:** each view implements `tea.Model` plus `Title() string`
  and `Keymap()` (for the status bar), and handles broadcast
  `tea.WindowSizeMsg`. No blocking calls in `Update` — all repo work runs as
  `tea.Cmd` goroutines returning typed messages; long operations stream
  progress through a `progress.Reporter` → tea-message adapter (existing
  pattern in `internal/ui`).

### Theme system

The Charm-expressive direction becomes a full `Theme` in `internal/ui`
(shared by CLI and TUI):

- Violet/pink/aqua accents as `lipgloss.AdaptiveColor` (light/dark terminals).
- Rounded borders, a single spacing scale, gradient title bar.
- Animated progress via `bubbles/progress` with `harmonica` spring easing;
  spinners for indeterminate waits.
- A matching custom `huh` theme so embedded forms look native.
- `NO_COLOR` respected; graceful degradation without truecolor (lipgloss
  handles profile detection); minimum-terminal-size guard screen.

### Component inventory (bubbles → real job)

| Component | Used for |
|---|---|
| `list` | sidebar rail, command palette results, pickers |
| `table` | snapshots, retention preview, schedule status |
| `viewport` | logs, check reports, recovery kit, IAM policy preview |
| `progress` (+ harmonica) | backup/restore/sync/prune transfer bars |
| `spinner` | indeterminate waits (bucket prepare, listing) |
| `textinput` | palette query, path input, passphrase (masked) |
| `help`, `key` | status-bar contextual keymaps |
| `paginator` | large snapshot lists |
| `stopwatch` | operation elapsed time |
| `filepicker` | backup source picker, restore destination picker |
| `huh` (as `tea.Model`) | policy editor, password rotation, setup steps |

Deliberately skipped (no honest use): `textarea`, `timer`. "Use all the
features" is interpreted as *every component with a real job*, not literal
completeness.

## Operation flows (Phase 2)

Every operation is a small state machine:
**configure → preview → confirm (if mutating) → run (live progress,
cancelable) → result.**

| Operation | Flow specifics |
|---|---|
| Backup | filepicker/path input → plan summary (files, est. new bytes, ignore hits) → run with animated progress, files/sec, stopwatch → result panel |
| Restore | snapshot table → destination picker (must-be-empty surfaced early) → dry-run plan → progress → optional verify pass → result |
| Prune | retention preview table with per-snapshot keep/drop reasons (`PlanRetentionExplain`) → **typed confirmation** ("type `prune`") → apply + GC → reclaimed stats |
| Check | quick/deep toggle → progress → report panels with issue drill-down (absorbs today's Operations view) |
| Sync | destination form (huh) → dry-run stats → progress (combined-total reporter) |
| Diff | current three-column view + snapshot-pair picker |
| Agent | current streaming view + per-recommendation apply with individual confirm modals; read-only by default (CLI invariant preserved) |
| Policies | list + huh add/edit form + delete confirm; writes via existing config renderer |
| Schedule | status table (launchd/systemd); install/uninstall with confirm; existing render engine |
| Password | masked huh form → typed confirmation → existing rotate-then-keyring path |
| Recovery kit | viewport render + export-to-file |
| Doctor | read-only diagnostics panel |

**Safety rules:**

- One mutating operation at a time — app-level guard mirroring the repo's
  advisory lock; a global indicator shows the running operation; read views
  stay live.
- Destructive operations (prune apply, restore into risky targets, password
  rotation, agent apply, schedule install/uninstall) require explicit modal
  confirmation; the most destructive (prune, password) require **typed**
  confirmation. The mutating `tea.Cmd` is not issued until the gate passes.
- `esc`/`x` cancels a running operation via its context, behind a confirm.

## Setup in the TUI + first-run (Phase 3)

### Entry routing

Bare `sentra` (or `sentra ui`):

1. **No `sentra.yaml`** (same resolution as the CLI) → **setup wizard**.
2. **Config exists, passphrase unavailable** (keyring miss / no
   `SENTRA_PASSPHRASE` / no file source) → masked **unlock screen** → dashboard.
3. **Both available** → dashboard.

### Setup wizard

Stepped pages with a progress trail:
**Storage backend → Bucket/region/prefix → AWS auth → Passphrase & keyring →
Review → Prepare & init.**

- Steps are `huh` forms embedded as `tea.Model`s, themed to match.
- Review step shows the IAM policy preview in a viewport with export.
- Prepare & init renders a spinner checklist (bucket created, public access
  blocked, default encryption on, repo initialized).
- **Browser auth:** `aws sso login`-style flows run via `tea.ExecProcess` —
  the TUI suspends, the external command owns the terminal, and the wizard
  resumes at the same step with the result.
- **Drafts:** non-secret resume state, exactly like the CLI drafts; quitting
  mid-wizard and relaunching resumes.
- **Secret rules preserved:** no secrets in `sentra.yaml`, drafts, or logs;
  keyring populated only after init (or verified open on already-initialized
  repos — the previously fixed behavior) — enforced in the shared engine.

### Setup engine extraction (`internal/setup`)

The import direction is `cli → tui`, so the TUI cannot import the setup logic
that lives in `internal/cli` today. The setup **engine** — plan types, AWS
prepare/inspect/auth operations, repo init, draft persistence, error-advice
mapping — moves to a new **`internal/setup`** package consumed by both:

- `internal/cli` keeps its existing `huh` wizard prompts and command wiring,
  now calling the engine (behavior-preserving move of existing code; all
  existing CLI setup tests stay green).
- `internal/tui` builds its Bubbletea wizard on the same engine.
- Every secret-handling rule lives once, in the engine, tested once.

### Settings view

Resolved non-secret config, keyring status, re-run setup entry point,
password-change entry point.

## Errors, concurrency, hygiene

- **Errors:** every async op returns a typed error message rendered as a
  non-blocking modal with *advice*: sentinels map to hints (`ErrRepoLocked` →
  holder info + stale-lock guidance; `ErrWrongPassphrase` → retry unlock; AWS
  failures reuse the existing setup error-advice tables). The shell catches
  everything; nothing panics the app.
- **Concurrency:** root context owned by App; each operation gets a
  cancelable child context. Progress messages are the only cross-goroutine
  channel (standard bubbletea message passing).
- **Secret hygiene:** passphrase inputs masked (`EchoPassword`), zeroized
  after use, never rendered, logged, or included in error text.

## Testing

- Existing table-driven model-test pattern: drive `Update`/`View` directly,
  assert state and rendered output at fixed window sizes.
- Every operation flow's state machine gets tests, with emphasis on:
  - **confirmation gating** — the mutating command is not issued until the
    typed/modal confirm passes; cancel paths issue nothing;
  - error paths render advice modals and leave the app usable;
  - registry consistency — every registered command reachable from both
    sidebar and palette.
- Engine extraction: existing `internal/cli` setup tests unchanged and green;
  new `internal/setup` unit tests own the engine behavior.
- Full `-race` suite + `golangci-lint` + `gofmt` + `go mod tidy -diff` gate
  per phase (CI-equivalent), as established.

## Phasing & delivery

One spec (this document); **three sequential implementation plans**, each
landing fully green and usable on `main`:

1. **Phase 1 — Shell:** isolated deps-bump commit (latest v1 Charm line, full
   gate), then theme system, App shell (sidebar, palette, status bar, modal
   stack, registry), and the 5 existing views ported (Dashboard, Snapshots,
   Diff, Agent, Operations-as-Check).
2. **Phase 2 — Operations:** the 12 operation flows with confirmations, live
   progress, cancelation, and the one-op guard.
3. **Phase 3 — Setup:** `internal/setup` engine extraction (behavior-
   preserving), TUI wizard, first-run routing, unlock screen, settings view.

## Risks & mitigations

- **Deps bump behavior drift** (bubbletea/bubbles/lipgloss minor upgrades
  change rendering or message timing): isolated first commit; full gate; the
  5 ported views' tests catch regressions early.
- **`tea.ExecProcess` terminal state** (suspend/resume around `aws` CLI):
  wizard step is self-contained and re-entrant; failure returns to the same
  step with advice.
- **Engine extraction churn** (`internal/cli` setup files move imports):
  behavior-preserving move validated by the untouched CLI setup tests.
- **TUI test brittleness** (string assertions at fixed sizes): assert on
  stable substrings and state, not full-frame golden matches.
- **Palette/sidebar drift:** impossible by construction — both render from
  the single command registry.

## Invariants preserved (explicit)

- CLI surface, flags, and outputs unchanged; AGENTS.md per-command contracts
  intact (setup scriptable, doctor read-only, recovery-kit non-secret, agent
  advise-ignore read-only).
- Agent recommendations read-only by default; apply is explicit per-action.
- No secrets in `sentra.yaml`, drafts, logs, recovery kits, tests, fixtures.
- `internal/repo` / crypto / GC / locking untouched.
