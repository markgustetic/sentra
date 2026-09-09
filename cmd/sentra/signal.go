package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/spf13/cobra"
)

// signalContext derives the context every command runs under. The first
// SIGINT/SIGTERM cancels it — that is what lets `policy run`'s deferred
// lock release run instead of leaving meta/lock behind, and what the
// --startup-delay select and every in-flight S3 call observe. A second
// signal calls exit(128+signum) so an operator is never trapped behind a
// release that hangs on an unreachable bucket. stop releases the
// registration without exiting, so the process regains the default
// disposition once the command has returned.
//
// The TUI needs no special case for the first signal: bubbletea
// registers its own SIGINT handler and returns ErrInterrupted, and the
// cancelled context reaches it only as Deps.Ctx, which stops whatever
// operation was in flight — the same intent, not a competing one. (In
// raw mode ctrl+c is a key, not a signal, so this path only sees a
// `kill`.) The SECOND signal is different: exit() leaves the process
// from this goroutine, so under `sentra ui` bubbletea never runs its
// terminal restore — the alt screen stays up and raw mode stays on
// until the shell resets it. That is accepted: the second signal exists
// to escape a release hanging on an unreachable bucket, and a garbled
// terminal (`reset` fixes it) beats a trapped operator.
//
// received reports the signal that cancelled ctx, if one did, so
// execute can exit 128+signum for an interrupted run.
func signalContext(parent context.Context, exit func(code int)) (ctx context.Context, stop context.CancelFunc, received func() (os.Signal, bool)) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	var (
		mu    sync.Mutex
		first os.Signal
	)
	go func() {
		select {
		case sig := <-sigs:
			mu.Lock()
			first = sig
			mu.Unlock()
			cancel()
		case <-done:
			return
		}
		select {
		case sig := <-sigs:
			exit(128 + signum(sig))
		case <-done:
		}
	}()
	var once sync.Once
	stop = func() {
		once.Do(func() {
			signal.Stop(sigs)
			close(done)
			cancel()
		})
	}
	received = func() (os.Signal, bool) {
		mu.Lock()
		defer mu.Unlock()
		return first, first != nil
	}
	return ctx, stop, received
}

// signum maps the two signals we listen for to their numbers, so the
// forced exit carries the conventional 128+N status (130 for SIGINT).
func signum(sig os.Signal) int {
	if s, ok := sig.(syscall.Signal); ok {
		return int(s)
	}
	return 2
}

// execute runs root under the signal context and returns the process
// exit code. Split from main so the wiring is testable with a fake exit.
//
// A run cut short by a signal exits 128+signum (130 for SIGINT, 143 for
// SIGTERM) whichever way the command returned — nil after a graceful
// unwind, or the context error it hit mid-call — so a supervisor can
// tell "interrupted" from "failed": launchd stopping a `policy run` at
// logout and a bucket outage must not both read as exit 1. The
// interrupted status wins over an error because the error is almost
// always the cancellation itself, wrapped.
func execute(root *cobra.Command, exit func(code int)) int {
	ctx, stop, received := signalContext(context.Background(), exit)
	defer stop()
	err := root.ExecuteContext(ctx)
	if sig, ok := received(); ok {
		return 128 + signum(sig)
	}
	if err != nil {
		// cobra prints the error itself when SilenceErrors is false; we
		// just need to propagate the non-zero exit so scripts can detect
		// failure.
		return 1
	}
	return 0
}
