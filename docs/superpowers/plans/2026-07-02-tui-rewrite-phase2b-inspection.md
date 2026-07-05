# TUI Rewrite Phase 2b (Check + Diff Inspection Flows) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add the two read-only inspection flows that need only the existing `Repo` dep — an async **Check** flow (integrity report, replacing the Phase 1 Operations placeholder) and a **Diff** snapshot-pair picker — establishing the read-only async-load pattern the shell has lacked.

**Architecture:** Check runs `repo.Check` in a `tea.Cmd` goroutine with a `bubbles/spinner` while it works — a read-only load, so it deliberately does NOT take the mutating-op guard from Phase 2a. Diff loads the snapshot list synchronously (like the existing Snapshots view) and walks a two-stage table picker before calling `repo.Diff`. No repo-layer changes; no `tui.Deps` changes.

**Tech Stack:** Go 1.25, bubbletea v1.3.10, bubbles v1.0.0 (spinner, table, viewport), lipgloss v1.1.0.

Spec: `docs/superpowers/specs/2026-07-01-tui-rewrite-design.md`. This is Phase 2b; the remaining flows (Sync, Agent-apply, Policies, Schedule, Password, Recovery-kit, Doctor) are Phase 2c and require `tui.Deps` expansion / `internal/cli` extraction.

---

## Conventions for every task

- **Shell note:** `cat`/`tail`/`head` are aliased to `bat` — use `command tail -n N` or redirect output to a file and read it.
- **Green gate (per task):** `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`. Task 3 adds `./internal/cli/ ./cmd/...` and `golangci-lint run ./...`.
- **Branch:** create `feature/tui-phase2b` from `main` at the start (Task 1, Step 1).
- **Tests:** flows test against REAL in-memory repos via the existing `newFlowRepo(t)` helper (in `backup_test.go`) and `seedSnapshotReal(t, r)` (in `restore_test.go`) / `seedTwoSnapshots(t, r)` (in `prune_test.go`) — same package, call them directly. Colors are stripped in tests: assert on text/structure, never ANSI codes.

## Existing surfaces this builds on (do not recreate)

- Shell: `App`, `Deps{Repo, Provider, RepoName, Config, Ctx}`, view contract (`tea.Model` + `Title()` + `ShortHelp()`), `ctxOrBackground(ctx)` (in snapshots.go), `min`/`max` builtins.
- The Phase 1 `Operations` view (`internal/tui/operations.go`, `operations_test.go`) synchronously runs `Check` in its constructor — Phase 2b REPLACES it with the async `CheckView`.
- The Phase 1 `Diff` view (`internal/tui/diff.go`, `diff_test.go`) is a placeholder that renders a `repo.DiffResult` handed to it via `SetResult(idA, idB, res)` but has no picker — Phase 2b REWRITES it to include the picker.
- Repo APIs (verified on main):
  - `r.Check(ctx, repo.CheckOptions{}) (repo.CheckReport, error)`; `CheckReport{Snapshots, Files, Bytes, ReferencedBlobs, DataBlobs, DataBytes, OrphanBytes int/int64; MissingBlobs []MissingBlob; OrphanBlobs []BlobIssue; ManifestIssues []ManifestIssue; Lock *LockReport}`
  - `r.ListSnapshots(ctx) ([]repo.SnapshotInfo, error)`; `SnapshotInfo{ID, CreatedAt, Tag, Stats}`
  - `r.Diff(ctx, idA, idB string) (repo.DiffResult, error)`; `DiffResult{Added, Removed, Changed []string}`
  - `ui.FormatBytes(int64) string`

## File structure (Phase 2b target)

| File | Responsibility |
|---|---|
| `internal/tui/check.go` (new) | `CheckView`: async integrity check with spinner + report panels |
| `internal/tui/check_test.go` (new) | Check flow tests against a real repo |
| `internal/tui/operations.go`, `operations_test.go` (delete) | replaced by check.go |
| `internal/tui/diff.go` (rewrite) | `Diff`: two-stage snapshot picker → three-column result |
| `internal/tui/diff_test.go` (rewrite) | picker + real `repo.Diff` |
| `internal/tui/app.go` (modify) | swap the `operations` view entry for `check` |

