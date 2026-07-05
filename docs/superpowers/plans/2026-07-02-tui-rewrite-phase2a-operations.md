# TUI Rewrite Phase 2a (Operation Framework + Backup/Restore/Prune) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give the Phase 1 shell its first real operations — an async operation framework (one-op guard, live progress, cancelation, typed confirmation) proven by the three core lifecycle flows: Backup, Restore, and Prune.

**Architecture:** Flows are state-machine views (configure → preview → confirm → running → result). All repo work runs in `tea.Cmd` goroutines; progress is polled from a mutex-guarded reporter via `tea.Tick`; the App enforces one mutating operation at a time through a `startOpMsg`/`opResultMsg` protocol and surfaces it in the status bar. Destructive prune requires a typed confirmation modal. The repo layer is untouched.

**Tech Stack:** Go 1.25, bubbletea v1.3.10, bubbles v1.0.0 (table, progress, spinner, textinput, stopwatch, filepicker), lipgloss v1.1.0.

Spec: `docs/superpowers/specs/2026-07-01-tui-rewrite-design.md`. This is the first half of Phase 2 ("Phase 2a"); the remaining nine flows (check, sync, diff picker, agent apply, policies, schedule, password, recovery kit, doctor) are Phase 2b on this framework.

---

## Conventions for every task

- **Shell note:** `cat`/`tail`/`head` are aliased to `bat` — use `command tail -n N` or redirect output to a file and read it.
- **Green gate (per task):** `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`. Tasks 1 and 7 add `./internal/cli/ ./cmd/...` (they touch wiring). Do NOT run golangci-lint until Task 8 (interim-unused symbols are expected).
- **Branch:** create `feature/tui-phase2a` from `main` at the start (Task 1, Step 1).
- **Tests:** flows are tested against REAL in-memory repos (`repo.Init` over `blobstore.NewMemory()` — the tui package already imports both). No mocks of the repo layer. Colors are stripped in tests: assert text/structure, never ANSI codes.
- **API drift:** if a bubbles v1.0.0 API differs from this plan's sketch (most likely: `filepicker`, `stopwatch`), check `go doc github.com/charmbracelet/bubbles/<pkg>` and adapt, keeping the tests' behavior contracts identical.

## Existing surfaces this plan builds on (do not recreate)

- `internal/tui/app.go`: `App` shell (sidebar/palette/statusbar/modals, `routeKey` precedence, `broadcast`, `resize` with `contentW/contentH`, `tooSmall` guard), `Deps{Repo, Provider, RepoName, Ctx}`, `activateMsg`, `badgeMsg`, `dismissModalMsg`, `confirmedMsg{id}`.
- `internal/tui/modal.go`: `Modal` interface (`Update(tea.Msg) (Modal, tea.Cmd)`, `View() string`, `SetSize(w,h int) Modal`), `ErrorModal`, `InfoModal`, `ConfirmModal`.
- `internal/tui/registry.go`: `Registry`, `Command{ID, Title, Category, Badge}`.
- `internal/tui/statusbar.go`: `StatusBar.View(repoLabel string, viewKeys []key.Binding, running string)` — the `running` param is currently always `""` in `App.View`; this plan wires it.
- Repo APIs (signatures verified on main):
  - `r.CreateSnapshot(ctx, root string, repo.SnapshotOptions{Tag string, Progress progress.Reporter, Walker walker.Options}) (repo.SnapshotInfo, error)`
  - `r.ListSnapshots(ctx) ([]repo.SnapshotInfo, error)`; `SnapshotInfo{ID, CreatedAt, Tag, Stats}`
  - `r.PlanRestore(ctx, snapID, destDir) (repo.RestorePlan, error)`; `r.Restore(ctx, snapID, destDir, repo.RestoreOptions{Progress, Concurrency})`; `r.VerifyRestore(ctx, snapID, destDir) (repo.RestoreVerification, error)`
  - `repo.PlanRetentionExplain(snaps, repo.RetentionPolicy{KeepLast, KeepDaily, KeepWeekly, KeepMonthly}) []repo.RetentionDecision{Snapshot, Keep, Reasons}`
  - `r.DeleteSnapshot(ctx, id)`; `r.GC(ctx, keepIDs map[string]bool) (repo.GCStats{LiveBlobs, DeletedBlobs, DeletedBytes}, error)`
  - `progress.Reporter{Total(int64); Add(int64)}` — implementations must be concurrency-safe.

## File structure (Phase 2a target)

| File | Responsibility |
|---|---|
| `internal/tui/ops.go` | Operation protocol: `startOpMsg`, `opResultMsg` marker, `opReporter` (poll-based progress), tick command |
| `internal/tui/modal.go` | + `TypedConfirmModal` (type-the-word destructive gate) |
| `internal/tui/backup.go` | Backup flow view (configure → running → done) |
| `internal/tui/restore.go` | Restore flow view (pick → dest → plan+confirm → running → verify → done) |
| `internal/tui/prune.go` | Prune flow view (preview → typed confirm → running → done) |
| `internal/tui/app.go` | + `Deps.Config`, op guard (running/cancel), `startOpMsg`/`opResultMsg`/`cancelOpMsg` handling, 3 new registry entries, statusbar running wire |
| `internal/cli/ui.go` | pass `Config: cfg` |

---

## Task 1: Branch, `Deps.Config`, and wiring

**Files:**
- Modify: `internal/tui/app.go` (Deps struct only), `internal/cli/ui.go`
- Test: `internal/tui/app_test.go` (one assertion)

- [ ] **Step 1: Create the branch**

```bash
cd /Users/markgustetic/Programming/portfolio/sentra
git checkout main && git pull && git checkout -b feature/tui-phase2a
```

