package tui

import (
	"context"
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

// driveToApplying walks a single-rec AgentView from review through the
// (single) confirm to the agentApplying stage, returning the view and
// the tea.Cmd the last confirm produced.
func driveToApplying(t *testing.T, v AgentView) (AgentView, tea.Cmd) {
	t.Helper()
	v = enterReview(t, v)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	m, cmd := v.Update(confirmedMsg{id: agentConfirmID})
	v = m.(AgentView)
	if v.applyStage != agentApplying {
		t.Fatalf("applyStage = %v, want agentApplying", v.applyStage)
	}
	return v, cmd
}

// TestAgentApply_StartOpDispatchesPrune runs the full apply for a real
// prune rec against a real in-memory repo and asserts the snapshot is
// gone after the run closure executes.
func TestAgentApply_StartOpDispatchesPrune(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r) // two snaps; prune one → repo not emptied
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	victim := snaps[0].ID
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: victim, Severity: "warn", Rationale: "stale"},
	}
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry()}
	m0, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	v := m0.(AgentView)

	_, cmd := driveToApplying(t, v)
	// The applying stage batches startOpMsg + opTick. Pull the startOpMsg
	// and execute its run closure directly (bypassing the App's guard,
	// which is exercised elsewhere) to verify the side effect.
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var found bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, found = s, true
		}
	}
	if !found {
		t.Fatal("agentApplying must emit a startOpMsg")
	}
	if start.name != "agent-apply" {
		t.Fatalf("op name = %q, want agent-apply", start.name)
	}
	done := start.run(context.Background())
	dm, ok := done.(agentApplyDoneMsg)
	if !ok {
		t.Fatalf("run returned %T, want agentApplyDoneMsg", done)
	}
	if dm.applied != 1 {
		t.Errorf("applied = %d, want 1", dm.applied)
	}
	// Verify the actual side effect: victim snapshot is gone.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.ID == victim {
			t.Fatalf("snapshot %s still present after apply", victim)
		}
	}
	// agentApplyDoneMsg is an opResultMsg.
	var _ opResultMsg = agentApplyDoneMsg{}
}

// TestAgentApply_DoneShowsTally feeds the terminal message and asserts the
// view renders the applied/declined/errors tally.
func TestAgentApply_DoneShowsTally(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	v.applyStage = agentApplying
	m, _ := v.Update(agentApplyDoneMsg{
		lines:    []string{"  - rec-1: pruned snap-x"},
		applied:  1,
		declined: 2,
		errs:     0,
	})
	got := m.(AgentView)
	if got.applyStage != agentApplyDone {
		t.Fatalf("applyStage = %v, want agentApplyDone", got.applyStage)
	}
	out := got.View()
	if !strings.Contains(out, "applied") || !strings.Contains(out, "declined") {
		t.Errorf("done view missing tally:\n%s", out)
	}
	if !strings.Contains(out, "rec-1") {
		t.Errorf("done view missing per-action line:\n%s", out)
	}
}

// TestAgentApply_RejectedResetsToReviewing asserts that if the App
// rejects the op (another op in flight), the flow leaves agentApplying.
func TestAgentApply_RejectedResetsToReviewing(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	v.applyStage = agentApplying
	m, _ := v.Update(opRejectedMsg{name: "agent-apply"})
	if m.(AgentView).applyStage != agentReviewing {
		t.Fatalf("applyStage = %v, want agentReviewing after rejection", m.(AgentView).applyStage)
	}
}

// agentIndexIn returns the index of the agent view in the App's view
// slice so the test can reach into it after routing.
func agentIndexIn(t *testing.T, app App) int {
	t.Helper()
	for i, v := range app.views {
		if v.id == "agent" {
			return i
		}
	}
	t.Fatal("agent view not registered in App")
	return -1
}

// TestAgentApply_EndToEndThroughApp routes an apply from review to done
// through the App so the op guard + modal broadcast are exercised, then
// asserts the repo side effect and the cleared guard.
func TestAgentApply_EndToEndThroughApp(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	victim := snaps[0].ID
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry(), Ctx: context.Background()}
	app := NewApp(deps)
	// Give the app a size so modals render.
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	app = m.(App)

	idx := agentIndexIn(t, app)
	// Seed the agent view with a prune rec via agentDoneMsg (broadcast).
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: victim, Severity: "warn", Rationale: "stale"},
	}
	m, _ = app.Update(agentDoneMsg{recs: recs})
	app = m.(App)
	// Focus the agent view.
	app.active = idx
	app.focus = focusContent

	// `a` → review, `enter` → confirm walk (2 snaps so no wipe gate).
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	app = m.(App)
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	// The enter produced a pushModalMsg cmd; run it and feed the result.
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 1 {
		t.Fatalf("expected a confirm modal on the stack, got %d", len(app.modals))
	}
	// Confirm the modal (enter). The modal emits confirmedMsg; the App
	// pops it and broadcasts back to the view, which starts the op.
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, cmd2 := app.Update(msg)
		app = m.(App)
		// The confirmedMsg → view startApply → startOpMsg → op runs.
		for _, msg2 := range execCmds(t, cmd2) {
			m, cmd3 := app.Update(msg2)
			app = m.(App)
			for _, msg3 := range execCmds(t, cmd3) {
				m, _ = app.Update(msg3)
				app = m.(App)
			}
		}
	}

	// The op guard must be cleared once the done message lands.
	if app.opRunning != "" {
		t.Errorf("opRunning = %q, want cleared after agent-apply", app.opRunning)
	}
	// Side effect: victim snapshot gone.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.ID == victim {
			t.Fatalf("snapshot %s still present after end-to-end apply", victim)
		}
	}
	// The agent view renders the done tally.
	av := app.views[idx].model.(AgentView)
	if av.applyStage != agentApplyDone {
		t.Fatalf("agent view stage = %v, want agentApplyDone", av.applyStage)
	}
	if !strings.Contains(av.View(), "applied") {
		t.Errorf("agent view should show tally:\n%s", av.View())
	}
}

