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
// The TUI needs no special case: bubbletea registers its own SIGINT
// handler and returns ErrInterrupted, and the cancelled context reaches
// it only as Deps.Ctx, which stops whatever operation was in flight —
// the same intent, not a competing one. (In raw mode ctrl+c is a key,
// not a signal, so this path only sees a `kill`.)
func signalContext(parent context.Context, exit func(code int)) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	sigs := make(chan os.Signal, 2)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-sigs:
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
	stop := func() {
		once.Do(func() {
			signal.Stop(sigs)
			close(done)
			cancel()
		})
	}
	return ctx, stop
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
func execute(root *cobra.Command, exit func(code int)) int {
	ctx, stop := signalContext(context.Background(), exit)
	defer stop()
	if err := root.ExecuteContext(ctx); err != nil {
		// cobra prints the error itself when SilenceErrors is false; we
		// just need to propagate the non-zero exit so scripts can detect
		// failure.
		return 1
	}
	return 0
}
