# Welcome / Logo Splash — Design

**Date:** 2026-07-09
**Status:** Approved (brainstorming complete; ready for an implementation plan)

## Goal

Give `sentra`'s TUI an identity moment at launch: a brief, centered logo screen
that appears before the shell, costs the user nothing, and can be turned off
permanently from the Settings view.

## Approved decisions

Settled during brainstorming; these are requirements, not options.

1. **Shows on every launch** of the TUI (`sentra`, `sentra ui`, `sentra local`) —
   not first-run only.
2. **Auto-advances after 2.5 seconds**; **any key skips it immediately**. The
   dismissing keypress is consumed (it does not fall through to the view behind).
   Consuming it is deliberate: people dismiss splashes with enter or space, and
   forwarding that keystroke to the unlock gate would submit an empty passphrase.
   Losing one character to a retype is the better failure.
3. **Treatment:** the existing `✦` brand glyph over a letter-spaced `sentra`
   wordmark, the product tagline, then `version · commit`. Centered, rendered in
   the existing adaptive pink/violet palette.
4. **Opt-out lives in the Settings view** as a persisted toggle — not a CLI flag
   and not an environment variable.
5. **The lockup animates in as a staged cascade** (see below), rather than
   appearing all at once.

## Animation

The reveal repaints once per 60 ms frame; when it completes, repainting stops and
a single tick holds the finished lockup until `splashDuration` expires. A still
image costs no frames.

| Time | Stage |
|---|---|
| 0–200 ms | `✦` twinkles in: `·` → `✧` → `✦` |
| 300–700 ms | wordmark letters appear left to right, 80 ms apart |
| 1050 ms | tagline, both lines |
| 1400 ms | `version · commit`; repainting stops |
| 1400–2500 ms | hold |

`App.splashFrame` is the animation's only state. `renderSplash` derives every
stage from it and never reads the clock, so a frame is a pure function of the
frame number and each stage is unit-testable.

The glyph twinkles by changing **shape**, not color. Lipgloss emits no ANSI under
the Ascii color profile that unit tests (and `NO_COLOR` terminals, and pipes)
render under, so a color-based animation would be both invisible and untestable
there.

**Hidden is not absent.** `lipgloss.Place` centers each line independently, so a
line's position depends on its own width. Drawing only the revealed letters would
grow the wordmark from one cell to sixteen and slide it leftward on every frame.
Unrevealed letters therefore render as spaces. The tagline and version lines are
likewise reserved as blanks, keeping the line count and the un-placed body's
geometry invariant. A test asserts the rendered width and height are identical
across every frame of the reveal.

Two alternatives were rejected. A **shimmer sweep** needs a per-theme highlight
color, must tick for the whole duration, and reads as a loading indicator. A
**color fade** cannot interpolate `lipgloss.AdaptiveColor` (a light/dark pair)
without background detection and a hand-built hex ramp, and bands visibly below
truecolor.

## Non-goals

- No `--no-splash` flag and no `SENTRA_NO_SPLASH` env var. The Settings toggle is
  the single opt-out. (Cheap to add later if the toggle proves insufficient.)
- No animation, no progress bar, no per-launch tips.
- The splash is not a navigable view and never appears in the sidebar or palette.

## Architecture

**App-level overlay state.** `App` gains a `splashActive bool`. While it is true,
`View()` renders the splash instead of the normal frame, and `routeKey` dismisses
on any key. Everything else — the views slice, the command registry, `InitialView`
routing, the op guard, the modal stack — is untouched.

Two alternatives were considered and rejected:

- *A splash view in the `views` slice* — would pollute the 17-view registry,
  `InitialView` routing, and every view-count assertion, for something that is
  never navigable.
- *Printing the logo before `tea.Program` starts* — cannot support "any key
  skips", flickers against the alt-screen, and would print in non-TTY contexts.

## Components

### 1. `internal/config` — the persisted setting

Add a new top-level section to `config.Config`:

```go
UI struct {
    HideSplash bool `koanf:"hide_splash"`
} `koanf:"ui"`
```

Rendered by `config.Render` as:

```yaml
ui:
  hide_splash: false      # set true to skip the welcome splash
```

**Why `hide_splash` and not `show_splash`:** Go's zero value for `bool` is
`false`. A `show_splash` field would default to *off* for every existing
`sentra.yaml` that predates this change, silently disabling the splash. With
`hide_splash`, an absent field means `false` means the splash shows — the
correct default, with no pointer field and no migration.

`config.Load` parses it; `config.Write`/`Render` round-trip it.

### 2. `internal/tui` — Deps

```go
ShowSplash bool   // runUI sets this; the zero value (false) keeps tests quiet
Version    string // e.g. "v1.2.0" or "dev"
Commit     string // e.g. "a1b2c3d" or "none"
```

`Deps{}` defaults `ShowSplash` to `false`, so **every existing App test renders
the normal frame and stays green**. Only `runUI` turns the splash on.

### 3. `internal/tui/app.go` — the overlay

- `splashActive bool`, initialized to `deps.ShowSplash` in `NewApp`.
- `Init()` returns `tea.Tick(splashDuration, func(time.Time) tea.Msg { return splashDoneMsg{} })`
  when `splashActive`, else `nil`. `const splashDuration = time.Second`.
- `Update`: `splashDoneMsg` clears `splashActive`. `WindowSizeMsg` is processed
  normally while the splash is up, so the frame behind it is correctly sized the
  instant it clears. All other messages flow through untouched.
