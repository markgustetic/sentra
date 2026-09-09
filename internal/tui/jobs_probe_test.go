package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// probeTimers reloads v from disk and runs the timer probe its reload
// returns the way the App would — synchronously here, so a test can
// assert on the settled Timer column.
func probeTimers(t *testing.T, v JobsView) JobsView {
	t.Helper()
	cmd := v.reload()
	if cmd == nil {
		return v
	}
	m, _ := v.Update(cmd())
	return m.(JobsView)
}

// TestJobs_ShownProbesTimersAsynchronously pins the rule: being shown
// re-reads sentra.yaml and stats the timer files synchronously (cheap),
// but asking launchd/systemd whether a timer is loaded — an exec with a
// 15s timeout, per installed job — happens in the returned cmd, never
// inside Update. The rail's live preview delivers viewShownMsg whenever
// the operator scrolls past Schedules, so a synchronous probe froze the
// rail on every pass.
func TestJobs_ShownProbesTimersAsynchronously(t *testing.T) {
	deps, path := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	installAlphaFiles(t, v, path)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)

	m, cmd := v.Update(viewShownMsg{})
	if len(f.calls) != 0 {
		t.Fatalf("viewShownMsg must not exec the scheduler runner synchronously, ran %q", f.calls)
	}
	v = m.(JobsView)
	if !v.rows[0].installed || v.rows[0].activeKnown {
		t.Fatalf("before the probe the row is installed with the OS answer pending: %+v", v.rows[0])
	}
	if out := v.View(); !strings.Contains(out, "installed") || strings.Contains(out, "inactive") {
		t.Fatalf("pending probe must render the on-disk fact, not an OS answer:\n%s", out)
	}
	if cmd == nil {
		t.Fatal("viewShownMsg with an installed job must return the probe cmd")
	}

	res := cmd()
	print := "launchctl print " + launchdGUIDomain() + "/com.sentra.alpha"
	if !f.ran(print) {
		t.Fatalf("the probe cmd must ask launchd, ran %q", f.calls)
	}
	m, _ = v.Update(res)
	v = m.(JobsView)
	if !v.rows[0].active || !v.rows[0].activeKnown {
		t.Fatalf("the status message must fill in the OS answer: %+v", v.rows[0])
	}
	if out := v.View(); !strings.Contains(out, "active") || !strings.Contains(out, "Mar 11 03:00") {
		t.Fatalf("settled column must read active with a next run:\n%s", out)
	}
}

// TestJobs_TimerStatusMsgUpdatesTheColumn: the status message drives the
// Timer column through every OS answer, and a message from a superseded
// reload is dropped so a slow probe cannot overwrite a newer one.
func TestJobs_TimerStatusMsgUpdatesTheColumn(t *testing.T) {
	deps, path := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	installAlphaFiles(t, v, path)
	sized, _ := v.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	v = sized.(JobsView)
	v.reload()

	cases := []struct {
		name     string
		status   timerStatus
		wantCell string
		wantNext bool
	}{
		{"loaded", timerStatus{active: true, known: true}, "active", true},
		{"files only", timerStatus{active: false, known: true}, "inactive", false},
		{"unknown", timerStatus{known: false}, "installed", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, _ := v.Update(jobsTimerStatusMsg{gen: v.probeGen, status: map[string]timerStatus{"alpha": tc.status}})
			out := m.(JobsView).View()
			if !strings.Contains(out, tc.wantCell) {
				t.Fatalf("timer column missing %q:\n%s", tc.wantCell, out)
			}
			if got := strings.Contains(out, "Mar 11 03:00"); got != tc.wantNext {
				t.Fatalf("next run shown = %v, want %v:\n%s", got, tc.wantNext, out)
			}
		})
	}

	stale, _ := v.Update(jobsTimerStatusMsg{gen: v.probeGen - 1, status: map[string]timerStatus{"alpha": {active: false, known: true}}})
	if strings.Contains(stale.(JobsView).View(), "inactive") {
		t.Fatal("a status message from a superseded reload must be dropped")
	}
}

// TestJobs_ProbeIsBoundedByADeadline: a runner that never returns must
// not pin the probe forever — the deadline fires, and the answer is
// "unknown" (plain installed), never "inactive".
func TestJobs_ProbeIsBoundedByADeadline(t *testing.T) {
	deps, path := jobsDeps(t)
	deps.SchedulerRunner = func(ctx context.Context, _ string, _ ...string) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.probeTimeout = 20 * time.Millisecond
	installAlphaFiles(t, v, path)
	cmd := v.reload()
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case res := <-done:
		m, _ := v.Update(res)
		if row := m.(JobsView).rows[0]; row.activeKnown {
			t.Fatalf("a timed-out probe must report unknown, got %+v", row)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not honor its deadline")
	}
}

// Manual and not-installed rows have nothing to ask the OS about, so a
// reload with none returns no probe at all.
func TestJobs_ReloadWithoutInstalledJobsReturnsNoProbe(t *testing.T) {
	deps, _ := jobsDeps(t)
	f := &fakeSchedRunner{}
	deps.SchedulerRunner = f.run
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	if cmd := v.reload(); cmd != nil {
		t.Fatal("reload with no installed timers must not schedule a probe")
	}
	if len(f.calls) != 0 {
		t.Fatalf("reload must never shell out itself, ran %q", f.calls)
	}
}