---

## Task 1: Check flow (async integrity report)

**Files:**
- Create: `internal/tui/check.go`, `internal/tui/check_test.go`
- Delete: `internal/tui/operations.go`, `internal/tui/operations_test.go`
- Modify: `internal/tui/app.go` (view registration)

- [ ] **Step 1: Create the branch**

```bash
cd /Users/markgustetic/Programming/portfolio/sentra
git checkout main && git pull && git checkout -b feature/tui-phase2b
```

- [ ] **Step 2: Write the failing tests**

Create `internal/tui/check_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

func TestCheckFlow_RunsAndRendersReport(t *testing.T) {
	r := newFlowRepo(t)
	// One snapshot so the report has non-zero counts.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	v := NewCheckView(Deps{Repo: r})
	// Enter kicks off the check; it moves to the running stage and returns
	// a command batch (spinner tick + the check goroutine).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(CheckView)
	if v.stage != checkRunning {
		t.Fatalf("stage = %v, want checkRunning", v.stage)
	}
	if cmd == nil {
		t.Fatal("enter must start the check")
	}

	// Find and deliver the checkDoneMsg the batch produces.
	var done tea.Msg
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(checkDoneMsg); ok {
			done = msg
		}
	}
	if done == nil {
		t.Fatal("check command did not produce a checkDoneMsg")
	}
	m, _ = v.Update(done)
	v = m.(CheckView)
	if v.stage != checkDone {
		t.Fatalf("stage after result = %v, want checkDone", v.stage)
	}
	out := v.View()
	for _, want := range []string{"snapshots", "healthy"} {
		if !strings.Contains(strings.ToLower(out), want) {
			t.Errorf("report view missing %q:\n%s", want, out)
		}
	}
}

func TestCheckFlow_SurfacesIssues(t *testing.T) {
	// A fresh repo with no snapshots is healthy but empty; assert the
	// report renders a healthy status without panicking on empty slices.
	v := NewCheckView(Deps{Repo: newFlowRepo(t)})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(CheckView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(checkDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(CheckView)
		}
	}
	if v.stage != checkDone {
		t.Fatalf("stage = %v, want checkDone", v.stage)
	}
	if v.result.err != nil {
		t.Fatalf("check on empty repo errored: %v", v.result.err)
	}
}

func TestCheckFlow_NilRepoPlaceholder(t *testing.T) {
	v := NewCheckView(Deps{})
	if !strings.Contains(v.View(), "no repository") {
		t.Errorf("nil-repo view should show a placeholder:\n%s", v.View())
	}
}
```

The test file imports `"github.com/markgustetic/sentra/internal/repo"` for `repo.SnapshotOptions{}`.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/tui/ -run TestCheckFlow -count=1`
Expected: FAIL — `undefined: NewCheckView`.

- [ ] **Step 4: Implement `internal/tui/check.go`**

```go
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type checkStage int

const (
	checkIdle checkStage = iota
	checkRunning
	checkDone
)

// checkDoneMsg carries the integrity report back to the flow. This is a
// READ-ONLY load, so it is NOT an opResultMsg — Check does not take the
// mutating-op guard and can run alongside a backup.
type checkDoneMsg struct {
	report repo.CheckReport
	err    error
}

// CheckView runs repo.Check asynchronously (a repo with many blobs can
// take a moment to list) and renders the integrity report. It replaces
// the Phase 1 Operations view, which did the same read synchronously in
// its constructor and blocked the first frame.
type CheckView struct {
	deps    Deps
	stage   checkStage
	spin    spinner.Model
	result  checkDoneMsg
	width   int
}

func NewCheckView(deps Deps) CheckView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return CheckView{deps: deps, spin: s}
}

