# AGENTS.md

Guidance for coding agents working in this repository.

If the local Codex skill `$sentra-maintainer` is available, use it for
Sentra code, docs, CI, or release workflow changes.

## Project Shape

- Sentra is a Go CLI/TUI for encrypted, deduplicated S3 backups.
- Main command wiring lives in `cmd/sentra/main.go`.
- Core repository behavior lives in `internal/repo`.
- CLI command implementations live in `internal/cli`.
- Agent heuristics/orchestration live in `internal/agent`.
- Bubbletea views live in `internal/tui`.
- The vendored FastCDC module has its own `go.mod` under `third_party/fastcdc-go`.

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
cd third_party/fastcdc-go && go test ./...
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

- `sentra check` is the shared integrity surface for CLI and TUI operations.
- `sentra restore --dry-run` must not create or write the destination.
- `sentra restore --verify` should compare restored files against manifest
  chunk hashes.
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
- `go test ./...` inside `third_party/fastcdc-go`
- `golangci-lint` via the pinned GitHub Action
