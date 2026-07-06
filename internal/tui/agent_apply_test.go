package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
)

// applyTestRecs is the canned recommendation set the apply-flow tests
// share: one prune, one ignore, one flag_secret. No "none" here — the
// none path is covered separately.
func applyTestRecs() []agent.Recommendation {
	return []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: "snap-old", Severity: "warn", Rationale: "stale"},
		{ID: "rec-ignore", Action: "add_to_ignore", Target: "*.log", Severity: "info", Rationale: "noise"},
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked key"},
	}
}

// agentViewWithRecs builds an AgentView that has already "finished" a
// scan carrying recs, so the apply-flow tests start from the state a
// real scan would leave behind.
func agentViewWithRecs(t *testing.T, deps Deps, recs []agent.Recommendation) AgentView {
	t.Helper()
	// Drive the model through agentDoneMsg so recs land via the real path.
	m, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	return m.(AgentView)
}

// TestAgentApply_EnterReviewOnA verifies pressing `a` with recs present
// moves the view into the reviewing stage (all recs approved by default).
func TestAgentApply_EnterReviewOnA(t *testing.T) {
	v := agentViewWithRecs(t, Deps{}, applyTestRecs())
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := m.(AgentView)
	if got.applyStage != agentReviewing {
		t.Fatalf("applyStage = %v, want agentReviewing", got.applyStage)
	}
	// Default: every actionable rec approved.
	for i := range applyTestRecs() {
		if !got.approved[i] {
			t.Errorf("rec %d not approved by default", i)
		}
	}
	if !strings.Contains(got.View(), "approve") {
		t.Errorf("reviewing view should mention approve/decline:\n%s", got.View())
	}
}

// TestAgentApply_ToggleDecline flips a row's approval off with space and
// asserts the map + rendered marker reflect it.
func TestAgentApply_ToggleDecline(t *testing.T) {
	v := agentViewWithRecs(t, Deps{}, applyTestRecs())
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(AgentView)
	// Cursor starts at row 0; toggle it off.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(AgentView)
	if v.approved[0] {
		t.Fatal("row 0 should be declined after space toggle")
	}
	if !strings.Contains(v.View(), "declined") {
		t.Errorf("view should show a declined row:\n%s", v.View())
	}
}

// TestAgentApply_NoReviewWithoutRecs asserts `a` is inert when the scan
// produced nothing — no recs means nothing to apply.
func TestAgentApply_NoReviewWithoutRecs(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.(AgentView).applyStage != agentIdle {
		t.Fatalf("applyStage = %v, want agentIdle (no recs)", m.(AgentView).applyStage)
	}
}