- [ ] **Step 2: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_DepsCarryConfig: flows need the resolved config (retention
// policy, walker options). Deps must carry it nil-tolerantly.
func TestApp_DepsCarryConfig(t *testing.T) {
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 7
	app := NewApp(Deps{RepoName: "x", Config: &cfg})
	if app.deps.Config == nil || app.deps.Config.Retention.KeepLast != 7 {
		t.Fatal("Deps.Config not carried through NewApp")
	}
}
```

Add import `"github.com/markgustetic/sentra/internal/config"` to the test file.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/tui/ -run TestApp_DepsCarryConfig -count=1`
Expected: FAIL — `unknown field Config in struct literal`.

- [ ] **Step 4: Add the field and wire it**

In `internal/tui/app.go`, add to the `Deps` struct (after `RepoName`):

```go
	// Config is the resolved sentra configuration. Operation flows
	// read retention limits and walker options from it. May be nil
	// (tests, unconfigured installs) — flows must fall back to
	// config.Defaults() semantics when absent.
	Config *config.Config
```

Add import `"github.com/markgustetic/sentra/internal/config"`.

In `internal/cli/ui.go`, extend the `tui.Deps` literal in `runUI` with `Config: cfg,`.

- [ ] **Step 5: Gate + commit**

Run: `go build ./... && go test -race -count=1 ./internal/tui/ ./internal/cli/ ./cmd/... && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

```bash
git add internal/tui internal/cli
git commit -m "Add Config to TUI Deps and wire it from sentra ui"
```

---

## Task 2: Operation protocol + guard

**Files:**
- Create: `internal/tui/ops.go`
- Modify: `internal/tui/app.go` (guard fields + Update cases + statusbar wire)
- Test: `internal/tui/ops_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/ops_test.go`:

```go
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeDoneMsg is a minimal opResultMsg for guard tests.
type fakeDoneMsg struct{ err error }

func (fakeDoneMsg) opResult() {}

func startFakeOp(name string, block <-chan struct{}) startOpMsg {
	return startOpMsg{
		name: name,
		run: func(ctx context.Context) tea.Msg {
			select {
			case <-block:
				return fakeDoneMsg{}
			case <-ctx.Done():
				return fakeDoneMsg{err: ctx.Err()}
			}
		},
	}
}

func TestOpGuard_StartSetsRunningAndStatusBarShowsIt(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{})
	m, cmd := app.Update(startFakeOp("backup", block))
	if cmd == nil {
		t.Fatal("start must return the op command")
	}
	a := m.(App)
	if a.opRunning != "backup" {
		t.Fatalf("opRunning = %q, want backup", a.opRunning)
	}
	if !strings.Contains(a.View(), "backup") {
		t.Error("status bar must show the running operation")
	}
	close(block)
	// Drain: the op cmd resolves to fakeDoneMsg; delivering it clears the guard.
	m, _ = a.Update(cmd())
	if got := m.(App).opRunning; got != "" {
		t.Fatalf("opRunning after result = %q, want empty", got)
	}
}

func TestOpGuard_RejectsSecondOpWithErrorModal(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{})
	defer close(block)
	m, _ := app.Update(startFakeOp("backup", block))
	m2, cmd2 := m.(App).Update(startFakeOp("prune", block))
	a := m2.(App)
	if a.opRunning != "backup" {
		t.Fatalf("second op must not replace the first; running = %q", a.opRunning)
	}
	if cmd2 != nil {
		t.Fatal("rejected op must not return a run command")
	}
	if len(a.modals) == 0 || !strings.Contains(a.modals[len(a.modals)-1].View(), "in progress") {
		t.Fatal("rejection must push an operation-in-progress error modal")
	}
}

func TestOpGuard_CancelMsgCancelsContext(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{}) // never closed: only cancel can finish the op
	m, cmd := app.Update(startFakeOp("backup", block))
	m2, _ := m.(App).Update(cancelOpMsg{})
	// The op goroutine observes ctx.Done and returns an error result.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		res, ok := msg.(fakeDoneMsg)
		if !ok || !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected canceled result, got %#v", msg)
		}
		m2, _ = m2.(App).Update(msg)
		if m2.(App).opRunning != "" {
			t.Fatal("guard must clear after canceled result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelOpMsg did not cancel the op context")
	}
}

