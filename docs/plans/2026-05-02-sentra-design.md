---
title: Sentra design
date: 2026-05-02
status: validated
---

# Sentra — Encrypted, Versioned S3 Backups with an Agentic Sidekick

Sentra is a Go CLI that backs up local directories to S3 as encrypted,
versioned snapshots and ships with a hybrid heuristics + LLM agent that
audits the repository and recommends actions (prune, ignore additions,
secret remediation, retention drift). It runs equally well as a scriptable
CLI (`sentra backup ./Documents`) or as a full-screen TUI dashboard
(`sentra ui`).

## Decisions (validated)

| # | Decision | Choice |
| - | -------- | ------ |
| 1 | Agent flavor | Hybrid: local heuristics first, LLM on interesting findings only |
| 2 | Backup model | Versioned snapshots: encrypted manifests + content-addressed blobs |
| 3 | UX shape | Hybrid: inline progress for subcommands, full TUI for `sentra ui` |
| 4 | LLM provider | Pluggable `Provider` interface; Anthropic Claude is the default impl |
| 5 | Encryption | Client-side XChaCha20-Poly1305, key derived from passphrase via Argon2id |
| 6 | v1 scope | Polished v1: init/backup/snapshots/diff/restore/prune/agent/ui + full CI + goreleaser |

## Architecture

### Module layout

```
sentra/
├── cmd/sentra/main.go          # entrypoint, wires Cobra
├── internal/
│   ├── cli/                    # Cobra commands (one file each)
│   │   ├── root.go init.go backup.go snapshots.go
│   │   ├── restore.go prune.go diff.go agent.go ui.go
│   ├── repo/                   # snapshot repository (manifests + blob refs)
│   ├── blobstore/              # interface + S3 + memory (test) impls
│   ├── crypto/                 # XChaCha20-Poly1305 + Argon2id key derivation
│   ├── chunker/                # content-defined chunking + zstd
│   ├── walker/                 # concurrent fs walk + .sentraignore
│   ├── agent/
│   │   ├── heuristics/         # local rules (entropy, dup, secrets, age)
│   │   ├── llm/                # Provider interface + anthropic impl + fake
│   │   └── tools/              # tool-use schema for the agent loop
│   ├── tui/                    # Bubbletea models, lipgloss theme
│   ├── ui/                     # shared lipgloss styles, progress, tables
│   └── config/                 # sentra.yaml parsing (koanf)
├── go.mod .golangci.yml .goreleaser.yaml
└── .github/workflows/ci.yml release.yml
```

`internal/` only — no `pkg/`. Cobra commands stay thin and delegate to
use-case functions in `internal/repo` and `internal/agent`.

### Command surface

```
sentra init                     # create sentra.yaml + prompt for passphrase
sentra backup <path> [--tag t]  # snapshot a directory immediately
sentra backup plan <path> --out plan.json [--tag t]
sentra backup apply plan.json
sentra snapshots [--json]       # list snapshots
sentra diff <snap-a> <snap-b>   # show changes between snapshots
sentra restore <snap> <dest>    # restore to a directory
sentra prune [--keep 30d,12w]   # GC blobs per retention policy
sentra agent scan [--apply]     # run hybrid agent, print recommendations
sentra ui                       # launch TUI dashboard (default w/ no args)
```

### Key dependencies

- CLI: `spf13/cobra`
- Config: `knadh/koanf` (no globals, env overlays out of the box)
- AWS: `aws-sdk-go-v2` and submodules
- Anthropic: `anthropics/anthropic-sdk-go`
- Charm stack: `bubbletea`, `lipgloss`, `bubbles`, `huh`, `log`
- Compression: `klauspost/compress/zstd`
- Crypto: `golang.org/x/crypto/argon2`
- Ignore matching: `sabhiram/go-gitignore`
- Keyring (opt-in): `zalando/go-keyring`
- Tests: `testcontainers-go` (MinIO), `bubbletea/teatest`

Go 1.24+.

## Storage format

### S3 layout

```
s3://<bucket>/<prefix>/
├── config                  # encrypted repo metadata (KDF salt, version, chunk params)
├── snapshots/<id>          # one encrypted manifest per snapshot
├── index/<id>              # encrypted blob-index files (speeds up listing)
└── data/<aa>/<sha256>      # encrypted blob, sharded by first 2 hex chars
```

### Chunking and dedup

- FastCDC content-defined chunking, ~1 MiB average chunk size.
- Each chunk SHA-256 hashed (plaintext) → that's the blob ID.
- Identical chunks across files / snapshots upload exactly once.
- A 50 GiB folder with one changed file uploads ~1 MiB on subsequent
  snapshots.

### Blob format on disk in S3

```
[1 byte version][24 byte nonce][XChaCha20-Poly1305(zstd(chunk))][16 byte tag]
```

Plaintext is zstd-compressed *before* encryption (encrypted output is
incompressible, so the order matters).

Threat-model note: a passive observer with S3 read access can count
blobs and learn their sizes; they cannot see contents or identify
files. Acceptable for v1; documented in `docs/threat-model.md`.

### Snapshot manifest

