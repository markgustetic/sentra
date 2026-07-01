# CLAUDE.md

Guidance for Claude Code in this repository. [AGENTS.md](AGENTS.md) holds the
full per-command behavior contract and is the source of truth; this file is the
quick reference and the load-bearing invariants.

## What this is

Sentra is a single-binary Go CLI/TUI (`cmd/sentra`) that backs up local
directories to S3 / S3-compatible storage as **encrypted, deduplicated,
content-addressed** snapshots. Go 1.25, module `github.com/markgustetic/sentra`.

## Commands (prefer `just`)

- Build: `just build` (→ `bin/sentra`), or `go build ./...`
- Test: `just test` — `go test -race -coverprofile=coverage.out ./...`
- Integration (needs Docker; testcontainers + MinIO): `just integration` —
  `go test -race -tags=integration ./...`
- Vendored FastCDC is a **separate module**: `go test ./third_party/fastcdc-go/...`
- Lint / vet / fmt: `just lint` (golangci-lint), `just vet`, `just fmt`
- Vuln scan: `just vuln` (govulncheck)
- Full local gate: `just check` (build, test, vet, lint, vuln, tidy, fmt, diff)
- Before claiming done, also: `go mod tidy -diff` and `git diff --check`.
- CI (`.github/workflows/ci.yml`) enforces: `go mod tidy -diff`,
  `gofmt -l cmd internal`, `go vet`, `go test -race`, the FastCDC module tests,
  and `golangci-lint`. Keep all of these clean.

## Package map

- `internal/crypto` — XChaCha20-Poly1305 AEAD + Argon2id KDF (`aead.go`, `kdf.go`)
- `internal/chunker` — FastCDC chunking + zstd compression
- `internal/blobstore` — `Store` interface; S3, in-memory, and retry wrappers
- `internal/repo` — snapshots, restore, GC, retention, locking, passwd (the core)
- `internal/config` — koanf config + passphrase / OS-keyring resolution
- `internal/walker` — filesystem walk + ignore matching
- `internal/agent` — local heuristics, LLM orchestration, tools, actions
- `internal/cli` — cobra commands; `internal/tui` / `internal/ui` — Bubbletea
- `internal/policy` — named-policy validation; `internal/progress` — reporters

## Invariants — do not break

- **Encryption.** New blobs use XChaCha20-Poly1305 with a per-blob 24-byte
  **random** nonce and the version byte bound as AEAD associated data; the data
  key is derived from the passphrase via Argon2id. The bucket must never see
  plaintext, and a nonce must never be reused under a key.
- **Content addressing.** A chunk's key is the SHA-256 of its **raw
  (decompressed) plaintext**. Restore re-derives and checks this hash on read
  (`ErrChunkHashMismatch`); restore is exact-byte by construction.
- **GC safety.** GC computes its live set from the snapshots **present in the
  store, listed under the repo lock** — never from a caller-supplied ID set. A
  blob referenced by any present manifest must never be reaped, including one
  committed by a concurrent backup before GC took the lock.
- **One repo lock.** `CreateSnapshot`, `GC`, `sync`, `passwd`, and
  snapshot-apply serialize on the advisory lock `meta/lock` (atomic
  `PutIfAbsent` / S3 `If-None-Match: *`). Release is fail-closed: never delete a
  lock whose ownership can't be confirmed.
- **Agent / LLM.** Local heuristics run first; the LLM sees **summaries only —
  never file contents or secret values**. Recommendations are read-only by
  default.
- **No secrets in artifacts.** Never write passphrases, wrapped keys, salts, MAC
  material, or AWS credentials into `sentra.yaml`, setup drafts, logs, recovery
  kits, tests, or fixtures.

## Conventions

- **TDD.** Write the failing test first, watch it fail for the right reason, then
  implement the minimum to pass. Repo-layer tests use the in-memory blobstore
  (`newTestRepo`); tests are table-driven where it fits.
- **Doc comments explain _why_** — rationale and failure modes, not just what.
  Match the surrounding density.
- **Errors** are sentinels wrapped with `%w`; callers branch with `errors.Is`.
- Keep changes small and within existing package boundaries. `coverage.out` and
  other local artifacts stay out of commits.
