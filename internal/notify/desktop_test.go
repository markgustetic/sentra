package notify

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type call struct {
	name string
	args []string
}

func recorder(calls *[]call, err error) Runner {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name: name, args: args})
		return nil, err
	}
}

// TestDesktop_ArgvPerPlatform pins the exact command each platform runs.
// The body carries quotes, a backslash, and a newline: on darwin every
// string must reach osascript as argv, never spliced into the script
// source, so a policy name or error text can't break out of it.
func TestDesktop_ArgvPerPlatform(t *testing.T) {
	body := `home: it's "done" \ ok` + "\nsecond"
	tests := []struct {
		goos string
		want *call
	}{
		{goos: "darwin", want: &call{name: "osascript", args: []string{
			"-e", "on run argv",
			"-e", "display notification (item 3 of argv) with title (item 1 of argv) subtitle (item 2 of argv)",
			"-e", "end run",
			"Sentra", "Backup complete", body,
		}}},
		{goos: "linux", want: &call{name: "notify-send", args: []string{
			"--app-name", "Sentra", "Backup complete", body,
		}}},
		{goos: "windows", want: nil},
		{goos: "freebsd", want: nil},
	}
	for _, tc := range tests {
		t.Run(tc.goos, func(t *testing.T) {
			var calls []call
			err := Desktop(context.Background(), recorder(&calls, nil), tc.goos, "Sentra", "Backup complete", body)
			if err != nil {
				t.Fatalf("Desktop: %v", err)
			}
			if tc.want == nil {
				if len(calls) != 0 {
					t.Fatalf("unsupported platform must make no call, got %+v", calls)
				}
				return
			}
			if len(calls) != 1 {
				t.Fatalf("want exactly one call, got %+v", calls)
			}
			if !reflect.DeepEqual(calls[0], *tc.want) {
				t.Errorf("argv mismatch\n got %q %q\nwant %q %q", calls[0].name, calls[0].args, tc.want.name, tc.want.args)
			}
		})
	}
}

// TestDesktop_NilRunnerIsOff: a zero-value Deps must never pop a real
// notification, so nil means disabled rather than ExecRunner.
func TestDesktop_NilRunnerIsOff(t *testing.T) {
	if err := Desktop(context.Background(), nil, "darwin", "Sentra", "x", "y"); err != nil {
		t.Fatalf("nil runner must be a silent no-op, got %v", err)
	}
}

// TestDesktop_RunnerErrorIsReturned so callers can log it; they own the
// decision that it never masks the run's own result.
func TestDesktop_RunnerErrorIsReturned(t *testing.T) {
	var calls []call
	boom := errors.New("osascript: not found")
	err := Desktop(context.Background(), recorder(&calls, boom), "darwin", "Sentra", "x", "y")
	if !errors.Is(err, boom) {
		t.Fatalf("want wrapped runner error, got %v", err)
	}
}

// supportedGOOS is every platform Desktop makes a call on. A new case in
// Desktop's switch goes here too, or the trailing-argv rule below is not
// asserted for it.
var supportedGOOS = []string{"darwin", "linux"}

// TestDesktop_TrailingArgvIsTitleSubtitleBody pins the rule every caller's
// test relies on: on every supported platform the LAST THREE argv entries
// are title, subtitle, body, in that order. The cli, tui, and policy
// suites all decode a recorded notification that way so they can run
// unchanged on the darwin and linux CI jobs; the linux argv once folded
// the title into notify-send's summary, which satisfied the per-platform
// snapshot above while breaking every one of those decoders on Linux
// only. The snapshot pins what each platform runs; this pins what they
// share.
func TestDesktop_TrailingArgvIsTitleSubtitleBody(t *testing.T) {
	for _, goos := range supportedGOOS {
		t.Run(goos, func(t *testing.T) {
			var calls []call
			if err := Desktop(context.Background(), recorder(&calls, nil), goos, "T", "S", "B"); err != nil {
				t.Fatalf("Desktop: %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("want one call, got %+v", calls)
			}
			a := calls[0].args
			if len(a) < 3 {
				t.Fatalf("argv too short: %q", a)
			}
			if got := a[len(a)-3:]; !reflect.DeepEqual(got, []string{"T", "S", "B"}) {
				t.Errorf("trailing argv = %q, want [T S B]", got)
			}
		})
	}
}
