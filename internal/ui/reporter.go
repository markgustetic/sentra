package ui

import "github.com/markgustetic/sentra/internal/progress"

// Compile-time assertion that *ByteProgress satisfies the cross-
// package progress.Reporter contract. The interface itself lives in
// internal/progress so the repo layer doesn't have to import internal/ui
// (which pulls in lipgloss + bubbles) to accept a reporter.
var _ progress.Reporter = (*ByteProgress)(nil)

// Total binds *ByteProgress to the progress.Reporter interface so a
// CLI command can pass it to repo.CreateSnapshot directly.
func (p *ByteProgress) Total(n int64) {
	p.mu.Lock()
	p.total = n
	p.mu.Unlock()
}

// Add binds *ByteProgress to the progress.Reporter interface. It
// applies a positive delta to the done count under the mutex; the
// underlying bubbles progress model is NOT touched here because
// progress.Model.SetPercent is not concurrency-safe and Add is
// called from the walker's worker pool. The bar is repainted at
// Render time, which inline mode calls from a single ticker
// goroutine — the only place SetPercent is safely callable.
//
// Concurrent calls are safe: the load + store of done is serialized
// under p.mu so two walker workers never observe a torn value.
func (p *ByteProgress) Add(delta int64) {
	p.mu.Lock()
	p.done += delta
	p.mu.Unlock()
}
