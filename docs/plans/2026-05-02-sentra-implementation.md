# Sentra Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task-by-task.
>
> Every task uses RED → GREEN → REFACTOR per `superpowers:test-driven-development`.
> At the end of each phase use `superpowers:verification-before-completion` before moving on.
> When tests fail unexpectedly, escalate to `superpowers:systematic-debugging`.

**Goal:** Build Sentra, a Go CLI that backs up directories to S3 as encrypted versioned snapshots, with a hybrid heuristics + LLM agent that audits the repo and recommends actions, plus a Bubbletea TUI.

**Design source of truth:** [docs/plans/2026-05-02-sentra-design.md](2026-05-02-sentra-design.md). If the plan and design disagree, the design wins — pause and reconcile before continuing.

**Architecture:** Cobra CLI with thin commands delegating to `internal/repo`, `internal/agent`, and `internal/tui`. Content-addressed encrypted blobs in S3 with FastCDC chunking. Pluggable `llm.Provider` interface (Anthropic default). Charmbracelet stack for visuals.

**Tech Stack:** Go 1.24+, cobra, koanf, aws-sdk-go-v2, anthropic-sdk-go, charmbracelet (bubbletea/lipgloss/bubbles/huh/log), klauspost/compress, x/crypto/argon2, sabhiram/go-gitignore, testcontainers-go.

---

## How to use this plan

- Tasks are **bite-sized TDD cycles**: write a failing test, watch it fail, write the minimum code, watch it pass, commit. Most tasks are 15-30 minutes.
- **Commit after every task.** Never batch.
- Phases group related tasks. Each phase delivers something demoable. Run `go test ./...` and `golangci-lint run` at the end of every phase before moving on.
- File paths are relative to repo root: `/Users/markgustetic/Programming/portfolio/go-lang/sentra-claude`.
- Do not skip ahead. If a later task needs something from an earlier task, the earlier task is wrong — fix it before continuing.

## Phase map

| Phase | What lands                                                  |
| ----- | ----------------------------------------------------------- |
| 0     | Bootstrap: module, deps, lint, Makefile, skeleton CI        |
| 1     | Crypto: Argon2id KDF + AES-256-GCM blob format              |
| 2     | Blobstore: interface + memory impl + S3 impl (MinIO tests)  |
| 3     | Chunker: FastCDC + zstd                                     |
| 4     | Walker: fs walk + .sentraignore                             |
| 5     | Repo: config init, snapshot create/list/restore             |
| 6     | UI primitives: theme, progress, tables, huh helpers         |
| 7     | CLI commands: init, backup, snapshots, restore, diff        |
| 8     | Prune + retention                                           |
| 9     | Agent — heuristics                                          |
| 10    | Agent — LLM provider interface + fake + Anthropic           |
| 11    | Agent — orchestrator + tool implementations                 |
| 12    | TUI: dashboard, snapshots, diff, agent views                |
| 13    | Release: goreleaser, CI hardening, docs                     |

---

# Phase 0 — Bootstrap

Goal: a runnable `sentra --version` with lint/test/CI plumbing in place.

### Task 0.1: Initialize Go module

**Files:**
- Create: `go.mod` (via `go mod init`)
- Create: `cmd/sentra/main.go`

**Steps:**

1. Create the module: `go mod init github.com/markgustetic/sentra`
2. Create `cmd/sentra/main.go`:
   ```go
   package main

   import "fmt"

   var version = "dev"

   func main() {
       fmt.Println("sentra", version)
   }
   ```
3. Run: `go run ./cmd/sentra` → expect `sentra dev`.
4. Commit:
   ```bash
   git add go.mod cmd/sentra/main.go
   git commit -m "chore: initialize go module and entrypoint"
   ```

### Task 0.2: Add Cobra root command with --version

**Files:**
- Modify: `cmd/sentra/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/root_test.go`

**Steps:**

1. Add cobra: `go get github.com/spf13/cobra@latest`
2. Write the failing test in `internal/cli/root_test.go`:
   ```go
   package cli

   import (
       "bytes"
       "strings"
       "testing"
   )

   func TestRoot_Version(t *testing.T) {
       buf := &bytes.Buffer{}
       cmd := NewRoot("1.2.3", "abc123", "2026-01-01")
       cmd.SetOut(buf)
       cmd.SetArgs([]string{"--version"})
       if err := cmd.Execute(); err != nil {
           t.Fatalf("execute: %v", err)
       }
       got := buf.String()
       if !strings.Contains(got, "1.2.3") {
           t.Errorf("expected version in output, got %q", got)
       }
   }
   ```
3. Run: `go test ./internal/cli` → expect FAIL (NewRoot undefined).
4. Implement `internal/cli/root.go`:
   ```go
   package cli

   import (
       "fmt"
       "github.com/spf13/cobra"
   )

   func NewRoot(version, commit, date string) *cobra.Command {
       cmd := &cobra.Command{
           Use:   "sentra",
           Short: "Encrypted versioned S3 backups with an agentic sidekick",
           Long:  "Sentra backs up directories to S3 as encrypted, versioned snapshots and runs a hybrid heuristics+LLM agent that audits the repo.",
           Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
       }
       cmd.SetVersionTemplate("{{.Version}}\n")
       return cmd
   }
   ```
5. Wire it from `cmd/sentra/main.go`:
   ```go
   package main

   import (
       "os"
       "github.com/markgustetic/sentra/internal/cli"
   )

   var (
       version = "dev"
       commit  = "none"
       date    = "unknown"
   )

   func main() {
       if err := cli.NewRoot(version, commit, date).Execute(); err != nil {
           os.Exit(1)
       }
   }
   ```
6. Run: `go test ./internal/cli` → expect PASS.
7. Run: `go run ./cmd/sentra --version` → expect `dev (commit none, built unknown)`.
8. Commit:
   ```bash
   git add go.mod go.sum cmd/sentra/main.go internal/cli/root.go internal/cli/root_test.go
   git commit -m "feat(cli): cobra root with version flag"
   ```

### Task 0.3: Add Makefile

**Files:**
- Create: `Makefile`

**Steps:**

1. Write `Makefile`:
   ```makefile
   .PHONY: build test lint fmt vet tidy clean

   GO ?= go
   BIN := bin/sentra
   PKG := ./...

   build:
   	$(GO) build -o $(BIN) ./cmd/sentra

   test:
   	$(GO) test -race -coverprofile=coverage.out $(PKG)

   integration:
   	$(GO) test -race -tags=integration ./...

   lint:
   	golangci-lint run

   fmt:
   	$(GO) fmt $(PKG)

   vet:
   	$(GO) vet $(PKG)

   tidy:
   	$(GO) mod tidy

   clean:
   	rm -rf bin coverage.out
   ```
