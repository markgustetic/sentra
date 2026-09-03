package scheduler

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner executes one scheduler-control command (launchctl on darwin,
// systemctl on linux) and returns its combined stdout+stderr. The error
// should expose the process exit status through an ExitCode() method —
// *exec.ExitError does — because the activation helpers tell "the OS
// refused" (a status they can interpret) apart from "the command never
// ran" (a missing binary, a cancelled context) by that method. Production
// passes nil, which selects ExecRunner; tests inject a recorder so the
// suite never shells out and never loads a real job on the developer's
// machine.
type Runner func(ctx context.Context, name string, args ...string) ([]byte, error)

// execTimeout bounds one launchctl/systemctl call. Both normally return in
// milliseconds; a hung user bus must not wedge the TUI's reload or a CLI
// command forever.
const execTimeout = 15 * time.Second

// ExecRunner is the production Runner: os/exec with combined output and a
// hard timeout.
func ExecRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, execTimeout)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput() //nolint:gosec // argv is built from validated policy names and our own paths
}

func orExec(run Runner) Runner {
	if run == nil {
		return ExecRunner
	}
	return run
}

// ActivationError reports that the OS scheduler did not accept (or release)
// an already-written set of files. The files stay on disk on purpose: a
// headless SSH session, a missing user bus, or an old launchctl can all
// refuse a perfectly good unit, and the fix is for the operator to run
// Command once a session exists — so the message carries it verbatim.
type ActivationError struct {
	// Op is "activate" or "deactivate".
	Op string
	// Command is the exact shell line the operator can run by hand.
	Command string
	// Reason is the first meaningful output line, else the runner error.
	Reason string
	// Err is the underlying runner error, for errors.Is/As.
	Err error
}

func (e *ActivationError) Error() string {
	switch e.Op {
	case "deactivate":
		return fmt.Sprintf("deactivate timer: files removed, but the OS job may still be loaded (%s); run: %s", e.Reason, e.Command)
	default:
		return fmt.Sprintf("activate timer: files written, but the OS did not load them (%s); run: %s", e.Reason, e.Command)
	}
}

func (e *ActivationError) Unwrap() error { return e.Err }

func activationError(op, command string, out []byte, err error) error {
	reason := firstLine(out)
	if reason == "" && err != nil {
		reason = err.Error()
	}
	return &ActivationError{Op: op, Command: command, Reason: reason, Err: err}
}

// Activate loads the files Install wrote into the user's scheduler so the
// timer fires without a re-login. darwin: `launchctl bootout` (so a
// re-install after an edit picks up the new plist — bootstrap refuses an
// already-loaded label) then `launchctl bootstrap gui/$UID <plist>`, falling
// back to the legacy `launchctl unload` / `load -w` pair when this launchctl
// predates bootstrap. linux: `systemctl --user daemon-reload` then
// `systemctl --user enable --now <timer>`. A failure is an *ActivationError
// naming the command to run by hand; the files are left in place.
func Activate(ctx context.Context, paths Paths, run Runner) error {
	run = orExec(run)
	switch paths.OS {
	case "darwin":
		return activateLaunchd(ctx, paths, run)
	case "linux":
		return activateSystemd(ctx, paths, run)
	default:
		return fmt.Errorf("unsupported scheduler OS %q", paths.OS)
	}
}

// Deactivate unloads the timer so an Uninstall does not leave a live job
// behind until logout. darwin: `launchctl bootout gui/$UID/<label>` (an
// unloaded label is already the desired state, not an error), legacy
// `launchctl unload <plist>`. linux: `systemctl --user disable --now
// <timer>`. Call it BEFORE removing the files: systemd resolves the
// [Install] section from the unit on disk, and launchd's legacy unload
// takes the plist path.
func Deactivate(ctx context.Context, paths Paths, run Runner) error {
	run = orExec(run)
	switch paths.OS {
	case "darwin":
		return deactivateLaunchd(ctx, paths, run)
	case "linux":
		return deactivateSystemd(ctx, paths, run)
	default:
		return fmt.Errorf("unsupported scheduler OS %q", paths.OS)
	}
}

// Active reports whether the OS scheduler currently has the timer loaded —
// the truth Installed cannot see, since files under LaunchAgents or
// systemd/user do nothing until launchd/systemd picks them up. A false with
// a nil error means the OS answered "not loaded"; an error means the
// question could not be asked (no user bus, no launchctl), and callers
// should say "unknown" rather than "inactive".
func Active(ctx context.Context, paths Paths, run Runner) (bool, error) {
	run = orExec(run)
	switch paths.OS {
	case "darwin":
		return activeLaunchd(ctx, paths, run)
	case "linux":
		return activeSystemd(ctx, paths, run)
	default:
		return false, fmt.Errorf("unsupported scheduler OS %q", paths.OS)
	}
}

// ActivateCommand is the shell line Activate runs, for status output and
// operator hints ("installed but not active — run: …").
func ActivateCommand(paths Paths) string {
	switch paths.OS {
	case "darwin":
		return "launchctl bootstrap " + launchdDomain() + " " + paths.Files[0]
	case "linux":
		return "systemctl --user daemon-reload && systemctl --user enable --now " + systemdTimerUnit(paths)
	default:
		return ""
	}
}