func TestOpReporter_SnapshotIsConcurrencySafe(t *testing.T) {
	r := newOpReporter()
	r.Total(100)
	donech := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			r.Add(1)
		}
		close(donech)
	}()
	for {
		total, done := r.Snapshot()
		if total != 100 {
			t.Fatalf("total = %d, want 100", total)
		}
		select {
		case <-donech:
			if _, d := r.Snapshot(); d != 50 {
				t.Fatalf("done = %d, want 50", d)
			}
			return
		default:
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestOpGuard_|TestOpReporter_' -count=1`
Expected: FAIL — `undefined: startOpMsg`.

- [ ] **Step 3: Implement `internal/tui/ops.go`**

```go
package tui

import (
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/progress"
)

// startOpMsg asks the App to launch a repository operation. Flows
// never spawn repo work themselves: routing every start through the
// App gives one enforcement point for the "one mutating operation at
// a time" rule (mirroring the repo's advisory lock) and one place
// that owns the cancelable context.
type startOpMsg struct {
	// name labels the operation in the status bar ("backup", "prune").
	name string
	// run executes the operation synchronously and returns the flow's
	// result message. It MUST honor ctx cancellation and MUST return a
	// message implementing opResultMsg so the guard clears.
	run func(ctx context.Context) tea.Msg
}

// cancelOpMsg cancels the running operation's context. The operation
// itself still finishes (with ctx.Canceled) and clears the guard via
// its opResultMsg — cancel is a request, not a state change.
type cancelOpMsg struct{}

// opResultMsg marks a flow's terminal operation message. The App
// clears the guard on ANY message implementing it, then broadcasts it
// so the owning flow can render its result.
type opResultMsg interface{ opResult() }

// opTickMsg drives progress repaints while an operation runs.
type opTickMsg struct{}

// opTick emits opTickMsg at ~10fps. Flows return it from Update while
// in their running stage; ticking stops when the stage leaves running.
func opTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return opTickMsg{} })
}

// opReporter is a poll-based progress.Reporter. Repo worker pools call
// Total/Add concurrently; the flow's Update polls Snapshot on each
// opTickMsg. Polling avoids per-chunk channel sends entirely — at
// 10fps the UI reads two ints under a mutex, which no upload rate can
// overwhelm.
type opReporter struct {
	mu    sync.Mutex
	total int64
	done  int64
}

var _ progress.Reporter = (*opReporter)(nil)

func newOpReporter() *opReporter { return &opReporter{} }

func (r *opReporter) Total(n int64) {
	r.mu.Lock()
	r.total = n
	r.mu.Unlock()
}

func (r *opReporter) Add(delta int64) {
	r.mu.Lock()
	r.done += delta
	r.mu.Unlock()
}

// Snapshot returns the current (total, done) pair.
func (r *opReporter) Snapshot() (int64, int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total, r.done
}
```

Add `"context"` to the imports.

- [ ] **Step 4: Wire the guard into `internal/tui/app.go`**

Add fields to `App`:

```go
	// opRunning names the in-flight operation ("" when idle); opCancel
	// cancels its context. One mutating operation at a time — the
	// repo's advisory lock would reject a second anyway; failing fast
	// here gives the user a clear modal instead of a lock error.
	opRunning string
	opCancel  context.CancelFunc
```

Add cases to `App.Update` (before the `tea.KeyMsg` case):

```go
	case startOpMsg:
		if m.opRunning != "" {
			m.modals = append(m.modals, NewErrorModal(
				fmt.Errorf("%s is already running", m.opRunning),
				"One operation at a time: wait for it to finish or cancel it with esc.",
				m.width, m.height))
			return m, nil
		}
		opCtx, cancel := context.WithCancel(m.appCtx())
		m.opRunning = msg.name
		m.opCancel = cancel
		run := msg.run
		return m, func() tea.Msg { return run(opCtx) }

	case cancelOpMsg:
		if m.opCancel != nil {
			m.opCancel()
		}
		return m, nil
```

And at the TOP of `Update`'s type switch — before the message reaches any other case — clear the guard on results, then fall through to broadcast so the owning flow sees its own result:

```go
	// Terminal operation results clear the guard regardless of type.
	if res, ok := msg.(opResultMsg); ok {
		_ = res
		m.opRunning = ""
		if m.opCancel != nil {
			m.opCancel()
			m.opCancel = nil
		}
		return m.broadcast(msg)
	}
```

(Place this as a plain `if` before `switch msg := msg.(type)`.)

`appCtx` helper: NewApp already derives a cancellable ctx but stores only `cancel`. Store the ctx too — add field `ctx context.Context` to App, set `ctx: ctx` in `NewApp`, and:

```go
// appCtx returns the App-scoped context operations derive from.
func (m App) appCtx() context.Context {
	if m.ctx != nil {
		return m.ctx
	}
	return context.Background()
}
```

Wire the status bar: in `App.View`, replace `m.status.View(m.deps.RepoName, viewKeys, "")` with `m.status.View(m.deps.RepoName, viewKeys, m.opRunning)`.

- [ ] **Step 5: Gate + commit**

Run: `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS (all Phase 1 tests still green — the new `if` only intercepts opResultMsg types, which didn't exist before).

```bash
git add internal/tui
git commit -m "Add TUI operation protocol: one-op guard, cancel, poll-based progress reporter"
```

---

## Task 3: TypedConfirmModal

**Files:**
- Modify: `internal/tui/modal.go`
- Test: `internal/tui/modal_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/modal_test.go`:

```go
func TestTypedConfirmModal_RequiresExactWord(t *testing.T) {
	m := Modal(NewTypedConfirmModal("Confirm prune", "Deletes 9 snapshots.", "prune", "prune-apply", 80, 24))

	// Enter with the wrong word: no confirmation, modal stays.
	for _, r := range "prun" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("wrong word must not confirm")
	}

	// Complete the word: enter confirms with the modal's id.
	m2 := Modal(NewTypedConfirmModal("Confirm prune", "b", "prune", "prune-apply", 80, 24))
	for _, r := range "prune" {
		m2, _ = m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, cmd = m2.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("exact word must confirm")
	}
	res, ok := cmd().(confirmedMsg)
	if !ok || res.id != "prune-apply" {
		t.Fatalf("got %#v, want confirmedMsg{prune-apply}", cmd())
	}
}

func TestTypedConfirmModal_EscCancelsAndQTypes(t *testing.T) {
	m := Modal(NewTypedConfirmModal("t", "b", "prune", "id", 80, 24))
	// 'q' must be typed into the input, not treated as quit/dismiss.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !strings.Contains(m.View(), "q") {
		t.Fatal("'q' should appear in the typed input")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, ok := cmd().(dismissModalMsg); !ok {
		t.Fatalf("esc must dismiss, got %#v", cmd())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestTypedConfirmModal -count=1`
Expected: FAIL — `undefined: NewTypedConfirmModal`.

- [ ] **Step 3: Implement in `internal/tui/modal.go`**

```go
// TypedConfirmModal is the destructive-operation gate: the user must
// type an exact word (e.g. "prune") before enter confirms. A plain
// yes/no modal is too easy to blow through for operations that delete
// data; retyping the verb forces deliberate intent — same rationale
// as the CLI's --yes-less confirmation prompts.
type TypedConfirmModal struct {
	title    string
	body     string
	word     string
	id       string
	input    textinput.Model
	width    int
	height   int
}

func NewTypedConfirmModal(title, body, word, id string, width, height int) TypedConfirmModal {
	ti := textinput.New()
	ti.Prompt = "> "
	ti.Focus()
	return TypedConfirmModal{title: title, body: body, word: word, id: id,
		input: ti, width: width, height: height}
}

func (m TypedConfirmModal) Update(msg tea.Msg) (Modal, tea.Cmd) {
	k, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	switch k.Type {
	case tea.KeyEnter:
		if m.input.Value() != m.word {
			return m, nil // wrong word: stay, let the user see the mismatch
		}
		id := m.id
		return m, func() tea.Msg { return confirmedMsg{id: id} }
	case tea.KeyEsc:
		return m, func() tea.Msg { return dismissModalMsg{} }
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m TypedConfirmModal) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render(m.title))
	b.WriteString("\n\n")
	b.WriteString(m.body)
	b.WriteString("\n\n")
	b.WriteString("Type " + ui.Danger.Bold(true).Render(m.word) + " to confirm:\n")
	b.WriteString(m.input.View())
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render("⏎ confirm · esc cancel"))
	box := ui.ModalBox.Width(min(m.width-8, 64)).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m TypedConfirmModal) SetSize(w, h int) Modal { m.width, m.height = w, h; return m }
```

Add `"github.com/charmbracelet/bubbles/textinput"` to modal.go's imports.

- [ ] **Step 4: Gate + commit**

Run: `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`
Expected: PASS.

```bash
git add internal/tui/modal.go internal/tui/modal_test.go
git commit -m "Add typed confirmation modal for destructive operations"
```

---

## Task 4: Backup flow

**Files:**
- Create: `internal/tui/backup.go`
- Test: `internal/tui/backup_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/backup_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// newFlowRepo creates a real in-memory repo for flow tests.
func newFlowRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, err := repo.Init(context.Background(), blobstore.NewMemory(), []byte("flow-test-pass"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func typeInto(v BackupView, s string) BackupView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(BackupView)
	}
	return v
}

func TestBackupFlow_EnterEmitsStartOpWithTypedPath(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	v := NewBackupView(Deps{Repo: r})
	v = typeInto(v, src)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd == nil {
		t.Fatal("enter on a valid path must emit a command")
	}
	start, ok := cmd().(startOpMsg)
	if !ok {
		t.Fatalf("expected startOpMsg, got %T", cmd())
	}
	if start.name != "backup" {
		t.Fatalf("op name = %q", start.name)
	}
	if v.stage != backupRunning {
		t.Fatalf("stage = %v, want backupRunning", v.stage)
	}

	// Execute the op synchronously (the App would run it as a tea.Cmd).
	res := start.run(context.Background())
	done, ok := res.(backupDoneMsg)
	if !ok {
		t.Fatalf("expected backupDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("backup failed: %v", done.err)
	}
	if done.info.Stats.Files != 1 {
		t.Fatalf("files = %d, want 1", done.info.Stats.Files)
	}

	// Delivering the result moves the flow to the done stage and renders stats.
	m, _ = v.Update(res)
	v = m.(BackupView)
	if v.stage != backupDone {
		t.Fatalf("stage after result = %v, want backupDone", v.stage)
	}
	if out := v.View(); !strings.Contains(out, done.info.ID) {
		t.Errorf("result panel should show the snapshot ID:\n%s", out)
	}

	// The snapshot really exists in the store.
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots after flow = %v, %v", snaps, err)
	}
}

func TestBackupFlow_MissingPathRefusesToStart(t *testing.T) {
	v := NewBackupView(Deps{Repo: newFlowRepo(t)})
	v = typeInto(v, "/definitely/not/a/real/path")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	if cmd != nil {
		t.Fatal("nonexistent path must not start an op")
	}
	if v.stage != backupConfigure {
		t.Fatal("flow must stay in configure on invalid path")
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the path error:\n%s", v.View())
	}
}

func TestBackupFlow_EscDuringRunEmitsCancel(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600)
	v := NewBackupView(Deps{Repo: r})
	v = typeInto(v, src)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(BackupView)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc while running must emit a command")
	}
	if _, ok := cmd().(cancelOpMsg); !ok {
		t.Fatalf("expected cancelOpMsg, got %#v", cmd())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestBackupFlow -count=1`
Expected: FAIL — `undefined: BackupView`.

- [ ] **Step 3: Implement `internal/tui/backup.go`**

```go
package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// backupStage is the backup flow's state machine position.
type backupStage int

const (
	backupConfigure backupStage = iota
	backupRunning
	backupDone
)

// backupDoneMsg is the flow's terminal message; implementing
// opResultMsg clears the App guard.
type backupDoneMsg struct {
	info repo.SnapshotInfo
	err  error
}

func (backupDoneMsg) opResult() {}

// BackupView drives configure → running → done for a new snapshot.
// The repo call runs in the App-managed op goroutine; this view only
// renders progress (polled via opTick) and the result.
type BackupView struct {
	deps     Deps
	stage    backupStage
	path     textinput.Model
	tag      textinput.Model
	focusTag bool
	pathErr  string

	reporter *opReporter
	bar      progress.Model
	result   backupDoneMsg
	width    int
}

func NewBackupView(deps Deps) BackupView {
	path := textinput.New()
	path.Prompt = "path> "
	path.Placeholder = "directory to back up"
	path.Focus()
	tag := textinput.New()
	tag.Prompt = "tag>  "
	tag.Placeholder = "optional label"
	return BackupView{
		deps: deps,
		path: path,
		tag:  tag,
		bar:  progress.New(progress.WithDefaultGradient()),
	}
}

func (BackupView) Init() tea.Cmd { return nil }

func (v BackupView) Title() string { return "Backup" }

func (v BackupView) ShortHelp() []key.Binding {
	switch v.stage {
	case backupRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case backupDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "start")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		}
	}
}