2. Run: `make build` → expect `bin/sentra` to be created.
3. Run: `make test` → expect PASS.
4. Commit:
   ```bash
   git add Makefile
   git commit -m "chore: add Makefile"
   ```

### Task 0.4: Add gosec to .golangci.yml

**Files:**
- Modify: `.golangci.yml`

**Steps:**

1. Add `gosec` to the `enable` list:
   ```yaml
   linters:
     disable-all: true
     enable:
       - errcheck
       - govet
       - ineffassign
       - staticcheck
       - unused
       - gosimple
       - misspell
       - unconvert
       - gofmt
       - gosec
   ```
2. Run: `make lint` → expect PASS (or no findings).
3. Commit:
   ```bash
   git add .golangci.yml
   git commit -m "chore(lint): enable gosec"
   ```

### Task 0.5: Skeleton GitHub Actions CI

**Files:**
- Create: `.github/workflows/ci.yml`

**Steps:**

1. Write the workflow:
   ```yaml
   name: ci
   on:
     push:
       branches: [main]
     pull_request:
   jobs:
     test:
       strategy:
         matrix:
           os: [ubuntu-latest, macos-latest]
       runs-on: ${{ matrix.os }}
       steps:
         - uses: actions/checkout@v4
         - uses: actions/setup-go@v5
           with:
             go-version: '1.24'
             cache: true
         - run: go vet ./...
         - run: go test -race -coverprofile=coverage.out ./...
         - uses: codecov/codecov-action@v4
           if: matrix.os == 'ubuntu-latest'
           with:
             files: coverage.out
     lint:
       runs-on: ubuntu-latest
       steps:
         - uses: actions/checkout@v4
         - uses: actions/setup-go@v5
           with:
             go-version: '1.24'
         - uses: golangci/golangci-lint-action@v6
           with:
             version: latest
   ```
2. Commit:
   ```bash
   git add .github/workflows/ci.yml
   git commit -m "ci: add GitHub Actions test and lint jobs"
   ```

**Phase 0 verification:** `make build && make test && make lint` all pass. Move on.

---

# Phase 1 — Crypto

Goal: round-trip-tested AES-256-GCM blob format and Argon2id KDF.

### Task 1.1: Argon2id key derivation

**Files:**
- Create: `internal/crypto/kdf.go`
- Create: `internal/crypto/kdf_test.go`

**Steps:**

1. Add dep: `go get golang.org/x/crypto/argon2`
2. Test:
   ```go
   package crypto

   import (
       "bytes"
       "testing"
   )

   func TestDeriveKEK_Deterministic(t *testing.T) {
       salt := []byte("0123456789abcdef")
       k1 := DeriveKEK([]byte("hunter2"), salt, DefaultKDFParams())
       k2 := DeriveKEK([]byte("hunter2"), salt, DefaultKDFParams())
       if !bytes.Equal(k1, k2) {
           t.Fatal("KDF must be deterministic")
       }
       if len(k1) != 32 {
           t.Fatalf("expected 32-byte key, got %d", len(k1))
       }
   }

   func TestDeriveKEK_DifferentSalt(t *testing.T) {
       k1 := DeriveKEK([]byte("hunter2"), []byte("0123456789abcdef"), DefaultKDFParams())
       k2 := DeriveKEK([]byte("hunter2"), []byte("fedcba9876543210"), DefaultKDFParams())
       if bytes.Equal(k1, k2) {
           t.Fatal("different salt must produce different keys")
       }
   }
   ```
3. Run: expect FAIL.
4. Implement `internal/crypto/kdf.go`:
   ```go
   package crypto

   import "golang.org/x/crypto/argon2"

   type KDFParams struct {
       Time    uint32
       Memory  uint32 // KiB
       Threads uint8
       KeyLen  uint32
   }

   func DefaultKDFParams() KDFParams {
       return KDFParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32}
   }

   func DeriveKEK(passphrase, salt []byte, p KDFParams) []byte {
       return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
   }
   ```
5. Run: PASS.
6. Commit: `feat(crypto): argon2id key derivation`.

### Task 1.2: AES-256-GCM blob seal / open

**Files:**
- Create: `internal/crypto/aead.go`
- Create: `internal/crypto/aead_test.go`

**Steps:**

1. Test (round-trip):
   ```go
   package crypto

   import (
       "bytes"
       "testing"
   )

   func TestSealOpen_RoundTrip(t *testing.T) {
       key := make([]byte, 32)
       for i := range key { key[i] = byte(i) }
       plaintext := []byte("the quick brown fox jumps over the lazy dog")
       sealed, err := Seal(key, plaintext)
       if err != nil { t.Fatal(err) }
       opened, err := Open(key, sealed)
       if err != nil { t.Fatal(err) }
       if !bytes.Equal(plaintext, opened) {
           t.Fatalf("round-trip failed: got %q want %q", opened, plaintext)
       }
   }

   func TestSeal_DifferentNoncesEachCall(t *testing.T) {
       key := make([]byte, 32)
       a, _ := Seal(key, []byte("x"))
       b, _ := Seal(key, []byte("x"))
       if bytes.Equal(a, b) {
           t.Fatal("nonces must be random per Seal call")
       }
   }

   func TestOpen_RejectsTampered(t *testing.T) {
       key := make([]byte, 32)
       sealed, _ := Seal(key, []byte("hello"))
       sealed[len(sealed)-1] ^= 0x01
       if _, err := Open(key, sealed); err == nil {
           t.Fatal("expected auth failure on tampered ciphertext")
       }
   }
   ```
2. Run: FAIL.
3. Implement:
   ```go
   package crypto

   import (
       "crypto/aes"
       "crypto/cipher"
       "crypto/rand"
       "errors"
       "fmt"
   )

   const (
       blobVersion = 0x01
       nonceSize   = 24 // we use 24 bytes from random; AEAD needs 12, we use first 12
   )

   // Format: [1 version][24 nonce][ciphertext+tag]
   // We carry a 24-byte random nonce in the header even though GCM uses 12 — the
   // extra entropy is reserved for a future XChaCha20 swap and costs 12 bytes.

   func Seal(key, plaintext []byte) ([]byte, error) {
       if len(key) != 32 { return nil, errors.New("key must be 32 bytes") }
       block, err := aes.NewCipher(key)
       if err != nil { return nil, fmt.Errorf("new cipher: %w", err) }
       gcm, err := cipher.NewGCM(block)
       if err != nil { return nil, fmt.Errorf("new gcm: %w", err) }

       nonce := make([]byte, nonceSize)
       if _, err := rand.Read(nonce); err != nil {
           return nil, fmt.Errorf("rand: %w", err)
       }
       out := make([]byte, 0, 1+nonceSize+len(plaintext)+gcm.Overhead())
       out = append(out, blobVersion)
       out = append(out, nonce...)
       out = gcm.Seal(out, nonce[:gcm.NonceSize()], plaintext, nil)
       return out, nil
   }

   func Open(key, sealed []byte) ([]byte, error) {
       if len(key) != 32 { return nil, errors.New("key must be 32 bytes") }
       if len(sealed) < 1+nonceSize { return nil, errors.New("sealed too short") }
       if sealed[0] != blobVersion { return nil, fmt.Errorf("unknown blob version %d", sealed[0]) }
       nonce := sealed[1 : 1+nonceSize]
       ciphertext := sealed[1+nonceSize:]
       block, err := aes.NewCipher(key)
       if err != nil { return nil, fmt.Errorf("new cipher: %w", err) }
       gcm, err := cipher.NewGCM(block)
       if err != nil { return nil, fmt.Errorf("new gcm: %w", err) }
       return gcm.Open(nil, nonce[:gcm.NonceSize()], ciphertext, nil)
   }
   ```
