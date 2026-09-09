package scheduler

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// TestResyncFor_ResolvesTargetLikeInstallFor: the two surfaces used to
// spell the PathsFor + Executable + Abs(cfgPath) prelude by hand before
// every timer write, and a relative config path slipped through one of
// them into the plist. Both helpers resolve the target once, the same
// way: an installed timer is re-rendered with the ABSOLUTE config path
// and the given executable, and a fresh install lands the same files.
func TestResyncFor_ResolvesTargetLikeInstallFor(t *testing.T) {
	daily := config.PolicySchedule{Cadence: policycfg.CadenceDaily, At: "03:00"}
	hourly := config.PolicySchedule{Cadence: policycfg.CadenceHourly}
	home := t.TempDir()
	t.Chdir(t.TempDir())
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	wantCfg := filepath.Join(cwd, "sentra.yaml")

	run := &fakeRunner{}
	if err := InstallFor(context.Background(), "darwin", home, "/opt/sentra", "sentra.yaml", "home", daily, run.run); err != nil {
		t.Fatalf("InstallFor: %v", err)
	}
	paths, err := PathsFor("darwin", home, "home")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(paths.Files[0]) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatalf("InstallFor wrote no plist: %v", err)
	}
	for _, want := range []string{wantCfg, "/opt/sentra", "<key>Hour</key>"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("installed plist lacks %q:\n%s", want, raw)
		}
	}
	if joined := strings.Join(run.calls, "\n"); !strings.Contains(joined, "launchctl bootstrap") {
		t.Errorf("InstallFor never bootstrapped the job; calls:\n%s", joined)
	}

	run = &fakeRunner{}
	got, err := ResyncFor(context.Background(), "darwin", home, "/opt/sentra", "sentra.yaml", "home", hourly, run.run)
	if err != nil {
		t.Fatalf("ResyncFor: %v", err)
	}
	if got != SyncReinstalled {
		t.Fatalf("outcome: got %v, want %v", got, SyncReinstalled)
	}
	raw, err = os.ReadFile(paths.Files[0]) //nolint:gosec // test-owned path
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), wantCfg) || !strings.Contains(string(raw), "<key>Minute</key>") {
		t.Errorf("resynced plist not re-rendered with the absolute config path and the new cadence:\n%s", raw)
	}
	if got, err := ResyncFor(context.Background(), "plan9", home, "/opt/sentra", "sentra.yaml", "home", hourly, run.run); err == nil || got != SyncSkipped {
		t.Errorf("unsupported OS: got %v, %v; want SyncSkipped with an error", got, err)
	}
}

// TestResync pins the rule for a schedule edit against an installed
// timer: not installed → nothing touched; edited to manual → the OS job
// is unloaded and the files removed; any other edit → re-rendered,
// reinstalled, and re-bootstrapped. Writing the plist alone is not
// enough — launchd keeps running the OLD calendar until the label is
// bootstrapped again — so `schedule status` showing the new time while
// the OS fired at the old one was exactly the bug.
func TestResync(t *testing.T) {
	daily := config.PolicySchedule{Cadence: policycfg.CadenceDaily, At: "03:00"}
	hourly := config.PolicySchedule{Cadence: policycfg.CadenceHourly}
	manual := config.PolicySchedule{Cadence: policycfg.CadenceManual}

	cases := []struct {
		name         string
		installed    bool
		schedule     config.PolicySchedule
		want         SyncOutcome
		wantFile     bool
		wantContains string
		wantRan      []string
		wantNotRan   []string
	}{
		{
			name:       "not installed leaves the OS alone",
			installed:  false,
			schedule:   hourly,
			want:       SyncSkipped,
			wantFile:   false,
			wantNotRan: []string{"launchctl"},
		},
		{
			name:       "installed, edited to manual: unload then remove",
			installed:  true,
			schedule:   manual,
			want:       SyncUninstalled,
			wantFile:   false,
			wantRan:    []string{"launchctl bootout"},
			wantNotRan: []string{"launchctl bootstrap"},
		},
		{
			name:         "installed, new cadence: rewrite then bootstrap",
			installed:    true,
			schedule:     hourly,
			want:         SyncReinstalled,
			wantFile:     true,
			wantContains: "<key>Minute</key>",
			wantRan:      []string{"launchctl bootout", "launchctl bootstrap"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			paths, err := PathsFor("darwin", home, "home")
			if err != nil {
				t.Fatal(err)
			}
			if tc.installed {
				files, err := Render(paths, "/usr/local/bin/sentra", "/cfg/sentra.yaml", "home", daily)
				if err != nil {
					t.Fatal(err)
				}
				if err := Install(files); err != nil {
					t.Fatal(err)
				}
			}
			run := &fakeRunner{}
			got, err := Resync(context.Background(), paths, "/usr/local/bin/sentra", "/cfg/sentra.yaml", tc.schedule, run.run)
			if err != nil {
				t.Fatalf("Resync: %v", err)
			}
			if got != tc.want {
				t.Fatalf("outcome: got %v, want %v", got, tc.want)
			}
			raw, statErr := os.ReadFile(paths.Files[0]) //nolint:gosec // test-owned path
			if exists := statErr == nil; exists != tc.wantFile {
				t.Fatalf("plist present = %v, want %v", exists, tc.wantFile)
			}
			if tc.wantContains != "" && !strings.Contains(string(raw), tc.wantContains) {
				t.Errorf("plist not re-rendered for the new schedule:\n%s", raw)
			}
			if tc.wantContains != "" && strings.Contains(string(raw), "<key>Hour</key>") {
				t.Errorf("plist still carries the old daily calendar:\n%s", raw)
			}
			joined := strings.Join(run.calls, "\n")
			for _, want := range tc.wantRan {
				if !strings.Contains(joined, want) {
					t.Errorf("runner never ran %q; calls:\n%s", want, joined)
				}
			}
			for _, notWant := range tc.wantNotRan {
				if strings.Contains(joined, notWant) {
					t.Errorf("runner ran %q but must not; calls:\n%s", notWant, joined)
				}
			}
		})
	}
}
