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
- Bubbletea views live in `internal/tui`.
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

## CI

CI is defined in `.github/workflows/ci.yml`. It should cover:

- `go mod tidy -diff`
- gofmt drift over `cmd/` and `internal/`
- `go vet ./...`
- `go test -race -coverprofile=coverage.out ./...`
- `go test ./third_party/fastcdc-go/...`
- `golangci-lint` via the pinned GitHub Action