func (v BackupView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.bar.Width = min(msg.Width-8, 60)
		return v, nil

	case backupDoneMsg:
		v.stage = backupDone
		v.result = msg
		return v, nil

	case opTickMsg:
		if v.stage == backupRunning {
			return v, opTick() // keep ticking while running
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v BackupView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case backupRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	case backupDone:
		if msg.Type == tea.KeyEnter {
			fresh := NewBackupView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil

	default: // backupConfigure
		switch msg.Type {
		case tea.KeyTab:
			v.focusTag = !v.focusTag
			if v.focusTag {
				v.path.Blur()
				v.tag.Focus()
			} else {
				v.tag.Blur()
				v.path.Focus()
			}
			return v, nil
		case tea.KeyEnter:
			return v.startBackup()
		}
		var cmd tea.Cmd
		if v.focusTag {
			v.tag, cmd = v.tag.Update(msg)
		} else {
			v.path, cmd = v.path.Update(msg)
			v.pathErr = "" // typing clears the last validation error
		}
		return v, cmd
	}
}

// startBackup validates the path and emits startOpMsg. Validation is
// deliberately cheap (stat only) — the walker surfaces everything else.
func (v BackupView) startBackup() (tea.Model, tea.Cmd) {
	root := strings.TrimSpace(v.path.Value())
	if root == "" {
		v.pathErr = "path is required"
		return v, nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		v.pathErr = fmt.Sprintf("directory not found: %s", root)
		return v, nil
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return v, nil
	}

	v.reporter = newOpReporter()
	v.stage = backupRunning
	r := v.deps.Repo
	reporter := v.reporter
	tag := strings.TrimSpace(v.tag.Value())
	var wopts walker.Options
	if v.deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    v.deps.Config.Backup.IgnoreFile,
			ExcludeCaches: v.deps.Config.Backup.ExcludeCaches,
		}
	}
	start := startOpMsg{
		name: "backup",
		run: func(ctx context.Context) tea.Msg {
			info, err := r.CreateSnapshot(ctx, root, repo.SnapshotOptions{
				Tag:      tag,
				Progress: reporter,
				Walker:   wopts,
			})
			return backupDoneMsg{info: info, err: err}
		},
	}
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v BackupView) View() string {
	var b strings.Builder
	switch v.stage {
	case backupRunning:
		total, done := v.reporter.Snapshot()
		b.WriteString(ui.Primary.Render("Backing up…"))
		b.WriteString("\n\n")
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(v.bar.ViewAs(pct))
		b.WriteString(fmt.Sprintf("\n\n%s / %s uploaded",
			ui.FormatBytes(done), ui.FormatBytes(total)))
		b.WriteString("\n" + ui.Muted.Render("esc cancel"))

	case backupDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Backup failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Backup complete"))
			info := v.result.info
			b.WriteString(fmt.Sprintf("\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  new       %s",
				info.ID, info.Stats.Files,
				ui.FormatBytes(info.Stats.Bytes), ui.FormatBytes(info.Stats.NewBytes)))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ run another backup"))

	default:
		b.WriteString(ui.Primary.Render("New backup"))
		b.WriteString("\n\n" + v.path.View())
		b.WriteString("\n" + v.tag.View())
		if v.pathErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.pathErr))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ start · tab switch field"))
	}
	return b.String()
}
```

Add `"context"` to the imports. NOTE: `ui.FormatBytes` exists (the CLI uses it — verify with `go doc ./internal/ui FormatBytes`; if it lives elsewhere adjust the call, do not reimplement). `SnapshotStats` field names: verify `Files`, `Bytes`, `NewBytes` against `internal/repo/snapshot.go` and adjust if they differ.

- [ ] **Step 4: Run tests until green**

Run: `go test -race -count=1 ./internal/tui/ -run TestBackupFlow`
Expected: PASS.

- [ ] **Step 5: Gate + commit**

Run: `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`

```bash
git add internal/tui/backup.go internal/tui/backup_test.go
git commit -m "Add backup flow view: configure, live progress, result panel"
```

---

## Task 5: Restore flow

**Files:**
- Create: `internal/tui/restore.go`
- Test: `internal/tui/restore_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/restore_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// seedSnapshot backs up a one-file directory and returns the snapshot ID
// plus the original file's content for byte-compare after restore.
func seedSnapshot(t *testing.T, r *repoHandle) (string, string) {
	t.Helper()
	src := t.TempDir()
	content := "restore-me-" + t.Name()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := r.CreateSnapshot(context.Background(), src, repoSnapshotOptions{})
	if err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	return info.ID, content
}

