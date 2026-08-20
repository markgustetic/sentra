# Connect gate: repo-open failures land in the TUI, not on stderr

**Date:** 2026-08-20
**Status:** Approved

## Problem

Bare `sentra` (and `sentra ui` / dashboard-path launches) exits to the CLI
when the configured repository cannot be opened — expired AWS SSO session,
unreachable bucket, network down. The operator sees an error line and is on
their own to know that `aws sso login --profile <profile>` fixes it. The TUI
already owns the other two launch states (first-run → wizard,
locked → unlock); "configured but unreachable" should be a TUI state too,
with the fix one keypress away.

Observed trigger: leftover AWS config + expired SSO produced
`open repo: … create oauth2 token: login session has expired, please
reauthenticate` and a dead CLI exit.

## Decision

Approach A of three considered (B: overload the unlock view with an error
banner — muddies unlock's single job; C: auto-run login without a gate —
runs a browser-opening external command the operator didn't ask for).

A new hidden launch-gate view, id `connect`, mirroring unlock's mechanics:

- `runUI`'s dashboard path, on `openRepoForConfig` failure, launches the
  App with `InitialView: "connect"` instead of returning the error.
- The gate shows the mapped open error and offers:
  - **`l` — reauthenticate:** run `aws sso login` (with `--profile` when the
    config names one) via `tea.ExecProcess`, exactly like the setup wizard's
    interactive auth step; when the child exits, automatically retry the
    repo open. Rendered ONLY when the backend is AWS proper
    (`Repo.S3.EndpointURL == ""`) — an S3-compatible target (MinIO, R2)
    gets nothing SSO can fix.
  - **`r` — retry:** re-run the repo open (transient network failures).
  - **`q` / ctrl+c — quit.**
- On a successful open the gate forwards `repoReadyMsg{repo, config}` — the
  App rebuilds against the live repo and lands on the dashboard, exactly as
  unlock does (`app.go` repoReadyMsg handling).

## Scope — which failures route here

Only "configured, passphrase available, open failed": the
`openRepoForConfig` error on `runUI`'s dashboard branch. Explicitly NOT
routed to the gate:

- config load / probe errors (broken YAML is a fix-the-file problem; still
  exits to CLI with the now-silenced usage),
- first-run (wizard) and no-passphrase (unlock) — existing gates,
- non-TUI commands (`sentra backup` etc.) — CLI errors stay CLI errors.

## Design

### CLI side (`internal/cli/ui.go`)

The dashboard branch becomes:

```go
r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
if err != nil {
    // launch the connect gate instead of dying to stderr
    return launchConnectGate(cmd, deps, cfgPath, absCfgPath, st, err, showSplash)
}
```

The gate launch constructs the App the same way the wizard/unlock branch
does (no repo), with `InitialView: "connect"` plus two new `tui.Deps`
fields:

- `ConnectError error` — the open failure the gate renders on first frame.
- `OpenRepo func(ctx context.Context) (*repo.Repo, *config.Config, error)`
  — retry closure. `runUI` wires it to re-run `openRepoForConfig` with the
  same cmd/deps/cfgPath, zeroizing the passphrase internally before
  returning. This is the same closure-injection seam style as
  `NewStore`/`SavePassphrase`, so `internal/tui` keeps zero imports of
  `internal/cli`. The closure must construct a fresh open per call (the
  passphrase chain re-resolves: env / file / keyring).

Note on ownership: on the gate path `runUI` must NOT `defer r.Close()` /
zeroize (there is no repo yet); the closure owns each attempt's lifecycle,
and the App's existing repoReadyMsg/cleanup path owns the successful repo.

### TUI side (`internal/tui/connect.go`, new)

`ConnectView`, registered beside unlock and added to `hiddenFromRail`
(`app.go:326`) so it never appears in the rail or palette. Small state
machine like unlock's (`connectIdle`, `connectOpening`, `connectAuthing`):

- **Render:** repo name / bucket, the error (wrapped, `ui.Muted` help line
  listing the keys), and — AWS-proper only — the exact command it will run
  (`aws sso login --profile sentra`), so the operator knows what `l` does
  before pressing it.
- **`r`:** returns a `tea.Cmd` invoking `deps.OpenRepo`; result comes back
  as a private `connectResultMsg{repo, config, err}` (mirror of
  `unlockResultMsg` — a launch-path open takes no advisory lock, so the
  one-op guard is not involved). Success → forward `repoReadyMsg`; failure
  → replace the displayed error, back to idle. A spinner renders while
  opening.
- **`l`:** reuse `interactiveAWSAuthCommand(ctx, effects,
  setup.AWSAuthSSO, profile, region)` (`setup_wizard.go:1131`) wrapped in
  `tea.ExecProcess`; completion arrives as the view's own
  `connectAuthDoneMsg{err}` (not the wizard's `awsAuthDoneMsg` — separate
  views must not share private message types). On child success → auto
  `r`; on child failure → show that error instead. `l` while already
  opening/authing is ignored (single in-flight action).
- **`CapturesText() == false`** (plain key commands, no text field);
  **`InertContent()`** is NOT declared — the gate owns real interactions.
- **Quit:** `q`/ctrl+c via the normal App bindings (no text capture).

Security notes:

- The exec'd argv is built from local config values (profile, region) by
  the existing builder — never from remote/manifest data, never through a
  shell.
- No secrets rendered: the error text comes from the SDK/repo layers,
  which do not embed credentials; the passphrase never reaches this view
  (the closure zeroizes internally).

### Message/type inventory (new)

- `tui.Deps.ConnectError error`, `tui.Deps.OpenRepo func(context.Context)
  (*repo.Repo, *config.Config, error)`
- `connectResultMsg{repo, config, err}`, `connectAuthDoneMsg{err}` —
  private to the view file.

## Testing (TDD)

- **cli (`ui_test.go`):** dashboard-path open failure routes to
  `InitialView == "connect"` with `ConnectError` set and `OpenRepo`
  non-nil (stub `NewStore` returning an erroring store); config-load
  failure still exits to CLI (no App constructed). `sentra local` path
  unaffected.
- **tui (`connect_test.go`):** table over the view contract —
  - AWS backend renders the `l` hint + exact command; endpoint backend
    hides it (`l` keypress is a no-op),
  - `r` with a succeeding `OpenRepo` stub yields `repoReadyMsg` carrying
    that repo,
  - `r` with a failing stub replaces the error and returns to idle,
  - `connectAuthDoneMsg{nil}` triggers an automatic open attempt;
    `connectAuthDoneMsg{err}` renders the auth error,
  - App-level: `connect` is hidden from rail and palette (mirror the
    existing unlock hidden-view test if one exists; otherwise assert via
    the sidebar/palette render).
- Selection/affordances follow the glyph-not-color rule; tests run under
  the Ascii profile as usual.

## Documentation

- AGENTS.md: the launch-state table gains "configured but unreachable →
  connect gate (retry / SSO login)".
- CLAUDE.md TUI notes: one line — repo-open failures land on the connect
  gate; only config errors exit to the CLI.

## Risks / notes

- `aws sso login` needs the AWS CLI; the wizard's builder path already
  handles the missing-binary case via `setup.ErrorAdvice` — the gate
  surfaces the ExecProcess error rather than pre-probing (YAGNI; the
  error text names the missing binary).
- Terminal state after `tea.ExecProcess` is Bubbletea-managed (wizard
  already relies on this; no new risk).
- The gate never auto-runs the login on entry — one explicit keypress.
