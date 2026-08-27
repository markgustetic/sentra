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
  storage logic.
- `sentra schedule` installs user-level OS scheduler files for named policies.
  It should generate launchd/systemd files that invoke `sentra policy run`;
  do not introduce a resident Sentra daemon or write secrets into scheduler
  files.
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
  regular files only.
- Snapshot references: everywhere a snapshot ID is accepted, "latest", a
  unique prefix, and a unique suffix resolve via `ResolveSnapshotID`;
  ambiguity is refused with candidates named, never first-match.
- `sentra ls <snapshot>` lists a snapshot's tree read-only (`--json` uses
  explicit kinds: file/dir/symlink).
- `sentra pin` / `sentra unpin` protect snapshots: retention always keeps a
  pinned snapshot (reason "pinned") and `DeleteSnapshot` — the choke point
  for prune, the TUI, and the agent's prune action — refuses with
  `ErrSnapshotPinned`. Pinning a nonexistent snapshot is an error.
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
  backs up different data.
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
  mean rail-listed: the rail holds six destinations (Dashboard, Backup,
  Snapshots, Maintenance, Settings, Help) and the rest of the floor lives
  one launcher inside them — restore/diff from a snapshot row,
  check/prune/sync/doctor from Maintenance, policies/schedule/recovery-kit/
  passphrase/setup from Settings. Stats and the agent are CLI-only
  (outside the floor by the sentence above). Per-run knobs
  (`prune --keep-*`, `--concurrency`, `--stale-lock-after`, agent
  `--root`/`--categories`/`--local-only`/`--max-tool-calls`) come from config
  in the TUI by design.
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
  retention reasons.
- `sentra agent scan --local-only` and `--no-llm` must not call the LLM provider.
- `sentra agent advise-ignore` is read-only and must not edit `.sentraignore`.
- `sentra recovery-kit` is non-secret documentation only.

## Config resolution

Every repo-facing command resolves its config path the same way:

1. An explicit `--config <path>` is used verbatim — no discovery.
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
discovery; its destination comes only from `--dst-config`.

Implementation: `config.DiscoverPath()` (internal/config/discover.go),
applied by `resolveConfigPath` (internal/cli/config_path.go) as the first
statement of every run body — at RunE time, because `Flags().Changed` is
only meaningful after argv parsing. `sentra doctor` prints the resolved
path.

## CI

CI is defined in `.github/workflows/ci.yml`. It should cover:

- `go mod tidy -diff`
- gofmt drift over `cmd/` and `internal/`
- `go vet ./...`
- `go test -race -coverprofile=coverage.out ./...`
- `go test ./third_party/fastcdc-go/...`
- `golangci-lint` via the pinned GitHub Action
