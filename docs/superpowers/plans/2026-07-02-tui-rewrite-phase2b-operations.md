# TUI Rewrite Phase 2b (Check/Diff/Sync/Agent-Apply/Recovery-Kit/Doctor) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Six more operations on the Phase 2a framework — on-demand integrity check, snapshot-pair diff picker, sync-to-mirror with progress, per-recommendation agent apply, recovery-kit viewer/export, and doctor diagnostics — plus the deferred cancel-behind-confirm rule.

**Architecture:** Same flow pattern as 2a (configure → preview → confirm → run-through-guard → result). New principle: cli-owned engines (recovery kit, doctor) are **injected into `tui.Deps` as closures** from `cli/ui.go` / `cmd/sentra` — the `cli → tui` dependency direction makes this free and avoids import cycles or code moves. Repo-layer flows (check, diff, sync) call `internal/repo` directly; agent apply uses the existing `action.Registry`.

**Tech Stack:** Go 1.25, bubbletea v1.3.10, bubbles v1.0.0 (viewport, table, progress, textinput), lipgloss v1.1.0.

Spec: `docs/superpowers/specs/2026-07-01-tui-rewrite-design.md`. Predecessor: Phase 2a (merged, `aa47f2f`). Successor: Phase 2c (policies, schedule, password — separate plan).

---

## Conventions (same as 2a)

- `cat`/`tail`/`head` are aliased to `bat` — use `command tail -n N` or redirect to files.
- **Green gate per task:** `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...` (+ `./internal/cli/ ./cmd/...` when wiring files change). golangci-lint only in the final task.
- **Branch:** create `feature/tui-phase2b` from `main` at Task 1 Step 1.
- Flow tests use real in-memory repos (`newFlowRepo`, `seedSnapshotReal`, `seedTwoSnapshots` helpers exist). Assert text/structure, never ANSI.
- Verified APIs: `r.Check(ctx, repo.CheckOptions{Now, StaleLockAfter}) (repo.CheckReport, error)` (report fields incl. `Snapshots`, `DataBlobs`, `OrphanBytes`, `MissingBlobs`, `ManifestIssues`, `Lock`); `r.Diff(ctx, idA, idB) (repo.DiffResult, error)`; tui `Diff` already has `SetResult(idA, idB string, res repo.DiffResult) Diff`; `r.SyncTo(ctx, dest blobstore.Store, repo.SyncOptions{InitDest, DryRun, Concurrency, Progress}) (repo.SyncStats, error)`; `action.NewDefaultRegistry()`, `Registry.Dispatch(ctx, env, action, id, target, severity, rationale)` with `action.Env{Repo, Stdout, Cwd, ...}` (verify Env fields at `internal/agent/action/action.go:61`); cli-internal `buildRecoveryKit(ctx, r, cfg, cfgPath)` + `renderRecoveryKitMarkdown(kit)`, `runDoctorAWS(ctx, deps, cfg, out) int` + `runDoctorRepo(ctx, deps, cfg, out) int`.

## File structure

| File | Responsibility |
|---|---|
| `internal/tui/app.go` | Deps closures (`NewStore`, `Actions`, `BuildRecoveryKit`, `RunDoctor`), cancel-confirm handling, new registry entries |
| `internal/cli/ui.go` + `cmd/sentra/commands.go` | closure wiring |
| `internal/cli/doctor.go` | extract exported `DoctorReport` (behavior-preserving; `runDoctor` calls it) |
| `internal/tui/operations.go` | upgrade to Check flow (re-run through guard, issues viewport) |
| `internal/tui/diffpick.go` | snapshot-pair picker feeding the existing Diff view |
| `internal/tui/sync.go` | sync flow (dst config → dry-run preview → confirm → progress) |
| `internal/tui/agent.go` | + per-recommendation apply (confirm modal → dispatch through guard) |
| `internal/tui/recoverykit.go` | markdown viewport + export |
| `internal/tui/doctorview.go` | diagnostics viewport, re-run |

---

## Task 1: Branch + Deps closures + wiring + `DoctorReport` extract

**Files:**
- Modify: `internal/tui/app.go` (Deps only), `internal/cli/ui.go`, `internal/cli/doctor.go`, `cmd/sentra/commands.go`
- Test: `internal/tui/app_test.go`, plus existing `internal/cli` doctor tests must stay green

- [ ] **Step 1: Branch**

```bash
cd /Users/markgustetic/Programming/portfolio/sentra
git checkout main && git pull && git checkout -b feature/tui-phase2b
```

- [ ] **Step 2: Failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_DepsCarryEngineClosures: Phase 2b flows receive cli-owned
// engines as injected closures; Deps must carry them nil-tolerantly.
func TestApp_DepsCarryEngineClosures(t *testing.T) {
	called := false
	app := NewApp(Deps{
		RunDoctor: func(ctx context.Context) (string, error) { called = true; return "ok", nil },
	})
	out, err := app.deps.RunDoctor(context.Background())
	if err != nil || out != "ok" || !called {
		t.Fatal("RunDoctor closure not carried through Deps")
	}
}
```

(`context` already imported in app