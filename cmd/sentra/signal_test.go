package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// The process must react to SIGINT/SIGTERM through the command context,
// not by dying: `policy run` releases meta/lock in a defer and sleeps on
// --startup-delay against cmd.Context(), and both are dead code when the
// signal's default disposition kills the process first. The rule has two
// halves — the first signal cancels the context, the second forces an
// exit so a hung release cannot trap the operator — and both are pinned
// here with a fake exit.

func selfSignal(t *testing.T, sig syscall.Signal) {
	t.Helper()
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
}

func awaitDone(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("context not cancelled after the first signal")
	}
}

func TestSignalContext_FirstSignalCancelsSecondForcesExit(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			exited := make(chan int, 1)
			ctx, stop := signalContext(context.Background(), func(code int) { exited <- code })
			defer stop()

			selfSignal(t, sig)
			awaitDone(t, ctx)
			select {
			case code := <-exited:
				t.Fatalf("first signal must cancel, not exit (got exit %d)", code)
			case <-time.After(100 * time.Millisecond):
			}

			selfSignal(t, sig)
			select {
			case code := <-exited:
				if want := 128 + int(sig); code != want {
					t.Errorf("exit code: got %d, want %d", code, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("second signal must force an exit")
			}
		})
	}
}

// TestSignalContext_StopReleasesWithoutExit: a normal completion calls
// stop; that must neither call exit nor leave the process deaf to a
// later signal (the registration is released).
func TestSignalContext_StopReleasesWithoutExit(t *testing.T) {
	exited := make(chan int, 1)
	ctx, stop := signalContext(context.Background(), func(code int) { exited <- code })
	stop()
	awaitDone(t, ctx)
	select {
	case code := <-exited:
		t.Fatalf("stop must not exit (got %d)", code)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestExecute_SignalReachesCommandContext: the wiring end to end — a
// signal delivered while a command runs cancels that command's
// cmd.Context(), which is what --startup-delay and the lock release
// select on.
func TestExecute_SignalReachesCommandContext(t *testing.T) {
	root := &cobra.Command{Use: "sentra", SilenceUsage: true, SilenceErrors: true}
	var got error
	root.AddCommand(&cobra.Command{
		Use: "wait",
		RunE: func(cmd *cobra.Command, _ []string) error {
			selfSignal(t, syscall.SIGINT)
			select {
			case <-cmd.Context().Done():
				got = cmd.Context().Err()
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("cmd.Context() never cancelled")
			}
		},
	})
	root.SetArgs([]string{"wait"})
	exited := make(chan int, 1)
	if code := execute(root, func(code int) { exited <- code }); code != 0 {
		t.Fatalf("execute exit code: got %d, want 0", code)
	}
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("command context: got %v, want context.Canceled", got)
	}
	select {
	case code := <-exited:
		t.Fatalf("a single signal must not force-exit (got %d)", code)
	default:
	}
}
