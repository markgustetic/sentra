package policy

import (
	"context"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/markgustetic/sentra/internal/notify"
	"github.com/markgustetic/sentra/internal/ui"
)

// Notification lives here — below both surfaces, next to the hooks —
// so a run announces itself identically whether the CLI, the TUI, or an
// OS timer with no terminal attached ran it.

// BackupOutcome is what a successful run gets summarized as: the policy
// name (or the folder's base name for a one-shot backup) and the totals
// across its snapshots.
type BackupOutcome struct {
	Name     string
	Files    int
	NewBytes int64
	Skipped  int
}

// notifyBodyMax bounds the failure body. A notification is one glance;
// a wrapped AWS error runs to paragraphs.
const notifyBodyMax = 160

// NotifyBackup posts the desktop notification for a finished run: a
// "Backup complete" summary when runErr is nil, else "Backup failed"
// with the error's first line. enabled is the operator's
// notify.disable_desktop negated; run nil means notifications are off
// (see notify.Runner). Best-effort by contract: a broken notifier is
// written to out — the timer log — and never returned, because runErr
// is the run's result and a missing osascript must not turn a good
// backup into a failed exit.
func NotifyBackup(ctx context.Context, out io.Writer, run notify.Runner, enabled bool, o BackupOutcome, runErr error) {
	if !enabled || run == nil {
		return
	}
	subtitle, body := "Backup complete", successBody(o)
	if runErr != nil {
		subtitle, body = "Backup failed", failureBody(o.Name, runErr)
	}
	if err := notify.Desktop(ctx, run, runtime.GOOS, "Sentra", subtitle, body); err != nil {
		fmt.Fprintf(out, "  notification failed: %v\n", err)
	}
}

func successBody(o BackupOutcome) string {
	body := fmt.Sprintf("%s: %d files, %s new", o.Name, o.Files, ui.FormatBytes(o.NewBytes))
	if o.Skipped > 0 {
		body += fmt.Sprintf(", %d skipped", o.Skipped)
	}
	return body
}

func failureBody(name string, err error) string {
	line, _, _ := strings.Cut(err.Error(), "\n")
	body := name + ": " + line
	// Rune-aware: a byte slice could split a multi-byte character in a
	// path and hand osascript invalid UTF-8.
	if r := []rune(body); len(r) > notifyBodyMax {
		body = string(r[:notifyBodyMax-1]) + "…"
	}
	return body
}
