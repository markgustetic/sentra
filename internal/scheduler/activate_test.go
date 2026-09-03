package scheduler

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// fakeExit mimics *exec.ExitError: an error that also reports the exit code,
// which is how Activate/Deactivate/Active tell "the OS said no" apart from
// "the command never ran".
type fakeExit int

func (e fakeExit) Error() string { return "exit status " + strconv.Itoa(int(e)) }
func (e fakeExit) ExitCode() int { return int(e) }

type fakeResp struct {
	out  string
	code int
}

// fakeRunner records every command it is handed and answers from a table
// keyed by the joined command line; anything not in the table succeeds
// silently. Tests never shell out.
type fakeRunner struct {
	calls []string
	resp  map[string]fakeResp
}

func (f *fakeRunner) run(_ context.Context, name string, args ...string) ([]byte, error) {
	line := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, line)
	r, ok := f.resp[line]
	if !ok || r.code == 0 {
		return []byte(r.out), nil
	}
	return []byte(r.out), fakeExit(r.code)
}

func guiDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func darwinPaths(t *testing.T) Paths {
	t.Helper()
	p, err := PathsFor("darwin", "/Users/op", "home")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func linuxPaths(t *testing.T) Paths {
	t.Helper()
	p, err := PathsFor("linux", "/home/op", "home")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func wantCalls(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n got %q\nwant %q", got, want)
	}
}

func TestActivate_DarwinBootsOutThenBootstrapsIntoGUIDomain(t *testing.T) {
	p := darwinPaths(t)
	f := &fakeRunner{resp: map[string]fakeResp{
		// First install: nothing to boot out. That must not abort.
		"launchctl bootout " + guiDomain() + "/com.sentra.home": {out: "Boot-out failed: 3: No such process", code: 3},
	}}
	if err := Activate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	wantCalls(t, f.calls, []string{
		"launchctl bootout " + guiDomain() + "/com.sentra.home",
		"launchctl bootstrap " + guiDomain() + " /Users/op/Library/LaunchAgents/com.sentra.home.plist",
	})
}

func TestActivate_DarwinFallsBackToLegacyLoadWhenBootstrapIsUnknown(t *testing.T) {
	p := darwinPaths(t)
	f := &fakeRunner{resp: map[string]fakeResp{
		"launchctl bootout " + guiDomain() + "/com.sentra.home":                                        {out: "Unrecognized subcommand: bootout", code: 1},
		"launchctl bootstrap " + guiDomain() + " /Users/op/Library/LaunchAgents/com.sentra.home.plist": {out: "Unrecognized subcommand: bootstrap", code: 1},
	}}
	if err := Activate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	wantCalls(t, f.calls, []string{
		"launchctl bootout " + guiDomain() + "/com.sentra.home",
		"launchctl bootstrap " + guiDomain() + " /Users/op/Library/LaunchAgents/com.sentra.home.plist",
		"launchctl unload /Users/op/Library/LaunchAgents/com.sentra.home.plist",
		"launchctl load -w /Users/op/Library/LaunchAgents/com.sentra.home.plist",
	})
}

func TestActivate_LinuxReloadsThenEnablesTimerNow(t *testing.T) {
	p := linuxPaths(t)
	f := &fakeRunner{}
	if err := Activate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	wantCalls(t, f.calls, []string{
		"systemctl --user daemon-reload",
		"systemctl --user enable --now sentra-home.timer",
	})
}

// A headless session (no user bus, no gui domain) must not lose the files
// and must hand the operator the exact command to run later.
func TestActivate_FailureNamesTheCommandAndReason(t *testing.T) {
	cases := []struct {
		name    string
		paths   Paths
		fail    string
		out     string
		wantCmd string
	}{
		{
			name:    "linux no bus",
			paths:   linuxPaths(t),
			fail:    "systemctl --user enable --now sentra-home.timer",
			out:     "Failed to connect to bus: No medium found",
			wantCmd: "systemctl --user daemon-reload && systemctl --user enable --now sentra-home.timer",
		},
		{
			name:    "darwin no gui domain",
			paths:   darwinPaths(t),
			fail:    "launchctl bootstrap " + guiDomain() + " /Users/op/Library/LaunchAgents/com.sentra.home.plist",
			out:     "Bootstrap failed: 125: Domain does not support specified action",
			wantCmd: "launchctl bootstrap " + guiDomain() + " /Users/op/Library/LaunchAgents/com.sentra.home.plist",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{resp: map[string]fakeResp{tc.fail: {out: tc.out, code: 1}}}
			err := Activate(context.Background(), tc.paths, f.run)
			var aerr *ActivationError
			if !errors.As(err, &aerr) {
				t.Fatalf("Activate error = %v (%T), want *ActivationError", err, err)
			}
			if aerr.Command != tc.wantCmd {
				t.Fatalf("Command = %q, want %q", aerr.Command, tc.wantCmd)
			}
			msg := err.Error()
			for _, want := range []string{tc.wantCmd, tc.out, "files"} {
				if !strings.Contains(msg, want) {
					t.Fatalf("error %q missing %q", msg, want)
				}
			}
		})
	}
}

