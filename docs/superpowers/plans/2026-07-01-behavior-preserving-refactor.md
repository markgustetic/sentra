# Behavior-Preserving Refactor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Improve maintainability of `sentra` (split oversized files, remove real duplication, delete one verified dead symbol, one `RetryStore` simplification) with **zero behavior change**.

**Architecture:** Refactor only. File splits are pure same-package code motion — no renamed or newly-exported symbols, so existing tests compile and pass unchanged. Dedups introduce internal helpers that produce byte-identical behavior (same error strings). The existing `-race` test suite is the correctness oracle for every task.

**Tech Stack:** Go 1.25; `cobra` CLI; tests via `go test -race`; lint via `golangci-lint`; format via `gofmt`.

Spec: `docs/superpowers/specs/2026-07-01-behavior-preserving-refactor-design.md`

---

## Conventions for every task

- **Shell note:** in this environment `cat`/`tail`/`head` are aliased to `bat`; use `command tail -n N` or redirect to a file and read it.
- **Green gate (per task):** `go build ./...`, then `go test -race ./internal/<pkg>/...` for the touched package(s), then `gofmt -l cmd internal` (must print nothing) and `go vet ./...`.
- **Full gate (after the last task):** `go test -race ./...`, `go vet ./...`, `golangci-lint run ./...`, `gofmt -l cmd internal`, `go mod tidy -diff`, `go test ./third_party/fastcdc-go/...`.
- **Splits:** create the new file with the `package` clause, move the listed symbols verbatim (cut, don't rewrite), then let `goimports`/manual fixups settle imports in both files. Confirm with `git diff` that only line locations changed, not logic.
- **Branch:** `refactor/behavior-preserving` (already created; the spec commit is its first commit).

---

## Task 1: Remove dead `DefaultAWSCheckIdentity` (C1)

**Files:**
- Modify: `internal/cli/setup_awscli.go` (delete the function + doc comment near line 59-65)

- [ ] **Step 1: Confirm it is dead**

Run: `command grep -rn "DefaultAWSCheckIdentity" internal cmd`
Expected: only its definition + doc comment in `setup_awscli.go`; no callers.

- [ ] **Step 2: Delete the function and its doc comment**

Remove the `DefaultAWSCheckIdentity` function (the ~3-line body running `aws sts get-caller-identity` via `runAWSCLI`) and the doc comment above it. Leave `runAWSCLI` and the other `Default*` functions untouched.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: build OK, tests PASS, gofmt prints nothing, vet clean.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/setup_awscli.go
git commit -m "Remove dead DefaultAWSCheckIdentity (superseded by SDK identity path)"
```

---

## Task 2: Split `setup.go` (A1)

**Files:**
- Modify: `internal/cli/setup.go`
- Create: `internal/cli/setup_auth.go`, `internal/cli/setup_errors.go`, `internal/cli/setup_init.go`, `internal/cli/setup_summary.go`

- [ ] **Step 1: Create the four new files, each `package cli`, and move symbols**

- `setup_auth.go` ← `runSetupAWSAuth`, `runSetupAWSLoginAuth`, `runSetupAWSSSOAuth`, `runSetupAWSExistingAuth`, `ensureSetupAWSCLI`, `trySetupAWSSDKIdentity`, `checkSetupAWSSDKIdentity`, `setupAWSSDKIdentityChecker`.
- `setup_errors.go` ← `wrapAWSSSOFlowError`, `wrapAWSPrepareError`, `wrapAWSLoginFlowError`, `isAWSMissingCredentialsError`, `printSetupErrorDetail`, `setupErrorAdvice`.
- `setup_init.go` ← `setupInitResult` type + `runSetupInit`.
- `setup_summary.go` ← `printSetupSummary`, `printSetupApplyHeader`, `printSetupRepairContinue`, `printSetupStep`, `printSetupOK`, `setupBackendLabel`, `setupAWSAuthMethodLabel`, `setupAWSPreparedLabel`, `validateSetupBucketName`.

`setup.go` keeps: exported types (`SetupBackend`, `SetupPlan`, `SetupDeps`, …), `NewSetup`, `runSetup`, and the draft/plan helpers (`writeSetupDraft`, `removeSetupDraft`, `setupDraftPath`, `applySetupAWSConfigOnly`, `applySetupPassphraseConfig`, `confirmSetupReviewIfNeeded`, `promptSetupAWSRepairIfNeeded`, `continueSetupAfterAWSRepair`, `resolveSetupAWSAuthMethod`).

- [ ] **Step 2: Settle imports**

Run `goimports -w internal/cli/setup*.go` (or hand-fix). Each new file imports only what it uses; `setup.go` drops now-unused imports.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: build OK, tests PASS (no test edits needed — same package), gofmt/vet clean.

- [ ] **Step 4: Sanity-check pure motion**

Run: `git diff --stat`
Expected: `setup.go` shrinks by ~600 lines; the four new files sum to roughly that. No other files changed.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/setup*.go
git commit -m "Split cli/setup.go into auth, errors, init, summary files"
```

---

## Task 3: Split `agent/orchestrator.go` (A2)

**Files:**
- Modify: `internal/agent/orchestrator.go`
- Create: `internal/agent/orchestrator_prompt.go`, `internal/agent/orchestrator_parse.go`

- [ ] **Step 1: Create the two new files, each `package agent`, and move symbols**

- `orchestrator_prompt.go` ← `systemPromptTemplate`, `buildInitialMessage`, `formatToolsForPrompt`, `filterFindingsByCategory`, `localRecommendations`, `localActionForFinding`.
- `orchestrator_parse.go` ← `parseRecommendations`, `tryUnmarshalArray`, `tryUnmarshalObject`, `stripFences`, `bracketSubstring`, `truncate` (only used by the parser).

`orchestrator.go` keeps: types (`Recommendation`, `Config`, `Agent`), `Defaults`, `actionsOrDefault`, the `Scan` loop, `collectWalked`, `computeLiveBlobs`, `writeStream`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/agent/orchestrator*.go`.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/agent/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS; `orchestrator_test.go` unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/orchestrator*.go
git commit -m "Split agent/orchestrator.go into prompt-building and response-parsing files"
```

---

## Task 4: Split `cli/schedule.go` (A3)

**Files:**
- Modify: `internal/cli/schedule.go`
- Create: `internal/cli/schedule_render.go`

- [ ] **Step 1: Create `schedule_render.go` (`package cli`) and move the rendering group**

Move: `renderScheduleFiles`, `renderLaunchAgent`, `launchdCalendar`, `launchdCalendarEntry`, `renderSystemdUserUnits`, `systemdOnCalendar`, `systemdDailyCalendar`, `scheduleClock`, `isTwoASCIIDigits`, `launchdWeekday`, `systemdWeekday`, `xmlEscape`, `systemdQuoteArg`.

`schedule.go` keeps: `ScheduleDeps`, `NewSchedule`, `runScheduleInstall/Status/Uninstall`, `loadScheduledPolicy`, `schedulePaths`/`schedulerPaths`, `scheduleExecutable`, `scheduleStdout`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/cli/schedule*.go`.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS; `schedule_test.go` unchanged.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/schedule*.go
git commit -m "Split cli/schedule.go: move launchd/systemd rendering into schedule_render.go"
```

---

## Task 5: Split `repo/snapshot.go` (A4)

**Files:**
- Modify: `internal/repo/snapshot.go`
- Create: `internal/repo/snapshot_write.go`

- [ ] **Step 1: Create `snapshot_write.go` (`package repo`) and move the write path**

Move: `captureFile`, `finishSnapshot`, `putManifest`, and the `snapState` type with its methods (`add`, `totalBytes`, `newBytes`, `snapshotTree`, etc.).

`snapshot.go` keeps: `CreateSnapshot`, `LoadSnapshot`, `SnapshotOptions`/`SnapshotInfo`, the ID helpers (`newSnapshotID`, `snapshotIDPattern`, `validateSnapshotID`), `ChunkKey`, `DataPrefix`, `snapshotPrefix`, `resolveWalkerOptions`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/repo/snapshot*.go`.

- [ ] **Step 3: Build + test + lint (this is the integrity-critical package — run the full repo suite)**

Run: `go build ./... && go test -race ./internal/repo/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/repo/snapshot*.go
git commit -m "Split repo/snapshot.go: move write path + snapState into snapshot_write.go"
```

---

## Task 6: Split `repo/restore.go` (A5)

**Files:**
- Modify: `internal/repo/restore.go`
- Create: `internal/repo/restore_verify.go`

- [ ] **Step 1: Create `restore_verify.go` (`package repo`) and move the read-only path**

Move: `PlanRestore`, `VerifyRestore`, `verifyRestoreFile`, `hashFileChunks`, `inspectDestDir`, and `RestorePlan`/`RestoreMismatch`/`RestoreVerification` (incl. `RestoreVerification.OK`).

`restore.go` keeps: `Restore`, `restoreFile`, `fetchChunk`, `ensureDestDir`, `RestoreOptions`, `ErrChunkHashMismatch`.

> Note: `inspectDestDir` and `ensureDestDir` are unified in Task 12 (B4); keep both as-is here — just relocate `inspectDestDir`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/repo/restore*.go`.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/repo/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/repo/restore*.go
git commit -m "Split repo/restore.go: move read-only plan/verify path into restore_verify.go"
```

---

## Task 7: Split `heuristics/secrets.go` (A6)

**Files:**
- Modify: `internal/agent/heuristics/secrets.go`
- Create: `internal/agent/heuristics/secrets_redact.go`

- [ ] **Step 1: Create `secrets_redact.go` (`package heuristics`) and move the redaction unit**

Move: `expandToToken`, `isSpaceByte`, `redactPreview`, and the `previewMaxLen` const (only they use it).

`secrets.go` keeps: the patterns (`dotEnvFilenames`, `secretPattern`, `secretPatterns`, `compileSecretPatterns`), `Secrets`, `NewSecrets`, `Run`, `scanFile`, `scanForSecrets`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/agent/heuristics/secrets*.go` (`redactPreview` uses `strings`/`slices`; make sure `secrets.go` keeps only what it still needs).

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/agent/heuristics/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/agent/heuristics/secrets*.go
git commit -m "Split heuristics/secrets.go: move leak-prevention redaction into secrets_redact.go"
```

---

## Task 8: Split `cli/agent.go` (A7)

**Files:**
- Modify: `internal/cli/agent.go`
- Create: `internal/cli/agent_apply.go`

- [ ] **Step 1: Create `agent_apply.go` (`package cli`) and move the output/apply cluster**

Move: `writeRecsTable`, `writeRecsJSON`, `applyRecommendations`, `dispatchAction`, `truncateRationale`.

`agent.go` keeps: `AgentDeps`, `agentFlags`, `NewAgent`, `NewAgentScan`, `runAgentScan`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/cli/agent*.go`.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/agent*.go
git commit -m "Split cli/agent.go: move recommendation output/apply into agent_apply.go"
```

---

## Task 9: Split `cli/setup_awss3.go` (A8)

**Files:**
- Modify: `internal/cli/setup_awss3.go`
- Create: `internal/cli/setup_awss3_ops.go`

- [ ] **Step 1: Create `setup_awss3_ops.go` (`package cli`) and move the low-level helpers**

Move: `loadSetupAWSConfig`, `headBucket`, `createBucket`, `waitForBucketExists`, `blockBucketPublicAccess`, `enableBucketDefaultEncryption`, `getBucketPublicAccessBlock`, `getBucketDefaultEncryption`, `isS3BucketMissing`, `isBucketAlreadyOwned`, `isAWSAPIErrCode`, `s3BucketARN`, `s3ObjectARN`.

`setup_awss3.go` keeps: `DefaultAWSCheckSDKIdentity`, `DefaultAWSInspect`, `DefaultAWSPrepare`.

- [ ] **Step 2: Settle imports** — `goimports -w internal/cli/setup_awss3*.go`.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli/setup_awss3*.go
git commit -m "Split cli/setup_awss3.go: move low-level S3 helpers into setup_awss3_ops.go"
```

---

## Task 10: `repoDeps` base struct + `openRepoForConfig` helper (B1 + B2)

**Files:**
- Create: `internal/cli/repo_open.go`
- Modify: `cmd/sentra/commands.go` (nest common Deps fields), and each command file's `Deps` type + read-path body: `backup.go`, `restore.go`, `check.go`, `prune.go`, `diff.go`, `snapshots.go`, `policy.go`, `recovery_kit.go`, `ui.go`, `agent.go`
- Modify tests: every `*_test.go` that constructs a `Deps` with `NewStore`/`Passphrase`/`PassphraseWithConfig`/`Stdout`

- [ ] **Step 1: Add the base struct + helper in `internal/cli/repo_open.go`**

```go
package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
)

// RepoDeps is the dependency set shared by every read-path command: the
// blobstore factory, the two passphrase resolvers, and the stdout sink.
// Commands embed it and add their own extra fields (Stderr, Confirm, ...).
// Exported so cmd/sentra can construct it when wiring each command.
type RepoDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// openRepoForConfig runs the shared load-config -> open-store -> resolve-
// passphrase -> open-repo sequence. On success it returns the opened repo,
// the passphrase bytes (caller owns `defer crypto.Zeroize(pass)` and
// `defer r.Close()`), and the loaded config. On any error it cleans up the
// passphrase itself and returns it nil. Error strings are identical to the
// per-command blocks this replaces.
func openRepoForConfig(cmd *cobra.Command, cfgPath string, deps RepoDeps) (*repo.Repo, []byte, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve passphrase: %w", err)
	}
	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		crypto.Zeroize(pass)
		return nil, nil, nil, fmt.Errorf("open repo: %w", err)
	}
	return r, pass, cfg, nil
}
```

- [ ] **Step 2: Convert each command's `Deps` to embed `RepoDeps`**

For each command, replace the four common fields with the embed. Example (`CheckDeps`):

```go
// before
type CheckDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}
// after
type CheckDeps struct {
	RepoDeps
}
```

Commands with extra fields keep them alongside the embed, e.g.:

```go
type BackupDeps struct {
	RepoDeps
	Stderr  io.Writer
	Confirm func(string) (bool, error)
}
```

Field access is transparent through the embed, so existing code that reads
`deps.NewStore` / `deps.Stdout` inside a command body keeps working unchanged.

- [ ] **Step 3: Replace each command body's 6-line preamble with the helper**

Pattern (apply at all 10 read-path sites — line ranges from the spec):

```go
// before
cfg, err := config.Load(cfgPath)
if err != nil {
	return fmt.Errorf("load config: %w", err)
}
store, err := deps.NewStore(cmd.Context(), cfg)
if err != nil {
	return fmt.Errorf("open blobstore: %w", err)
}
pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
if err != nil {
	return fmt.Errorf("resolve passphrase: %w", err)
}
defer crypto.Zeroize(pass)
r, err := repo.Open(cmd.Context(), store, pass)
if err != nil {
	return fmt.Errorf("open repo: %w", err)
}
defer r.Close()

// after
r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.repoDeps)
if err != nil {
	return err
}
defer crypto.Zeroize(pass)
defer r.Close()
```

Sites: `backup.go` runBackup + runBackupApply, `restore.go`, `check.go`, `prune.go`, `diff.go`, `snapshots.go`, `policy.go`, `recovery_kit.go`, `ui.go`, `agent.go`. Drop the now-unused `cfg`/`pass` locals only where they were used solely for the preamble; keep `cfg` where the body reads `cfg.Backup`/`cfg.Retention`. Remove now-unused imports (`config`, `crypto`, `repo`, `blobstore`) from files that no longer reference them directly.

- [ ] **Step 4: Update production wiring in `cmd/sentra/commands.go`**

`RepoDeps` is exported, so `cmd/sentra` nests it directly:

```go
CheckDeps{RepoDeps: cli.RepoDeps{NewStore: newS3Store, PassphraseWithConfig: openPassphrase, Stdout: os.Stdout}}
```

Confirm the exact current wiring/field values in `cmd/sentra/commands.go` and preserve them — only the nesting changes.

- [ ] **Step 5: Update test fixtures**

Every `*_test.go` that builds a `Deps` literal with the four common fields must nest them under `RepoDeps: RepoDeps{...}`. Mechanical; `go build ./...` and the test compiler will point at each site.

- [ ] **Step 6: Build + full CLI test + lint**

Run: `go build ./... && go test -race ./internal/cli/ ./cmd/... && gofmt -l cmd internal && go vet ./...`
Expected: PASS. If any error string changed, revert to exact wording.

- [ ] **Step 7: Commit**

```bash
git add internal/cli cmd/sentra
git commit -m "Add repoDeps base struct + openRepoForConfig helper; collapse 10 CLI preambles"
```

---

## Task 11: `cmdStdout`/`cmdStderr` helpers (B3)

**Files:**
- Modify: `internal/cli/repo_open.go` (or a small `internal/cli/output.go`)
- Modify call sites: `backup.go`, `restore.go`, `check.go`, `diff.go`, `prune.go`, `snapshots.go`, `policy.go`, `recovery_kit.go` (and any other `Stdout`/`Stderr` nil-fallback sites)

- [ ] **Step 1: Add the helpers**

```go
// cmdStdout returns w, or the command's default stdout when w is nil.
func cmdStdout(cmd *cobra.Command, w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return cmd.OutOrStdout()
}

// cmdStderr returns w, or the command's default stderr when w is nil.
func cmdStderr(cmd *cobra.Command, w io.Writer) io.Writer {
	if w != nil {
		return w
	}
	return cmd.ErrOrStderr()
}
```

- [ ] **Step 2: Replace each fallback block**

```go
// before
out := deps.Stdout
if out == nil {
	out = cmd.OutOrStdout()
}
// after
out := cmdStdout(cmd, deps.Stdout)
```

Same for the `stderr`/`ErrOrStderr` twin.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli
git commit -m "Add cmdStdout/cmdStderr helpers; collapse repeated nil-fallback blocks"
```

---

## Task 12: `statDestDir` unification (B4)

**Files:**
- Modify: `internal/repo/restore.go` (`ensureDestDir`) and `internal/repo/restore_verify.go` (`inspectDestDir`, moved there in Task 6)

- [ ] **Step 1: Add the shared helper (in `restore.go` or `path.go`)**

```go
// statDestDir stats a restore destination and reports whether it exists
// and (if a directory) whether it is empty. A non-directory at dest is an
// error. Shared by ensureDestDir (which creates on absence) and
// inspectDestDir (which only reports). Error wording is preserved exactly.
func statDestDir(dest string) (exists, empty bool, err error) {
	info, err := os.Stat(dest)
	if errors.Is(err, os.ErrNotExist) {
		return false, true, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("repo: stat dest %s: %w", dest, err)
	}
	if !info.IsDir() {
		return true, false, fmt.Errorf("repo: dest %s exists and is not a directory", dest)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		return true, false, fmt.Errorf("repo: read dest %s: %w", dest, err)
	}
	return true, len(entries) == 0, nil
}
```

> Confirm the EXACT current error strings in `ensureDestDir`/`inspectDestDir` before writing this; the wording above must match byte-for-byte (tests may assert on it).

- [ ] **Step 2: Rewrite `ensureDestDir` and `inspectDestDir` to delegate**

`ensureDestDir`: call `statDestDir`; if `!exists` do `os.MkdirAll(dest, 0o755)`; if `exists && !empty` return the existing "is not empty (%d entries)" error (recompute the count or keep that branch in the caller — preserve the exact message); else nil. `inspectDestDir`: return `statDestDir`'s `(exists, empty, err)` directly (adapting to its current return signature/messages).

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/repo/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS, including any test asserting on the "not empty"/"not a directory" wording.

- [ ] **Step 4: Commit**

```bash
git add internal/repo
git commit -m "Unify ensureDestDir/inspectDestDir on a shared statDestDir helper"
```

---

## Task 13: `putSealedJSON` envelope helper (B5)

**Files:**
- Modify: `internal/repo/snapshot_write.go` (`putManifest`, moved there in Task 5) and `internal/repo/index.go` (`saveSnapshotIndex`)

- [ ] **Step 1: Add the helper on `*Repo`**

```go
// putSealedJSON marshals v, compresses, seals under repoKey, and Puts the
// result at key. Shared write envelope for manifests and the snapshot
// index. Callers wrap the returned error with their own site-specific
// prefix so operator-facing messages ("seal manifest" vs "seal index")
// are preserved.
func (r *Repo) putSealedJSON(ctx context.Context, repoKey []byte, key string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	compressed, err := chunker.Compress(raw)
	if err != nil {
		return fmt.Errorf("compress: %w", err)
	}
	sealed, err := crypto.Seal(repoKey, compressed)
	if err != nil {
		return fmt.Errorf("seal: %w", err)
	}
	if err := r.store.Put(ctx, key, bytes.NewReader(sealed)); err != nil {
		return fmt.Errorf("put: %w", err)
	}
	return nil
}
```

> Verify the exact Marshal/Compress/Seal/Put calls in the two current sites (arg order, whether they marshal `v` vs `&v`) and match them. If the current per-step error prefixes differ from the generic ones above, either keep the generic ones and wrap at the call site, or pass a label — choose whichever preserves the observable messages tests/operators rely on.

- [ ] **Step 2: Rewrite `putManifest` and `saveSnapshotIndex` to call it**

`putManifest` → `return r.putSealedJSON(ctx, repoKey, snapshotPrefix+m.ID, m)` (wrapped as before). `saveSnapshotIndex` re-stamps `Version`/`Updated` first, then calls `putSealedJSON`, wrapping the error with its "index" prefix.

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/repo/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/repo
git commit -m "Extract shared putSealedJSON write envelope for manifest + index"
```

---

## Task 14: `checkFailedError` helper (B6)

**Files:**
- Modify: `internal/cli/check.go` and `internal/cli/policy.go`

- [ ] **Step 1: Add the helper (in `check.go`)**

```go
// checkFailedError builds the standard ErrCheckFailed error from a check
// report, centralizing the stale-lock predicate. Byte-identical to the
// former inline construction at both call sites.
func checkFailedError(report repo.CheckReport) error {
	return fmt.Errorf("%w: missing=%d manifests=%d lock_stale=%t",
		ErrCheckFailed,
		len(report.MissingBlobs),
		len(report.ManifestIssues),
		report.Lock != nil && (report.Lock.Stale || report.Lock.Unreadable))
}
```

> Confirm the exact type name (`repo.CheckReport`), field names, and format string against the current `check.go`/`policy.go` code before writing.

- [ ] **Step 2: Replace both inline constructions with `checkFailedError(report)`**

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/cli/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/cli
git commit -m "Extract checkFailedError helper (dedupe stale-lock predicate)"
```

---

## Task 15: `RetryStore` generic `retryResult` (D1)

**Files:**
- Modify: `internal/blobstore/retry.go`

- [ ] **Step 1: Add the generic helper**

```go
// retryResult runs op through the store's retry policy, capturing and
// returning its typed result. Collapses the per-method result-capture
// boilerplate shared by Get/Stat/List/BatchDelete.
func retryResult[T any](r *RetryStore, ctx context.Context, op func() (T, error)) (T, error) {
	var out T
	err := r.retry(ctx, func() error {
		v, err := op()
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}
```

> Confirm the exact signature of the private `r.retry(...)` method before writing (arg order, whether it takes ctx). Match it.

- [ ] **Step 2: Rewrite `Get`, `Stat`, `List`, `BatchDelete` as one-liners over `retryResult`**

Keep each method's existing doc comment verbatim (they document distinct retry semantics — "initial request only", "whole pagination retried", etc.). Example:

```go
func (r *RetryStore) Stat(ctx context.Context, key string) (Info, error) {
	return retryResult(r, ctx, func() (Info, error) { return r.inner.Stat(ctx, key) })
}
```

Leave `Put`, `PutIfAbsent`, `Delete` unchanged (they have special-casing / no result value).

- [ ] **Step 3: Build + test + lint**

Run: `go build ./... && go test -race ./internal/blobstore/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS (including `retry_test.go`, which asserts retry counts).

- [ ] **Step 4: Commit**

```bash
git add internal/blobstore/retry.go
git commit -m "Collapse RetryStore result wrappers with a generic retryResult helper"
```

---

## Task 16: Full gate + integration

- [ ] **Step 1: Run the complete CI-equivalent gate**

```bash
go build ./... \
 && go vet ./... \
 && gofmt -l cmd internal \
 && go test -race -count=1 ./... \
 && go test ./third_party/fastcdc-go/... \
 && go mod tidy -diff \
 && golangci-lint run ./...
```
Expected: all pass; `gofmt -l` and `go mod tidy -diff` print nothing; golangci-lint "0 issues".

- [ ] **Step 2: Confirm no behavior drift in the diff**

Run: `git diff --stat main` and skim: production diffs should be code motion + helper extraction only; no changed error strings, wire formats, or exported signatures beyond the new `NewXDeps` constructors.

- [ ] **Step 3 (optional): rerun the review workflow** against the branch to confirm no regressions were introduced by the motion.

---

## Self-review notes (author)

- **Spec coverage:** A1–A8 → Tasks 2–9; B1+B2 → Task 10; B3 → Task 11; B4 → Task 12; B5 → Task 13; B6 → Task 14; C1 → Task 1; D1 → Task 15. All spec items covered.
- **Ordering:** C1 first (isolated), splits before dedups (so B4/B5 operate on already-relocated `inspectDestDir`/`putManifest`), full gate last.
- **Highest-churn task:** Task 10 (B1+B2) touches every command's `Deps` type, `cmd/sentra/commands.go`, and every test fixture that builds a `Deps`. `RepoDeps` is exported so the `cmd/sentra` boundary is a plain nested literal (no extra constructors). The compiler pinpoints every fixture site; the change is mechanical field-nesting. This is the one task that isn't pure code motion.
- **Error-string preservation** (B4/B5/B6): the plan flags, at each site, to verify the exact current wording before writing the helper — the green gate (tests asserting on message text) is the backstop.
