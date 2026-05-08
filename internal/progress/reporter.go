// Package progress defines the byte-counting reporter interface that
// long-running repo operations (CreateSnapshot, Restore, BackupPlan)
// surface progress through. The interface lives in its own package so
// the domain layer (internal/repo) doesn't have to import the UI layer
// (internal/ui) just to consume it — that prior layer inversion meant
// any future internal/sync, internal/daemon, or embedded SDK use of
// the repo package dragged in the entire Charmbracelet TUI dependency
// tree.
//
// The dependency graph now flows in one direction:
//
//	tui → ui → progress ← repo ← cli
//
// The CLI's *ui.ByteProgress and the test-only RecordingReporter
// here both satisfy Reporter; the TUI adapts events into tea.Msgs;
// callers that don't care about progress pass NopReporter so call
// sites don't need nil checks at every Add.
package progress

import "sync"

// Reporter is the contract long-running repo operations call into to
// surface byte-level progress.
//
// Total is called once at the start with the total bytes the operation
// expects to process. It may be called again if the total is revised
// mid-operation (e.g. additional files discovered during a streaming
// walk). Add is called with positive deltas as bytes are processed.
//
// Implementations must be safe for concurrent calls — repo operations
// fan out across worker pools and report from every worker.
type Reporter interface {
	Total(n int64)
	Add(delta int64)
}

// NopReporter satisfies Reporter and discards every call. Repo callers
// that don't pass a reporter use this so the call sites don't need nil
// checks at every Add.
type NopReporter struct{}

// Total is a no-op.
func (NopReporter) Total(int64) {}

// Add is a no-op.
func (NopReporter) Add(int64) {}

// RecordingReporter is a thread-safe Reporter that records every call.
// Tests use it to assert that the repo layer reports the right totals
// and progresses the right amounts.
type RecordingReporter struct {
	mu     sync.Mutex
	total  int64
	done   int64
	events int
}

// Total records the latest total under the lock.
func (r *RecordingReporter) Total(n int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
	r.events++
}

// Add accumulates delta into done under the lock.
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
