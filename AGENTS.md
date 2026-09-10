# AGENTS.md

Guidance for coding agents working in this repository.

If the local Codex skill `$sentra-maintainer` is available, use it for
Sentra code, docs, CI, or release workflow changes.

## Project Shape

- Sentra is a Go CLI/TUI for encrypted, deduplicated S3 backups.
- Main command wiring lives in `cmd/sentra/main.go`.
- Production passphrase/keyring wiring lives in `cmd/sentra/passphrase.go`.
- Core repository behavior lives in `internal/repo`.
- Passphrase resolution and OS keyring helpers live in `internal/config`.
- CLI command implementations live in `internal/cli`.
- Named policy validation lives in `internal/policy`.
- Agent heuristics/orchestration live in `internal/agent`.
- Bubbletea views live in `internal/tui`. The TUI is the default surface: bare
  `sentra` falls through to `sentra ui`, fronted by a first-run setup wizard. It
  owes the CLI no flag-for-flag coverage — see the surface contract in Feature
  Notes below.
- The MCP server lives in `internal/mcpserver` (official
  `modelcontextprotocol/go-sdk`); `sentra mcp` (`internal/cli/mcp.go`) serves
  it over stdio. See its Feature Note for the metadata-only / two-phase
  mutation contract.
- The headless setup engine — a pure state model plus an `Effects` seam for
  AWS/keyring and a stepwise `Engine` — lives in `internal/setup`; the TUI
  wizard drives it directly, and `sentra setup` is a thin CLI launcher for that
  same wizard, so setup logic is never duplicated between them.
- Vendored FastCDC source lives under `third_party/fastcdc-go`.

## Working Rules

- Prefer small, focused changes that match the existing package boundaries.
- Use `rg`/`rg --files` for searching.
- Use `apply_patch` for manual edits.
- Do not revert unrelated user changes.
- Keep generated or local files out of commits. `coverage.out` is ignored.
- Do not put secrets, passphrases, wrapped keys, salts, or MAC material in docs,
  tests, logs, recovery kits, or fixtures.

## Verification

Run the narrowest relevant tests while developing, then run the broader checks
before claiming completion:

```bash
just test
just vet
go mod tidy -diff
git diff --check
```

Also run the vendored module tests when changes touch chunking, module setup,
or CI:

```bash
go test ./third_party/fastcdc-go/...
```

`just lint` requires `golangci-lint`. If it is missing, install it with:

```bash
brew install golangci-lint
```

or:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
```

## Feature Notes

- `sentra setup` is the guided first-run surface. It may print non-secret IAM
  policy JSON and stop, invoke AWS CLI browser login, AWS CLI SSO
  configure/login flows, write config, prepare AWS S3 bucket settings, and
  initialize the repo. It may save a repository passphrase to the OS keyring
  after the user chooses that setup option, but must never write secret
  material to `sentra.yaml`, setup drafts, docs, or logs. Setup drafts are
  non-secret resume state only and should be removed after successful setup.
  It resolves `--passphrase-file` then `SENTRA_PASSPHRASE` before offering a
  passphrase field: the repo must initialize under the same secret every later
  command resolves, or the mismatch surfaces later as an undecryptable repo.
  The review screen names the SOURCE, never the secret.
  After a browser-login or SSO sign-in it may create IAM user
  `sentra-backup`, attach the canonical `BuildIAMPolicy` document as that
  bucket's customer-managed policy (`sentra-s3-backup-<bucket>`), mint an
  access key into a dedicated `~/.aws/credentials` profile (default
  `sentra`, or `sentra-backup` when the session profile is itself called
  `sentra` — `setup.ResolveBackupUserProfile`), verify it, and switch
  `sentra.yaml` to that profile — the session identity is used once and
  retired. The step is pre-checked for browser login, offered unchecked for
  SSO, and absent for existing-credentials/skip/S3-compatible
  (`setup.ShouldProvisionBackupUser` is the single gate). It must never
  write the `default` credentials profile, never modify `~/.aws/config`,
  never overwrite a credentials section that already holds keys, never use
  the profile setup signed in with or one `~/.aws/config` already defines
  (`ErrBackupUserProfileIsSession` / `ErrConfigProfileExists`: static keys
  under that name would shadow the SSO or role definition, since
  aws-sdk-go-v2 resolves a profile's keys before its `sso_*` settings), and
  never let the secret reach the
  report, plan, draft, review text, logs, or an error. The profile switch
  happens only after the new identity verifies (bounded retry); any failure
  degrades to `BackupUserReport.Warning` and setup continues on the session
  credentials — provisioning never blocks setup. Buckets accumulate on the
  user: each has its own managed policy, so a later wizard run in the same
  account must never revoke an earlier bucket's grant. An existing policy is
  reconciled by merging its stored resources into the canonical document (a
  sibling prefix in the same bucket keeps its access) and rewritten as a new
  default version only when that changes something, deleting the oldest
  non-default version first at IAM's five-version cap. The inline policy
  from before per-bucket policies (`sentra-s3-backup`) is deleted only after
  the managed policy is attached and only when the managed one covers every
  grant it made; that cleanup is best-effort and never fails setup. Every
  IAM mutation that can fail precedes the key mint, so a policy failure
  never strands a live secret. The ten-managed-policies-per-user quota is
  its own warning (`PolicyLimit`); the two-keys quota (`KeyLimit`) is bound
  to `CreateAccessKey` alone.
  AWS CLI **brew auto-install is currently absent**: it needed a confirm prompt,
  and `huh` cannot run inside a live `tea.Program`. `setup.DefaultEnsureAWSCLI`
  keeps the machinery behind a confirm no caller arms, so restoring it means a
  TUI confirm modal — not new logic. Until then a missing `aws` binary gets one
  actionable message on every platform.
- `sentra setup iam-policy` must emit non-secret IAM JSON only.
- `sentra doctor` is read-only. It may validate config, AWS identity, bucket
  access/settings, and repo health, but must not create buckets, change bucket
  settings, initialize repos, or write config.
- `sentra check` is the shared integrity surface for CLI and TUI operations.
- **A skipped folder is never silent.** The walker drops a subdirectory
  whose listing is denied (`fs.ErrPermission`; TCC-protected folders under
  `~/Library` on macOS) and keeps going, so the snapshot succeeds short of
  it. Every surface that takes a snapshot must say so: `SnapshotOptions.OnSkip`
  is the seat the CLI and TUI wire (`resolveWalkerOptions` carries a
  `Walker.OnSkip` through its zero-value defaulting, and plan-driven walks
  re-attach it from `SnapshotOptions` because the JSON plan cannot hold a
  callback), and `SnapshotStats.Skipped` records the count in the manifest
  whether or not anyone listened. `backup` / `backup plan` / `backup apply`
  print `skipped <path>: permission denied` on stderr as it happens (beside
  the progress bar, and under `--json` too so stdout stays one document);
  `policy run` prints the same line to its log; `--json` rows, the TUI
  backup and job-run done screens, and `confirm_backup` carry the count.
  Human summaries show the count only when it is non-zero — it is a
  warning — while JSON always emits it. A new snapshot-taking surface
  must wire `OnSkip` or show `Stats.Skipped`; the walker's silent default
  is for library callers, not operators.
- Config rewrites must not persist env overrides. `config.Load` returns the
  *resolved* config (sentra.yaml + `SENTRA_*` overlay); rendering that back to
  disk would make a transient override permanent. To change a field of an
  existing `sentra.yaml` (settings toggles, policy add/remove, `passwd forget`),
  use `config.Update`, which rebases the edit on the file as it exists on disk.
  `config.Write` authors the whole file from a resolved config and is correct
  only for `sentra init` and `sentra setup`, which must record the bucket they
  just provisioned against.
- `sentra policy` manages non-secret named backup policies in `sentra.yaml`.
  Policy config may include local paths, tags, schedule metadata, and
  post-backup check/prune preferences, but must never include passphrases,
  key material, AWS credentials, or other secrets. `sentra policy run` should
  reuse existing repo snapshot/check/prune primitives instead of duplicating
  storage logic. **Policy paths are stored absolute.** `policy add` resolves
  every `--path` through `policy.ResolvePath` (`~` → home, relative → the
  operator's cwd, then `repo.ResolveRoot`'s form: cleaned, symlinks
  resolved) before persisting, and `Validate` rejects a path that cannot
  resolve or that exists as a non-directory. A path that does not exist yet
  resolves its longest existing prefix and keeps the rest as spelled, so the
  stored string is the root a later snapshot of it records; a dangling
  symlink at or above the path is refused (`policy.ErrDanglingSymlink`),
  since storing the link's spelling would stop matching the moment its
  target appears. A
  timer-launched run has no cwd the operator chose
  (launchd starts jobs in `/`), so `policy run` resolves each stored path
  again with `policy.ResolvePathFrom`, anchoring any relative path that a
  hand-edited or pre-resolution `sentra.yaml` still carries to the config
  file's directory — never the process cwd. `policy add --replace` that
  changes the schedule resyncs an installed timer through
  `scheduler.Resync` (manual → deactivate + uninstall; otherwise re-render,
  reinstall, re-activate): the OS keeps firing whatever calendar it loaded,
  so rewriting `sentra.yaml` alone leaves `schedule status` lying. An
  unchanged schedule never shells out. `policy remove` uninstalls the
  policy's OS timer when present — deactivates it, then removes the files
  (best-effort, warning on failure) — an installed timer for a deleted
  policy can only fail.
- `sentra schedule` installs user-level OS scheduler entries for named
  policies. It generates launchd/systemd files that invoke `sentra policy
  run`; do not introduce a resident Sentra daemon or write secrets into
  scheduler files. **Writing the files is not installing:** a LaunchAgent is
  only picked up at the next login and an un-enabled systemd timer never
  fires, so `install` also activates — darwin `launchctl bootout` (so a
  re-render replaces the loaded job) then `launchctl bootstrap gui/$UID
  <plist>`, falling back to legacy `launchctl load -w` when bootstrap is
  unknown; linux `systemctl --user daemon-reload && systemctl --user enable
  --now sentra-<name>.timer`. `uninstall` deactivates FIRST (`bootout` /
  `disable --now`; an already-unloaded label is success), then removes the
  files, and removes them even when the OS could not be told. Activation
  runs through an injectable `scheduler.Runner` (nil = `ExecRunner`); the
  render/write helpers (`Render`, `Install`, `Uninstall`, `Installed`) stay
  filesystem-only, and **tests must inject a fake runner** — the real one
  loads a job on the developer's machine. A failed activation leaves the
  files in place and returns `*scheduler.ActivationError`, whose message
  names the exact command for the operator (headless SSH, no user bus,
  missing binary); `install`/`uninstall` still print their summary and exit
  non-zero. `schedule status` asks the OS (`launchctl print` /
  `systemctl --user is-active`) and prints `timer: active`, `timer: not
  active — run: <command>`, or `timer: unknown (<why>)` when it could not
  ask; the computed next run (`policy.NextRun` — wall-clock, mirrors the
  renderers) is printed only for an active or unknown timer, never for one
  the OS will not fire. The TUI jobs view's Timer column shows the same
  three states (`active` / `inactive` / `installed` for unknown) and its
  install, uninstall, delete, and edit paths activate/deactivate through
  `Deps.SchedulerRunner`. The view never asks the OS on the UI goroutine:
  being shown re-reads sentra.yaml and stats the timer files, renders an
  installed row as `installed`, and returns a deadline-bounded probe cmd
  whose result settles the column — the rail's live preview shows the
  view on every scroll past it. Its install/uninstall/delete/save, the
  Backup wizard's schedule install, and the Settings toggles run under
  the App's one-op guard (serialized, esc-cancellable, spinner), never
  synchronously inside `Update`.
- Missed slots are caught up anacron-style, and the catch-up lives in the
  command, not the timer. A slot that passes while the machine sleeps fires
  on wake on both platforms; one that passes while it is shut down is
  skipped by launchd (systemd's `Persistent=true` already replays it). So
  the launchd plist sets `RunAtLoad` and both platforms install one command
  shape: `policy run <name> --if-due --startup-delay 1m --config … --log-level
  info`. `--if-due` lists snapshots, finds the policy's last run
  (`policy.LastRun` — the newest snapshot tagged `policy:<name>`, else the
  newest rooted at one of its paths; the same resolver behind the TUI's
  Last-run column) and compares it with the most recent slot
  (`policy.PreviousRun`, NextRun's backward twin): a run at or after the slot
  prints one "not due until <NextRun>" line and exits 0 without taking the
  repo lock; no matching snapshot means due, so a fresh schedule runs at
  once — and since `install` bootstraps the plist immediately, "at once"
  means right after `schedule install` on darwin, not at the next login.
  The check runs inside the hook envelope — a not-due run fires no hook, a
  failed check still fires `on_failure`. `--startup-delay` is for the login
  path only, so the network and keyring can settle. `schedule status` and
  the Schedules view keep showing the computed next slot, never the
  catch-up; the slot math never shells out — only the activation layer
  (`scheduler.Activate` / `Deactivate` / `Active`) talks to `launchctl` /
  `systemctl`.
- `sentra restore --dry-run` must not create or write the destination.
- `sentra restore --verify` should compare restored files against manifest
  chunk hashes.
- `sentra restore <snap> <dest> [path...]` scopes the restore to the named
  files or subtrees; a selector matching nothing is an error, and the
  dry-run/verify forms must scope identically to the real run. Restore is
  phased — dirs, then files, then symlinks LAST, then dir metadata — and the
  order is a security property: no manifest symlink may exist while file
  writes happen, and every write re-checks its resolved parent stays inside
  the destination.
- Snapshot manifests are format v2: entries carry Kind/LinkTarget so symlinks
  (never followed) and directories (modes, empty dirs) round-trip. Loaders
  must refuse manifests newer than they understand. `Stats.Files` counts
  regular files only. A loader also refuses a manifest whose embedded ID
  differs from its key (`ErrManifestIDMismatch`): GC aborts before reaping,
  `check` reports it as a manifest issue.
- The backup root is canonicalised by `repo.ResolveRoot` — absolute,
  cleaned, symlinks resolved, and it must be a directory (`ErrRootNotDir`).
  `Manifest.Root` records the RESOLVED path, so a linked and a real spelling
  of one directory share a retention group; anything that compares a
  configured path against `SnapshotInfo.Root` must resolve the same way.
  `backup plan` writes the resolved root and `backup apply` refuses a plan
  whose root is not already that spelling (`ErrBackupPlanRootUnresolved`)
  rather than rewriting it: a hand-edited or pre-canonical plan must be
  re-planned, not silently snapshotted under a name nobody reviewed.
  The incremental scan confirms each reused chunk still exists (one Stat per
  unique chunk per snapshot) and re-reads the file when one is gone.
- `sentra sync` never deletes a snapshot or chunk on the destination; the one
  thing it removes is the mirror's derived `meta/snapshots` index, after
  copying a manifest that index cannot know about, so the next listing on the
  mirror rebuilds it. That removal runs even when the manifest phase failed
  or was cancelled, on a context detached from the caller's (bounded like
  the lock release): a manifest that landed is on the mirror for good and
  must not stay hidden behind the stale index. A `ListSnapshots` fan-out
  persists its rebuilt index only if it can take the repo lock without
  waiting.
- Snapshot references: everywhere a snapshot ID is accepted, "latest", a
  unique prefix, and a unique suffix resolve via `ResolveSnapshotID`;
  ambiguity is refused with candidates named, never first-match.
- `sentra ls <snapshot>` lists a snapshot's tree read-only (`--json` uses
  explicit kinds: file/dir/symlink).
- `sentra pin` / `sentra unpin` protect snapshots: retention always keeps a
  pinned snapshot (reason "pinned") and `DeleteSnapshot` — the choke point
  for prune, the TUI, and the agent's prune action — refuses with
  `ErrSnapshotPinned`. Pinning a nonexistent snapshot is an error.
- A retention plan is never computed without the pin set. Every planner
  builds its policy through `policycfg.RetentionFromConfig`, which loads
  `meta/pins` first; if that read fails, the CLI's `prune`/`policy run`,
  the TUI's job run (`policyRunDoneMsg` carries the error, nothing is
  deleted, failure hooks fire), and the TUI prune view (a load error, no
  drop list) all fail closed rather than plan around an empty set — a plan
  blind to pins would drop a pinned snapshot on paper and then either fail
  at the choke point or silently skip it. The one tolerance is at delete
  time: a pin placed *between* planning and deleting reaches
  `DeleteSnapshot`'s `ErrSnapshotPinned`, and the automatic prune skips
  that snapshot the way it skips one already gone (GC's live set comes
  from what is present, so nothing of it is reaped). Refusal is tolerated;
  ignorance is not.
- Retention groups by source root (restic-style group-then-apply): each
  backed-up directory gets the policy's full budget. Never regress to flat
  global bucketing — multiple sources in one repo would prune each other.
- Backups are incremental by default: files whose size+mtime match the
  newest prior snapshot of the same root reuse its chunk list unread.
  `backup --rescan` forces a full re-read.
- `sentra check --read-data[-subset]` deep-verifies chunks through the same
  read path restore uses; corrupt blobs are findings (health failures), not
  aborts. Subset sampling must be deterministic.
- `sentra stats` is read-only reporting: dedup factor and per-snapshot
  unique bytes.
- `sentra sync --snapshot <ref>` (repeatable) copies only the selected
  snapshots' manifests plus their chunk closure; unknown selections fail
  before any dest write. SyncTo lists `snapshots/` BEFORE `data/` — the
  unlocked source means the reverse order can copy a manifest whose chunks
  the frozen data listing never saw.
- Policy hooks (`hooks.before/after/on_failure`) run via `sh -c`; a failing
  before-hook aborts the run. The failure webhook URL lives in an env var —
  only the variable NAME may appear in `sentra.yaml`. Hook execution lives in
  `internal/policy` (below both surfaces) and MUST run identically from
  `sentra policy run` and the TUI's policy run — a surface that skips hooks
  backs up different data. A hook's output goes to the timer log, so
  `RunHook` echoes only `hook <label>: running` — never the command line,
  which carries inline credentials (`PGPASSWORD=… pg_dump`) — and the child
  environment is `HookEnv`: `os.Environ()` minus every `SENTRA_*` variable
  and the configured webhook variable. Pass `hooks.OnFailureWebhookEnv` to
  `RunHook` from every surface so before/after are scrubbed like on_failure.
- Every backup run notifies the desktop (`internal/notify`: osascript on
  darwin with the strings as argv into an `on run` handler, notify-send on
  linux, no-op elsewhere) through `policy.NotifyBackup`, called from
  `sentra policy run`, the TUI's policy run, and the TUI's one-shot backup.
  On by default; `notify.disable_desktop` is the negated opt-out so older
  files keep notifying. Best-effort: a notifier failure is logged, never
  returned. A nil `notify.Runner` is OFF — the opposite of
  `scheduler.Runner` — so a zero-value Deps in tests never pops a real
  notification; production wires `notify.ExecRunner` explicitly, and
  `TestProductionWiresNotifyRunner` guards those literals. A not-due
  `--if-due` launch and config-shape errors notify nothing, like hooks.
  Ad-hoc `sentra backup` does not notify.
- Surface contract — the obligation between the two surfaces runs ONE WAY.
  The CLI is the machine and recovery surface: every capability lands in the
  core layer plus a CLI verb, always. Three consumers depend on that and none
  of them can press a key — `internal/scheduler` emits systemd/cron units whose
  `ExecStart` invokes this binary, the recovery kit prints commands to type when
  the machine is gone, and the test suite drives the CLI. A mutating capability
  with no CLI verb is a defect, not a style choice.
  The TUI is the human surface and the default one, and it owes the CLI no
  flag-for-flag coverage. It owes a floor instead: setup/reconfigure, unlock,
  backup, run a named policy, restore, browse snapshots, check, prune, and
  recovery kit must each be completable start to finish without leaving the
  TUI. A gap inside the floor is a bug; anything outside it is CLI-at-will,
  needing no TUI affordance and no entry in any list. Completable does not
  mean rail-listed: the rail holds seven destinations (Dashboard, Backup,
  Schedules, Snapshots, Maintenance, Settings, Help) and the rest of
  the floor lives one launcher inside them — restore/diff from a snapshot
  row, check/prune/sync/doctor from Maintenance, recovery-kit/passphrase/
  setup from Settings. The jobs view (id `jobs`, rail title "Schedules")
  sits on the rail under Backup; the Backup view is a three-step wizard
  (Location → Schedule → Confirm) whose Schedule step writes a named policy
  and OS timer through the same `config.Update` + `scheduler` path
  `policy add`/`schedule install` use. It replaced the separate Policies and
  Schedule views and is what satisfies the floor's run-a-named-policy item.
  Stats and the agent are CLI-only (outside the floor by the sentence above).
  Per-run knobs (`prune --keep-*`, `--concurrency`, `--stale-lock-after`,
  agent `--root`/`--categories`/`--local-only`/`--max-tool-calls`) come
  from config in the TUI by design.
- `repo.s3.storage_class` passes through to PutObject; GLACIER and
  DEEP_ARCHIVE must stay refused (synchronous chunk reads cannot retrieve
  them). `backup.max_upload_rate` paces uploads only — never throttle
  restore.
- `sentra password` rotates the wrapping passphrase. If
  `passphrase.use_keyring` is true, it rotates the repo passphrase FIRST, then
  overwrites the OS-keyring entry with the new passphrase. The entry is keyed by
  bucket+prefix, which rotation does not change, so the save overwrites in place
  and no pre-delete is needed. If the rotation fails, the keyring is left
  untouched so the repo and keyring stay consistent on the old passphrase. If
  saving the new keyring entry fails after a successful rotation, return a clear
  error that the repo passphrase was rotated but the keyring update failed.
  `sentra passwd` is a compatibility alias.
- OS keyring entries are scoped by configured S3 bucket and prefix so multiple
  repos can share one bucket under different prefixes. Keyring lookup may try
  legacy bucket-only entries only after the bucket+prefix entry is not found;
  it must not fall back after other keyring errors. `sentra password forget`
  may remove current and legacy keyring entries and disable keyring lookup
  locally, but must not change the repo passphrase or delete S3 data.
- `sentra prune` is dry-run by default. `--apply` mutates; `--explain` shows
  retention reasons. `--apply` runs GC even when retention drops nothing —
  orphaned blobs (crashed backups, out-of-band deletes) never enter the drop
  set, and prune is the only sanctioned way to reclaim them. The empty-drop
  pass calls GC with nil keepIDs (bare-orphans mode), which refuses a
  zero-snapshot store rather than treat "no manifests" as "everything is
  garbage". The same rule binds `policy run`'s apply-mode prune step and the
  TUI job runner; there the ErrEmptyRepo refusal is a calm no-op, never a
  failed run.
- `sentra agent scan --local-only` and `--no-llm` must not call the LLM provider.
- `sentra agent advise-ignore` is read-only and must not edit `.sentraignore`.
- `sentra recovery-kit` is non-secret documentation only.
- `sentra mcp` serves the repository to MCP clients over stdio. stdin/stdout
  ARE the protocol channel, so the passphrase must resolve non-interactively
  (`--passphrase-file` / `SENTRA_PASSPHRASE` / OS keyring) — a missing source
  is a startup error, never a prompt — and diagnostics go to stderr only.
  The read tools (`list_snapshots`, `snapshot_files`, `find`,
  `diff_snapshots`, `repo_stats`) return METADATA only — ids, tags, dates,
  file names and sizes — never file contents and never secret values.
  Mutations are two-phase because MCP has no interactive confirm:
  `plan_backup` / `plan_restore` change nothing and return a human-readable
  plan plus a single-use token (`plan_backup` names the symlink-resolved
  root `confirm_backup` will record as `Manifest.Root`, so the reviewed
  path and the recorded one never differ) (10-minute TTL, bound to its own kind — a
  backup token cannot confirm a restore); only the matching `confirm_*`
  call carrying that token executes. A token is consumed on use, success
  or failure. `confirm_backup` walks with the same resolved `backup.*`
  options (`ignore_file`, `exclude_caches`, `concurrency`) as
  `sentra backup` — the CLI passes them in via `mcpserver.Options`, since
  the server never reads `sentra.yaml` itself. Never add an MCP tool that
  mutates in one call or that can return file contents.
- The TUI assistant chat overlay (`ctrl+a`, `internal/tui/chat.go`) is a
  conversational command palette, distinct from `sentra agent`. It requires a
  configured LLM provider (`ANTHROPIC_API_KEY`); without one it opens with a
  setup hint and stays inert. Its read tools answer from snapshot METADATA
  only, and its action tools never execute anything — they compile into the
  exact messages the keyboard already routes (`activateMsg`, `chatBackupMsg`,
  `launchRestoreMsg`), so every existing confirmation gate applies by
  construction. Do not add a chat tool that bypasses those messages or the
  one-op guard. The overlay is unavailable inside the startup gates
  (wizard / unlock / connect), and `esc` cancels an in-flight streaming turn
  before a second `esc` closes the overlay.

## Config resolution

Every repo-facing command resolves its config path the same way:

1. An explicit `--config <path>` is used verbatim — no discovery — and must
   already exist. A missing explicit path fails before any load with an
   error naming it (`--config <path>: file does not exist …`), never a
   defaults-only run: `config.Load` tolerates absence on purpose, and
   letting that reach an explicit path made `policy list` report "No
   policies configured" for a typo and let `config.Update` author a new
   file there. Only `ui` and `setup`, which host the wizard that creates
   the file, accept an explicit path that does not exist yet.
2. Otherwise `./sentra.yaml`, when it exists as a regular file.
3. Otherwise `$XDG_CONFIG_HOME/sentra/sentra.yaml`, with unset/empty
   `XDG_CONFIG_HOME` defaulting to `~/.config` (the gh-CLI convention,
   not `os.UserConfigDir`).

When neither file exists, the home path is still the resolved target: a
first run from any directory lands on the TUI setup wizard, which
persists `~/.config/sentra/sentra.yaml`, so bare `sentra` opens the
configured repo from anywhere afterwards. `config.Write` creates the
missing parent directory (0700).

A config that loads but whose passphrase no source can supply lands on
the unlock gate instead.

Configured but unreachable — the config loads and a passphrase source
answers, but the repository fails to open (expired AWS credentials,
unreachable bucket) — lands on the **connect gate**: it explains the open
error in plain words when the cause is known (`diag.Explain`; the raw
chain renders only for unrecognized causes — the CLI stays verbatim for
detail) and offers its actions as a glyph-selected menu
(`↑`/`↓` + enter) with matching hotkeys: `r` retry and, for AWS-proper
backends only (no `endpoint_url`), `l` to run the profile's reauth
command via a suspended terminal — `aws sso login` when the AWS CLI
config shows an SSO-configured profile, the browser `aws login` flow
otherwise (`--region`/`--profile` from config; the child's stderr is
captured and shown on failure, since the alt-screen restore erases the
scrollback) — auto-retrying on return, plus `q` quit. A successful open swaps the live
repo in and lands on the dashboard. Config-load errors still exit to the
CLI. The login never auto-runs.

Exceptions: `sentra init` writes `./sentra.yaml` only (scripting /
recovery surface; never reaches outside cwd). `sentra local` always uses
`.sentra-local.yaml`. `sentra sync` resolves its *source* config through
discovery; its destination comes only from `--dst-config`, which must
also name an existing file.

Implementation: `config.DiscoverPath()` (internal/config/discover.go),
applied by `resolveConfigPath` (internal/cli/config_path.go) as the first
statement of every run body — at RunE time, because `Flags().Changed` is
only meaningful after argv parsing. It returns the missing-file error;
`resolveConfigPathForLaunch` is the unchecked variant only `runUI` takes.
`TestExplicitMissingConfig_FailsEveryCommand` (cmd/sentra) walks the
production tree and fails for any `--config`-bearing command it has no
row for, so a new command either inherits the rule or is exempted by
name. `sentra doctor` prints the resolved path.

## CI

CI is defined in `.github/workflows/ci.yml`. It should cover:

- `go mod tidy -diff`
- gofmt drift over `cmd/` and `internal/`
- `go vet ./...`
- `go test -race -coverprofile=coverage.out ./...`
- `go test ./third_party/fastcdc-go/...`
- `golangci-lint` via the pinned GitHub Action
