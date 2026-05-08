# Backup Plan Apply Crypto Chunker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Remove the FastCDC global mutex bottleneck, make blob nonce semantics unambiguous, and add a reviewable backup plan/apply workflow.

**Architecture:** Patch `jotfs/fastcdc-go` locally through a module `replace` so each `Chunker` owns its seeded lookup table and `ChunkAll` can run concurrently without a package mutex. Move new blob writes to versioned XChaCha20-Poly1305 with 24-byte nonces while keeping legacy AES-GCM v1 reads. Add a `repo.BackupPlan` JSON schema plus CLI `sentra backup plan` and `sentra backup apply` commands; apply validates file metadata before writing a snapshot.

**Tech Stack:** Go 1.25/1.26 toolchain, Cobra CLI, `golang.org/x/crypto/chacha20poly1305`, patched `github.com/jotfs/fastcdc-go`, existing `blobstore`, `repo`, `walker`, and `ui` packages.

---

### Task 1: FastCDC Concurrency

**Files:**
- Create/modify: `third_party/fastcdc-go/fastcdc.go`
- Modify: `go.mod`
- Modify: `internal/chunker/fastcdc.go`
- Modify: `internal/chunker/fastcdc_test.go`

- [x] Write a race-safe concurrency test that runs multiple `ChunkAll` calls concurrently and verifies the output matches a single-threaded baseline.
- [x] Patch `fastcdc-go` so `NewChunker` builds an instance-local table instead of mutating the package table.
- [x] Remove the package-level `chunkerMu` from `internal/chunker/fastcdc.go`.
- [x] Run `go test -race ./internal/chunker`.

### Task 2: XChaCha20-Poly1305 Blob Format

**Files:**
- Modify: `internal/crypto/aead.go`
- Modify: `internal/crypto/aead_test.go`
- Modify: `internal/crypto/kdf.go`
- Modify docs mentioning AES-GCM-only blob format.

- [x] Add tests that new `Seal` output is version `0x02`, uses a 24-byte XChaCha20-Poly1305 nonce, and rejects tampering in every nonce byte.
- [x] Add a legacy v1 fixture test so old AES-GCM blobs still open.
- [x] Implement XChaCha20-Poly1305 for new writes and legacy AES-GCM v1 for reads.
- [x] Run `go test ./internal/crypto ./internal/repo`.

### Task 3: Backup Plan And Apply

**Files:**
- Create: `internal/repo/backup_plan.go`
- Create/modify: `internal/repo/backup_plan_test.go`
- Modify: `internal/repo/snapshot.go`
- Modify: `internal/cli/backup.go`
- Modify: `internal/cli/backup_test.go`
- Modify: `cmd/sentra/main.go`
- Modify docs/README command references.

- [x] Add tests for `PlanSnapshot` producing deterministic reviewable JSON metadata.
- [x] Add tests for `CreateSnapshotFromPlan` refusing metadata drift and snapshotting only reviewed files.
- [x] Add CLI tests for `sentra backup plan <path> --out <file>` and `sentra backup apply <file> --yes`.
- [x] Implement the repo plan/apply API and wire the CLI subcommands.
- [ ] Run targeted tests, then `go test ./...`.