// DeactivateCommand is the shell line Deactivate runs.
func DeactivateCommand(paths Paths) string {
	switch paths.OS {
	case "darwin":
		return "launchctl bootout " + launchdDomain() + "/" + launchdLabel(paths)
	case "linux":
		return "systemctl --user disable --now " + systemdTimerUnit(paths)
	default:
		return ""
	}
}

// --- launchd -------------------------------------------------------------

func launchdDomain() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func launchdLabel(paths Paths) string { return "com.sentra." + paths.Name }

func activateLaunchd(ctx context.Context, paths Paths, run Runner) error {
	plist := paths.Files[0]
	// Best effort: the label is usually not loaded yet, and an old
	// launchctl rejects the subcommand — either way bootstrap decides.
	_, _ = run(ctx, "launchctl", "bootout", launchdDomain()+"/"+launchdLabel(paths))
	out, err := run(ctx, "launchctl", "bootstrap", launchdDomain(), plist)
	if err == nil {
		return nil
	}
	if !unknownSubcommand(out) {
		return activationError("activate", ActivateCommand(paths), out, err)
	}
	_, _ = run(ctx, "launchctl", "unload", plist)
	out, err = run(ctx, "launchctl", "load", "-w", plist)
	if err != nil {
		return activationError("activate", "launchctl load -w "+plist, out, err)
	}
	return nil
}

func deactivateLaunchd(ctx context.Context, paths Paths, run Runner) error {
	out, err := run(ctx, "launchctl", "bootout", launchdDomain()+"/"+launchdLabel(paths))
	switch {
	case err == nil, noSuchProcess(out, err):
		return nil
	case unknownSubcommand(out):
		plist := paths.Files[0]
		out, err = run(ctx, "launchctl", "unload", plist)
		if err != nil {
			return activationError("deactivate", "launchctl unload "+plist, out, err)
		}
		return nil
	default:
		return activationError("deactivate", DeactivateCommand(paths), out, err)
	}
}

func activeLaunchd(ctx context.Context, paths Paths, run Runner) (bool, error) {
	out, err := run(ctx, "launchctl", "print", launchdDomain()+"/"+launchdLabel(paths))
	if err == nil {
		return true, nil
	}
	if !exited(err) {
		return false, fmt.Errorf("query launchd: %w", err)
	}
	if !unknownSubcommand(out) {
		return false, nil
	}
	_, err = run(ctx, "launchctl", "list", launchdLabel(paths))
	if err == nil {
		return true, nil
	}
	if !exited(err) {
		return false, fmt.Errorf("query launchd: %w", err)
	}
	return false, nil
}

// --- systemd -------------------------------------------------------------

func systemdTimerUnit(paths Paths) string { return "sentra-" + paths.Name + ".timer" }

func activateSystemd(ctx context.Context, paths Paths, run Runner) error {
	if out, err := run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return activationError("activate", ActivateCommand(paths), out, err)
	}
	if out, err := run(ctx, "systemctl", "--user", "enable", "--now", systemdTimerUnit(paths)); err != nil {
		return activationError("activate", ActivateCommand(paths), out, err)
	}
	return nil
}

func deactivateSystemd(ctx context.Context, paths Paths, run Runner) error {
	if out, err := run(ctx, "systemctl", "--user", "disable", "--now", systemdTimerUnit(paths)); err != nil {
		return activationError("deactivate", DeactivateCommand(paths), out, err)
	}
	return nil
}

func activeSystemd(ctx context.Context, paths Paths, run Runner) (bool, error) {
	out, err := run(ctx, "systemctl", "--user", "is-active", systemdTimerUnit(paths))
	if err == nil {
		return true, nil
	}
	if !exited(err) {
		return false, fmt.Errorf("query systemd: %w", err)
	}
	// is-active prints the unit state on its first line; anything that is
	// not a state (a bus error, a usage message) means we could not ask.
	switch firstLine(out) {
	case "inactive", "failed", "unknown", "deactivating", "activating", "reloading":
		return false, nil
	}
	return false, fmt.Errorf("query systemd: %s: %w", firstLine(out), err)
}

// --- helpers -------------------------------------------------------------

// exited reports whether err carries a process exit status, i.e. the
// command ran and the OS answered — as opposed to a missing binary or a
// dead context, which say nothing about the timer.
func exited(err error) bool {
	var ec interface{ ExitCode() int }
	return errors.As(err, &ec)
}

// unknownSubcommand recognizes a pre-10.10 launchctl rejecting bootstrap /
// bootout / print, which is the cue to fall back to load / unload / list.
func unknownSubcommand(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "unrecognized subcommand") || strings.Contains(s, "unknown subcommand")
}

// noSuchProcess recognizes bootout of a label that is not loaded — the
// state Deactivate wants, so it is success, not failure.
func noSuchProcess(out []byte, err error) bool {
	if strings.Contains(string(out), "No such process") {
		return true
	}
	var ec interface{ ExitCode() int }
	return errors.As(err, &ec) && ec.ExitCode() == 3
}

func firstLine(out []byte) string {
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
