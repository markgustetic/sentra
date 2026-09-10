package policy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

type notifyCall struct {
	name string
	args []string
}

func notifyRecorder(calls *[]notifyCall, err error) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, notifyCall{name: name, args: args})
		return nil, err
	}
}

// lastArgs are the message strings: on every supported platform the
// title, subtitle, and body are the trailing argv entries, so the tests
// stay platform-agnostic — they run on the darwin and linux CI jobs.
func lastArgs(t *testing.T, calls []notifyCall) (title, subtitle, body string) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("want exactly one notification, got %+v", calls)
	}
	a := calls[0].args
	if len(a) < 3 {
		t.Fatalf("argv too short: %q", a)
	}
	return a[len(a)-3], a[len(a)-2], a[len(a)-1]
}

func TestNotifyBackup_SuccessNamesTheOutcome(t *testing.T) {
	var calls []notifyCall
	var out bytes.Buffer
	NotifyBackup(context.Background(), &out, notifyRecorder(&calls, nil), true,
		BackupOutcome{Name: "home", Files: 1204, NewBytes: 38 << 20, Skipped: 2}, nil)
	title, subtitle, body := lastArgs(t, calls)
	if title != "Sentra" || subtitle != "Backup complete" {
		t.Errorf("title/subtitle = %q/%q", title, subtitle)
	}
	for _, want := range []string{"home:", "1204 files", "38.0 MiB new", "2 skipped"} {
		if !strings.Contains(body, want) {
			t.Errorf("body %q missing %q", body, want)
		}
	}
	if out.Len() != 0 {
		t.Errorf("a delivered notification must write nothing to the log, got %q", out.String())
	}
}

func TestNotifyBackup_SuccessOmitsZeroSkipped(t *testing.T) {
	var calls []notifyCall
	NotifyBackup(context.Background(), &bytes.Buffer{}, notifyRecorder(&calls, nil), true,
		BackupOutcome{Name: "home", Files: 3}, nil)
	_, _, body := lastArgs(t, calls)
	if strings.Contains(body, "skipped") {
		t.Errorf("body %q must not mention skipped when none were", body)
	}
}

// The failure body carries only the error's first line, truncated: a
// wrapped AWS error is paragraphs long and a notification is one glance.
func TestNotifyBackup_FailureCarriesFirstErrorLine(t *testing.T) {
	var calls []notifyCall
	long := strings.Repeat("x", 300)
	cause := errors.New("snapshot /home: " + long + "\nsecond line")
	NotifyBackup(context.Background(), &bytes.Buffer{}, notifyRecorder(&calls, nil), true,
		BackupOutcome{Name: "home"}, cause)
	_, subtitle, body := lastArgs(t, calls)
	if subtitle != "Backup failed" {
		t.Errorf("subtitle = %q", subtitle)
	}
	if !strings.HasPrefix(body, "home: snapshot /home: xxx") {
		t.Errorf("body %q must open with the name and the error", body)
	}
	if strings.Contains(body, "second line") {
		t.Errorf("body %q must carry the first line only", body)
	}
	if n := utf8.RuneCountInString(body); n > notifyBodyMax {
		t.Errorf("body length %d runes exceeds %d", n, notifyBodyMax)
	}
}

func TestNotifyBackup_DisabledMakesNoCall(t *testing.T) {
	var calls []notifyCall
	NotifyBackup(context.Background(), &bytes.Buffer{}, notifyRecorder(&calls, nil), false,
		BackupOutcome{Name: "home"}, nil)
	if len(calls) != 0 {
		t.Fatalf("disabled must not notify, got %+v", calls)
	}
}

// A broken notifier is logged and swallowed: the run's own result is what
// the caller reports, and a missing osascript must never turn a good
// backup into a failed timer exit.
func TestNotifyBackup_RunnerFailureIsLoggedNotFatal(t *testing.T) {
	var calls []notifyCall
	var out bytes.Buffer
	NotifyBackup(context.Background(), &out, notifyRecorder(&calls, errors.New("exec: not found")), true,
		BackupOutcome{Name: "home"}, nil)
	if !strings.Contains(out.String(), "notification failed") || !strings.Contains(out.String(), "not found") {
		t.Errorf("log must name the notifier failure, got %q", out.String())
	}
}
