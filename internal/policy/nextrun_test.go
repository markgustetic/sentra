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

func TestPreviousRun(t *testing.T) {
	cases := []struct {
		name string
		s    config.PolicySchedule
		now  time.Time
		want time.Time
		ok   bool
	}{
		{"manual never fired", config.PolicySchedule{Cadence: "manual"}, base, time.Time{}, false},
		{"empty cadence is manual", config.PolicySchedule{}, base, time.Time{}, false},
		{"hourly top of this hour", config.PolicySchedule{Cadence: "hourly"}, base,
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), true},
		{"hourly on the boundary is the boundary", config.PolicySchedule{Cadence: "hourly"},
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC),
			time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC), true},
		{"daily earlier today", config.PolicySchedule{Cadence: "daily", At: "09:00"}, base,
			time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC), true},
		{"daily exactly now is at-or-before", config.PolicySchedule{Cadence: "daily", At: "14:30"}, base,
			time.Date(2026, 3, 10, 14, 30, 0, 0, time.UTC), true},
		{"daily still ahead rolls to yesterday", config.PolicySchedule{Cadence: "daily", At: "15:00"}, base,
			time.Date(2026, 3, 9, 15, 0, 0, 0, time.UTC), true},
		{"weekly earlier today", config.PolicySchedule{Cadence: "weekly", Weekday: "tue", At: "09:00"}, base,
			time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC), true},
		{"weekly ahead today wraps a week back", config.PolicySchedule{Cadence: "weekly", Weekday: "tue", At: "15:00"}, base,
			time.Date(2026, 3, 3, 15, 0, 0, 0, time.UTC), true},
		{"weekly other day", config.PolicySchedule{Cadence: "weekly", Weekday: "wed", At: "09:00"}, base,
			time.Date(2026, 3, 4, 9, 0, 0, 0, time.UTC), true},
		{"weekly sunday from tuesday", config.PolicySchedule{Cadence: "weekly", Weekday: "sun", At: "09:00"}, base,
			time.Date(2026, 3, 8, 9, 0, 0, 0, time.UTC), true},
		{"monthly past the 1st is this month", config.PolicySchedule{Cadence: "monthly", At: "09:00"}, base,
			time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC), true},
		{"monthly on the 1st before the clock is last month", config.PolicySchedule{Cadence: "monthly", At: "15:00"},
			time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC),
			time.Date(2026, 2, 1, 15, 0, 0, 0, time.UTC), true},
		{"monthly on the 1st at the clock is today", config.PolicySchedule{Cadence: "monthly", At: "14:30"},
			time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC),
			time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC), true},
		{"january monthly wraps the year back", config.PolicySchedule{Cadence: "monthly", At: "09:00"},
			time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC),
			time.Date(2025, 12, 1, 9, 0, 0, 0, time.UTC), true},
		{"malformed clock degrades to no-previous-run", config.PolicySchedule{Cadence: "daily", At: "9:00"}, base, time.Time{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PreviousRun(tc.s, tc.now)
			if ok != tc.ok || !got.Equal(tc.want) {
				t.Fatalf("PreviousRun(%+v, %s) = %s, %t; want %s, %t",
					tc.s, tc.now, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestPreviousRunIsNeverAfterNow_AndNextRunFollowsIt pins the pair's
// contract as a rule rather than by example: for every cadence and a
// spread of instants, PreviousRun is at-or-before now, NextRun is
// strictly after, and stepping NextRun from the previous slot lands
// exactly on the next — the two functions bracket now with adjacent
// slots. --if-due relies on exactly this bracketing.
func TestPreviousRunIsNeverAfterNow_AndNextRunFollowsIt(t *testing.T) {
	schedules := []config.PolicySchedule{
		{Cadence: "hourly"},
		{Cadence: "daily", At: "03:00"},
		{Cadence: "weekly", Weekday: "mon", At: "04:30"},
		{Cadence: "monthly", At: "00:00"},
	}
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	for _, s := range schedules {
		for now := time.Date(2026, 1, 1, 0, 0, 0, 0, ny); now.Year() == 2026; now = now.Add(7*time.Hour + 13*time.Minute) {
			prev, pok := PreviousRun(s, now)
			next, nok := NextRun(s, now)
			if !pok || !nok {
				t.Fatalf("%+v at %s: ok = %t/%t", s, now, pok, nok)
			}
			if prev.After(now) {
				t.Fatalf("%+v at %s: PreviousRun %s is after now", s, now, prev)
			}
			if !next.After(now) {
				t.Fatalf("%+v at %s: NextRun %s is not after now", s, now, next)
			}
			if step, _ := NextRun(s, prev); !step.Equal(next) {
				t.Fatalf("%+v at %s: NextRun(PreviousRun)=%s, want %s", s, now, step, next)
			}
		}
	}
}

// DST edges: the OS timers fire on local wall time, so the previous
// slot is found by wall-clock arithmetic (AddDate), never by subtracting
// 24h of absolute time — on the day the clocks change those differ.
func TestPreviousRunAcrossDST(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skip("tzdata unavailable")
	}
	daily := config.PolicySchedule{Cadence: "daily", At: "23:30"}
	// Spring forward 2026-03-08 02:00 EST → 03:00 EDT. Noon that day is
	// 23 absolute hours after the previous evening's slot.
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	want := time.Date(2026, 3, 7, 23, 30, 0, 0, ny)
	if got, ok := PreviousRun(daily, now); !ok || !got.Equal(want) {
		t.Fatalf("spring-forward: got %s, %t; want %s", got, ok, want)
	}
	// Fall back 2026-11-01 02:00 EDT → 01:00 EST: 25 absolute hours.
	now = time.Date(2026, 11, 1, 12, 0, 0, 0, ny)
	want = time.Date(2026, 10, 31, 23, 30, 0, 0, ny)
	if got, ok := PreviousRun(daily, now); !ok || !got.Equal(want) {
		t.Fatalf("fall-back: got %s, %t; want %s", got, ok, want)
	}
	// A weekly slot a week before the change is exactly seven wall days.
	weekly := config.PolicySchedule{Cadence: "weekly", Weekday: "sun", At: "23:30"}
	now = time.Date(2026, 3, 8, 12, 0, 0, 0, ny)
	want = time.Date(2026, 3, 1, 23, 30, 0, 0, ny)
	if got, ok := PreviousRun(weekly, now); !ok || !got.Equal(want) {
		t.Fatalf("weekly across spring-forward: got %s, %t; want %s", got, ok, want)
	}
}

// Hourly must be a WALL-clock boundary, mirroring NextRun.
func TestPreviousRunHourlyHalfHourZone(t *testing.T) {
	ist := time.FixedZone("IST", 5*3600+1800)
	now := time.Date(2026, 3, 10, 14, 30, 0, 0, ist)
	got, ok := PreviousRun(config.PolicySchedule{Cadence: "hourly"}, now)
	want := time.Date(2026, 3, 10, 14, 0, 0, 0, ist)
	if !ok || !got.Equal(want) {
		t.Fatalf("got %s, %t; want %s", got, ok, want)
	}
}
