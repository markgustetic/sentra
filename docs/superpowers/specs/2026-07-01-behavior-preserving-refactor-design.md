# Behavior-Preserving Refactor of `sentra` — Design

**Date:** 2026-07-01
**Status:** Approved scope ("Everything defensible"), pending spec review.

## Goal

Improve the maintainability of the `sentra` codebase (split oversized files,
remove real duplication, delete verified dead code, one targeted simplification)
**without changing any behavior**. A read-only survey (7 analyzers, cross-checked
with `deadcode`, `staticcheck` U1000, and `gofmt -s`) confirmed the codebase is
already clean: no package/architecture change is warranted, `gofmt -s` is clean,
and there is exactly one dead first-party symbol. The refactor is therefore a
curated, low-risk set — not a rewrite.

## Principles (non-negotiable)

1. **Behavior-preserving.** No change to observable behavior, error strings, wire
   formats, generated YAML, exported API, or the security model.
2. **Green at every step.** After each commit: `go build ./...`, `go vet ./...`,
   `go test -race ./...`, `golangci-lint run ./...`, `gofmt -l cmd internal`,
   `go mod tidy -diff`, and the vendored `go test ./third_party/fastcdc-go/...`
   all pass.
3. **One reviewable commit per item.** File splits are pure same-package code
   motion (no logic edits, no renamed or newly-exported symbols), so existing
   tests compile and pass unchanged.
4. **No scope creep.** Package boundaries, `agent.provider`, and readability
   rewrites are explicitly out of scope (see "Out of scope" with rationale).

## In scope

### A. File splits (same-package code motion)

Each split moves cohesive groups of functions/types into a new file in the **same
package**. No signatures change; no symbols are renamed or exported. Tests
reference package-level identifiers, so they need no edits.

| # | Source (LOC) | New file(s) | What moves |
|---|---|---|---|
| A1 | `internal/cli/setup.go` (1026) | `setup_auth.go`, `setup_errors.go`, `setup_init.go`, `setup_summary.go` | auth: `runSetupAWSAuth`, `runSetupAWS{Login,SSO,Existing}Auth`, `ensureSetupAWSCLI`, `trySetupAWSSDKIdentity`, `checkSetupAWSSDKIdentity`, `setupAWSSDKIdentityChecker`. errors: `wrapAWS{SSOFlow,Prepare,LoginFlow}Error`, `isAWSMissingCredentialsError`, `printSetupErrorDetail`, `setupErrorAdvice`. init: `setupInitResult` type + `runSetupInit`. summary: `printSetupSummary`, `printSetupApplyHeader`, `printSetupRepairContinue`, `printSetup{Step,OK}`, `setupBackendLabel`, `setupAWSAuthMethodLabel`, `setupAWSPreparedLabel`, `validateSetupBucketName`. `setup.go` keeps exported types, `SetupDeps`, `NewSetup`, `runSetup`, and draft/plan helpers. |
| A2 | `internal/agent/orchestrator.go` (642) | `orchestrator_prompt.go`, `orchestrator_parse.go` | prompt: `systemPromptTemplate`, `buildInitialMessage`, `formatToolsForPrompt`, `filterFindingsByCategory`, `localRecommendations`, `localActionForFinding`. parse: `parseRecommendations`, `tryUnmarshalArray`, `tryUnmarshalObject`, `stripFences`, `bracketSubstring`, `truncate`. `orchestrator.go` keeps types, `Config`, `Agent`, the `Scan` loop, `collectWalked`, `computeLiveBlobs`, `writeStream`. |
| A3 | `internal/cli/schedule.go` (506) | `schedule_render.go` | OS-file rendering: `renderScheduleFiles`, `renderLaunchAgent`, `launchdCalendar`, `launchdCalendarEntry`, `renderSystemdUserUnits`, `systemdOnCalendar`, `systemdDailyCalendar`, `scheduleClock`, `isTwoASCIIDigits`, `launchdWeekday`, `systemdWeekday`, `xmlEscape`, `systemdQuoteArg`. `schedule.go` keeps cobra wiring, `run*`, `loadScheduledPolicy`, path/exe resolution. |
| A4 | `internal/repo/snapshot.go` (515) | `snapshot_write.go` | write path: `captureFile`, `finishSnapshot`, `putManifest`, `snapState` + its methods. `snapshot.go` keeps `CreateSnapshot`, `LoadSnapshot`, option/info types, ID helpers (`newSnapshotID`, `snapshotIDPattern`, `validateSnapshotID`), `ChunkKey`, `DataPrefix`, `snapshotPrefix`, `resolveWalkerOptions`. A single two-file split (no third `snapshot_id.go`). |
| A5 | `internal/repo/restore.go` (507) | `restore_verify.go` | read-only path: `PlanRestore`, `VerifyRestore`, `verifyRestoreFile`, `hashFileChunks`, `inspectDestDir`, and `RestorePlan`/`RestoreMismatch`/`RestoreVerification` (+ `.OK`). `restore.go` keeps `Restore`, `restoreFile`, `fetchChunk`, `ensureDestDir`, `RestoreOptions`, `ErrChunkHashMismatch`. |
| A6 | `internal/agent/heuristics/secrets.go` (370) | `secrets_redact.go` | redaction unit: `expandToToken`, `isSpaceByte`, `redactPreview`, and the `previewMaxLen` const. `secrets.go` keeps patterns, `Secrets`, `Run`, `scanForSecrets`. |
| A7 | `internal/cli/agent.go` (479) | `agent_apply.go` | output/apply: `writeRecsTable`, `writeRecsJSON`, `applyRecommendations`, `dispatchAction`, `truncateRationale`. `agent.go` keeps deps, flags, cobra wiring, `runAgentScan`. |
| A8 | `internal/cli/setup_awss3.go` (287) | `setup_awss3_ops.go` | low-level S3 helpers + error/ARN utils: `loadSetupAWSConfig`, `headBucket`, `createBucket`, `waitForBucketExists`, `blockBucketPublicAccess`, `enableBucketDefaultEncryption`, `getBucketPublicAccessBlock`, `getBucketDefaultEncryption`, `isS3BucketMissing`, `isBucketAlreadyOwned`, `isAWSAPIErrCode`, `s3BucketARN`, `s3ObjectARN`. `setup_awss3.go` keeps the `DefaultAWS{CheckSDKIdentity,Inspect,Prepare}` entry points. |