JSON, then zstd-compressed, then encrypted, stored at `snapshots/<id>`:

```json
{
  "version": 1,
  "id": "snap-20260502T150405Z-a1b2",
  "created_at": "2026-05-02T15:04:05Z",
  "host": "mark-mbp",
  "tag": "weekly",
  "root": "/Users/mark/Docs",
  "tree": [
    {
      "path": "foo/bar.txt",
      "size": 1234,
      "mode": 420,
      "mtime": "2026-05-02T15:04:05Z",
      "chunks": ["abc123def...", "deadbeef..."]
    }
  ],
  "stats": {"files": 1234, "bytes": 5000000000, "new_bytes": 120000000}
}
```

Two notes on the wire format that diverge from earlier drafts of this
section. First, `mode` is the numeric value of Go's `os.FileMode` (e.g.
`420` decimal == `0o644` octal), not a stringified octal literal — it
serializes naturally with `encoding/json` and is more compact. Second,
each chunks entry is bare hex of the SHA-256 of the (plaintext) chunk,
not prefixed with `"sha256:"` — the prefix is redundant given the
schema and would inflate every manifest by ~7 bytes per chunk reference.
The implementation in `internal/repo/manifest.go` is authoritative.

### Index files

Optimization. A flat list of blob IDs referenced by N recent snapshots,
written every M snapshots. `prune` and `agent scan` use these so they do
not have to deserialize every manifest to figure out which blobs are
live.

### Snapshot IDs

Timestamp + 4 random bytes hex (e.g. `snap-20260502T150405Z-a1b2`).
Sortable by creation time; collisions impossible in practice.

## Encryption and config

### Key derivation (one-time, on `sentra init`)

1. Collect passphrase via `huh` prompt (or `SENTRA_PASSPHRASE`).
2. Generate random 16-byte salt + random 32-byte **repo key**.
3. Derive a **KEK** from passphrase + salt using **Argon2id**
   (memory 64 MiB, iterations 3, parallelism 4).
4. Encrypt repo key with KEK → store in `config` alongside salt + KDF
   parameters.
5. Repo key is what encrypts every blob, manifest, and index from then
   on.

Indirection (passphrase → KEK → repo key) is small extra code now and
makes a future `sentra passwd` (rotate passphrase) cheap — only `config`
gets rewritten, never the data.

### Per-blob encryption

XChaCha20-Poly1305 with a fresh 24-byte random nonce per blob. Open
keeps legacy AES-GCM v1 read support for repositories written before
the v2 blob format. Single-purpose key + 192-bit random nonce →
collision risk negligible.

### Passphrase resolution order

1. `--passphrase-file` flag
2. `SENTRA_PASSPHRASE` env var
3. OS keyring (`zalando/go-keyring`, opt-in via `sentra.yaml`)
4. Interactive `huh` prompt — TTY only, never in scripts

### sentra.yaml

No secrets in this file, ever.

```yaml
repo:
  s3:
    bucket: my-backups
    prefix: sentra/
    region: us-west-2
    profile: default              # AWS SDK profile, optional
    endpoint_url: ""              # MinIO/LocalStack support
agent:
  provider: anthropic
  model: claude-sonnet-4-6
  max_findings_to_llm: 50
backup:
  ignore_file: .sentraignore
  exclude_caches: true            # honor CACHEDIR.TAG (per spec)
retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6
```

### .sentraignore

gitignore-style globs via `sabhiram/go-gitignore`. Loaded per walk
root. Nested `.sentraignore` files are **not** honored in v1 (single
root file only — keeps walking simple). Sample at
`.sentraignore.example`.

## The agent

### Phase 1 — heuristics

`internal/agent/heuristics`. Each runs concurrently over the walk
results and produces typed `Finding` structs:

| Heuristic         | Flags                                                    |
| ----------------- | -------------------------------------------------------- |
| `secrets`         | AWS / GitHub / private-key patterns in text files        |
| `large_files`     | Files > threshold (default 100 MiB)                      |
| `cache_dirs`      | `node_modules`, `.venv`, `target/`, etc. not in ignore   |
| `stale_paths`     | Tracked paths untouched > N days                         |
| `dup_paths`       | Identical content at multiple paths in same snapshot     |
| `orphan_blobs`    | Blobs in S3 with zero manifest references (post-crash)   |
| `retention_drift` | Snapshot history violates configured policy              |

Each `Finding` carries `severity`, `category`, `path` / `target`, and a
small structured `details` map. Never file contents.

### Phase 2 — LLM

The agent calls `llm.Provider.Generate` with the findings summarized
into the prompt and a small read-only **toolset**:

- `list_snapshots(limit, since)` → snapshot metadata
- `snapshot_stats(id)` → counts, total bytes, file-type histogram
- `diff_snapshots(a, b)` → added / removed / changed paths only
- `inspect_finding(id)` → finding details (no contents)

The LLM emits a structured `[]Recommendation` (enforced via tool-use
schema):

```go
type Recommendation struct {
    ID         string   // stable hash of action+target
    Action     string   // prune_snapshot | add_to_ignore | flag_secret | none
    Target     string
    Severity   string   // info | warn | critical
    Rationale  string   // one paragraph
}
```