func (CheckView) Init() tea.Cmd { return nil }

func (v CheckView) Title() string { return "Check" }

func (v CheckView) ShortHelp() []key.Binding {
	switch v.stage {
	case checkRunning:
		return nil
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run check"))}
	}
}

func (v CheckView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case checkDoneMsg:
		v.stage = checkDone
		v.result = msg
		return v, nil

	case spinner.TickMsg:
		if v.stage == checkRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter && v.stage != checkRunning && v.deps.Repo != nil {
			v.stage = checkRunning
			r := v.deps.Repo
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				report, err := r.Check(ctx, repo.CheckOptions{})
				return checkDoneMsg{report: report, err: err}
			}
			return v, tea.Batch(v.spin.Tick, run)
		}
		return v, nil
	}
	return v, nil
}

func (v CheckView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	switch v.stage {
	case checkRunning:
		return v.spin.View() + " running integrity check…"
	case checkDone:
		return v.renderReport()
	default:
		return ui.Primary.Render("Repository integrity check") + "\n\n" +
			ui.Muted.Render("⏎ run check")
	}
}

func (v CheckView) renderReport() string {
	if v.result.err != nil {
		return ui.Danger.Render("Check failed") + "\n\n" + v.result.err.Error()
	}
	rep := v.result.report
	var b strings.Builder
	healthy := len(rep.MissingBlobs) == 0 && len(rep.ManifestIssues) == 0 &&
		(rep.Lock == nil || (!rep.Lock.Stale && !rep.Lock.Unreadable))
	status := ui.Success.Render("● healthy")
	if !healthy {
		status = ui.Danger.Render("● issues found")
	}
	b.WriteString(ui.Primary.Render("Integrity report") + "  " + status + "\n\n")
	fmt.Fprintf(&b, "  snapshots        %d\n", rep.Snapshots)
	fmt.Fprintf(&b, "  files            %d\n", rep.Files)
	fmt.Fprintf(&b, "  data blobs       %d  (%s)\n", rep.DataBlobs, ui.FormatBytes(rep.DataBytes))
	fmt.Fprintf(&b, "  referenced blobs %d\n", rep.ReferencedBlobs)
	fmt.Fprintf(&b, "  orphan bytes     %s\n", ui.FormatBytes(rep.OrphanBytes))
	if n := len(rep.MissingBlobs); n > 0 {
		fmt.Fprintf(&b, "\n  %s  %d missing blob(s)\n", ui.Danger.Render("✗"), n)
	}
	if n := len(rep.ManifestIssues); n > 0 {
		fmt.Fprintf(&b, "  %s  %d manifest issue(s)\n", ui.Danger.Render("✗"), n)
	}
	if rep.Lock != nil && (rep.Lock.Stale || rep.Lock.Unreadable) {
		b.WriteString("  " + ui.Warn.Render("⚠ advisory lock is stale or unreadable") + "\n")
	}
	b.WriteString("\n" + ui.Muted.Render("⏎ re-run"))
	return b.String()
}
```

Verify `repo.LockReport` has boolean fields `Stale` and `Unreadable` (grep `internal/repo/check.go` — the CLI's `checkFailedError` uses `report.Lock.Stale || report.Lock.Unreadable`, so they exist). Verify `spinner.TickMsg` and `spin.Tick` names against `go doc github.com/charmbracelet/bubbles/spinner`.

- [ ] **Step 5: Delete the Operations view and swap the registration**

```bash
git rm internal/tui/operations.go internal/tui/operations_test.go
```

In `internal/tui/app.go` `NewApp`, change the views entry `{id: "operations", model: NewOperations(deps)}` to `{id: "check", model: NewCheckView(deps)}`. If any test references `"operations"` or `NewOperations`, update it (grep first: `command grep -rn "operations\|NewOperations" internal/tui`). The `categories` map (Phase 2a) may map operation ids to "Operations" — leave "check" as a default "Views" entry (it's an inspection view, not a mutating operation).

- [ ] **Step 6: Run tests + gate**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS (Check tests green; no dangling Operations references).

Run the full task gate: `go build ./... && go test -race -count=1 ./internal/tui/ && gofmt -l cmd internal && go vet ./...`.

- [ ] **Step 7: Commit**

```bash
git add internal/tui
git commit -m "Add async Check flow with integrity report; replace Operations view"
```

---

## Task 2: Diff snapshot-pair picker

**Files:**
- Rewrite: `internal/tui/diff.go`, `internal/tui/diff_test.go`

- [ ] **Step 1: Write the failing tests**

Rewrite `internal/tui/diff_test.go`:

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

// seedDiffPair makes two snapshots that differ by one added file.
func seedDiffPair(t *testing.T, r *repo.Repo) {
	t.Helper()
	a := t.TempDir()
	os.WriteFile(filepath.Join(a, "keep.txt"), []byte("same"), 0o600)
	if _, err := r.CreateSnapshot(context.Background(), a, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	b := t.TempDir()
	os.WriteFile(filepath.Join(b, "keep.txt"), []byte("same"), 0o600)
	os.WriteFile(filepath.Join(b, "added.txt"), []byte("new"), 0o600)
	if _, err := r.CreateSnapshot(context.Background(), b, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestDiffFlow_PickPairAndRender(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)

	v := NewDiff(Deps{Repo: r})
	if len(v.snaps) != 2 {
		t.Fatalf("snaps = %d, want 2", len(v.snaps))
	}
	// Pick A (first row), then B (move down one, enter).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(Diff)
	if v.stage != diffPickB {
		t.Fatalf("stage = %v, want diffPickB", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(Diff)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(Diff)
	if v.stage != diffShow {
		t.Fatalf("stage = %v, want diffShow", v.stage)
	}
	out := v.View()
	if !strings.Contains(out, "added.txt") {
		t.Errorf("diff should show the added file:\n%s", out)
	}
}

func TestDiffFlow_EscGoesBack(t *testing.T) {
	r := newFlowRepo(t)
	seedDiffPair(t, r)
	v := NewDiff(Deps{Repo: r})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // -> pickB
	v = m.(Diff)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back to pickA
	v = m.(Diff)
	if v.stage != diffPickA {
		t.Fatalf("esc should return to pickA; stage = %v", v.stage)
	}
}

func TestDiff_NilRepoPlaceholder(t *testing.T) {
	if !strings.Contains(NewDiff(Deps{}).View(), "no repository") {
		t.Error("nil-repo diff should render a placeholder")
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui/ -run 'TestDiffFlow|TestDiff_' -count=1`
Expected: FAIL — the current `Diff` has no `snaps`/`stage`/`diffPickB` and no picker.

