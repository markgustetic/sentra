// Package notify posts desktop notifications. It is a leaf package —
// below internal/cli, internal/tui, and internal/policy — so a backup
// run notifies identically from every surface, including the launchd
// and systemd timers that invoke `sentra policy run` with no terminal
// attached.
package notify

import (
	"context"
	"fmt"
	"os/exec"
	"time"
)

// Runner executes one notifier command and returns its combined output.
// Production passes ExecRunner. A nil Runner means notifications are OFF:
// this deliberately differs from scheduler.Runner, whose nil selects the
// real thing. A missing timer activation is a broken feature the operator
// notices at once; a missing notification is not, and the failure mode of
// the other default is every test that runs a backup popping a real
// notification on the developer's desktop. So a zero-value Deps is silent
// and production wires ExecRunner explicitly (guarded by a test).
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execTimeout bounds one notifier call. Both osascript and notify-send
// return in well under a second; a hung notification daemon must not
// hold a timer run's exit — or the TUI's op — hostage.
const execTimeout = 5 * time.Second

// ExecRunner is the production Runner: os/exec with a hard timeout.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // argv is our own fixed script plus message text passed as data
}

// Desktop posts one notification for goos via run. Unsupported platforms
// make no call and return nil; a nil run is a silent no-op. The runner's
// error is returned wrapped so the caller can log it — by contract the
// caller never lets it mask the outcome being reported.
//
// On darwin the title, subtitle, and body travel as argv into an
// `on run argv` handler rather than being spliced into the AppleScript
// source: a policy name or error text containing quotes or backslashes
// would otherwise terminate the string literal and run as script.
func Desktop(ctx context.Context, run Runner, goos, title, subtitle, body string) error {
	if run == nil {
		return nil
	}
	var name string
	var args []string
	switch goos {
	case "darwin":
		name = "osascript"
		args = []string{
			"-e", "on run argv",
			"-e", "display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)",
			"-e", "end run",
			title, subtitle, body,
		}
	case "linux":
		name = "notify-send"
		args = []string{"--app-name", title, title + " — " + subtitle, body}
	default:
		return nil
	}
	if out, err := run(ctx, name, args...); err != nil {
		return fmt.Errorf("%s: %w: %s", name, err, firstLine(string(out)))
	}
	return nil
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
