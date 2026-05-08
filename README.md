# sentra

Encrypted, deduplicated, agent-aware backups for S3 and S3-compatible storage.

[![CI](https://github.com/markgustetic/sentra/actions/workflows/ci.yml/badge.svg)](https://github.com/markgustetic/sentra/actions/workflows/ci.yml)
[![Codecov](https://codecov.io/gh/markgustetic/sentra/branch/main/graph/badge.svg)](https://codecov.io/gh/markgustetic/sentra)
[![Go Report Card](https://goreportcard.com/badge/github.com/markgustetic/sentra)](https://goreportcard.com/report/github.com/markgustetic/sentra)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

`sentra` is a single-binary Go CLI that backs up local directories to S3 as
encrypted, content-addressed snapshots. It ships with a built-in agent —
local heuristics first, an optional LLM second — that audits the repository
and surfaces recommendations: prune candidates, ignore-list additions, secret
findings, retention drift. It runs equally well as a scriptable CLI
(`sentra backup ./Documents`, or `sentra backup plan` / `apply` for
reviewed runs) or a full-screen TUI (`sentra ui`).

- **Client-side encryption.** New blobs use XChaCha20-Poly1305 with
  per-blob 24-byte random nonces. The data key is derived from your
  passphrase via Argon2id. The S3 bucket sees ciphertext, never plaintext.
- **Content-defined dedup.** FastCDC chunking + SHA-256 content addressing.
  A 50 GiB tree with one changed file uploads ~1 MiB on the next snapshot.
- **Versioned snapshots.** Each snapshot is an immutable, encrypted manifest
  pointing at chunk hashes. `restore` is exact-byte by construction.
- **Agent.** Heuristics run locally; the LLM is invoked on summaries only,
  never on file contents. Recommendations are read-only by default.
- **TUI dashboard.** `sentra ui` is a Bubbletea app with snapshots, diff,
  and agent views. The bare `sentra` command launches it.

## Quickstart

End-to-end against a local MinIO instance (no AWS account needed).

### 1. Start MinIO

Save as `docker-compose.yaml`:

```yaml
services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - minio-data:/data
volumes:
  minio-data:
```

```bash
docker compose up -d
# Create the bucket through the MinIO console at http://localhost:9001
# (login minioadmin / minioadmin), or via the AWS CLI:
AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
  aws --endpoint-url http://localhost:9000 s3 mb s3://sentra-test
```

### 2. Initialize a repo

Point `sentra` at MinIO via `sentra.yaml`:

```yaml
repo:
  s3:
    bucket: sentra-test
    region: us-east-1
    endpoint_url: http://localhost:9000
backup:
  ignore_file: .sentraignore
  exclude_caches: true
retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6
```

```bash
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
export SENTRA_PASSPHRASE='change-me-to-something-good'

sentra init
```

### 3. Take a snapshot

```bash
sentra backup ./Documents --tag weekly
```

For a reviewed two-step run, write a JSON plan first and apply it after
inspection:

```bash
sentra backup plan ./Documents --tag weekly --out weekly-plan.json
sentra backup apply weekly-plan.json
```

### 4. List snapshots

```bash
sentra snapshots
sentra snapshots --json | jq .
```

### 5. Restore

```bash
sentra restore <snapshot-id> /tmp/restored
```

### 6. Run the agent

```bash
sentra agent scan
# Apply recommendations interactively:
sentra agent scan --apply
```

The full version of this walkthrough lives in
[`docs/QUICKSTART.md`](docs/QUICKSTART.md).

## Install

### Homebrew

```bash
brew install markgustetic/tap/sentra
```

### `go install`

```bash
go install github.com/markgustetic/sentra/cmd/sentra@latest
```

### Docker (GHCR)

```bash
docker pull ghcr.io/markgustetic/sentra:latest
docker run --rm -v "$PWD:/work" -w /work ghcr.io/markgustetic/sentra:latest --version
```

### Prebuilt binaries

Download platform-specific archives from the
[releases page](https://github.com/markgustetic/sentra/releases). Each
release includes `checksums.txt` plus a cosign keyless signature
(`checksums.txt.sig` and `checksums.txt.pem`) and a syft SBOM per archive.

```bash
cosign verify-blob \
  --certificate checksums.txt.pem \
  --signature  checksums.txt.sig \
  --certificate-identity-regexp 'https://github.com/markgustetic/sentra' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt
```

## Commands

| Command                         | Description                                                                |
| ------------------------------- | -------------------------------------------------------------------------- |
| `sentra init`                   | Create `sentra.yaml`, derive a repo key, write the encrypted config blob. |
| `sentra backup <path>`          | Snapshot a directory immediately. `--tag` to label the snapshot.           |
| `sentra backup plan <path>`     | Write a reviewable JSON plan file for the exact file set.                  |
| `sentra backup apply <plan>`    | Validate and snapshot from a reviewed plan file. `--yes` skips confirm.    |
| `sentra snapshots`              | List snapshots, newest first. `--json` for scripting.                      |
| `sentra diff <a> <b>`           | Show added / removed / changed paths between two snapshots.                |
| `sentra restore <snap> <dest>`  | Restore a snapshot byte-identical to a destination directory.              |
| `sentra prune`                  | Delete snapshots that violate the retention policy and GC orphan blobs.    |
| `sentra agent scan`             | Run heuristics + LLM agent. `--apply` for interactive remediation.         |
| `sentra ui`                     | Launch the Bubbletea TUI. Bare `sentra` (no args) is equivalent.           |

Every subcommand respects:

- `--config <path>` — override the config search (default `sentra.yaml`).
- `--passphrase-file <path>` — read the passphrase from a file (highest priority).
- `SENTRA_PASSPHRASE` — env var (second priority).
- OS keyring — opt in via `passphrase.use_keyring: true` in `sentra.yaml`.
- Interactive `huh` prompt — last resort, TTY only.

## Configuration

`sentra.yaml` lives at the repo root. **No secrets in this file, ever.**

```yaml
repo:
  s3:
    bucket: my-backups            # required
    prefix: sentra/               # optional, lets multiple repos share a bucket
    region: us-west-2             # optional, falls back to AWS SDK chain
    profile: default              # optional, AWS shared-credentials profile
    endpoint_url: ""              # optional, set for MinIO / LocalStack

agent:
  provider: anthropic
  model: claude-sonnet-4-6
  max_findings_to_llm: 50         # cap on prompt size

backup:
  ignore_file: .sentraignore
  exclude_caches: true            # honor CACHEDIR.TAG (per spec)

retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6

passphrase:
  use_keyring: false              # opt in to OS keyring lookup
```

A `.sentraignore` file at the walk root applies gitignore-style globs
(via `sabhiram/go-gitignore`). A starter file ships at
`.sentraignore.example`.

The Anthropic LLM provider needs `ANTHROPIC_API_KEY`. Without it, every
non-agent command still works; `sentra agent scan` returns a clear error.

## Threat model

`sentra` is a backup tool, so it's worth being explicit about what the
encryption protects against — and what it doesn't. See
[`docs/threat-model.md`](docs/threat-model.md) for the full write-up;
in brief:

- Protects: file contents, file paths, manifest metadata, snapshot tags.
- Does not protect: object counts, object sizes (zstd-then-encrypt leaks
  approximate plaintext size), S3 access logs.
- Out of scope: forward secrecy across passphrase compromise, post-quantum
  key strength.

## Architecture

See [`docs/architecture.md`](docs/architecture.md) for the storage model,
the agent loop, and Mermaid diagrams of the `backup`, `restore`, and
`agent scan` flows.

## Development

```bash
make build       # builds bin/sentra
make test        # unit tests with -race
make integration # spins MinIO via testcontainers; Linux only
make lint        # golangci-lint run
```

Go 1.25+ is required (the dependency ecosystem moved past 1.24 mid-Phase 13).
The codebase is `internal/`-only — no public Go API shipped in v1.

## Releasing

Releases are cut by pushing a `v*` tag, which triggers
`.github/workflows/release.yml`. The workflow runs goreleaser to:

- Cross-compile `linux/darwin/windows` × `amd64/arm64` archives
- Generate a SHA-256 `checksums.txt`
- Sign the checksums file with cosign keyless (GitHub OIDC)
- Build a multi-arch GHCR image at `ghcr.io/markgustetic/sentra`
- Update the Homebrew tap at `markgustetic/homebrew-tap`
- Attach a syft-generated SBOM per archive

Required GitHub Actions secrets:

- `GITHUB_TOKEN` — provided automatically by Actions.
- `HOMEBREW_TAP_TOKEN` — a personal access token with `contents: write`
  scope on the `markgustetic/homebrew-tap` repo.

## License

MIT. See [`LICENSE`](LICENSE).
