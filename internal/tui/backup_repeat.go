package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// nextRepeat cycles the backup flow's repeat cadence: off → daily →
// weekly → monthly → off. The simple periods only, deliberately — the
// policies view owns the full schedule vocabulary (hourly, weekday, at).
func nextRepeat(cur string) string {
	switch cur {
	case "":
		return policycfg.CadenceDaily
	case policycfg.CadenceDaily:
		return policycfg.CadenceWeekly
	case policycfg.CadenceWeekly:
		return policycfg.CadenceMonthly
	default:
		return ""
	}
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

// installRepeat persists a named policy for root (paths + tag + the armed
// cadence) into sentra.yaml and installs the OS scheduler entry that runs
// it. It is the backup flow's bridge to the policy/scheduler machinery the
// Settings-side views manage — one confirm arms the standing schedule the
// operator asked for.
//
// Naming: the policy is the directory's basename, sanitized. A clash with
// an existing policy pointing somewhere ELSE is uniquified (name-2,
// name-3, …), never clobbered; the SAME directory reuses its policy —
// cadence and tag refresh, config-authored hooks survive, mirroring
// `policy add --replace`. The collision check runs inside config.Update's
// closure, against the on-disk map, so a concurrent edit can't be lost.
func (v BackupView) installRepeat(root string) error {
	if strings.TrimSpace(v.deps.ConfigPath) == "" {
		return fmt.Errorf("no config file to hold the policy — run setup first")
	}
	// The simple cadences run off-hours by default: 02:00, Sundays for
	// weekly, the 1st for monthly (the scheduler fixes the day). An
	// operator who wants a different clock edits the policy afterwards —
	// that vocabulary belongs to the policies view, not this flow.
	schedule := config.PolicySchedule{Cadence: v.repeat, At: "02:00"}
	if v.repeat == policycfg.CadenceWeekly {
		schedule.Weekday = "sun"
	}
	schedule = policycfg.NormalizeSchedule(schedule)
	tag := strings.TrimSpace(v.tag.Value())

	name := repeatPolicyName(filepath.Base(root))
	var chosen string
	err := config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		chosen = name
		for i := 2; ; i++ {
			existing, exists := cfg.Policies[chosen]
			if !exists || (len(existing.Paths) == 1 && existing.Paths[0] == root) {
				break
			}
			chosen = fmt.Sprintf("%s-%d", name, i)
		}
		p := cfg.Policies[chosen] // zero value when new; hooks survive when reused
		p.Paths = []string{root}
		p.Tags = nil
		if tag != "" {
			p.Tags = []string{tag}
		}
		p.Schedule = schedule
		cfg.Policies[chosen] = p
		return nil
	})
	if err != nil {
		return err
	}

	paths, err := scheduler.PathsFor(v.schedGOOS, v.schedHome, chosen)
	if err != nil {
		return err
	}
	exe, err := scheduler.Executable(v.schedExe)
	if err != nil {
		return err
	}
	files, err := scheduler.Render(paths, exe, v.deps.ConfigPath, chosen, schedule)
	if err != nil {
		return err
	}
	if err := scheduler.Install(files); err != nil {
		return err
	}

	// Keep the shared resolved config coherent so the Policies/Schedule
	// views list the new policy without a relaunch. Disk is already the
	// source of truth (config.Update above); this mirrors it in memory.
	if v.deps.Config != nil {
		if v.deps.Config.Policies == nil {
			v.deps.Config.Policies = map[string]config.PolicyConfig{}
		}
		p := v.deps.Config.Policies[chosen]
		p.Paths = []string{root}
		p.Tags = nil
		if tag != "" {
			p.Tags = []string{tag}
		}
		p.Schedule = schedule
		v.deps.Config.Policies[chosen] = p
	}
	return nil
}
