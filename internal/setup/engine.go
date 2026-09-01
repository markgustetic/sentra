package setup

import (
	"context"
	"time"
)

// Engine sequences the side-effecting steps of setup — AWS auth + bucket
// prep, backup-user provisioning, config write, repo init — over an injected
// Effects seam. It contains NO huh forms, NO stdout writes, and NO cobra: the
// TUI wizard is the only sequencer, driving it from tea messages. `sentra
// setup` is a thin CLI launcher for that same wizard, not a second driver of
// the engine.
type Engine struct {
	eff Effects
	// sleep is the retry loop's clock seam. Production sleeps for real;
	// tests substitute an instant sleep and assert the requested durations,
	// so a 30-second backoff schedule is verified in microseconds.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewEngine returns an Engine backed by eff.
func NewEngine(eff Effects) *Engine { return &Engine{eff: eff, sleep: sleepCtx} }

// sleepCtx waits d or until ctx is done, whichever comes first, so a
// cancelled setup never sits in a retry sleep.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
