package tui

import (
	"context"
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