func TestRestoreFlow_FullPath(t *testing.T) {
	r := newFlowRepo(t)
	snapID, content := seedSnapshotReal(t, r)

	v := NewRestoreView(Deps{Repo: r})
	// The view loads snapshots on Init (synchronous Phase 1-style hydrate).
	if len(v.snaps) != 1 {
		t.Fatalf("snaps loaded = %d, want 1", len(v.snaps))
	}

	// Stage 1: pick the snapshot.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreDest {
		t.Fatalf("stage = %v, want restoreDest", v.stage)
	}

	// Stage 2: type an empty destination dir.
	dest := filepath.Join(t.TempDir(), "out")
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage != restoreConfirm {
		t.Fatalf("stage = %v, want restoreConfirm (plan preview)", v.stage)
	}
	if !strings.Contains(v.View(), "1 file") && !strings.Contains(v.View(), "files") {
		t.Errorf("plan preview should show file count:\n%s", v.View())
	}

	// Stage 3: confirm starts the op.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if cmd == nil {
		t.Fatal("confirm must emit startOpMsg")
	}
	start, ok := cmd().(startOpMsg)
	if !ok || start.name != "restore" {
		t.Fatalf("got %#v, want startOpMsg{restore}", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(restoreDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("restore result: %#v", res)
	}

	// Bytes actually landed.
	got, err := os.ReadFile(filepath.Join(dest, "f.txt"))
	if err != nil || string(got) != content {
		t.Fatalf("restored content = %q (%v), want %q", got, err, content)
	}

	m, _ = v.Update(res)
	v = m.(RestoreView)
	if v.stage != restoreDone {
		t.Fatalf("stage after result = %v", v.stage)
	}
	_ = snapID
}

func TestRestoreFlow_NonEmptyDestSurfacedBeforeStart(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	v := NewRestoreView(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // pick
	v = m.(RestoreView)

	dest := t.TempDir() // non-empty? make it so:
	os.WriteFile(filepath.Join(dest, "existing.txt"), []byte("x"), 0o600)
	for _, r := range dest {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(RestoreView)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RestoreView)
	if v.stage == restoreConfirm && cmd != nil {
		t.Fatal("non-empty destination must not reach a startable confirm")
	}
	if !strings.Contains(strings.ToLower(v.View()), "empty") {
		t.Errorf("view should explain the non-empty destination:\n%s", v.View())
	}
}
```

NOTE to implementer: `seedSnapshot`/`repoHandle`/`repoSnapshotOptions` in the first sketch are placeholders from drafting — implement ONE helper `seedSnapshotReal(t, r *repo.Repo) (string, string)` in `restore_test.go` using `r.CreateSnapshot(ctx, src, repo.SnapshotOptions{})` exactly as `backup_test.go` does, and delete the unused first sketch. The behavioral contracts (stage transitions, non-empty-dest surfacing, byte-exact restore) are the requirement.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestRestoreFlow -count=1`
Expected: FAIL — `undefined: NewRestoreView`.

- [ ] **Step 3: Implement `internal/tui/restore.go`**

```go
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type restoreStage int

const (
	restorePick restoreStage = iota
	restoreDest
	restoreConfirm
	restoreRunning
	restoreDone
)

type restoreDoneMsg struct {
	verification *repo.RestoreVerification // nil when verify was off
	err          error
}

func (restoreDoneMsg) opResult() {}

// RestoreView drives pick → dest → plan/confirm → running → done.
// PlanRestore runs synchronously at the dest step (it is a metadata
// read, cheap); the actual Restore runs through the App op guard.
type RestoreView struct {
	deps  Deps
	stage restoreStage

	snaps []repo.SnapshotInfo
	tbl   table.Model

	dest    textinput.Model
	destErr string

	plan    repo.RestorePlan
	verify  bool
	snapID  string

	reporter *opReporter
	bar      progress.Model
	result   restoreDoneMsg
	width    int
}

func NewRestoreView(deps Deps) RestoreView {
	v := RestoreView{
		deps: deps,
		bar:  progress.New(progress.WithDefaultGradient()),
	}
	ti := textinput.New()
	ti.Prompt = "dest> "
	ti.Placeholder = "empty or new directory"
	v.dest = ti

	// Synchronous hydrate, Phase 1 style (async loading arrives with a
	// later phase). Nil repo renders a placeholder.
	if deps.Repo != nil {
		ctx, cancel := context.WithTimeout(depsCtx(deps), hydrateTimeout)
		defer cancel()
		if snaps, err := deps.Repo.ListSnapshots(ctx); err == nil {
			v.snaps = snaps
		}
	}
	cols := []table.Column{
		{Title: "ID", Width: 34},
		{Title: "Created", Width: 17},
		{Title: "Tag", Width: 10},
		{Title: "Files", Width: 7},
	}
	rows := make([]table.Row, len(v.snaps))
	for i, s := range v.snaps {
		rows[i] = table.Row{s.ID, s.CreatedAt.UTC().Format("2006-01-02 15:04"), s.Tag,
			fmt.Sprintf("%d", s.Stats.Files)}
	}
	v.tbl = table.New(table.WithColumns(cols), table.WithRows(rows), table.WithFocused(true))
	return v
}

func (RestoreView) Init() tea.Cmd { return nil }

func (v RestoreView) Title() string { return "Restore" }

func (v RestoreView) ShortHelp() []key.Binding {
	switch v.stage {
	case restorePick:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "snapshot")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "choose")),
		}
	case restoreDest:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "plan"))}
	case restoreConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "restore")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle verify")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case restoreRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	}
}

func (v RestoreView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.bar.Width = min(msg.Width-8, 60)
		v.tbl.SetHeight(max(msg.Height-8, 3))
		return v, nil
	case restoreDoneMsg:
		v.stage = restoreDone
		v.result = msg
		return v, nil
	case opTickMsg:
		if v.stage == restoreRunning {
			return v, opTick()
		}
		return v, nil
	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v RestoreView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case restorePick:
		if msg.Type == tea.KeyEnter && len(v.snaps) > 0 {
			v.snapID = v.snaps[v.tbl.Cursor()].ID
			v.stage = restoreDest
			v.dest.Focus()
			return v, nil
		}
		var cmd tea.Cmd
		v.tbl, cmd = v.tbl.Update(msg)
		return v, cmd

	case restoreDest:
		switch msg.Type {
		case tea.KeyEsc:
			v.stage = restorePick
			return v, nil
		case tea.KeyEnter:
			return v.planIt()
		}
		var cmd tea.Cmd
		v.dest, cmd = v.dest.Update(msg)
		v.destErr = ""
		return v, cmd

	case restoreConfirm:
		switch {
		case msg.Type == tea.KeyEsc:
			v.stage = restoreDest
			return v, nil
		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'v':
			v.verify = !v.verify
			return v, nil
		case msg.Type == tea.KeyEnter:
			return v.startRestore()
		}
		return v, nil

	case restoreRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	default: // restoreDone
		if msg.Type == tea.KeyEnter {
			fresh := NewRestoreView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil
	}
}

// planIt validates the destination via PlanRestore (which enforces the
// empty-or-absent rule) and advances to the confirm stage on success.
func (v RestoreView) planIt() (tea.Model, tea.Cmd) {
	dest := strings.TrimSpace(v.dest.Value())
	if dest == "" {
		v.destErr = "destination is required"
		return v, nil
	}
	ctx, cancel := context.WithTimeout(depsCtx(v.deps), hydrateTimeout)
	defer cancel()
	plan, err := v.deps.Repo.PlanRestore(ctx, v.snapID, dest)
	if err != nil {
		v.destErr = err.Error()
		return v, nil
	}
	if plan.DestExists && !plan.DestEmpty {
		v.destErr = "destination is not empty — restore requires an empty or new directory"
		return v, nil
	}
	v.plan = plan
	v.stage = restoreConfirm
	return v, nil
}

func (v RestoreView) startRestore() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.stage = restoreRunning
	r := v.deps.Repo
	reporter := v.reporter
	snapID, dest, doVerify := v.snapID, v.plan.DestDir, v.verify
	start := startOpMsg{
		name: "restore",
		run: func(ctx context.Context) tea.Msg {
			if err := r.Restore(ctx, snapID, dest, repo.RestoreOptions{Progress: reporter}); err != nil {
				return restoreDoneMsg{err: err}
			}
			if doVerify {
				rep, err := r.VerifyRestore(ctx, snapID, dest)
				if err != nil {
					return restoreDoneMsg{err: err}
				}
				return restoreDoneMsg{verification: &rep}
			}
			return restoreDoneMsg{}
		},
	}
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v RestoreView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	var b strings.Builder
	switch v.stage {
	case restorePick:
		b.WriteString(ui.Primary.Render("Restore: choose a snapshot"))
		b.WriteString("\n\n" + v.tbl.View())
	case restoreDest:
		b.WriteString(ui.Primary.Render("Restore " + v.snapID))
		b.WriteString("\n\n" + v.dest.View())
		if v.destErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.destErr))
		}
	case restoreConfirm:
		b.WriteString(ui.Primary.Render("Ready to restore"))
		b.WriteString(fmt.Sprintf("\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  dest      %s",
			v.plan.SnapshotID, v.plan.Files, ui.FormatBytes(v.plan.Bytes), v.plan.DestDir))
		mark := "off"
		if v.verify {
			mark = "on"
		}
		b.WriteString("\n  verify    " + mark)
		b.WriteString("\n\n" + ui.Muted.Render("⏎ restore · v toggle verify · esc back"))
	case restoreRunning:
		total, done := v.reporter.Snapshot()
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(ui.Primary.Render("Restoring…"))
		b.WriteString("\n\n" + v.bar.ViewAs(pct))
		b.WriteString(fmt.Sprintf("\n\n%s / %s", ui.FormatBytes(done), ui.FormatBytes(total)))
	default:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Restore failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Restore complete"))
			if v.result.verification != nil {
				if v.result.verification.OK() {
					b.WriteString("\n\nverification: " + ui.Success.Render("all files match"))
				} else {
					b.WriteString(fmt.Sprintf("\n\nverification: %s (%d mismatches)",
						ui.Danger.Render("FAILED"), len(v.result.verification.Mismatches)))
				}
			}
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ restore another"))
	}
	return b.String()
}
```

Shared helpers this file references — add to `internal/tui/ops.go`:

```go
// hydrateTimeout bounds the synchronous metadata reads flows do at
// construction / stage transitions (list snapshots, plan restore).
const hydrateTimeout = 20 * time.Second

// depsCtx returns the deps context or Background for tests with Deps{}.
func depsCtx(d Deps) context.Context {
	if d.Ctx != nil {
		return d.Ctx
	}
	return context.Background()
}
```

Verify `repo.RestorePlan` field names (`SnapshotID`, `DestDir`, `DestExists`, `DestEmpty`, `Files`, `Bytes`) against `internal/repo/restore_verify.go` — adjust to actual names.

- [ ] **Step 4: Run tests until green**

Run: `go test -race -count=1 ./internal/tui/ -run TestRestoreFlow`
Expected: PASS.

- [ ] **Step 5: Gate + commit**

```bash
git add internal/tui/restore.go internal/tui/restore_test.go internal/tui/ops.go
git commit -m "Add restore flow view: snapshot picker, plan preview, verify toggle"
```

---

## Task 6: Prune flow (typed confirmation)

**Files:**
- Create: `internal/tui/prune.go`
- Test: `internal/tui/prune_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/prune_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// seedTwoSnapshots creates two snapshots with distinct content so a
// KeepLast=1 policy drops exactly one.
func seedTwoSnapshots(t *testing.T, r *repo.Repo) {
	t.Helper()
	for _, name := range []string{"one", "two"} {
		src := t.TempDir()
		if err := os.WriteFile(filepath.Join(src, name+".txt"),
			[]byte(strings.Repeat(name, 200)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
}

func pruneDeps(r *repo.Repo) Deps {
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 1
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	return Deps{Repo: r, Config: &cfg}
}

func TestPruneFlow_PreviewShowsKeepAndDropWithReasons(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))
	out := v.View()
	if !strings.Contains(out, "keep") || !strings.Contains(out, "drop") {
		t.Errorf("preview must show keep/drop decisions:\n%s", out)
	}
}

// TestPruneFlow_NoDeletionWithoutTypedConfirm is THE confirmation-gate
// test: starting the flow and pressing enter must NOT delete anything —
// only the typed-confirm path may. This is the spec's core safety rule.
func TestPruneFlow_NoDeletionWithoutTypedConfirm(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))

	// Enter requests the typed-confirm modal (a pushModalMsg), nothing else.
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should request the confirm modal")
	}
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 2 {
		t.Fatalf("snapshots deleted without confirmation: %d left", len(snaps))
	}
}

func TestPruneFlow_ConfirmedRunDeletesAndGCs(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))

	// Simulate the App delivering the typed-confirm result.
	m, cmd := v.Update(confirmedMsg{id: pruneConfirmID})
	v = m.(PruneView)
	if cmd == nil {
		t.Fatal("confirmation must start the op")
	}
	start, ok := cmd().(startOpMsg)
	if !ok || start.name != "prune" {
		t.Fatalf("got %#v, want startOpMsg{prune}", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("prune result: %#v", res)
	}
	if done.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", done.deleted)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("snapshots after prune = %d, want 1", len(snaps))
	}
	m, _ = v.Update(res)
	if m.(PruneView).stage != pruneDone {
		t.Fatal("flow must land in done stage")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestPruneFlow -count=1`
Expected: FAIL — `undefined: NewPruneView` (and `pushModalMsg`).

- [ ] **Step 3: Add `pushModalMsg` plumbing**

Flows can't append to `App.modals` directly. Add to `internal/tui/modal.go`:

```go
// pushModalMsg asks the App to push a modal onto the stack. Flows emit
// it (e.g. prune's typed confirm) so modal ownership stays with the
// shell — the single place that routes keys modal-first.
type pushModalMsg struct{ modal Modal }
```

Handle it in `App.Update` (next to dismissModalMsg):

```go
	case pushModalMsg:
		m.modals = append(m.modals, msg.modal.SetSize(m.width, m.height))
		return m, nil
```

- [ ] **Step 4: Implement `internal/tui/prune.go`**

```go
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type pruneStage int

const (
	prunePreview pruneStage = iota
	pruneRunning
	pruneDone
)

// pruneConfirmID ties the typed-confirm modal back to this flow.
const pruneConfirmID = "prune-apply"

type pruneDoneMsg struct {
	deleted int
	stats   repo.GCStats
	err     error
}

func (pruneDoneMsg) opResult() {}

// PruneView shows the retention preview and, after a TYPED confirmation
// ("prune"), deletes the dropped snapshots and runs GC. Mirrors the CLI
// prune --apply sequence exactly: DeleteSnapshot per drop, then GC with
// the keep-set (GC's live set is derived from the store under its lock;
// keepIDs only marks the deliberate-prune path).
type PruneView struct {
	deps      Deps
	stage     pruneStage
	decisions []repo.RetentionDecision
	keep      []string
	drop      []string
	loadErr   string

	result pruneDoneMsg
	width  int
}

func NewPruneView(deps Deps) PruneView {
	v := PruneView{deps: deps}
	if deps.Repo == nil {
		v.loadErr = "no repository configured"
		return v
	}
	ctx, cancel := context.WithTimeout(depsCtx(deps), hydrateTimeout)
	defer cancel()
	snaps, err := deps.Repo.ListSnapshots(ctx)
	if err != nil {
		v.loadErr = err.Error()
		return v
	}
	policy := repo.RetentionPolicy{}
	if deps.Config != nil {
		policy = repo.RetentionPolicy{
			KeepLast:    deps.Config.Retention.KeepLast,
			KeepDaily:   deps.Config.Retention.KeepDaily,
			KeepWeekly:  deps.Config.Retention.KeepWeekly,
			KeepMonthly: deps.Config.Retention.KeepMonthly,
		}
	}
	v.decisions = repo.PlanRetentionExplain(snaps, policy)
	for _, d := range v.decisions {
		if d.Keep {
			v.keep = append(v.keep, d.Snapshot.ID)
		} else {
			v.drop = append(v.drop, d.Snapshot.ID)
		}
	}
	return v
}

func (PruneView) Init() tea.Cmd { return nil }

func (v PruneView) Title() string { return "Prune" }

func (v PruneView) ShortHelp() []key.Binding {
	if v.stage == prunePreview && len(v.drop) > 0 {
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "prune…"))}
	}
	return nil
}

func (v PruneView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case pruneDoneMsg:
		v.stage = pruneDone
		v.result = msg
		return v, nil

	case confirmedMsg:
		if msg.id != pruneConfirmID || v.stage != prunePreview {
			return v, nil
		}
		return v.startPrune()

	case tea.KeyMsg:
		if v.stage == prunePreview && msg.Type == tea.KeyEnter && len(v.drop) > 0 {
			body := fmt.Sprintf("This deletes %d snapshot(s) and reclaims their unique chunks.\nChunks still referenced by kept snapshots are never touched.", len(v.drop))
			modal := NewTypedConfirmModal("Confirm prune", body, "prune", pruneConfirmID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		}
		if v.stage == pruneDone && msg.Type == tea.KeyEnter {
			fresh := NewPruneView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil
	}
	return v, nil
}

func (v PruneView) startPrune() (tea.Model, tea.Cmd) {
	v.stage = pruneRunning
	r := v.deps.Repo
	drop := append([]string(nil), v.drop...)
	keep := append([]string(nil), v.keep...)
	start := startOpMsg{
		name: "prune",
		run: func(ctx context.Context) tea.Msg {
			deleted := 0
			for _, id := range drop {
				if err := r.DeleteSnapshot(ctx, id); err != nil {
					return pruneDoneMsg{deleted: deleted, err: err}
				}
				deleted++
			}
			keepIDs := make(map[string]bool, len(keep))
			for _, id := range keep {
				keepIDs[id] = true
			}
			stats, err := r.GC(ctx, keepIDs)
			return pruneDoneMsg{deleted: deleted, stats: stats, err: err}
		},
	}
	return v, func() tea.Msg { return start }
}

func (v PruneView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	var b strings.Builder
	switch v.stage {
	case pruneRunning:
		b.WriteString(ui.Primary.Render("Pruning…"))
	case pruneDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Prune failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Prune complete"))
			b.WriteString(fmt.Sprintf("\n\n  deleted snapshots  %d\n  reclaimed blobs    %d\n  reclaimed bytes    %s\n  live blobs         %d",
				v.result.deleted, v.result.stats.DeletedBlobs,
				ui.FormatBytes(v.result.stats.DeletedBytes), v.result.stats.LiveBlobs))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ recompute"))
	default:
		b.WriteString(ui.Primary.Render("Retention preview"))
		b.WriteString(fmt.Sprintf("  %s\n\n", ui.Muted.Render(
			fmt.Sprintf("keep %d · drop %d", len(v.keep), len(v.drop)))))
		for _, d := range v.decisions {
			verdict := ui.Success.Render("keep")
			reason := strings.Join(d.Reasons, ", ")
			if !d.Keep {
				verdict = ui.Danger.Render("drop")
				if reason == "" {
					reason = "not selected by retention policy"
				}
			}
			b.WriteString(fmt.Sprintf("  %s  %s  %s\n", d.Snapshot.ID, verdict, ui.Muted.Render(reason)))
		}
		if len(v.drop) > 0 {
			b.WriteString("\n" + ui.Muted.Render("⏎ prune (typed confirmation required)"))
		} else {
			b.WriteString("\n" + ui.Muted.Render("nothing to prune"))
		}
	}
	return b.String()
}
```

- [ ] **Step 5: Run tests + gate + commit**

Run: `go test -race -count=1 ./internal/tui/ -run TestPruneFlow` then the full task gate.

```bash
git add internal/tui/prune.go internal/tui/prune_test.go internal/tui/modal.go internal/tui/app.go
git commit -m "Add prune flow: retention preview, typed confirmation, delete + GC"
```

---

## Task 7: Register the flows + end-to-end guard test

**Files:**
- Modify: `internal/tui/app.go` (view registration)
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd: the three
// flows appear in sidebar+palette (registry-driven), and starting a
// backup through the real App shows it in the status bar.
func TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd(t *testing.T) {
	app := newTestApp(t)
	out := app.View()
	for _, want := range []string{"Backup", "Restore", "Prune"} {
		if !strings.Contains(out, want) {
			t.Errorf("sidebar missing operation %q", want)
		}
	}
	if got := len(app.views); got != 8 {
		t.Fatalf("views = %d, want 8 (5 + 3 operations)", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run TestApp_OperationsRegistered -count=1`
Expected: FAIL — views = 5.

- [ ] **Step 3: Register in `NewApp`**

Extend the `views` slice in `NewApp` (after the 5 existing entries):

```go
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
```

And give operations their own category when registering: change the registration loop to use a per-view category map:

```go
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
	}
	for _, v := range views {
		title := v.id
		if t, ok := v.model.(interface{ Title() string }); ok {
			title = t.Title()
		}
		cat := categories[v.id]
		if cat == "" {
			cat = "Views"
		}
		registry.Add(Command{ID: v.id, Title: title, Category: cat})
	}
```

- [ ] **Step 4: Gate + commit**

Run: `go build ./... && go test -race -count=1 ./internal/tui/ ./internal/cli/ ./cmd/... && gofmt -l cmd internal && go vet ./...`
Expected: PASS — including every Phase 1 shell test (8 views now; check none asserted the count 5 — `TestApp_NumberKeyJumpsToView` uses '4' which still maps to agent, fine).

```bash
git add internal/tui
git commit -m "Register backup/restore/prune flows in the TUI shell"
```

---

## Task 8: Full gate

- [ ] **Step 1: Complete CI-equivalent gate**

```bash
go build ./... \
 && go vet ./... \
 && gofmt -l cmd internal \
 && go test -race -count=1 ./... \
 && go test ./third_party/fastcdc-go/... \
 && go mod tidy -diff \
 && golangci-lint run ./...
```
Expected: all green; golangci-lint "0 issues" (everything interim-unused is now consumed).

- [ ] **Step 2: Commit any final fixes; do NOT merge**

Leave the branch for review + the user's interactive smoke test (flows need a real terminal to feel right).

---

## Self-review notes (author)

- **Spec coverage (Phase 2a slice):** op framework (guard, cancel, progress, status-bar indicator) → Task 2; typed confirm → Task 3; Backup/Restore/Prune flows with the spec's stage patterns → Tasks 4–6; registry/sidebar integration → Task 7; gate → Task 8. Deferred to 2b: the other nine flows, stopwatch/paginator/filepicker/spinner adoption where flows grow them, harmonica-animated bars (bubbles progress gradient used now).
- **Known simplifications vs the spec (deliberate, called out):** path entry uses `textinput` in 2a; the `filepicker` component lands in 2b when restore/backup grow directory browsing — the spec's component inventory is satisfied across Phase 2 as a whole. Restore confirm is a plain stage (not a modal) because the plan preview IS the confirmation surface; prune uses the typed modal as specced. The spec's "cancel a running op behind a confirm" is relaxed in 2a to direct esc-cancel (cancel is safe by construction: interrupted backup/restore leave no partial state thanks to temp+rename and content addressing); 2b wraps cancel in a ConfirmModal when the op set grows to include sync/schedule where interruption is more disruptive.
- **Type consistency:** `startOpMsg{name, run}` (Tasks 2/4/5/6), `opResultMsg` implementers `backupDoneMsg`/`restoreDoneMsg`/`pruneDoneMsg` (+ test-only `fakeDoneMsg`), `pushModalMsg` (Tasks 6/2's App changes), `depsCtx`/`hydrateTimeout` defined in Task 5's ops.go addition and used by Tasks 5–6.
- **API-verification checkpoints flagged inline:** `ui.FormatBytes` location, `SnapshotStats` field names, `RestorePlan` field names, bubbles `table`/`progress` v1 API. The restore test file's first sketch contains two placeholder helpers the implementer must consolidate (explicitly noted in Task 5 Step 1).
- **`max`:** Go builtin (1.21+), fine alongside `min`.