4. Run: PASS.
5. Commit: `feat(crypto): aes-256-gcm seal/open with versioned blob format`.

### Task 1.3: Random salt + repo key generation

**Files:**
- Create: `internal/crypto/keys.go`
- Create: `internal/crypto/keys_test.go`

**Steps:**

1. Test:
   ```go
   func TestGenerateRepoKey_Length(t *testing.T) {
       k, err := GenerateRepoKey()
       if err != nil { t.Fatal(err) }
       if len(k) != 32 { t.Fatalf("want 32 bytes, got %d", len(k)) }
   }

   func TestGenerateSalt_Length(t *testing.T) {
       s, err := GenerateSalt()
       if err != nil { t.Fatal(err) }
       if len(s) != 16 { t.Fatalf("want 16 bytes, got %d", len(s)) }
   }
   ```
2. Run: FAIL.
3. Implement using `crypto/rand`.
4. Run: PASS.
5. Commit: `feat(crypto): random salt and repo key generators`.

**Phase 1 verification:** All `internal/crypto` tests pass with `-race`. Lint clean.

---

# Phase 2 — Blobstore

Goal: a `Store` interface, an in-memory impl for tests, and an S3 impl exercised against MinIO via testcontainers.

### Task 2.1: Define Store interface

**Files:**
- Create: `internal/blobstore/store.go`

**Steps:**

1. Write the interface (no test yet — interfaces are tested via implementations):
   ```go
   package blobstore

   import (
       "context"
       "errors"
       "io"
   )

   var ErrNotFound = errors.New("blob not found")

   type Store interface {
       Put(ctx context.Context, key string, r io.Reader) error
       Get(ctx context.Context, key string) (io.ReadCloser, error)
       Stat(ctx context.Context, key string) (Info, error)
       Delete(ctx context.Context, key string) error
       List(ctx context.Context, prefix string) ([]Info, error)
   }

   type Info struct {
       Key  string
       Size int64
   }
   ```
2. Commit: `feat(blobstore): define Store interface`.

### Task 2.2: In-memory Store implementation

**Files:**
- Create: `internal/blobstore/memory.go`
- Create: `internal/blobstore/memory_test.go`

**Steps:**

1. Test (table-driven for the interface contract):
   ```go
   package blobstore

   import (
       "bytes"
       "context"
       "errors"
       "io"
       "strings"
       "testing"
   )

   func TestMemory_PutGet(t *testing.T) {
       s := NewMemory()
       ctx := context.Background()
       if err := s.Put(ctx, "k", strings.NewReader("hello")); err != nil { t.Fatal(err) }
       rc, err := s.Get(ctx, "k")
       if err != nil { t.Fatal(err) }
       defer rc.Close()
       got, _ := io.ReadAll(rc)
       if !bytes.Equal(got, []byte("hello")) { t.Fatalf("got %q", got) }
   }

   func TestMemory_GetMissing(t *testing.T) {
       s := NewMemory()
       _, err := s.Get(context.Background(), "missing")
       if !errors.Is(err, ErrNotFound) { t.Fatalf("want ErrNotFound, got %v", err) }
   }

   func TestMemory_List(t *testing.T) {
       s := NewMemory()
       ctx := context.Background()
       _ = s.Put(ctx, "a/1", strings.NewReader("x"))
       _ = s.Put(ctx, "a/2", strings.NewReader("yy"))
       _ = s.Put(ctx, "b/1", strings.NewReader("zzz"))
       got, err := s.List(ctx, "a/")
       if err != nil { t.Fatal(err) }
       if len(got) != 2 { t.Fatalf("want 2, got %d", len(got)) }
   }

   func TestMemory_Delete(t *testing.T) {
       s := NewMemory()
       ctx := context.Background()
       _ = s.Put(ctx, "k", strings.NewReader("v"))
       if err := s.Delete(ctx, "k"); err != nil { t.Fatal(err) }
       if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
           t.Fatalf("want ErrNotFound after delete")
       }
   }
   ```
2. Run: FAIL.
3. Implement `memory.go` with `sync.RWMutex`-guarded `map[string][]byte`.
4. Run: PASS.
5. Commit: `feat(blobstore): in-memory implementation`.

### Task 2.3: S3 Store skeleton

**Files:**
- Create: `internal/blobstore/s3.go`
- Create: `internal/blobstore/s3_unit_test.go`

**Steps:**

1. Add deps: `go get github.com/aws/aws-sdk-go-v2/config github.com/aws/aws-sdk-go-v2/service/s3`
2. Implement `s3.go` with:
   - `type S3Config struct { Bucket, Prefix, Region, Profile, EndpointURL string }`
   - `func NewS3(ctx, cfg S3Config) (*S3, error)` — load AWS config with options for endpoint URL (MinIO) and force path-style
   - `Put/Get/Stat/Delete/List` methods using `s3.Client`
3. Add a unit test that constructs `*S3` with a fake endpoint and asserts no error on construction:
   ```go
   func TestNewS3_WithEndpoint(t *testing.T) {
       _, err := NewS3(context.Background(), S3Config{
           Bucket: "test", Region: "us-east-1", EndpointURL: "http://127.0.0.1:9000",
       })
       if err != nil { t.Fatalf("NewS3: %v", err) }
   }
   ```
4. Commit: `feat(blobstore): s3 implementation skeleton`.

### Task 2.4: S3 integration test against MinIO

**Files:**
- Create: `internal/blobstore/s3_integration_test.go`

**Steps:**

