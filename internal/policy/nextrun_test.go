package policy

import (
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// base is Tuesday 2026-03-10 14:30 UTC. (2026-01-01 is a Thursday;
// Feb 1 and Mar 1 land on Sundays, so Mar 10 is a Tuesday.)
var base = time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC)

func TestNextRun(t *testing.T) {
	cases := []struct {
		name string
		s    config.PolicySchedule
		now  time.Time
		want time.Time
		ok   bool
	}{
		{"manual never fires", config.PolicySchedule{Cadence: "manual"}, base, time.Time{}, false},
		{"empty cadence is manual", config.PolicySchedule{}, base, time.Time{}, false},
		{"hourly next top of hour", config.PolicySchedule{Cadence: "hourly"}, base,
			time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC), true},
		{"hourly on the boundary rolls forward", config.PolicySchedule{Cadence: "hourly"},
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
			time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC), true},
		{"daily later today", config.PolicySchedule{Cadence: "daily", At: "15:00"}, base,
			time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC), true},
		{"daily already past rolls to tomorrow", config.PolicySchedule{Cadence: "daily", At: "09:00"}, base,
			time.Date(2026, 3, 11, 9, 0, 0, 0, time.UTC), true},
		{"weekly later today", config.PolicySchedule{Cadence: "weekly", Weekday: "tue", At: "15:00"}, base,
			time.Date(2026, 3, 10, 15, 0, 0, 0, time.UTC), true},
		{"weekly past today wraps a week", config.PolicySchedule{Cadence: "weekly", Weekday: "tue", At: "09:00"}, base,
			time.Date(2026, 3, 17, 9, 0, 0, 0, time.UTC), true},
		{"weekly other day", config.PolicySchedule{Cadence: "weekly", Weekday: "mon", At: "09:00"}, base,
			time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC), true},
		{"monthly past the 1st wraps a month", config.PolicySchedule{Cadence: "monthly", At: "09:00"}, base,
			time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC), true},
		{"monthly on the 1st before the clock", config.PolicySchedule{Cadence: "monthly", At: "15:00"},
			time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 1, 15, 0, 0, 0, time.UTC), true},
		{"december monthly wraps the year", config.PolicySchedule{Cadence: "monthly", At: "09:00"},
			time.Date(2026, 12, 2, 10, 0, 0, 0, time.UTC),
			time.Date(2027, 1, 1, 9, 0, 0, 0, time.UTC), true},
		{"malformed clock degrades to no-next-run", config.PolicySchedule{Cadence: "daily", At: "9:00"}, base, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := NextRun(tc.s, tc.now)
			if ok != tc.ok || !got.Equal(tc.want) {
				t.Fatalf("NextRun(%+v, %s) = %s, %t; want %s, %t",
					tc.s, tc.now, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// Hourly must be a WALL-clock boundary. Absolute-time rounding
// (time.Truncate) lands on :30 in a half-hour-offset zone; the OS
// timers fire on local wall time, so NextRun must too.
func TestNextRunHourlyHalfHourZone(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 3, 10, 14, 30, 0, 0, ist)
	got, ok := NextRun(config.PolicySchedule{Cadence: "hourly"}, now)
	want := time.Date(2026, 3, 10, 15, 0, 0, 0, ist)
	if !ok || !got.Equal(want) {
		t.Fatalf("got %s, %t; want %s", got, ok, want)
	}
}
