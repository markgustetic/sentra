package tui

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
)

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
// hooks survive, mirroring `policy add --replace`.
func (v BackupView) installRepeat(root, name string, schedule config.PolicySchedule, tag string) error {
	if strings.TrimSpace(v.deps.ConfigPath) == "" {
		return fmt.Errorf("no config file to hold the policy — run setup first")
	}
	schedule = policycfg.NormalizeSchedule(schedule)
	var tags []string
	if tag = strings.TrimSpace(tag); tag != "" {
		tags = []string{tag}
	}
	err := config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if existing, exists := cfg.Policies[name]; exists &&
			(len(existing.Paths) != 1 || existing.Paths[0] != root) {
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
	paths, err := scheduler.PathsFor(v.schedGOOS, v.schedHome, name)
	if err != nil {
		return err
	}
	exe, err := scheduler.Executable(v.schedExe)
	if err != nil {
		return err
	}
	files, err := scheduler.Render(paths, exe, v.deps.ConfigPath, name, schedule)
	if err != nil {
		return err
	}
	if err := scheduler.Install(files); err != nil {
		return err
	}
	// Mirror disk in the shared resolved config so the Scheduled backups
	// tab lists the policy without a relaunch.
	if v.deps.Config != nil {
		if v.deps.Config.Policies == nil {
			v.deps.Config.Policies = map[string]config.PolicyConfig{}
		}
		p := v.deps.Config.Policies[name]
		p.Paths = []string{root}
		p.Tags = tags
		p.Schedule = schedule
		v.deps.Config.Policies[name] = p
	}
	return nil
}
