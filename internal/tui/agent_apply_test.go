package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
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

// enterReview drives an AgentView (already carrying recs) into the
// reviewing stage.
func enterReview(t *testing.T, v AgentView) AgentView {
	t.Helper()
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	return m.(AgentView)
}

// TestAgentApply_EnterQueuesConfirmModals verifies that pressing enter in
// review pushes a simple confirm modal for the first approved rec and
// arms the confirm queue.
func TestAgentApply_EnterQueuesConfirmModals(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r) // 2 snaps → a single prune can't empty the repo
	recs := []agent.Recommendation{
		{ID: "rec-ignore", Action: "add_to_ignore", Target: "*.log", Severity: "info", Rationale: "noise"},
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	if v.applyStage != agentConfirming {
		t.Fatalf("applyStage = %v, want agentConfirming", v.applyStage)
	}
	if len(v.confirmQueue) != 2 {
		t.Fatalf("confirmQueue len = %d, want 2", len(v.confirmQueue))
	}
	// The command must push a modal.
	msgs := execCmds(t, cmd)
	var pushed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed = true
			if _, isSimple := pm.modal.(ConfirmModal); !isSimple {
				t.Errorf("first modal should be a simple ConfirmModal, got %T", pm.modal)
			}
		}
	}
	if !pushed {
		t.Fatal("enter must push a confirm modal")
	}
}

// TestAgentApply_WipeGuardInsertsTypedModal is the safety test: when the
// approved prunes would empty the repo, the FIRST modal must be the
// typed "wipe" gate, not a plain confirm.
func TestAgentApply_WipeGuardInsertsTypedModal(t *testing.T) {
	r := newFlowRepo(t)
	snapID, _ := seedSnapshotReal(t, r) // exactly ONE snapshot
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: snapID, Severity: "warn", Rationale: "stale"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	if !v.wipePending {
		t.Fatal("wipe guard should be armed when the last snapshot would be pruned")
	}
	msgs := execCmds(t, cmd)
	var typed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			if tc, ok := pm.modal.(TypedConfirmModal); ok {
				typed = true
				if tc.word != "wipe" {
					t.Errorf("typed word = %q, want wipe", tc.word)
				}
			}
		}
	}
	if !typed {
		t.Fatal("empty-repo prune must gate on the typed wipe modal first")
	}
}

// TestAgentApply_ConfirmWalkReachesApplying walks all confirm modals and
// asserts the flow arrives at agentApplying after the last confirm.
func TestAgentApply_ConfirmWalkReachesApplying(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	recs := []agent.Recommendation{
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // arms confirming, first modal
	v = m.(AgentView)

	// The single rec's confirm arrives back as confirmedMsg{agentConfirmID}.
	m, _ = v.Update(confirmedMsg{id: agentConfirmID})
	v = m.(AgentView)
	if v.applyStage != agentApplying {
		t.Fatalf("applyStage = %v, want agentApplying after last confirm", v.applyStage)
	}
}
