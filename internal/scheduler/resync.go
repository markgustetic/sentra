package scheduler

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

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