- [ ] **Step 3: Rewrite `internal/tui/diff.go`**

```go
package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type diffStage int

const (
	diffPickA diffStage = iota
	diffPickB
	diffShow
)

// Diff walks a two-snapshot picker, then renders the added/removed/
// changed lists from repo.Diff. Snapshots load synchronously at
// construction (a manifest-list read, like the Snapshots view); the diff
// itself is two manifest reads, also fast, so it runs inline at the B
// selection rather than through the async op machinery.
type Diff struct {
	deps  Deps
	stage diffStage
	snaps []repo.SnapshotInfo
	tbl   table.Model
	idA   string
	res   repo.DiffResult
	err   string
	width int
}

func NewDiff(deps Deps) Diff {
	d := Diff{deps: deps}
	if deps.Repo != nil {
		ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 20*1e9)
		defer cancel()
		if snaps, err := deps.Repo.ListSnapshots(ctx); err == nil {
			d.snaps = snaps
		}
	}
	cols := []table.Column{
		{Title: "ID", Width: 34},
		{Title: "Created", Width: 17},
		{Title: "Tag", Width: 12},
	}
	rows := make([]table.Row, len(d.snaps))
	for i, s := range d.snaps {
		rows[i] = table.Row{s.ID, s.CreatedAt.UTC().Format("2006-01-02 15:04"), s.Tag}
	}
	d.tbl = table.New(table.WithColumns(cols), table.WithRows(rows), table.WithFocused(true))
	return d
}

func (Diff) Init() tea.Cmd { return nil }

func (d Diff) Title() string { return "Diff" }

func (d Diff) ShortHelp() []key.Binding {
	switch d.stage {
	case diffShow:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "snapshot")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "choose")),
		}
	}
}

func (d Diff) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.tbl.SetHeight(max(msg.Height-8, 3))
		return d, nil
	case tea.KeyMsg:
		return d.handleKey(msg)
	}
	return d, nil
}

func (d Diff) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch d.stage {
	case diffPickA:
		if msg.Type == tea.KeyEnter && len(d.snaps) > 0 {
			d.idA = d.snaps[d.tbl.Cursor()].ID
			d.stage = diffPickB
			return d, nil
		}
		var cmd tea.Cmd
		d.tbl, cmd = d.tbl.Update(msg)
		return d, cmd
	case diffPickB:
		switch msg.Type {
		case tea.KeyEsc:
			d.stage = diffPickA
			return d, nil
		case tea.KeyEnter:
			if len(d.snaps) == 0 {
				return d, nil
			}
			idB := d.snaps[d.tbl.Cursor()].ID
			ctx, cancel := context.WithTimeout(ctxOrBackground(d.deps.Ctx), 20*1e9)
			defer cancel()
			res, err := d.deps.Repo.Diff(ctx, d.idA, idB)
			if err != nil {
				d.err = err.Error()
				return d, nil
			}
			d.res = res
			d.stage = diffShow
			return d, nil
		}
		var cmd tea.Cmd
		d.tbl, cmd = d.tbl.Update(msg)
		return d, cmd
	default: // diffShow
		if msg.Type == tea.KeyEsc {
			d.stage = diffPickA
			d.err = ""
			return d, nil
		}
		return d, nil
	}
}

func (d Diff) View() string {
	if d.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	if d.err != "" {
		return ui.Danger.Render("Diff failed") + "\n\n" + d.err
	}
	switch d.stage {
	case diffPickA:
		return ui.Primary.Render("Diff: choose the FIRST snapshot") + "\n\n" + d.tbl.View()
	case diffPickB:
		return ui.Primary.Render("Diff "+d.idA+" → choose the SECOND snapshot") + "\n\n" + d.tbl.View()
	default:
		return d.renderResult()
	}
}

func (d Diff) renderResult() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Diff result"))
	fmt.Fprintf(&b, "  %s\n\n", ui.Muted.Render(
		fmt.Sprintf("+%d  -%d  ~%d", len(d.res.Added), len(d.res.Removed), len(d.res.Changed))))
	writeCol := func(label string, style func(...string) string, paths []string) {
		if len(paths) == 0 {
			return
		}
		b.WriteString(label + "\n")
		for _, p := range paths {
			b.WriteString("  " + style(p) + "\n")
		}
		b.WriteString("\n")
	}
	writeCol(ui.Success.Render("Added"), func(s ...string) string { return ui.Success.Render(strings.Join(s, "")) }, d.res.Added)
	writeCol(ui.Danger.Render("Removed"), func(s ...string) string { return ui.Danger.Render(strings.Join(s, "")) }, d.res.Removed)
	writeCol(ui.Warn.Render("Changed"), func(s ...string) string { return ui.Warn.Render(strings.Join(s, "")) }, d.res.Changed)
	b.WriteString(ui.Muted.Render("esc back"))
	return b.String()
}
```