// The command never ran at all (binary missing): still an ActivationError
// carrying the command, so the operator message is uniform.
func TestActivate_MissingBinaryIsAnActivationError(t *testing.T) {
	p := linuxPaths(t)
	notFound := errors.New(`exec: "systemctl": executable file not found in $PATH`)
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, notFound }
	err := Activate(context.Background(), p, run)
	var aerr *ActivationError
	if !errors.As(err, &aerr) {
		t.Fatalf("error = %v, want *ActivationError", err)
	}
	if !errors.Is(err, notFound) {
		t.Fatalf("ActivationError must unwrap to the runner error, got %v", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error %q must carry the underlying reason", err)
	}
}

func TestDeactivate_DarwinBootsOutAndTreatsNoSuchProcessAsDone(t *testing.T) {
	p := darwinPaths(t)
	f := &fakeRunner{resp: map[string]fakeResp{
		"launchctl bootout " + guiDomain() + "/com.sentra.home": {out: "Boot-out failed: 3: No such process", code: 3},
	}}
	if err := Deactivate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Deactivate on an unloaded job must be a no-op, got %v", err)
	}
	wantCalls(t, f.calls, []string{"launchctl bootout " + guiDomain() + "/com.sentra.home"})
}

func TestDeactivate_DarwinFallsBackToLegacyUnload(t *testing.T) {
	p := darwinPaths(t)
	f := &fakeRunner{resp: map[string]fakeResp{
		"launchctl bootout " + guiDomain() + "/com.sentra.home": {out: "Unrecognized subcommand: bootout", code: 1},
	}}
	if err := Deactivate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	wantCalls(t, f.calls, []string{
		"launchctl bootout " + guiDomain() + "/com.sentra.home",
		"launchctl unload /Users/op/Library/LaunchAgents/com.sentra.home.plist",
	})
}

func TestDeactivate_LinuxDisablesTimerNow(t *testing.T) {
	p := linuxPaths(t)
	f := &fakeRunner{}
	if err := Deactivate(context.Background(), p, f.run); err != nil {
		t.Fatalf("Deactivate: %v", err)
	}
	wantCalls(t, f.calls, []string{"systemctl --user disable --now sentra-home.timer"})
}

func TestDeactivate_FailureNamesTheCommand(t *testing.T) {
	p := linuxPaths(t)
	f := &fakeRunner{resp: map[string]fakeResp{
		"systemctl --user disable --now sentra-home.timer": {out: "Failed to connect to bus: No medium found", code: 1},
	}}
	err := Deactivate(context.Background(), p, f.run)
	var aerr *ActivationError
	if !errors.As(err, &aerr) {
		t.Fatalf("error = %v, want *ActivationError", err)
	}
	if aerr.Command != "systemctl --user disable --now sentra-home.timer" {
		t.Fatalf("Command = %q", aerr.Command)
	}
	if !strings.Contains(err.Error(), "No medium found") {
		t.Fatalf("error %q must carry the reason", err)
	}
}