- `routeKey`, in order: (1) `ctrl+c` quits — unchanged, still first; (2) the
  existing `tooSmall()` guard — unchanged, so an undersized terminal behaves
  exactly as today; (3) **if `splashActive`, clear it and return** — the key is
  consumed and does not reach any view or modal; (4) everything else unchanged
  (modals, startup gate / text capture, palette, globals, number jump, focus).
  Only a `tea.KeyMsg` dismisses the splash — a `WindowSizeMsg` or any other
  message does not.
- `View()`: the existing "terminal too small" guard wins first; then, if
  `splashActive`, render the splash; then the normal frame.

Splash body (centered horizontally and vertically via `lipgloss.Place`):

```
                              ✦

                        s  e  n  t  r  a

         Encrypted, deduplicated, agent-aware backups
                   for S3-compatible storage

                       v1.2.0 · a1b2c3d
```

The `✦` and wordmark use `ui.AccentPink`; the tagline `ui.FgMuted`; the version
line `ui.FgMuted`. The version line renders as `version · shortCommit` when the
commit is known, and as just `version` when `Commit` is empty or `"none"`.
`shortCommit` truncates to 7 characters.

### 4. Wiring — `cmd/sentra` → `internal/cli` → `internal/tui`

`cmd/sentra/main.go` already holds `version`/`commit`. Thread them into
`cli.UIDeps` (new `Version`, `Commit` fields), and in `runUI`:

```go
showSplash := true                      // first run: no config on disk yet
if st.ConfigExists {
    showSplash = !st.Config.UI.HideSplash
}
```

`probeLaunchState` already loads the config on both the gate and dashboard
paths, and `launchState.Config` is documented as always non-nil on a nil error —
so no extra load and no nil check are needed.

Pass `ShowSplash`, `Version`, `Commit` into `tui.Deps`. `sentra local` inherits
this automatically (it calls the same `runUI`), persisting its own toggle in
`.sentra-local.yaml`.

### 5. `internal/tui/settings.go` — the toggle

Add a toggle row alongside the existing launcher entries:

```
Welcome splash        [on]     show the logo screen at launch
```

- `enter` on the row takes a **copy** of `*deps.Config`, flips
  `UI.HideSplash` on the copy, and calls `config.Write(deps.ConfigPath, &copy)`.
  Only on a successful write does it update the in-memory `deps.Config` — so a
  failed write never leaves the process disagreeing with the file on disk.
- Config-only mutation: no repo lock, no confirmation modal — it is
  non-destructive and instantly reversible.
- Takes effect on the next launch (the current session's splash is already gone).
  The row's help text says so.
- When `deps.Config == nil` or `deps.ConfigPath == ""` (first run, before setup
  writes a config), the row renders disabled with the hint
  `available after setup`.
- A failed `config.Write` renders an inline error on the view and leaves the
  in-memory value unchanged.

## Data flow

```
cmd/sentra (version, commit)
   └─> cli.UIDeps{Version, Commit}
        └─> runUI: reads cfg.UI.HideSplash  ->  ShowSplash
             └─> tui.Deps{ShowSplash, Version, Commit}
                  └─> NewApp: splashActive = ShowSplash
                       ├─ Init()  -> tea.Tick(1s) -> splashDoneMsg -> splashActive = false
                       ├─ routeKey: any key (except ctrl+c) -> splashActive = false, key consumed
                       └─ View():  tooSmall > splash > normal frame

SettingsView: enter on toggle -> cfg.UI.HideSplash = !… -> config.Write -> next launch
```

## Error handling

- **Config write fails on toggle:** show the error inline in the Settings view;
  do not mutate the in-memory config; the splash setting is unchanged.
- **No config (first run):** splash shows (default on); the Settings toggle is
  disabled with an explanatory hint.
- **Unknown version/commit:** `version` falls back to whatever `cmd/sentra`
  holds (`"dev"`); the commit segment is omitted when `Commit` is `""` or
  `"none"`.
- **Terminal below minimum size:** the existing too-small guard renders instead
  of the splash; no special handling.

## Testing

`internal/config`
- `Render` emits the `ui:` section; `Load` parses `hide_splash`; a config file
  without the section loads as `HideSplash == false` (splash on).
- Round-trip: `Write` → `Load` preserves the value.

`internal/tui` (drive the **App**, since the splash lives in the shell)
- `Deps{ShowSplash: true}` → `View()` contains the wordmark; `Deps{}` → it does
  not (guards every existing frame test).
- `splashDoneMsg` clears the splash and the normal frame renders.
- Any key while the splash is up clears it **and is consumed** (the view behind
  does not receive it).
- `ctrl+c` quits even while the splash is up.
- The too-small guard takes precedence over the splash.
- Version line: renders `version · shortcommit`, and just `version` when the
  commit is `"none"`.

`internal/tui/settings.go`
- `enter` on the toggle flips `HideSplash` and calls `config.Write`.
- Disabled state when `Config`/`ConfigPath` are absent.
- A write error surfaces inline and leaves the value unchanged.

`internal/cli`
- `runUI` sets `ShowSplash=false` when `hide_splash: true`, `true` when absent,
  and `true` on the first-run path.

**Regression guard:** the whole existing `internal/tui` suite must stay green
without modification — `Deps{}` leaves the splash off, so no frame, overflow,
registration, or routing test changes.

## Scope

One implementation plan. Five small, well-bounded units (config field, Deps,
App overlay, wiring, Settings toggle), each independently testable, no new
package, no change to the view registry or key-routing precedence beyond a
single early-return.