1. Add `//go:build integration` build tag at the top of the file.
2. Add testcontainers dep: `go get github.com/testcontainers/testcontainers-go github.com/testcontainers/testcontainers-go/modules/minio`
3. Write a test that:
   - Spins MinIO container.
   - Creates the bucket.
   - Constructs `S3` with the container endpoint and credentials.
   - Runs the same Put/Get/List/Delete contract as the memory test.
4. Run: `go test -tags=integration ./internal/blobstore -run S3Integration -v` → expect PASS.
5. Add the integration job to `.github/workflows/ci.yml` (Linux only, after the regular test job).
6. Commit: `test(blobstore): s3 integration test against minio testcontainer`.

**Phase 2 verification:** `make test` and `make integration` both pass.

---

# Phase 3 — Chunker

Goal: deterministic chunking + zstd compression with round-trip tests.

### Task 3.1: zstd compress/decompress wrapper

**Files:**
- Create: `internal/chunker/compress.go`
- Create: `internal/chunker/compress_test.go`

**Steps:**

1. Add dep: `go get github.com/klauspost/compress/zstd`
2. Test round-trip:
   ```go
   func TestCompressDecompress_RoundTrip(t *testing.T) {
       in := bytes.Repeat([]byte("hello world "), 1000)
       c, err := Compress(in)
       if err != nil { t.Fatal(err) }
       if len(c) >= len(in) { t.Errorf("compression should shrink redundant data, got %d", len(c)) }
       out, err := Decompress(c)
       if err != nil { t.Fatal(err) }
       if !bytes.Equal(in, out) { t.Fatal("round-trip failed") }
   }
   ```
3. Implement.
4. Commit: `feat(chunker): zstd compress/decompress`.

### Task 3.2: FastCDC chunker

**Files:**
- Create: `internal/chunker/fastcdc.go`
- Create: `internal/chunker/fastcdc_test.go`

**Steps:**

1. Add dep: `go get github.com/jotfs/fastcdc-go` (verify availability; fall back to writing FastCDC inline ~150 LOC if upstream is dead).
2. Test:
   ```go
   func TestChunker_StableBoundaries(t *testing.T) {
       data := bytes.Repeat([]byte("abcdefghij"), 200_000) // 2 MiB
       a, err := ChunkAll(bytes.NewReader(data))
       if err != nil { t.Fatal(err) }
       b, err := ChunkAll(bytes.NewReader(data))
       if err != nil { t.Fatal(err) }
       if len(a) != len(b) { t.Fatalf("chunk count differs: %d vs %d", len(a), len(b)) }
       for i := range a {
           if !bytes.Equal(a[i].Data, b[i].Data) {
               t.Fatalf("chunk %d differs", i)
           }
       }
   }

   func TestChunker_LocalChange_AffectsFewChunks(t *testing.T) {
       data := bytes.Repeat([]byte("abcdefghij"), 200_000)
       mod := make([]byte, len(data))
       copy(mod, data)
       mod[len(mod)/2] = 'Z' // single byte change in the middle
       a, _ := ChunkAll(bytes.NewReader(data))
       b, _ := ChunkAll(bytes.NewReader(mod))
       differing := 0
       am := map[string]struct{}{}
       for _, c := range a { am[string(c.Hash)] = struct{}{} }
       for _, c := range b {
           if _, ok := am[string(c.Hash)]; !ok { differing++ }
       }
       if differing > 5 {
           t.Fatalf("local change should produce few new chunks, got %d", differing)
       }
   }
   ```
3. Implement:
   ```go
   type Chunk struct { Hash []byte; Data []byte; Offset int64 }
   func ChunkAll(r io.Reader) ([]Chunk, error) { /* fastcdc-go iterator + sha256 each */ }
   ```
4. Commit: `feat(chunker): fastcdc with sha256 hashing`.

**Phase 3 verification:** all chunker tests pass; `make lint` clean.

---

# Phase 4 — Walker

Goal: concurrent fs walk that respects `.sentraignore` and emits a stream of file metadata.

### Task 4.1: .sentraignore matcher

**Files:**
- Create: `internal/walker/ignore.go`
- Create: `internal/walker/ignore_test.go`

**Steps:**

1. Add dep: `go get github.com/sabhiram/go-gitignore`
2. Test:
   ```go
   func TestIgnore_MatchesGlobs(t *testing.T) {
       patterns := []string{"*.log", "node_modules/**", "build/"}
       m := NewMatcher(patterns)
       cases := map[string]bool{
           "foo.log":              true,
           "node_modules/x/y.js":  true,
           "build/out":            true,
           "src/foo.go":           false,
           "logs/keep.txt":        false,
       }
       for path, want := range cases {
           if got := m.Match(path); got != want {
               t.Errorf("Match(%q)=%v, want %v", path, got, want)
           }
       }
   }
   ```
3. Implement.
4. Commit: `feat(walker): .sentraignore matcher`.

### Task 4.2: Concurrent file walker

**Files:**
- Create: `internal/walker/walker.go`
- Create: `internal/walker/walker_test.go`

**Steps:**

1. Test (using `t.TempDir()` to build a tree):
   ```go
   func TestWalk_RespectsIgnore(t *testing.T) {
       root := t.TempDir()
       writeFile(t, filepath.Join(root, "keep.txt"), "k")
       writeFile(t, filepath.Join(root, "skip.log"), "s")
       writeFile(t, filepath.Join(root, ".sentraignore"), "*.log\n")

       got := []string{}
       err := Walk(context.Background(), root, Options{}, func(e Entry) error {
           got = append(got, e.RelPath); return nil
       })
       if err != nil { t.Fatal(err) }
       sort.Strings(got)
       want := []string{".sentraignore", "keep.txt"}
       if !reflect.DeepEqual(got, want) { t.Errorf("got %v want %v", got, want) }
   }

   func TestWalk_HonorsCachedirTag(t *testing.T) {
       root := t.TempDir()
       cache := filepath.Join(root, "cache")
       _ = os.Mkdir(cache, 0o755)
       writeFile(t, filepath.Join(cache, "CACHEDIR.TAG"), "Signature: 8a477f597d28d172789f06886806bc55")
       writeFile(t, filepath.Join(cache, "junk"), "x")
       writeFile(t, filepath.Join(root, "real.txt"), "r")
       got := []string{}
       err := Walk(context.Background(), root, Options{ExcludeCaches: true}, func(e Entry) error {
           got = append(got, e.RelPath); return nil
       })
       if err != nil { t.Fatal(err) }
       for _, p := range got {
           if strings.HasPrefix(p, "cache/junk") { t.Fatalf("cache should be skipped, got %v", got) }
       }
   }
   ```