### B. Deduplication

| # | What | Change |
|---|---|---|
| B1 | 11 CLI command bodies repeat an identical `config.Load → NewStore → resolvePassphrase → repo.Open` block (same 4 error strings). | Add `openRepoForConfig(...)` in new `internal/cli/repo_open.go` returning `(*repo.Repo, pass []byte, *config.Config, error)`. Callers keep their own `defer crypto.Zeroize(pass)` and `defer r.Close()` at the call site so key/handle lifetimes are unchanged. `init.go`/`setup.go` excluded (they call `repo.Init`). |
| B2 | 15 per-command `Deps` structs redeclare the same `NewStore`/`Passphrase`/`PassphraseWithConfig`/`Stdout` fields. | Define `type repoDeps struct { ... }` and embed it in each command's `Deps`. `openRepoForConfig` takes a `repoDeps`. Update all construction sites in `cmd/sentra/commands.go` and `*_test.go` fixtures (mechanical field-nesting). Sequenced with B1. |
| B3 | `if w == nil { cmd.OutOrStdout() }` fallback repeated ~43×. | Add `cmdStdout(cmd, w)` / `cmdStderr(cmd, w)` helpers; replace each block. |
| B4 | `ensureDestDir`/`inspectDestDir` duplicate a security-relevant stat/empty guard verbatim. | Extract `statDestDir(dest) (exists, empty bool, err error)` with the **exact** existing error strings; both functions delegate. |
| B5 | `putManifest` and `saveSnapshotIndex` share a marshal→compress→seal→put envelope. | Add `putSealedJSON(ctx, repoKey, key, v)` on `*Repo`; both call it (index still re-stamps Version/Updated first). Preserve per-site error prefixes ("seal manifest" vs "seal index"). Do **not** unify the read side. |
| B6 | The `%w: missing=… manifests=… lock_stale=…` construction (incl. the nil-guarded stale-lock predicate) is duplicated in `check.go` and `policy.go`. | Add `checkFailedError(report)` in `cli`; call from both. Byte-identical error value. |

### C. Dead code

| # | What | Change |
|---|---|---|
| C1 | `DefaultAWSCheckIdentity` (`setup_awscli.go`) — verified dead (`deadcode -test ./...` reports it as the only unreachable first-party symbol; superseded by the SDK identity path). | Delete the function + its doc comment. No callers, no test references. |

### D. Simplification

| # | What | Change |
|---|---|---|
| D1 | `RetryStore.{Get,Stat,List,BatchDelete}` repeat a result-capture-over-`retry` shape. | Add unexported generic `retryResult[T any](r, ctx, op)`; rewrite the four methods as one-liners. `Put`/`PutIfAbsent`/`Delete` unchanged. **Preserve each method's distinct doc comment** on the thin wrapper. |

## Out of scope (investigated, deliberately excluded)

- **Package/architecture restructuring.** The internal import graph is a clean
  acyclic DAG with correct layering; `internal/cli`'s size is a one-file-per-command
  layout, not a god-package. A `cli/setup` subpackage would force exporting a large
  private surface (worse encapsulation). No change is warranted — honoring the
  "restructure" goal by having checked and found nothing to do.
- **`agent.provider` field.** Parsed but unwired; removing it would drop `provider:`
  from generated YAML and break round-tripping — **not** behavior-preserving. Left as
  forward-compat surface (a separate product decision, not this refactor).
- **Readability rewrites.** `gofmt -s` is clean and the large functions are
  well-structured with thorough docs; no convoluted-function rewrite is justified.

## Execution plan (commit sequence)

Low-risk / isolated first, then splits (pure motion), then dedups, then the one
simplification. Each is a separate commit; the full green gate runs after each.

1. **C1** — delete dead `DefaultAWSCheckIdentity`.
2. **A1–A8** — file splits, one commit each (verify affected package builds/tests).
3. **B1 + B2** — `openRepoForConfig` + `repoDeps` base struct (one commit; helper
   consumes the base struct).
4. **B3** — `cmdStdout`/`cmdStderr`.
5. **B4** — `statDestDir`.
6. **B5** — `putSealedJSON`.
7. **B6** — `checkFailedError`.
8. **D1** — `RetryStore` generic helper.

## Verification strategy

- Per commit: build + affected-package `go test -race` + `gofmt -l`.
- After the full sequence: the complete CI-equivalent gate (`go test -race ./...`,
  `go vet`, `golangci-lint`, `gofmt -l cmd internal`, `go mod tidy -diff`,
  `go test ./third_party/fastcdc-go/...`).
- For splits: a `git diff --stat` sanity check that each split commit only moves
  lines (no net logic change) — reviewer can confirm behavior preservation by
  inspection.

## Risks & mitigations

- **Test-fixture churn from B2** (touches every `Deps` construction). Mitigation:
  mechanical field-nesting only; run `go test ./internal/cli/...` immediately.
- **Error-string drift in B4/B5/B6.** Mitigation: preserve exact wording; any test
  asserting on message text must still pass (part of the green gate).
- **Import cleanup after splits.** Moving functions may leave a source file importing
  a package it no longer uses (build error) — caught immediately by `go build`.
