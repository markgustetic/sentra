package policy

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestValidate_AcceptsDailyPolicy(t *testing.T) {
	p := config.PolicyConfig{
		Paths: []string{"~/Documents"},
		Tags:  []string{"home", "daily"},
		Schedule: config.PolicySchedule{
			Cadence: "daily",
			At:      "03:00",
		},
		AfterBackup: config.PolicyAfterBackup{
			Check: true,
			Prune: "dry-run",
		},
	}
	if err := Validate("home", p); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidate_RejectsInvalidName(t *testing.T) {
	p := config.PolicyConfig{Paths: []string{"."}}
	err := Validate("../bad", p)
	if err == nil || !strings.Contains(err.Error(), "name") {
		t.Fatalf("Validate error: got %v, want name error", err)
	}
}

func TestValidate_RejectsMissingPaths(t *testing.T) {
	err := Validate("home", config.PolicyConfig{})
	if err == nil || !strings.Contains(err.Error(), "path") {
		t.Fatalf("Validate error: got %v, want path error", err)
	}
}

func TestValidate_RejectsUnsupportedCadence(t *testing.T) {
	p := config.PolicyConfig{
		Paths:    []string{"."},
		Schedule: config.PolicySchedule{Cadence: "yearly"},
	}
	err := Validate("home", p)
	if err == nil || !strings.Contains(err.Error(), "cadence") {
		t.Fatalf("Validate error: got %v, want cadence error", err)
	}
}

func TestValidate_RejectsInvalidClock(t *testing.T) {
	p := config.PolicyConfig{
		Paths: []string{"."},
		Schedule: config.PolicySchedule{
			Cadence: "daily",
			At:      "25:00",
		},
	}
	err := Validate("home", p)
	if err == nil || !strings.Contains(err.Error(), "HH:MM") {
		t.Fatalf("Validate error: got %v, want HH:MM error", err)
	}
}

// TestValidClock_RejectsSignedComponents: strconv.Atoi accepts a leading
// sign, so "+9" parses to 9 and would pass a naive range check while
// producing a malformed systemd OnCalendar spec ("*-*-* +9:00:00") that
// silently never fires. validClock must require exactly two ASCII digits
// per component.
func TestValidClock_RejectsSignedComponents(t *testing.T) {
	for _, bad := range []string{"+9:00", "09:+0", "+0:05", "-1:30", "1x:00"} {
		if validClock(bad) {
			t.Errorf("validClock(%q) = true, want false", bad)
		}
	}
	for _, good := range []string{"00:00", "09:30", "23:59"} {
		if !validClock(good) {
			t.Errorf("validClock(%q) = false, want true", good)
		}
	}
}

func TestValidate_RejectsUnsupportedPruneMode(t *testing.T) {
	p := config.PolicyConfig{
		Paths:       []string{"."},
		AfterBackup: config.PolicyAfterBackup{Prune: "delete-everything"},
	}
	err := Validate("home", p)
	if err == nil || !strings.Contains(err.Error(), "prune") {
		t.Fatalf("Validate error: got %v, want prune error", err)
	}
}

func TestParseScheduleSpec(t *testing.T) {
	cases := map[string]config.PolicySchedule{
		"":                 {Cadence: "manual"},
		"manual":           {Cadence: "manual"},
		"hourly":           {Cadence: "hourly"},
		"daily@03:00":      {Cadence: "daily", At: "03:00"},
		"weekly@mon:04:30": {Cadence: "weekly", Weekday: "mon", At: "04:30"},
		"monthly@05:15":    {Cadence: "monthly", At: "05:15"},
	}
	for in, want := range cases {
		t.Run(in, func(t *testing.T) {
			got, err := ParseScheduleSpec(in)
			if err != nil {
				t.Fatalf("ParseScheduleSpec: %v", err)
			}
			if got != want {
				t.Fatalf("schedule: got %+v, want %+v", got, want)
			}
		})
	}
}

func TestFormatScheduleSpec(t *testing.T) {
	cases := map[config.PolicySchedule]string{
		{Cadence: "manual"}:                              "manual",
		{Cadence: "hourly"}:                              "hourly",
		{Cadence: "daily", At: "03:00"}:                  "daily@03:00",
		{Cadence: "weekly", Weekday: "mon", At: "04:30"}: "weekly@mon:04:30",
		{Cadence: "monthly", At: "05:15"}:                "monthly@05:15",
	}
	for in, want := range cases {
		t.Run(want, func(t *testing.T) {
			if got := FormatScheduleSpec(in); got != want {
				t.Fatalf("FormatScheduleSpec: got %q, want %q", got, want)
			}
		})
	}
}