2. Implement:
   ```go
   type Entry struct {
       AbsPath, RelPath string
       Size int64
       Mode os.FileMode
       MTime time.Time
   }

   type Options struct {
       IgnoreFile    string  // default ".sentraignore"
       ExcludeCaches bool
   }

   func Walk(ctx context.Context, root string, opts Options, fn func(Entry) error) error {
       // Read root .sentraignore once. Use filepath.WalkDir.
       // For each dir entry, check CACHEDIR.TAG if ExcludeCaches.
       // Skip ignored paths via matcher.
       // Call fn for each non-ignored regular file.
   }
   ```
3. Commit: `feat(walker): concurrent file walker with ignore support`.

**Phase 4 verification:** all walker tests pass.

---

# Phase 5 — Repo

Goal: snapshot create / list / restore against the in-memory store; this stitches crypto + chunker + walker + blobstore together.

### Task 5.1: Manifest types

**Files:**
- Create: `internal/repo/manifest.go`
- Create: `internal/repo/manifest_test.go`

**Steps:**

1. Test JSON round-trip of `Manifest`, `FileEntry`.
2. Implement structs matching the design's manifest schema. Use `encoding/json` tags.
3. Commit: `feat(repo): manifest types`.

### Task 5.2: Repo config (encrypted) + Init/Open

**Files:**
- Create: `internal/repo/config.go`
- Create: `internal/repo/repo.go`
- Create: `internal/repo/repo_test.go`

**Steps:**

1. Tests:
   - `TestInit_WritesEncryptedConfig` — calls `Init(ctx, store, passphrase)` then asserts `config` blob exists and decrypts to expected struct.
   - `TestOpen_WrongPassphraseFails` — asserts auth failure.
   - `TestInit_TwiceFails` — second init returns "repo already initialized".
2. Implement:
   ```go
   type RepoConfig struct {
       Version   int        `json:"version"`
       ID        string     `json:"id"`
       KDF       crypto.KDFParams `json:"kdf"`
       Salt      []byte     `json:"salt"`
       WrappedRepoKey []byte `json:"wrapped_repo_key"` // repo key encrypted with KEK
       CreatedAt time.Time  `json:"created_at"`
   }

   type Repo struct {
       store   blobstore.Store
       repoKey []byte // 32 bytes, in-memory only
       cfg     RepoConfig
   }

   func Init(ctx context.Context, s blobstore.Store, passphrase []byte) (*Repo, error)
   func Open(ctx context.Context, s blobstore.Store, passphrase []byte) (*Repo, error)
   ```
3. Commit: `feat(repo): init/open with encrypted config`.

### Task 5.3: Snapshot create

**Files:**
- Create: `internal/repo/snapshot.go`
- Create: `internal/repo/snapshot_test.go`

**Steps:**

1. Test:
   ```go
   func TestCreateSnapshot_RoundTrip(t *testing.T) {
       ctx := context.Background()
       store := blobstore.NewMemory()
       repo, _ := Init(ctx, store, []byte("hunter2"))

       root := t.TempDir()
       writeFile(t, filepath.Join(root, "a.txt"), "hello")
       writeFile(t, filepath.Join(root, "sub/b.txt"), "world")

       snap, err := repo.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "test"})
       if err != nil { t.Fatal(err) }
       if snap.Stats.Files != 2 { t.Errorf("files=%d", snap.Stats.Files) }

       loaded, err := repo.LoadSnapshot(ctx, snap.ID)
       if err != nil { t.Fatal(err) }
       if loaded.Tag != "test" { t.Errorf("tag=%q", loaded.Tag) }
       if len(loaded.Tree) != 2 { t.Errorf("tree=%d", len(loaded.Tree)) }
   }

   func TestCreateSnapshot_DedupsAcrossSnapshots(t *testing.T) {
       // Same content twice → second snapshot uploads no new blobs.
       ctx := context.Background()
       store := blobstore.NewMemory()
       repo, _ := Init(ctx, store, []byte("hunter2"))
       root := t.TempDir()
       writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("hello ", 1000))
       _, _ = repo.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "first"})
       blobsBefore, _ := store.List(ctx, "data/")
       _, _ = repo.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "second"})
       blobsAfter, _ := store.List(ctx, "data/")
       if len(blobsBefore) != len(blobsAfter) {
           t.Fatalf("expected dedup, blobs went from %d to %d", len(blobsBefore), len(blobsAfter))
       }
   }
   ```
2. Implement `CreateSnapshot` to:
   - Walk root.
   - For each file, chunk + hash + encrypt + upload (skip if `Stat` shows the blob already exists).
   - Build manifest.
   - Encrypt + upload manifest.
3. Commit: `feat(repo): create snapshot with chunk-level dedup`.

### Task 5.4: List snapshots

**Files:**
- Create: `internal/repo/list.go`
- Create: `internal/repo/list_test.go`

**Steps:**

1. Test creates 3 snapshots and asserts `List` returns them sorted newest-first.
2. Implement using `store.List(ctx, "snapshots/")` then load each manifest header.
3. Commit: `feat(repo): list snapshots`.

### Task 5.5: Restore snapshot

**Files:**
- Create: `internal/repo/restore.go`
- Create: `internal/repo/restore_test.go`

**Steps:**

1. Test creates a snapshot from a tree, restores to a fresh dir, asserts byte-identical contents (recursive walk + compare).
2. Implement `Restore(ctx, snapID, destDir)`.
3. Commit: `feat(repo): restore snapshot`.

**Phase 5 verification:** the full lifecycle works end-to-end against in-memory store. Run `go test -race -count=3 ./internal/repo/...` to flake-check.

---

# Phase 6 — UI primitives

Goal: shared lipgloss theme + reusable styled components used by both inline mode and the future TUI.

### Task 6.1: Theme

**Files:**
- Create: `internal/ui/theme.go`

**Steps:**

1. Add dep: `go get github.com/charmbracelet/lipgloss`
2. Define semantic styles:
   ```go
   package ui

   import "github.com/charmbracelet/lipgloss"

   var (
       Primary = lipgloss.NewStyle().Foreground(lipgloss.Color("#7C3AED")).Bold(true)
       Success = lipgloss.NewStyle().Foreground(lipgloss.Color("#10B981"))
       Warn    = lipgloss.NewStyle().Foreground(lipgloss.Color("#F59E0B"))
       Danger  = lipgloss.NewStyle().Foreground(lipgloss.Color("#EF4444"))
       Muted   = lipgloss.NewStyle().Foreground(lipgloss.Color("#6B7280"))
       Subtle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#9CA3AF"))
       Panel   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
   )

   func Severity(level string) lipgloss.Style {
       switch level {
       case "critical": return Danger.Bold(true)
       case "warn":     return Warn
       case "info":     return Subtle
       default:         return Muted
       }
   }
   ```
3. Commit: `feat(ui): semantic theme`.