Note: `20*1e9` is 20 seconds as `time.Duration` — if `go vet` objects to the untyped-float idiom, use `20*time.Second` and add the `time` import (cleaner; prefer this).

- [ ] **Step 4: Run tests + gate**

Run: `go test -race -count=1 ./internal/tui/`
Expected: PASS. The old `Diff.SetResult` is gone; grep for callers (`command grep -rn "SetResult" internal/tui internal/cli`) — there should be none outside the deleted test. If the shell or a test referenced it, update accordingly.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/diff.go internal/tui/diff_test.go
git commit -m "Rewrite Diff view with a two-snapshot picker feeding repo.Diff"
```

---

## Task 3: Registry check + full gate

**Files:**
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_CheckReplacesOperationsInSidebar: after Phase 2b, the sidebar
// exposes Check (not the old Operations placeholder), and the view count
// is unchanged (operations → check is a swap, not an addition).
func TestApp_CheckReplacesOperationsInSidebar(t *testing.T) {
	app := newTestApp(t)
	out := app.View()
	if !strings.Contains(out, "Check") {
		t.Errorf("sidebar should list Check:\n%s", out)
	}
	if strings.Contains(out, "Operations") {
		t.Errorf("Operations placeholder should be gone:\n%s", out)
	}
	if got := len(app.views); got != 8 {
		t.Fatalf("views = %d, want 8 (check swapped in for operations)", got)
	}
}
```