### Safety rails

- LLM never sees file contents.
- LLM never executes anything; it only emits recommendations.
- `sentra agent scan` prints recommendations as a styled table.
- `sentra agent scan --apply` opens an interactive `huh` confirm flow
  per recommendation.
- Tool-call budget capped (default 10).
- `max_findings_to_llm` from config (default 50) caps prompt size.

### Provider interface

```go
type Provider interface {
    Generate(ctx context.Context,
             sys string,
             msgs []Message,
             tools []Tool,
             stream chan<- string) ([]ToolCall, string, error)
}
```

Anthropic impl uses streaming + tool use. A `fake.Provider` for tests
returns canned tool calls — no live LLM in CI.

## TUI architecture

### Shared visual layer

`internal/ui` exposes one lipgloss theme with semantic colors
(`Primary`, `Success`, `Warn`, `Danger`, `Muted`, `Subtle`) and reusable
styled components (table, badge, panel, progress). Inline-mode commands
and the full TUI both pull from this — single visual language.

### Inline mode

- `bubbles/spinner` during scan
- `bubbles/progress` during uploads
- `huh` for passphrase / confirm-per-recommendation / bucket pick
- `lipgloss` tables for snapshot lists, agent recommendations, diffs
- `--json` flag on every command bypasses bubbletea and emits JSON

### Full TUI

`sentra ui`, also `sentra` with no args. Single Bubbletea program;
`tea.Model` per view; parent `App` model owns active view + global
state.

```
┌─ sentra ─ repo: my-backups ─────── 47 snapshots ─ 12.3 GiB ─┐
│ [d]ashboard  [s]napshots  [a]gent  [c]onfig  [?]help [q]uit │
├─────────────────────────────────────────────────────────────┤
│ (active view renders here)                                  │
│                                                             │
├─────────────────────────────────────────────────────────────┤
│ ↑/↓ navigate  ⏎ select  esc back  ?  help                   │
└─────────────────────────────────────────────────────────────┘
```

Views:

- **Dashboard** — repo stats card, last snapshot card, agent
  recommendation count badge, sparkline of snapshot sizes over time
  (custom lipgloss render).
- **Snapshots** — `bubbles/table`, sortable; `enter` opens detail view
  with `bubbles/list` file tree + stats.
- **Diff** — pick two snapshots, render added / removed / changed paths
  in three columns.
- **Agent** — split view: top half streams LLM reasoning live (viewport
  with auto-scroll, fed by a channel from `Provider.Generate`); bottom
  half is the recommendations table that fills in as tool calls return.
  `a` to apply selected recommendation (with confirm).

### Logging

Stdlib `log/slog` as the API; `charmbracelet/log` as the slog handler →
pretty colored output that matches the theme; switches to JSON handler
when `--json` is set.

## Testing and release

### Test layers

| Layer        | What                                                                 |
| ------------ | -------------------------------------------------------------------- |
| Unit         | Per-package, table-driven. Pure functions covered exhaustively.      |
| Round-trip   | `crypto`, `chunker`, `repo`: encrypt→decrypt, chunk→reassemble, snapshot→restore must be byte-identical. |
| In-memory    | `blobstore.Memory` for fast `repo`-level tests with no S3.           |
| Integration  | `testcontainers-go` spins MinIO; real S3 protocol; gated by `-tags integration`; Linux CI only. |
| Agent        | `llm.fake.Provider` returns canned tool calls; verifies the full agent loop deterministically. No live LLM in CI. |
| TUI          | `bubbletea/teatest` — drive models with synthetic key events, assert on rendered frames. |

### TDD discipline

Every feature lands with its tests written first. RED → GREEN →
REFACTOR. No production code without a failing test that demands it.
Coverage target 70%+ — but coverage is a smoke detector, not the goal.

### Linting

Existing `.golangci.yml` already enables `errcheck`, `govet`,
`staticcheck`, etc. Add `gosec` for security checks (worth it for a
backup / crypto tool).

### CI

`.github/workflows/ci.yml`: Go 1.24 on Ubuntu + macOS. `go vet`,
`golangci-lint run`, `go test -race -coverprofile=coverage.out`.
Integration job (Linux only, `-tags integration`) spins MinIO via
testcontainers. Codecov upload.

### Release

`.github/workflows/release.yml` triggered on `v*` tags via
**goreleaser**:

- Cross-compile linux / darwin / windows × amd64 / arm64
- Checksums + cosign keyless signing (OIDC)
- SBOM via syft
- Docker image to `ghcr.io/markgustetic/sentra`
- Homebrew tap formula update
- Conventional-commits changelog

### Docs

- `README.md` — quickstart + asciinema cast of the TUI
- `docs/architecture.md` — mermaid diagram of the storage model
- `docs/threat-model.md` — what the encryption does and does not protect
  against (important for a backup tool)

## Out of scope for v1

Punted, may revisit:

- Cross-region replication
- S3 Glacier / lifecycle tiering driven by the agent
- FUSE mount for browse-as-filesystem
- Web UI
- `agent watch` continuous mode
- Nested `.sentraignore` files
