package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// repeatInstallOpName is the guard name of the schedule install, distinct
// from "backup" so an opRejectedMsg bounces the stage that armed it.
const repeatInstallOpName = "backup-schedule"

// repeatInstalledMsg is the guard-clearing result of the schedule install.
// On success it carries what the wizard mirrors into the shared resolved
// config and records for the done screen; err returns the wizard to
// Confirm with the reason.
type repeatInstalledMsg struct {
	root     string
	name     string
	schedule config.PolicySchedule
	tags     []string
	err      error
}

func (repeatInstalledMsg) opResult() {}

// startRepeatInstall leaves Confirm for the Installing stage and hands
// installRepeat to the App's one-op guard. It used to run inline in
// confirmRun: a config write is quick, but scheduler.Activate execs
// launchctl/systemctl with a 15s timeout per call — up to a minute across
// bootout/bootstrap/fallback — and inline in Update that froze the whole
// TUI with no esc. Under the guard it is serialized with every other
// mutation, cancellable, and drawn with a spinner. Leaving Confirm blurs
// the tag: nothing renders it on the Installing stage.
//
// root is resolved ONCE here and the same value goes to both the op and
// the result: installRepeat resolving on its own would let the path on
// disk and the in-memory mirror finishRepeatInstall applies diverge. A
// refused root (a tilde with no home) goes through the guard like any
// other failure: the op returns it in repeatInstalledMsg and
// finishRepeatInstall lands on Confirm with the error shown.
func (v BackupView) startRepeatInstall(root, name string, schedule config.PolicySchedule, tag string) (tea.Model, tea.Cmd) {
	v.confirm.blur()
	v.stage = backupInstalling
	v.pathErr = ""
	schedule = policycfg.NormalizeSchedule(schedule)
	var tags []string
	if tag = strings.TrimSpace(tag); tag != "" {
		tags = []string{tag}
	}
	root, rootErr := policycfg.ResolvePath(root)
	install := v.installRepeat
	start := startOpMsg{
		name: repeatInstallOpName,
		run: func(ctx context.Context) tea.Msg {
			if rootErr != nil {
				return repeatInstalledMsg{name: name, err: rootErr}
			}
			err := install(ctx, root, name, schedule, tag)
			return repeatInstalledMsg{root: root, name: name, schedule: schedule, tags: tags, err: err}
		},
	}
	return v, tea.Batch(func() tea.Msg { return start }, v.spin.Tick)
}

// finishRepeatInstall consumes the install result. A failure returns to
// Confirm with the reason, the tag re-focused for the retry. Success
// mirrors the policy into the shared resolved config — here, on the UI
// goroutine, never inside the op, because deps.Config is read by every
// view and a write from the op's goroutine would race them — records the
// done screen's "next run", and starts the backup itself through its own
// guarded startOpMsg; the guard has already cleared on this result, so
// that start is accepted.
func (v BackupView) finishRepeatInstall(msg repeatInstalledMsg) (tea.Model, tea.Cmd) {
	if v.stage != backupInstalling {
		return v, nil
	}
	if msg.err != nil {
		v.stage = backupConfirm
		v.pathErr = "could not install the schedule: " + msg.err.Error()
		cmd := v.confirm.refocus()
		return v, cmd
	}
	if v.deps.Config != nil {
		if v.deps.Config.Policies == nil {
			v.deps.Config.Policies = map[string]config.PolicyConfig{}
		}
		p := v.deps.Config.Policies[msg.name]
		p.Paths = []string{msg.root}
		p.Tags = msg.tags
		p.Schedule = msg.schedule
		v.deps.Config.Policies[msg.name] = p
	}
	v.installedName = msg.name
	v.installedNext, v.installedNextOK = policycfg.NextRun(msg.schedule, v.clock())
	return v.startBackup(v.pending)
}

// repeatPolicyName derives a policy name from a directory basename that is
// safe for config keys, scheduler labels, and generated filenames (see
// policy.ValidateName): every disallowed rune becomes '-', leading
// non-alphanumerics are trimmed, and an empty result falls back to
// "backup".
func repeatPolicyName(base string) string {
	var b strings.Builder
	for _, r := range base {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	name := strings.Trim(b.String(), "-_")
	if name == "" || policycfg.ValidateName(name) != nil {
		return "backup"
	}
	return name
}

// installRepeat persists policy `name` for root (path + tag + schedule)
// into sentra.yaml and installs the OS scheduler entry that runs it. The
// wizard's Schedule step resolved the name and cadence; this only writes
// them. The collision check inside config.Update's closure runs against the
// on-disk map (a concurrent edit can't be lost) and REFUSES a name owned by
// another directory rather than uniquifying: the operator just confirmed
// this name, and silently renaming it would lie to them. The same
// directory reuses its policy — cadence and tag refresh, config-authored
// hooks survive, mirroring `policy add --replace`. It runs inside the
// guarded op (see startRepeatInstall) and touches nothing but disk and the
// OS scheduler; the in-memory mirror happens on the result.
func (v BackupView) installRepeat(ctx context.Context, root, name string, schedule config.PolicySchedule, tag string) error {
	if strings.TrimSpace(v.deps.ConfigPath) == "" {
		return fmt.Errorf("no config file to hold the policy — run setup first")
	}
	// The picker hands over absolute directories; the chat intent may
	// not. Resolve before the collision check and the write, so the
	// policy the timer runs names the directory that was confirmed
	// (idempotent on the already-resolved root startRepeatInstall
	// passes). A tilde with no home is an error, never a cwd guess.
	root, err := policycfg.ResolvePath(root)
	if err != nil {
		return err
	}
	schedule = policycfg.NormalizeSchedule(schedule)
	var tags []string
	if tag = strings.TrimSpace(tag); tag != "" {
		tags = []string{tag}
	}
	err = config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		// The stored entry may predate symlink-resolved roots (an older
		// build wrote /tmp/docs where the wizard now holds
		// /private/tmp/docs), so compare it through the same resolver
		// rather than as a raw string.
		home, _ := os.UserHomeDir()
		if existing, exists := cfg.Policies[name]; exists &&
			(len(existing.Paths) != 1 || policycfg.NormalizePath(existing.Paths[0], home) != root) {
			return fmt.Errorf("policy %q already backs up %s", name, strings.Join(existing.Paths, ", "))
		}
		p := cfg.Policies[name] // zero value when new; hooks survive when reused
		p.Paths = []string{root}
		p.Tags = tags
		p.Schedule = schedule
		cfg.Policies[name] = p
		return nil
	})
	if err != nil {
		return err
	}
	// Render, write, and load in one call: the files alone wait for the
	// next login (launchd) or never fire (an un-enabled systemd timer). A
	// failure leaves the policy and files in place and names the command;
	// the wizard shows it instead of starting a run it cannot promise to
	// repeat.
	_, err = scheduler.InstallFor(ctx, v.schedGOOS, v.schedHome, v.schedExe, v.deps.ConfigPath, name, schedule, v.deps.SchedulerRunner)
	return err
}