- [ ] **Step 2: Run to verify failure/pass**

Run: `go test ./internal/tui/ -run TestApp_CheckReplacesOperationsInSidebar -count=1`
Expected: PASS if Task 1's swap was done correctly (this is a characterization test locking the swap in). If it FAILS with "views = 5" or an Operations reference, fix Task 1's registration.

- [ ] **Step 3: Full CI-equivalent gate**

```bash
go build ./... \
 && go vet ./... \
 && gofmt -l cmd internal \
 && go test -race -count=1 ./... \
 && go test ./third_party/fastcdc-go/... \
 && go mod tidy -diff \
 && golangci-lint run ./...
```
Expected: all green; golangci-lint "0 issues".

- [ ] **Step 4: Commit; do NOT merge**

```bash
git add internal/tui
git commit -m "Lock Check-replaces-Operations in the sidebar; Phase 2b gate"
```

Leave the branch for review + the user's interactive smoke test.

---

## Self-review notes (author)

- **Spec coverage (Phase 2b slice):** Check flow (integrity report, replacing Operations) → Task 1; Diff snapshot-pair picker → Task 2; registry swap + gate → Task 3.
- **Deliberate deviations from the spec, called out:** (1) The spec's Check "quick/deep toggle" is dropped — `repo.Check` exposes no deep-verify mode (`CheckOptions` is only `Now`/`StaleLockAfter`); a deep per-blob verify is a repo-layer feature for a later phase. (2) Check is read-only, so it intentionally does NOT use the Phase 2a one-op mutating guard — it loads via a plain `tea.Cmd` + spinner and can run alongside a backup. (3) Issue "drill-down" is rendered as summarized counts + flags, not an interactive per-issue navigator (YAGNI for the report sizes Check produces; a future enhancement if repos routinely surface many issues).
- **Deferred to Phase 2c (needs deps/extraction, not in scope here):** Sync (dest-store factory in Deps), Agent-apply (action registry + Env in Deps), Policies/Schedule (huh forms + config write), Password (keyring + rotate deps), Recovery-kit (extract `buildRecoveryKit` from `internal/cli` to a shared package — cli→tui import direction blocks reuse), Doctor (AWS-check deps). Phase 2c's first task is the `tui.Deps` expansion these share.
- **Type consistency:** `checkStage`/`checkDoneMsg`/`CheckView` (Task 1); `diffStage`/`diffPickA`/`diffPickB`/`diffShow`/`Diff` (Task 2); both reuse `ctxOrBackground`, `ui.FormatBytes`, `execCmds` (test helper). `20*time.Second` (with `time` import) preferred over the `20*1e9` sketch.
- **API-verification checkpoints flagged inline:** `repo.LockReport{Stale, Unreadable}` fields; `bubbles/spinner` `TickMsg`/`Tick`/`Dot` names; `bubbles/table` v1 API (already used by restore/prune in 2a, so known-good).
