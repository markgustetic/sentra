// Package policy validates Sentra's named backup policy configuration.
package policy

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
)

const (
	CadenceManual  = "manual"
	CadenceHourly  = "hourly"
	CadenceDaily   = "daily"
	CadenceWeekly  = "weekly"
	CadenceMonthly = "monthly"

	PruneOff    = "off"
	PruneDryRun = "dry-run"
	PruneApply  = "apply"
)

var policyNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidateName checks a policy name is safe for config keys, scheduler
// labels, and generated filenames.
func ValidateName(name string) error {
	if !policyNameRE.MatchString(name) {
		return fmt.Errorf("policy name %q must start with a letter or number and contain only letters, numbers, '-' or '_'", name)
	}
	return nil
}

// Validate checks one named policy for user-facing semantic errors.
func Validate(name string, p config.PolicyConfig) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if len(p.Paths) == 0 {
		return fmt.Errorf("policy %q must include at least one path", name)
	}
	for i, path := range p.Paths {
		if strings.TrimSpace(path) == "" {
			return fmt.Errorf("policy %q path %d must not be empty", name, i+1)
		}
	}
	for i, tag := range p.Tags {
		if strings.TrimSpace(tag) == "" {
			return fmt.Errorf("policy %q tag %d must not be empty", name, i+1)
		}
		if strings.ContainsAny(tag, "\r\n") {
			return fmt.Errorf("policy %q tag %d must not contain newlines", name, i+1)
		}
	}
	if err := validateSchedule(p.Schedule); err != nil {
		return fmt.Errorf("policy %q schedule: %w", name, err)
	}
	if err := validatePruneMode(p.AfterBackup.Prune); err != nil {
		return fmt.Errorf("policy %q after_backup.prune: %w", name, err)
	}
	return nil
}

// NormalizeSchedule returns the effective schedule with an omitted
// cadence treated as manual.
func NormalizeSchedule(s config.PolicySchedule) config.PolicySchedule {
	s.Cadence = strings.ToLower(strings.TrimSpace(s.Cadence))
	s.At = strings.TrimSpace(s.At)
	s.Weekday = strings.ToLower(strings.TrimSpace(s.Weekday))
	if s.Cadence == "" {
		s.Cadence = CadenceManual
	}
	return s
}

func validateSchedule(s config.PolicySchedule) error {
	s = NormalizeSchedule(s)
	switch s.Cadence {
	case CadenceManual:
		return nil
	case CadenceHourly:
		if s.At != "" || s.Weekday != "" {
			return fmt.Errorf("hourly schedule must not set at or weekday")
		}
		return nil
	case CadenceDaily:
		if !validClock(s.At) {
			return fmt.Errorf("daily schedule requires HH:MM")
		}
		return nil
	case CadenceWeekly:
		if !validWeekday(s.Weekday) {
			return fmt.Errorf("weekly schedule requires weekday mon-sun")
		}
		if !validClock(s.At) {
			return fmt.Errorf("weekly schedule requires HH:MM")
		}
		return nil
	case CadenceMonthly:
		if !validClock(s.At) {
			return fmt.Errorf("monthly schedule requires HH:MM")
		}
		return nil
	default:
		return fmt.Errorf("unsupported cadence %q", s.Cadence)
	}
}

func validatePruneMode(mode string) error {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", PruneOff, PruneDryRun, PruneApply:
		return nil
	default:
		return fmt.Errorf("unsupported prune mode %q", mode)
	}
}

// ParseScheduleSpec parses CLI schedule shorthand:
// manual, hourly, daily@HH:MM, weekly@mon:HH:MM, monthly@HH:MM.
func ParseScheduleSpec(spec string) (config.PolicySchedule, error) {
	spec = strings.ToLower(strings.TrimSpace(spec))
	if spec == "" {
		return config.PolicySchedule{Cadence: CadenceManual}, nil
	}
	cadence, rest, hasRest := strings.Cut(spec, "@")
	if !hasRest {
		s := config.PolicySchedule{Cadence: cadence}
		if err := validateSchedule(s); err != nil {
			return config.PolicySchedule{}, err
		}
		return NormalizeSchedule(s), nil
	}

	s := config.PolicySchedule{Cadence: cadence}
	switch cadence {
	case CadenceDaily, CadenceMonthly:
		s.At = rest
	case CadenceWeekly:
		weekday, at, ok := strings.Cut(rest, ":")
		if !ok {
			return config.PolicySchedule{}, fmt.Errorf("weekly schedule must use weekly@mon:HH:MM")
		}
		s.Weekday = weekday
		s.At = at
	default:
		return config.PolicySchedule{}, fmt.Errorf("unsupported cadence %q", cadence)
	}
	if err := validateSchedule(s); err != nil {
		return config.PolicySchedule{}, err
	}
	return NormalizeSchedule(s), nil
}

// FormatScheduleSpec returns the CLI shorthand for a schedule.
func FormatScheduleSpec(s config.PolicySchedule) string {
	s = NormalizeSchedule(s)
	switch s.Cadence {
	case CadenceHourly, CadenceManual:
		return s.Cadence
	case CadenceWeekly:
		return fmt.Sprintf("%s@%s:%s", s.Cadence, s.Weekday, s.At)
	default:
		return fmt.Sprintf("%s@%s", s.Cadence, s.At)
	}
}

func validClock(s string) bool {
	hour, minute, ok := strings.Cut(s, ":")
	if !ok || len(hour) != 2 || len(minute) != 2 {
		return false
	}
	h, err := strconv.Atoi(hour)
	if err != nil || h < 0 || h > 23 {
		return false
	}
	m, err := strconv.Atoi(minute)
	return err == nil && m >= 0 && m <= 59
}

func validWeekday(s string) bool {
	switch strings.ToLower(s) {
	case "mon", "tue", "wed", "thu", "fri", "sat", "sun":
		return true
	default:
		return false
	}
}