### Task 6.2: Progress + spinner helpers

**Files:**
- Create: `internal/ui/progress.go`
- Create: `internal/ui/progress_test.go`

**Steps:**

1. Add: `go get github.com/charmbracelet/bubbles@latest`
2. Provide `NewByteProgress(total int64)` returning a model that wraps `bubbles/progress` and renders bytes-formatted (e.g. "12.3 / 50.4 MiB").
3. Test with `teatest` that one increment + render produces the expected substring.
4. Commit: `feat(ui): byte progress component`.

### Task 6.3: Styled table helper

**Files:**
- Create: `internal/ui/table.go`
- Create: `internal/ui/table_test.go`

**Steps:**

1. Function `RenderTable(headers []string, rows [][]string) string` using `lipgloss/table` (the v0.11+ table package) with the theme.
2. Test golden-file: known rows produce a known string.
3. Commit: `feat(ui): styled table helper`.

### Task 6.4: Passphrase prompt via huh

**Files:**
- Create: `internal/ui/passphrase.go`

**Steps:**

1. Add: `go get github.com/charmbracelet/huh@latest`
2. Function `PromptPassphrase(prompt string) ([]byte, error)` using `huh.NewInput().EchoMode(huh.EchoModePassword)`.
3. Document that this is TTY-only; commands resolve passphrase via flag/env/keyring first, falling back to this.
4. Commit: `feat(ui): passphrase prompt`.

**Phase 6 verification:** package builds, components render in a smoke `cmd/sentra` debug branch (deleted before commit) or via `teatest`.

---

# Phase 7 — CLI commands

Goal: the user-facing commands `init`, `backup`, `snapshots`, `restore`, `diff`. Inline-mode visuals via Phase 6 primitives.

### Task 7.1: Config loader

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

**Steps:**

1. Add: `go get github.com/knadh/koanf/v2 github.com/knadh/koanf/parsers/yaml github.com/knadh/koanf/providers/file github.com/knadh/koanf/providers/env`
2. Define `Config` struct mirroring `sentra.yaml`.
3. `Load(path string) (*Config, error)` with env-overlay (`SENTRA_REPO_S3_BUCKET` etc).
4. Test loads a fixture YAML and asserts fields, then sets env var and asserts override.
5. Commit: `feat(config): koanf-based loader`.

### Task 7.2: Passphrase resolver

**Files:**
- Create: `internal/config/passphrase.go`
- Create: `internal/config/passphrase_test.go`

**Steps:**

1. Function `ResolvePassphrase(flag *string, useKeyring bool) ([]byte, error)` with priority `--passphrase-file` → `SENTRA_PASSPHRASE` → keyring → prompt.
2. Test priorities with environment manipulation (`t.Setenv`).
3. Commit: `feat(config): passphrase resolver`.

### Task 7.3: `sentra init` command

**Files:**
- Create: `internal/cli/init.go`
- Create: `internal/cli/init_test.go`
- Modify: `internal/cli/root.go` (register subcommand)

**Steps:**

1. Test (using a temp dir + memory store via dep injection):
   - Asserts `sentra.yaml` is created with sensible defaults.
   - Asserts `repo.Init` is called and config blob exists.
2. Implement `NewInit()` returning `*cobra.Command`.
3. Commit: `feat(cli): init command`.

### Task 7.4: `sentra backup` command

**Files:**
- Create: `internal/cli/backup.go`
- Create: `internal/cli/backup_test.go`

**Steps:**

1. Test: backup of a temp dir produces a snapshot whose `Stats.Files` matches the dir.
2. Implement: `repo.Open` → `repo.CreateSnapshot` with progress via `internal/ui`.
3. Commit: `feat(cli): backup command with progress UI`.

### Task 7.5: `sentra snapshots` command

**Files:**
- Create: `internal/cli/snapshots.go`
- Create: `internal/cli/snapshots_test.go`

**Steps:**

1. Test: lists snapshots, supports `--json`.
2. Implement.
3. Commit: `feat(cli): snapshots command`.

### Task 7.6: `sentra restore` command

**Files:**
- Create: `internal/cli/restore.go`
- Create: `internal/cli/restore_test.go`

**Steps:**

1. Test: full round-trip of backup → restore byte-identical.
2. Implement.
3. Commit: `feat(cli): restore command`.

### Task 7.7: `sentra diff` command

**Files:**
- Create: `internal/cli/diff.go`
- Create: `internal/cli/diff_test.go`

**Steps:**