// pullStartOp extracts the startOpMsg from the batch a startApply-driven
// command produces, failing the test if none is present.
func pullStartOp(t *testing.T, cmd tea.Cmd) startOpMsg {
	t.Helper()
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok {
			return s
		}
	}
	t.Fatal("no startOpMsg produced")
	return startOpMsg{}
}

// TestAgentApply_WipeRailRefusesUnconfirmedEmptyingPrune is the safety
// regression for the apply-time wipe rail. A single-snapshot repo with a
// prune of that snapshot would empty the repo. When the typed "wipe" gate
// was NOT satisfied (wipeConfirmed == false), startApply's run closure
// MUST refuse the prune — the last snapshot has to survive. Before the
// fix the rail was dead code (wipeAllowed hardcoded true) and the prune
// went through, deleting the last snapshot.
func TestAgentApply_WipeRailRefusesUnconfirmedEmptyingPrune(t *testing.T) {
	r := newFlowRepo(t)
	snapID, _ := seedSnapshotReal(t, r) // exactly ONE snapshot
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: snapID, Severity: "warn", Rationale: "stale"},
	}
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry()}
	m0, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	v := m0.(AgentView)

	// Set up the confirmed set as if review + per-rec confirm passed, but
	// WITHOUT satisfying the typed wipe gate.
	v.confirmQueue = []int{0}
	v.wipeConfirmed = false

	m, cmd := v.startApply()
	v = m.(AgentView)
	start := pullStartOp(t, cmd)
	done := start.run(context.Background())
	dm, ok := done.(agentApplyDoneMsg)
	if !ok {
		t.Fatalf("run returned %T, want agentApplyDoneMsg", done)
	}
	// The prune must have been refused, not applied.
	if dm.applied != 0 {
		t.Errorf("applied = %d, want 0 (prune refused)", dm.applied)
	}
	if dm.errs != 1 {
		t.Errorf("errs = %d, want 1 (prune refused)", dm.errs)
	}
	joined := strings.Join(dm.lines, "\n")
	if !strings.Contains(joined, "refused") {
		t.Errorf("expected a refusal line in the tally, got:\n%s", joined)
	}
	// Critical: the last snapshot must survive.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var survived bool
	for _, s := range after {
		if s.ID == snapID {
			survived = true
		}
	}
	if !survived {
		t.Fatalf("last snapshot %s was deleted despite the unconfirmed wipe rail", snapID)
	}
}

// TestAgentApply_WipeRailAllowsConfirmedEmptyingPrune is the companion:
// once the typed "wipe" gate is satisfied (wipeConfirmed == true), the
// repo-emptying prune is allowed to proceed and the snapshot is deleted.
func TestAgentApply_WipeRailAllowsConfirmedEmptyingPrune(t *testing.T) {
	r := newFlowRepo(t)
	snapID, _ := seedSnapshotReal(t, r) // exactly ONE snapshot
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: snapID, Severity: "warn", Rationale: "stale"},
	}
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry()}
	m0, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	v := m0.(AgentView)

	v.confirmQueue = []int{0}
	v.wipeConfirmed = true // user typed "wipe"

	m, cmd := v.startApply()
	v = m.(AgentView)
	start := pullStartOp(t, cmd)
	done := start.run(context.Background())
	dm, ok := done.(agentApplyDoneMsg)
	if !ok {
		t.Fatalf("run returned %T, want agentApplyDoneMsg", done)
	}
	if dm.applied != 1 {
		t.Errorf("applied = %d, want 1 (prune proceeds when wipe confirmed)", dm.applied)
	}
	if dm.errs != 0 {
		t.Errorf("errs = %d, want 0", dm.errs)
	}
	// The snapshot is gone.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.ID == snapID {
			t.Fatalf("snapshot %s still present after confirmed wipe", snapID)
		}
	}
}
