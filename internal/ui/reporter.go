package ui

import "sync"

// ProgressReporter is the contract long-running repo operations
// (CreateSnapshot, Restore) call into to surface progress. The
// repo layer doesn't depend on lipgloss or bubbletea — it just
// reports counts. Inline CLI mode wraps a *ByteProgress; the TUI
// adapts events into tea.Msgs; tests pass a recording stub.
//
// Total is called once at the start with the total bytes the
// operation expects to process. It may be called again if the
// total is revised mid-operation (e.g. additional files discovered
// during a streaming walk). Add is called with positive deltas as
// bytes are processed. Implementations must be safe for concurrent
// calls — repo.CreateSnapshot fans out across walker workers.
type ProgressReporter interface {
	Total(n int64)
	Add(delta int64)
}

// NopReporter satisfies ProgressReporter and discards every call.
// repo callers that don't pass a reporter use this so the call
// sites don't need nil checks at every Add.
type NopReporter struct{}

func (NopReporter) Total(int64) {}
func (NopReporter) Add(int64)   {}

// RecordingReporter is a thread-safe ProgressReporter that records
// every call. Tests use it to assert that the repo layer reports
// the right totals and progresses the right amounts.
type RecordingReporter struct {
	mu     sync.Mutex
	total  int64
	done   int64
	events int
}

func (r *RecordingReporter) Total(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
	r.events++
}

func (r *RecordingReporter) Add(delta int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done += delta
	r.events++
}

// Snapshot returns the latest total, sum of Add deltas, and event
// count. Useful in tests to assert post-conditions without exporting
// the internal fields.
func (r *RecordingReporter) Snapshot() (total, done int64, events int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total, r.done, r.events
}

// Total binds *ByteProgress to the ProgressReporter interface so a
// CLI command can pass it to repo.CreateSnapshot directly.
func (p *ByteProgress) Total(n int64) {
	p.total = n
}

// Add binds *ByteProgress to the ProgressReporter interface. It
// applies a positive delta to the done count and updates the
// underlying bubbles progress model. The returned tea.Cmd is
// discarded in inline mode; tea-mode callers should call SetDone
// directly instead so they receive the cmd.
func (p *ByteProgress) Add(delta int64) {
	p.SetDone(p.done + delta)
}