func TestActive_Darwin(t *testing.T) {
	p := darwinPaths(t)
	print := "launchctl print " + guiDomain() + "/com.sentra.home"
	cases := []struct {
		name    string
		resp    map[string]fakeResp
		want    bool
		wantErr bool
		calls   []string
	}{
		{name: "loaded", want: true, calls: []string{print}},
		{
			name:  "not loaded",
			resp:  map[string]fakeResp{print: {out: "Could not find service \"com.sentra.home\" in domain for user gui: 501", code: 113}},
			calls: []string{print},
		},
		{
			name: "legacy launchctl lists the label",
			resp: map[string]fakeResp{print: {out: "Unrecognized subcommand: print", code: 1}},
			want: true, calls: []string{print, "launchctl list com.sentra.home"},
		},
		{
			name: "legacy launchctl does not list the label",
			resp: map[string]fakeResp{
				print:                            {out: "Unrecognized subcommand: print", code: 1},
				"launchctl list com.sentra.home": {out: "Could not find service", code: 1},
			},
			calls: []string{print, "launchctl list com.sentra.home"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{resp: tc.resp}
			got, err := Active(context.Background(), p, f.run)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Active = %v, want %v", got, tc.want)
			}
			wantCalls(t, f.calls, tc.calls)
		})
	}
}

func TestActive_Linux(t *testing.T) {
	p := linuxPaths(t)
	isActive := "systemctl --user is-active sentra-home.timer"
	cases := []struct {
		name    string
		resp    map[string]fakeResp
		want    bool
		wantErr bool
	}{
		{name: "active", resp: map[string]fakeResp{isActive: {out: "active\n"}}, want: true},
		{name: "inactive", resp: map[string]fakeResp{isActive: {out: "inactive\n", code: 3}}},
		{name: "not loaded", resp: map[string]fakeResp{isActive: {out: "inactive\n", code: 4}}},
		{name: "failed", resp: map[string]fakeResp{isActive: {out: "failed\n", code: 3}}},
		// A missing user bus is not "inactive": the caller must know it
		// could not tell, not paint the timer as dead.
		{name: "no bus", resp: map[string]fakeResp{isActive: {out: "Failed to connect to bus: No medium found", code: 1}}, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeRunner{resp: tc.resp}
			got, err := Active(context.Background(), p, f.run)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Active = %v, want %v", got, tc.want)
			}
			wantCalls(t, f.calls, []string{isActive})
		})
	}
}

func TestActive_MissingBinaryIsAnError(t *testing.T) {
	p := linuxPaths(t)
	notFound := errors.New(`exec: "systemctl": executable file not found in $PATH`)
	run := func(context.Context, string, ...string) ([]byte, error) { return nil, notFound }
	if _, err := Active(context.Background(), p, run); !errors.Is(err, notFound) {
		t.Fatalf("Active err = %v, want wrapped %v", err, notFound)
	}
}

// ActivateCommand / DeactivateCommand are what status output and docs
// print for the operator, so they must match what Activate/Deactivate run.
func TestActivateCommand_MatchesWhatActivateRuns(t *testing.T) {
	if got, want := ActivateCommand(linuxPaths(t)), "systemctl --user daemon-reload && systemctl --user enable --now sentra-home.timer"; got != want {
		t.Fatalf("linux ActivateCommand = %q, want %q", got, want)
	}
	if got, want := ActivateCommand(darwinPaths(t)), "launchctl bootstrap "+guiDomain()+" /Users/op/Library/LaunchAgents/com.sentra.home.plist"; got != want {
		t.Fatalf("darwin ActivateCommand = %q, want %q", got, want)
	}
	if got, want := DeactivateCommand(linuxPaths(t)), "systemctl --user disable --now sentra-home.timer"; got != want {
		t.Fatalf("linux DeactivateCommand = %q, want %q", got, want)
	}
	if got, want := DeactivateCommand(darwinPaths(t)), "launchctl bootout "+guiDomain()+"/com.sentra.home"; got != want {
		t.Fatalf("darwin DeactivateCommand = %q, want %q", got, want)
	}
}

// ExecRunner is the one production seam that shells out: it must return
// combined output and an exit-coded error so the detection above works on
// real processes too.
func TestExecRunner_CombinedOutputAndExitCode(t *testing.T) {
	out, err := ExecRunner(context.Background(), "sh", "-c", "echo out; echo err 1>&2; exit 3")
	if !strings.Contains(string(out), "out") || !strings.Contains(string(out), "err") {
		t.Fatalf("combined output = %q", out)
	}
	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() != 3 {
		t.Fatalf("err = %v, want exit code 3", err)
	}
}

func TestExecRunner_MissingBinary(t *testing.T) {
	_, err := ExecRunner(context.Background(), "sentra-no-such-binary-xyz")
	if err == nil {
		t.Fatal("ExecRunner must fail for a missing binary")
	}
}
