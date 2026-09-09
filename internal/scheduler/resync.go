package scheduler

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// target resolves the trio every timer write needs — the OS-specific
// file paths, the absolute sentra executable, and the ABSOLUTE config
// path — from the raw seams both surfaces hold (goos/home/exe overrides
// for tests, "" for production defaults). One prelude, so the CLI and
// the TUI cannot drift on which of the three they remember to resolve:
// a relative cfgPath rendered into a plist is a timer that runs
// `--config sentra.yaml` from `/` and finds nothing.
func target(goos, home, exeOverride, cfgPath, name string) (Paths, string, string, error) {
	paths, err := PathsFor(goos, home, name)
	if err != nil {
		return Paths{}, "", "", err
	}
	exe, err := Executable(exeOverride)
	if err != nil {
		return Paths{}, "", "", err
	}
	absCfg, err := filepath.Abs(cfgPath)
	if err != nil {
		return Paths{}, "", "", fmt.Errorf("resolve config path: %w", err)
	}
	return paths, exe, absCfg, nil
}

// InstallFor renders, writes, and loads the timer for policy name from
// the raw seams (see target). Writing the files alone waits for the
// next login (launchd) or forever (an un-enabled systemd timer), so the
// job is activated in the same call; on an activation failure the files
// stay in place and the error names the command to finish with, which
// the callers show rather than promising a repeat they cannot keep.
//
// The returned Paths names the files on disk once Install succeeded —
// including alongside an *ActivationError, which is exactly when a
// caller reporting "installed, but not active" needs the list. It is
// zero on any earlier failure, when nothing was written.
func InstallFor(ctx context.Context, goos, home, exeOverride, cfgPath, name string, schedule config.PolicySchedule, run Runner) (Paths, error) {
	paths, exe, absCfg, err := target(goos, home, exeOverride, cfgPath, name)
	if err != nil {
		return Paths{}, err
	}
	files, err := Render(paths, exe, absCfg, name, schedule)
	if err != nil {
		return Paths{}, err
	}
	if err := Install(files); err != nil {
		return Paths{}, err
	}
	return paths, Activate(ctx, paths, run)
}

// ResyncFor is Resync from the raw seams (see target): the CLI's
// `policy add --replace` and the TUI's job edit both call this so the
// prelude lives once.
func ResyncFor(ctx context.Context, goos, home, exeOverride, cfgPath, name string, schedule config.PolicySchedule, run Runner) (SyncOutcome, error) {
	paths, exe, absCfg, err := target(goos, home, exeOverride, cfgPath, name)
	if err != nil {
		return SyncSkipped, err
	}
	return Resync(ctx, paths, exe, absCfg, schedule, run)
}

// SyncOutcome is what Resync did to an installed timer.
type SyncOutcome int

const (
	// SyncSkipped: no timer was installed, so there was nothing to
	// reconcile — a manual policy stays manual, a scheduled one waits
	// for `schedule install`.
	SyncSkipped SyncOutcome = iota
	// SyncUninstalled: the schedule became manual, so the timer was
	// unloaded and its files removed.
	SyncUninstalled
	// SyncReinstalled: the files were re-rendered for the new schedule
	// and the OS job bootstrapped again.
	SyncReinstalled
)

func (o SyncOutcome) String() string {
	switch o {
	case SyncUninstalled:
		return "uninstalled"
	case SyncReinstalled:
		return "reinstalled"
	default:
		return "skipped"
	}
}

// Resync reconciles an installed timer with a policy's schedule after
// the policy was rewritten (`policy add --replace`, the TUI's edit).
// Editing sentra.yaml changes what `schedule status` reports, but the
// OS keeps firing whatever calendar it loaded: launchd runs the OLD
// plist until the label is bootstrapped again, and systemd needs a
// daemon-reload to see a new OnCalendar. Callers should skip the call
// when the schedule spec did not change — a no-op bootout/bootstrap
// still costs a launchctl round trip. Not installed is not an error.
func Resync(ctx context.Context, paths Paths, exe, cfgPath string, schedule config.PolicySchedule, run Runner) (SyncOutcome, error) {
	installed, err := Installed(paths)
	if err != nil || !installed {
		return SyncSkipped, err
	}
	if policycfg.NormalizeSchedule(schedule).Cadence == policycfg.CadenceManual {
		// Unload before removing the files, or the OS keeps firing the
		// old cadence until logout.
		if err := Deactivate(ctx, paths, run); err != nil {
			return SyncSkipped, err
		}
		if err := Uninstall(paths); err != nil {
			return SyncSkipped, err
		}
		return SyncUninstalled, nil
	}
	files, err := Render(paths, exe, cfgPath, paths.Name, schedule)
	if err != nil {
		return SyncSkipped, fmt.Errorf("render timer: %w", err)
	}
	if err := Install(files); err != nil {
		return SyncSkipped, err
	}
	if err := Activate(ctx, paths, run); err != nil {
		return SyncReinstalled, err
	}
	return SyncReinstalled, nil
}