1. Test: two snapshots with known differences produce expected `added/removed/changed` lists.
2. Implement (uses `repo.Diff(a, b)` helper which you'll add to `internal/repo/diff.go`).
3. Commit: `feat(cli): diff command`.

**Phase 7 verification:** `make build && ./bin/sentra init`, `backup`, `snapshots`, `restore`, `diff` all work end-to-end against a local MinIO. Add a `docs/QUICKSTART.md` snippet showing the local MinIO recipe.

---

# Phase 8 — Prune + retention

### Task 8.1: Retention policy parser

**Files:**
- Create: `internal/repo/retention.go`
- Create: `internal/repo/retention_test.go`

**Steps:**

1. Test: given a list of snapshot timestamps and a policy `{KeepLast:3, KeepDaily:7, KeepWeekly:4}`, returns the IDs to keep / drop.
2. Implement using a borg-style algorithm.
3. Commit: `feat(repo): retention policy planner`.

### Task 8.2: GC unreferenced blobs

**Files:**
- Create: `internal/repo/gc.go`
- Create: `internal/repo/gc_test.go`

**Steps:**

1. Test: after deleting a snapshot, `GC` removes its uniquely-referenced blobs but keeps shared ones.
2. Implement: build live-set from remaining manifests, delete `data/*` not in set.
3. Commit: `feat(repo): garbage collect unreferenced blobs`.

### Task 8.3: `sentra prune` command

**Files:**
- Create: `internal/cli/prune.go`
- Create: `internal/cli/prune_test.go`

**Steps:**

1. Test: dry-run shows would-be deletions; with `--apply` actually deletes.
2. Implement using retention planner + GC.
3. Commit: `feat(cli): prune command`.

**Phase 8 verification:** prune dry-run works, prune --apply works, blobs counted before/after match.

---

# Phase 9 — Agent: heuristics

### Task 9.1: Finding type + registry

**Files:**
- Create: `internal/agent/heuristics/finding.go`
- Create: `internal/agent/heuristics/registry.go`
- Create: `internal/agent/heuristics/registry_test.go`

**Steps:**

1. Define:
   ```go
   type Finding struct {
       ID       string         // stable hash of category+target
       Category string         // "secrets", "large_file", ...
       Severity string         // "info" | "warn" | "critical"
       Target   string
       Details  map[string]any
   }

   type Heuristic interface {
       Name() string
       Run(ctx context.Context, in Input) ([]Finding, error)
   }

   type Input struct {
       Walked    []walker.Entry
       Snapshots []repo.SnapshotInfo
       LiveBlobs map[string]struct{}
   }
   ```
2. Registry collects heuristics; concurrent run via `errgroup`.
3. Test: a fake heuristic emits known findings; registry merges them.
4. Commit: `feat(agent/heuristics): finding type and registry`.

### Task 9.2: secrets heuristic

**Files:**
- Create: `internal/agent/heuristics/secrets.go`
- Create: `internal/agent/heuristics/secrets_test.go`
- Create: `internal/agent/heuristics/testdata/` with fixture files

**Steps:**

1. Test: a `.env` file containing `AWS_SECRET_ACCESS_KEY=...` produces a `severity:critical` finding; a benign `.go` file produces none.
2. Implement: regex set for AWS keys, GitHub tokens, private-key headers, generic high-entropy strings in `.env`-named files. Cap scan to first 1 MiB per file. Skip binary files (heuristic: NUL byte in first 8 KiB).
3. Commit: `feat(agent/heuristics): secrets detection`.

### Task 9.3: large_files heuristic

**Files:**
- Create: `internal/agent/heuristics/large.go`
- Create: `internal/agent/heuristics/large_test.go`

**Steps:**

1. Test: file > 100 MiB → finding; smaller → none. Threshold configurable.
2. Implement.
3. Commit: `feat(agent/heuristics): large files`.

### Task 9.4: cache_dirs heuristic

**Files:**
- Create: `internal/agent/heuristics/cache.go`
- Create: `internal/agent/heuristics/cache_test.go`

**Steps:**

1. Test: tree containing `node_modules/` not in `.sentraignore` → warn finding; ignored → no finding.
2. Implement: known-name list, check against the active matcher.
3. Commit: `feat(agent/heuristics): cache directory detection`.

### Task 9.5: stale_paths heuristic

**Files:**
- Create: `internal/agent/heuristics/stale.go`
- Create: `internal/agent/heuristics/stale_test.go`

**Steps:**

1. Test: paths with mtime > N days → finding.
2. Implement, with N from config (default 365 days).
3. Commit: `feat(agent/heuristics): stale path detection`.

### Task 9.6: dup_paths heuristic

**Files:**
- Create: `internal/agent/heuristics/dup.go`
- Create: `internal/agent/heuristics/dup_test.go`

**Steps:**

1. Test: two files with identical content → finding listing both paths.
2. Implement using the chunker output (full-file hashes).
3. Commit: `feat(agent/heuristics): duplicate path detection`.

### Task 9.7: orphan_blobs heuristic

**Files:**
- Create: `internal/agent/heuristics/orphan.go`
- Create: `internal/agent/heuristics/orphan_test.go`

**Steps:**

1. Test: store has a blob not referenced by any manifest → finding.
2. Implement using `Input.LiveBlobs` minus actual `data/` listing.
3. Commit: `feat(agent/heuristics): orphan blob detection`.

### Task 9.8: retention_drift heuristic

**Files:**
- Create: `internal/agent/heuristics/retention.go`
- Create: `internal/agent/heuristics/retention_test.go`

**Steps:**

1. Test: snapshot history that violates the configured policy (e.g. 47 weekly snapshots when policy is keep_weekly:4) → finding.
2. Implement using the Phase 8 retention planner.
3. Commit: `feat(agent/heuristics): retention drift detection`.

**Phase 9 verification:** `go test ./internal/agent/heuristics/...` all green.

---

# Phase 10 — Agent: LLM provider

### Task 10.1: Provider interface + Message/Tool types

**Files:**
- Create: `internal/agent/llm/provider.go`

**Steps:**

1. Define:
   ```go
   type Role string
   const (RoleSystem Role = "system"; RoleUser Role = "user"; RoleAssistant Role = "assistant"; RoleTool Role = "tool")

   type Message struct {
       Role    Role
       Content string
       ToolUse *ToolUse
       ToolResult *ToolResult
   }

   type Tool struct {
       Name        string
       Description string
       Schema      map[string]any // JSON schema for inputs
   }

   type ToolCall struct { ID, Name string; Input map[string]any }
   type ToolUse struct { ID, Name string; Input map[string]any }
   type ToolResult struct { ID string; Content string; Error string }

   type Provider interface {
       Generate(ctx context.Context,
                sys string,
                msgs []Message,
                tools []Tool,
                stream chan<- string) ([]ToolCall, string, error)
   }
   ```
2. Commit: `feat(agent/llm): provider interface`.

### Task 10.2: Fake provider for tests

**Files:**
- Create: `internal/agent/llm/fake.go`
- Create: `internal/agent/llm/fake_test.go`

**Steps:**

1. `fake.Provider` returns scripted `[]ToolCall` then a final text on the next call. Useful for orchestrator tests.
2. Commit: `feat(agent/llm): fake provider for tests`.

### Task 10.3: Anthropic provider

**Files:**
- Create: `internal/agent/llm/anthropic.go`
- Create: `internal/agent/llm/anthropic_test.go` (unit-level only — request building / response parsing; no network calls)

**Steps:**

1. Add: `go get github.com/anthropics/anthropic-sdk-go@latest`
2. Implement `NewAnthropic(model string)` returning a `Provider`. Translate our `Message`/`Tool` types to the SDK's. Stream content blocks; forward text deltas to the `stream` channel; collect tool_use blocks into `[]ToolCall`.
3. Test with the SDK's HTTP-level stub or by injecting a test server. Avoid live calls.
4. Commit: `feat(agent/llm): anthropic provider with streaming and tool use`.

**Phase 10 verification:** `go test ./internal/agent/llm/...` green; no live API calls in test runs.

---

# Phase 11 — Agent: orchestrator + tools

### Task 11.1: Tool implementations

**Files:**
- Create: `internal/agent/tools/tools.go`
- Create: `internal/agent/tools/tools_test.go`

**Steps:**

1. Implement, each as a function taking the repo + JSON input and returning JSON output:
   - `list_snapshots`
   - `snapshot_stats`
   - `diff_snapshots`
   - `inspect_finding`
2. Test each against an in-memory repo.
3. Commit: `feat(agent/tools): read-only investigation tools`.

### Task 11.2: Orchestrator

**Files:**
- Create: `internal/agent/orchestrator.go`
- Create: `internal/agent/orchestrator_test.go`

**Steps:**

1. Test (with `fake.Provider`):
   - Heuristics produce 3 findings.
   - Provider scripted to call `inspect_finding` once, then emit 2 recommendations.
   - Orchestrator returns those 2 recommendations.
   - Tool-call budget enforced (provider tries 11 calls, orchestrator stops at 10).
2. Implement:
   ```go
   type Recommendation struct {
       ID, Action, Target, Severity, Rationale string
   }

   type Agent struct {
       Repo      *repo.Repo
       Walker    walker.Func
       Heuristics []heuristics.Heuristic
       Provider  llm.Provider
       Config    Config // includes MaxFindings, MaxToolCalls
   }

   func (a *Agent) Scan(ctx context.Context, root string, stream chan<- string) ([]Recommendation, error)
   ```
3. Commit: `feat(agent): orchestrator stitches heuristics + LLM + tools`.

### Task 11.3: `sentra agent scan` command

**Files:**
- Create: `internal/cli/agent.go`
- Create: `internal/cli/agent_test.go`

**Steps:**

1. Test: with a fake provider injected, command exits 0 and prints recommendations table; `--json` emits valid JSON; `--apply` triggers per-recommendation `huh` confirm flow.
2. Implement.
3. Commit: `feat(cli): agent scan command with --apply`.

**Phase 11 verification:** end-to-end `sentra agent scan` against MinIO + fake provider works; switching to real Anthropic provider with `ANTHROPIC_API_KEY` set produces output (manual smoke test, not in CI).

---

# Phase 12 — TUI

Goal: full-screen Bubbletea dashboard at `sentra ui` (also default with no args).

### Task 12.1: App skeleton

**Files:**
- Create: `internal/tui/app.go`
- Create: `internal/tui/app_test.go`

**Steps:**

1. Add: `go get github.com/charmbracelet/bubbletea@latest`
2. Define `App` model with a `view` enum (dashboard/snapshots/agent/config), a `KeyMap`, and a `bubbles/help` model.
3. Test (teatest): pressing `s` switches to snapshots, `q` quits.
4. Commit: `feat(tui): app skeleton with view switching`.

### Task 12.2: Dashboard view

**Files:**
- Create: `internal/tui/dashboard.go`
- Create: `internal/tui/dashboard_test.go`

**Steps:**

1. Renders: repo name, snapshot count, total bytes, last snapshot card, agent recommendation count.
2. Test (teatest snapshot of initial render).
3. Commit: `feat(tui): dashboard view`.

### Task 12.3: Snapshots view

**Files:**
- Create: `internal/tui/snapshots.go`
- Create: `internal/tui/snapshots_test.go`

**Steps:**

1. `bubbles/table` with snapshot list; `enter` opens detail; `esc` returns.
2. Detail view shows file tree using `bubbles/list` with hierarchical items.
3. Test navigation with synthetic key events.
4. Commit: `feat(tui): snapshots view + detail`.

### Task 12.4: Diff view

**Files:**
- Create: `internal/tui/diff.go`
- Create: `internal/tui/diff_test.go`

**Steps:**

1. Pick two snapshots from the snapshots view, render added/removed/changed in three columns.
2. Test rendering with known input.
3. Commit: `feat(tui): diff view`.

### Task 12.5: Agent view (streaming)

**Files:**
- Create: `internal/tui/agent.go`
- Create: `internal/tui/agent_test.go`

**Steps:**

1. Top half: viewport that tails LLM reasoning tokens (fed via a `tea.Cmd` reading the stream channel from `Provider.Generate`).
2. Bottom half: recommendations table that fills in as the agent emits them; `a` triggers apply confirm.
3. Test: feed scripted tokens via the stream channel; assert viewport contains them.
4. Commit: `feat(tui): agent view with live streaming`.

### Task 12.6: `sentra ui` command + default

**Files:**
- Create: `internal/cli/ui.go`
- Modify: `internal/cli/root.go` (default command if no args)

**Steps:**

1. Wire `sentra ui` to launch `tui.NewProgram(...).Run()`.
2. If `os.Args` has no subcommand (just `sentra`), fall through to `ui` after loading config.
3. Commit: `feat(cli): ui subcommand and default-on-no-args`.

**Phase 12 verification:** manual `sentra ui` works against a populated repo; teatest CI tests pass.

---

# Phase 13 — Release & docs

### Task 13.1: goreleaser config

**Files:**
- Create: `.goreleaser.yaml`

**Steps:**

1. Multi-OS / arch matrix, `ldflags` injecting `version`/`commit`/`date`, checksums, archives, Homebrew tap, Docker image.
2. Test locally with `goreleaser release --snapshot --clean`.
3. Commit: `chore(release): goreleaser config`.

### Task 13.2: Release workflow

**Files:**
- Create: `.github/workflows/release.yml`

**Steps:**

1. Trigger on `v*` tag. Set up Go, run goreleaser with `GITHUB_TOKEN` + `HOMEBREW_TAP_TOKEN` secrets. Cosign keyless signing via OIDC. SBOM via syft.
2. Commit: `ci(release): goreleaser workflow with cosign and sbom`.

### Task 13.3: README + asciinema cast

**Files:**
- Create: `README.md`
- Create: `docs/architecture.md`
- Create: `docs/threat-model.md`
- Create: `docs/QUICKSTART.md`

**Steps:**

1. README: badges, screenshot/cast, `init` → `backup` → `agent scan` walkthrough, install (homebrew, go install, docker).
2. `docs/architecture.md`: mermaid diagram of storage model + agent loop.
3. `docs/threat-model.md`: what client-side encryption protects against, what it doesn't (size correlation, access logs, key handling).
4. Commit: `docs: README, architecture, threat model, quickstart`.

### Task 13.4: Tag v0.1.0

**Steps:**

1. Confirm CI is green on main.
2. `git tag -a v0.1.0 -m "v0.1.0 — initial release"`
3. `git push origin v0.1.0` — release workflow runs.
4. Verify GitHub release artifacts appear.
5. Manual smoke: `brew install markgustetic/tap/sentra && sentra --version`.

**Phase 13 verification:** `v0.1.0` is downloadable from GitHub; the binaries run; the README's quickstart works end to end.

---

# Definition of done for v1

- [ ] `make test` green with `-race` on Linux + macOS
- [ ] `make integration` green on Linux
- [ ] `make lint` clean (errcheck, govet, staticcheck, gosec, etc.)
- [ ] `sentra init`, `backup`, `snapshots`, `diff`, `restore`, `prune`, `agent scan`, `ui` all work end to end
- [ ] Coverage ≥ 70% on `internal/...`
- [ ] README quickstart works on a clean machine
- [ ] `v0.1.0` released via goreleaser with signed artifacts and SBOM
- [ ] No `TODO` comments without an issue link

When all boxes are checked: tag v0.1.0, write a launch post, drink a beer.
