# Desktop notifications for backup runs

Date: 2026-09-10

## Problem

A backup that runs from a launchd or systemd timer at 3am is silent. The
result lands in `~/Library/Logs/sentra-<name>.log` and nobody is told. The
only way to be notified today is to hand-write `hooks.after` /
`hooks.on_failure` shell commands in `sentra.yaml`, which neither surface
exposes and the README does not mention. An operator who set up a schedule
in the TUI's Backup wizard has no idea whether last night's run happened.

## Behavior

- **On by default; nothing to set up.** Every policy run fires a desktop
  notification on success and on failure, from every surface that runs a
  policy: the TUI's Scheduled backups run, the Backup wizard's scheduled
  policy, and `sentra policy run` (what the OS timers invoke). The TUI's
  one-shot backup notifies too, because the operator may have switched away
  from the terminal during a long run.
- **Success**: title `Sentra`, subtitle `Backup complete`, body
  `<name>: <files> files, <new bytes> new` (plus `, <n> skipped` when the
  walk dropped denied subtrees). `<name>` is the policy name, or the source
  folder's base name for a one-shot backup.
- **Failure**: subtitle `Backup failed`, body `<name>: <first line of the
  error>`, truncated so the notification stays readable.
- **A not-due `--if-due` launch stays silent**, exactly as it owes no hook.
  Config-shape errors (policy not found, invalid policy) are not run failures
  and do not notify either.
- **Opt out** with `notify.disable_desktop: true` in `sentra.yaml`, or the
  new "Desktop notifications" toggle in the TUI Settings view. The toggle
  persists through `config.Update` like the splash toggle, so a
  `SENTRA_*` overlay is never written back.
- **Ad-hoc `sentra backup` stays quiet.** It is a foreground command whose
  result prints to the terminal it ran in.

## Mechanism

### `internal/notify` (new, leaf package)

```go
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error)
func Desktop(ctx context.Context, run Runner, goos, title, subtitle, body string) error
```

- `darwin`: `osascript -e 'on run argv' -e 'display notification (item 3 of
  argv) with title (item 1 of argv) subtitle (item 2 of argv)' -e 'end run'
  <title> <subtitle> <body>`. The strings travel as argv, never inside the
  AppleScript source, so a policy name or error text containing quotes or
  backslashes cannot break out of the script.
- `linux`: `notify-send --app-name Sentra "<title> — <subtitle>" <body>`.
- Any other GOOS: no call, nil error.
- `ExecRunner` bounds the call with a 5 s timeout. Best-effort by contract:
  callers log a failure to the run's output and never let it mask the run's
  own result.
- A nil `Runner` means notifications are off. Production wires
  `notify.ExecRunner` explicitly; a zero-value `Deps` in tests can never pop
  a real notification. (This deliberately differs from `scheduler.Runner`,
  whose nil means the real thing: a missing timer activation is a broken
  feature the operator notices immediately, a missing notification is not,
  and the failure mode of the other default is the test suite spamming the
  developer's desktop.)

### `internal/policy/notify.go`

```go
type BackupOutcome struct {
    Name     string // policy name or folder base name
    Files    int
    NewBytes int64
    Skipped  int
}
func NotifyBackup(ctx context.Context, out io.Writer, run notify.Runner, enabled bool, o BackupOutcome, runErr error)
```

Formats the success or failure message, calls `notify.Desktop` with
`runtime.GOOS`, and on error writes `notification failed: <err>` to `out`.
Returns nothing: the caller's `runErr` is the run's result. Lives in
`internal/policy` next to `FireFailureHooks` so the CLI and TUI cannot
drift. `enabled` is `!cfg.Notify.DisableDesktop`; a nil config is enabled.

### Config

```go
Notify struct {
    DisableDesktop bool `koanf:"disable_desktop"`
} `koanf:"notify"`
```

Negated like `ui.hide_splash`: a file written before the field existed loads
as "notify", with no migration and no pointer field. `render.go` emits the
`notify:` section with a comment.

### Wiring

- `cli.PolicyDeps.Notify notify.Runner`; `runPolicy` calls `NotifyBackup`
  after the hook envelope with the aggregated snapshot stats, skipping
  `errPolicyNotDue`. `cmd/sentra/commands.go` wires `notify.ExecRunner`.
- `tui.Deps.Notify notify.Runner`; `buildPolicyRunOp` and
  `BackupView.startBackup` call `NotifyBackup` inside their op closures.
  `internal/cli/ui.go` wires `notify.ExecRunner` in each `tui.Deps` literal.
- Settings view: `entryToggleNotify`, label "Desktop notifications", state
  `[on]`/`[off]`, same op pattern as the splash toggle.

### Testing

- `internal/notify`: table test over GOOS asserting the exact argv for
  darwin and linux (including that quotes in the body reach argv verbatim)
  and no call for other platforms; nil runner is a no-op.
- `internal/policy`: `NotifyBackup` success and failure bodies; disabled
  makes no call; a runner error is written to `out` and does not panic.
- `internal/cli`: `policy run` fires the recorder on success and on failure,
  and not for a not-due `--if-due` launch or an unknown policy.
- `internal/tui`: the jobs run and the one-shot backup fire the recorder;
  settings toggle persists `disable_desktop` and flips the row label.
- A guard test in `internal/cli` that the production `tui.Deps` literals
  set `Notify` (by grepping `ui.go`, the way the config-path walker test
  guards every command) so the wiring cannot be dropped silently.

### Docs

README "Configuration" gains a "Notifications and hooks" subsection
(default on, opt-out key, the Settings toggle, the one-time macOS
"Script Editor" permission prompt, and the existing hooks/webhook). AGENTS.md
gains a contract line next to the policy-hooks bullet.

## Out of scope

Success webhooks, per-policy notification overrides, a bundled
`terminal-notifier`, Windows.
