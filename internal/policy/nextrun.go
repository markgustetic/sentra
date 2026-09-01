package policy

import (
	"strconv"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// weekdayIndex maps the schedule's weekday tokens to time.Weekday.
var weekdayIndex = map[string]time.Weekday{
	"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday,
	"wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday,
	"sat": time.Saturday,
}

// NextRun returns the next wall-clock instant strictly after now at
// which the schedule fires, in now's location. launchd and systemd both
// fire on local wall time, so the computation mirrors exactly what the
// renderers install (internal/scheduler/render.go): hourly at minute 0,
// daily/weekly at At (weekly on Weekday), monthly on day 1. ok is false
// for manual and — defensively — for a schedule whose clock does not
// parse: callers validated the schedule long before, but a stale
// hand-edit must degrade to "no next run", never a wrong one.
func NextRun(s config.PolicySchedule, now time.Time) (time.Time, bool) {
	s = NormalizeSchedule(s)
	loc := now.Location()
	switch s.Cadence {
	case CadenceHourly:
		top := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, loc)
		return top.Add(time.Hour), true
	case CadenceDaily:
		hh, mm, ok := clockParts(s.At)
		if !ok {
			return time.Time{}, false
		}
		t := time.Date(now.Year(), now.Month(), now.Day(), hh, mm, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t, true
	case CadenceWeekly:
		hh, mm, ok := clockParts(s.At)
		target, wok := weekdayIndex[s.Weekday]
		if !ok || !wok {
			return time.Time{}, false
		}
		days := (int(target) - int(now.Weekday()) + 7) % 7
		t := time.Date(now.Year(), now.Month(), now.Day()+days, hh, mm, 0, 0, loc)
		if !t.After(now) {
			t = t.AddDate(0, 0, 7)
		}
		return t, true
	case CadenceMonthly:
		hh, mm, ok := clockParts(s.At)
		if !ok {
			return time.Time{}, false
		}
		t := time.Date(now.Year(), now.Month(), 1, hh, mm, 0, 0, loc)
		if !t.After(now) {
			// time.Date normalizes month 13 to January of the next year.
			t = time.Date(now.Year(), now.Month()+1, 1, hh, mm, 0, 0, loc)
		}
		return t, true
	default: // manual, or anything unrecognized
		return time.Time{}, false
	}
}

// clockParts splits a validated "HH:MM". validClock pins the shape to
// exactly two ASCII digits per component, so the slicing is safe.
func clockParts(at string) (int, int, bool) {
	if !validClock(at) {
		return 0, 0, false
	}
	hh, _ := strconv.Atoi(at[:2])
	mm, _ := strconv.Atoi(at[3:5])
	return hh, mm, true
}
