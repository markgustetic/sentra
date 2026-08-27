package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// applyStage tracks the agent-apply state machine, which is layered on
// top of the scan flow: a completed scan leaves recommendations in the
// table, and pressing `a` walks them through review → confirm → apply →
// done. It is deliberately separate from the scan's `busy` flag so the
// two flows can't corrupt each other's state.
type applyStage int

const (
	agentIdle       applyStage = iota // no apply in progress (scan-only view)
	agentReviewing                    // per-row approve/decline toggling
	agentConfirming                   // walking the per-rec confirm modals
	agentApplying                     // op guard held; dispatching actions
	agentApplyDone                    // per-action results + tally shown
)

// agentRunner is the hook that actually runs the orchestrator. It
// receives the cancelable ctx the view created (cancelled when the
// user quits or switches away from the scan) plus the stream channel
// for reasoning tokens. Tests inject a closure that pushes scripted
// output and returns canned recommendations without touching the
// real Provider.
type agentRunner func(ctx context.Context, stream chan<- string) ([]agent.Recommendation, error)

// tokenMsg is one streamed reasoning token — the agent sends the
// model's text mid-flight on the stream channel; the view's reader
// goroutine wraps each value in this message and pumps it back into
// the Bubbletea Update loop. Buffer-of-one strings keeps the view
// scrolling smoothly even when tokens arrive in bursts.
type tokenMsg string

// agentDoneMsg is the terminal message: the orchestrator finished
// (success or error). Mutually exclusive with further tokenMsgs;
// the runner closes the stream channel before sending this so the
// drainer can exit cleanly.
type agentDoneMsg struct {
	recs []agent.Recommendation
	err  error
}

// adviceDoneMsg carries the local-heuristics ignore suggestions back
// to the view — the TUI face of `agent advise-ignore`, no LLM involved.
type adviceDoneMsg struct {
	advice []heuristics.IgnoreAdvice
	err    error
}

// agentApplyDoneMsg is the terminal message of the agent-apply flow. It
// carries the per-action result lines and the applied/declined/errors
// tally. It implements opResult() so the App's one-op-at-a-time guard
// clears when apply finishes — apply mutates the repo (prune → GC under
// the repo lock) so it MUST go through the mutating-op protocol.
type agentApplyDoneMsg struct {
	lines    []string
	applied  int
	declined int
	errs     int
	err      error
}

func (agentApplyDoneMsg) opResult() {}

// agentConfirmID ties a per-rec simple confirm modal back to this view;
// agentWipeConfirmID ties the empty-repo typed "wipe" gate back to it.
const (
	agentConfirmID     = "agent-apply-confirm"
	agentWipeConfirmID = "agent-apply-wipe"
)

// AgentView renders the streaming agent: top half is a viewport
// auto-tailing reasoning tokens, bottom half is a recommendations
// table that fills in once the orchestrator finishes. Pressing `s`
// kicks off a scan; `a` (when results are present) would trigger
// apply confirms in a future iteration.
type AgentView struct {
	deps     Deps
	viewport viewport.Model
	tbl      table.Model

	// streamBuf is a plain string (not a Builder) because AgentView
	// is passed by value through tea.Model — strings.Builder forbids
	// copy and panics on the second Update tick. The growth path is
	// O(N²) on the byte count, but the agent's reasoning text is in
	// the tens of KB at most, so the cost is invisible in practice.
	streamBuf string
	recs      []agent.Recommendation
	busy      bool
	doneErr   error

	// --- agent-apply state (layered on top of the scan flow) ---

	// applyStage is the apply state machine's current stage. agentIdle
	// means no apply is in flight; the scan view renders normally.
	applyStage applyStage

	// approved[i] records whether recommendation recs[i] is approved for
	// apply. Populated true-for-every-actionable-rec when review starts;
	// space toggles the row under the cursor. "none" recs are never
	// approvable (they carry no side effect) so they're seeded false.
	approved map[int]bool

	// cursor is the highlighted row during agentReviewing. Up/down move
	// it; space toggles approved[cursor].
	cursor int

	// confirmQueue holds the indices into recs (in table order) of the
	// approved, actionable recommendations still awaiting their per-rec
	// confirm modal. Popped front-to-back as each confirmedMsg arrives;
	// when it empties the flow moves to agentApplying.
	confirmQueue []int

	// wipePending is set when the approved prunes would delete every
	// snapshot in the repo. It gates on an extra TypedConfirmModal
	// (word "wipe") shown before the per-rec confirms — the TUI mirror
	// of the CLI's --allow-wipe rail. Cleared once the typed gate is
	// satisfied.
	wipePending bool

	// wipeConfirmed records that the operator actually typed "wipe" in the
	// destructive-repo gate. Unlike wipePending (which is cleared the
	// instant the modal is satisfied, so it can't survive to apply time),
	// this stays true through startApply and is what the apply-time wipe
	// rail reads: a repo-emptying prune is refused unless this is set. It
	// is the durable signal that authorizes emptying the repo.
	wipeConfirmed bool

	// confirmCursor walks confirmQueue during agentConfirming: each
	// per-rec confirmedMsg advances it; when it reaches len(confirmQueue)
	// the last confirm has cleared and the flow moves to applying.
	confirmCursor int

	// result carries the terminal apply outcome for the done screen.
	result agentApplyDoneMsg

	// run is the orchestrator hook. Real production wires it via
	// the deps' Provider; tests inject a closure. nil run + nil
	// Provider = "agent unavailable" placeholder.
	run agentRunner

	// stream / doneCh are kept on the model so the tokenMsg handler
	// can re-arm waitForAgentEvent after each token. Without this,
	// only the first token would surface — the cmd would terminate
	// after delivering one msg and Bubbletea wouldn't re-invoke it.
	stream chan string
	doneCh chan agentDoneMsg
	// cancel cancels the in-flight scan's context. Called from
	// Cleanup() when the user quits the TUI mid-scan; without it,
	// the LLM call stays running and the goroutine leaks until the
	// network round-trip finishes (potentially minutes).
	cancel context.CancelFunc
}

// NewAgentView constructs the agent view with a production runner
// derived from deps. When deps.Provider is nil, the runner is also
// nil and the view renders the "configure ANTHROPIC_API_KEY" hint
// instead of a streaming pane.
func NewAgentView(deps Deps) AgentView {
	if deps.Provider == nil || deps.Repo == nil {
		return baseAgentView(deps, nil)
	}
	runner := func(ctx context.Context, stream chan<- string) ([]agent.Recommendation, error) {
		// Production heuristics: same set the CLI wires. Importing
		// from cmd/sentra would create a cycle; this duplication is
		// minor and acceptable for v1.
		hs := heuristics.NewRegistry(productionHeuristics()...)
		// Mirror the CLI's agent.Config construction: the operator's
		// configured model, LLM budget, and retention feed the scan.
		// A TUI scan that ignored cfg.Agent.Model would quietly use a
		// different (and differently-priced) model than `agent scan`.
		agentCfg := agent.Config{}.Defaults()
		if deps.Config != nil {
			agentCfg.Model = deps.Config.Agent.Model
			agentCfg.MaxFindingsToLLM = deps.Config.Agent.MaxFindingsToLLM
			agentCfg.InputConfig.Retention = repo.RetentionPolicy{
				KeepLast:    deps.Config.Retention.KeepLast,
				KeepDaily:   deps.Config.Retention.KeepDaily,
				KeepWeekly:  deps.Config.Retention.KeepWeekly,
				KeepMonthly: deps.Config.Retention.KeepMonthly,
			}
			agentCfg = agentCfg.Defaults() // re-default any zero fields the config left unset
		}
		a := &agent.Agent{
			Repo:       deps.Repo,
			Heuristics: hs,
			Provider:   deps.Provider,
			Config:     agentCfg,
		}
		return a.Scan(ctx, ".", stream)
	}
	return baseAgentView(deps, runner)
}

// ConsumesArrows: up/down move the cursor over recommendations and scroll the
// detail viewport. With no recommendations there is nothing to move, so the
// arrows belong to the nav rail.
func (a AgentView) ConsumesArrows() bool { return len(a.recs) > 0 }

// Title names the view in the sidebar, palette, and title bar.
func (AgentView) Title() string { return "Agent" }

// ShortHelp lists the view-specific keys for the status bar. The set
// depends on stage: scan-only until recs land, then apply keys while
// reviewing.
// ConsumesEscape: during the apply flow esc abandons it — from reviewing back
// to the scan, from confirming back to reviewing. The idle scan view leaves esc
// to the shell.
func (a AgentView) ConsumesEscape() bool {
	return a.applyStage == agentReviewing || a.applyStage == agentConfirming
}

func (a AgentView) ShortHelp() []key.Binding {
	switch a.applyStage {
	case agentReviewing:
		return []key.Binding{
			key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "apply…")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	default:
		binds := []key.Binding{
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "scan")),
			key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "ignore advice")),
		}
		if len(a.recs) > 0 && !a.busy {
			binds = append(binds, key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply")))
		}
		return binds
	}
}

// NewAgentViewWithRunner is the tests' construction path. It lets
// callers inject a deterministic runner closure that pushes scripted
// stream output and returns canned recommendations.
func NewAgentViewWithRunner(deps Deps, run agentRunner) AgentView {
	return baseAgentView(deps, run)
}

// baseAgentView builds the shared internals. Pulled out so the two
// constructors don't drift out of sync on viewport / table init.
func baseAgentView(deps Deps, run agentRunner) AgentView {
	vp := viewport.New(80, 12)
	vp.SetContent("")

	cols := []table.Column{
		{Title: "ID", Width: 14},
		{Title: "Severity", Width: 10},
		{Title: "Action", Width: 16},
		{Title: "Target", Width: 24},
		{Title: "Rationale", Width: 30},
	}
	tbl := table.New(
		table.WithColumns(cols),
		table.WithFocused(true),
		table.WithHeight(8),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.Foreground(lipgloss.Color("#7C3AED")).Bold(true)
	st.Selected = st.Selected.Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#7C3AED"))
	tbl.SetStyles(st)

	return AgentView{
		deps:     deps,
		viewport: vp,
		tbl:      tbl,
		run:      run,
	}
}

// Init is a no-op. The view doesn't poll; user keypresses drive
// every transition.
func (AgentView) Init() tea.Cmd { return nil }

// Update routes messages:
//   - `s` keypress: kick off a scan if not already busy.
//   - tokenMsg: append to the viewport.
//   - agentDoneMsg: flip busy off, populate the table.
//   - WindowSizeMsg: resize the viewport and the table together.
//
// Unknown messages are forwarded to the embedded viewport / table
// so they continue to handle their own scroll / cursor keys.
func (a AgentView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		a.viewport.Width = maxInt(40, msg.Width-4)
		a.viewport.Height = maxInt(8, msg.Height/2-4)
		a.tbl.SetHeight(maxInt(4, msg.Height/2-6))
		return a, nil

	case tokenMsg:
		a.streamBuf += string(msg)
		a.viewport.SetContent(a.streamBuf)
		a.viewport.GotoBottom()
		// Re-arm for the next event so subsequent tokens / done
		// messages keep flowing. Skip when channels are nil (i.e.
		// tokens were synthesized from a test message) so unit
		// tests don't block forever.
		if a.stream != nil && a.doneCh != nil {
			return a, waitForAgentEvent(a.stream, a.doneCh)
		}
		return a, nil

	case agentDoneMsg:
		a.busy = false
		a.doneErr = msg.err
		a.recs = msg.recs
		a.tbl.SetRows(recsToRows(msg.recs))
		// Drop the channels — they're done.
		a.stream = nil
		a.doneCh = nil
		return a, nil

	case adviceDoneMsg:
		a.busy = false
		if msg.err != nil {
			a.streamBuf = ui.Danger.Render("ignore advice failed: ") + msg.err.Error()
		} else if len(msg.advice) == 0 {
			a.streamBuf = ui.Subtle.Render("no ignore suggestions — the tree looks clean")
		} else {
			var b strings.Builder
			b.WriteString(ui.Primary.Render("Suggested .sentraignore patterns") + "\n\n")
			for _, ad := range msg.advice {
				size := ""
				if ad.Size > 0 {
					size = "  " + ui.FormatBytes(ad.Size)
				}
				fmt.Fprintf(&b, "  %s%s\n", ad.Pattern, size)
				fmt.Fprintf(&b, "    %s\n", ui.Muted.Render(ad.Reason))
			}
			b.WriteString("\n" + ui.Muted.Render("read-only — add the patterns you want to .sentraignore"))
			a.streamBuf = b.String()
		}
		a.viewport.SetContent(a.streamBuf)
		a.viewport.GotoTop()
		return a, nil

	case confirmedMsg:
		if a.applyStage != agentConfirming {
			return a, nil
		}
		// The typed wipe gate clears first, then the per-rec walk begins.
		if msg.id == agentWipeConfirmID && a.wipePending {
			a.wipePending = false
			// Durable authorization: the operator typed "wipe". This
			// survives to apply time where the wipe rail reads it, unlike
			// wipePending which we just cleared.
			a.wipeConfirmed = true
			return a, a.pushNextConfirm()
		}
		if msg.id == agentConfirmID && len(a.confirmQueue) > 0 {
			// Approved: advance the cursor over the fixed queue. When the
			// last confirm clears, move to applying. We track progress with
			// confirmCursor rather than popping so the queue stays intact as
			// the approved-and-confirmed set the apply task consumes.
			a.confirmCursor++
			if a.confirmCursor >= len(a.confirmQueue) {
				return a.startApply()
			}
			return a, a.pushNextConfirmAt(a.confirmCursor)
		}
		return a, nil

	case agentApplyDoneMsg:
		a.applyStage = agentApplyDone
		a.result = msg
		a.confirmQueue = nil
		a.confirmCursor = 0
		return a, nil

	case opRejectedMsg:
		// Our apply start was refused (another op holds the guard). Leave
		// the optimistic applying stage so we don't hang; return to review
		// so the operator can retry once the other op finishes.
		if a.applyStage == agentApplying && msg.name == "agent-apply" {
			a.applyStage = agentReviewing
			a.confirmQueue = nil
			a.confirmCursor = 0
		}
		return a, nil

	case tea.KeyMsg:
		if a.applyStage == agentConfirming && msg.Type == tea.KeyEsc {
			// A modal esc already popped the overlay; return to review so
			// the operator can re-decide rather than being stranded.
			a.applyStage = agentReviewing
			a.confirmQueue = nil
			a.confirmCursor = 0
			a.wipePending = false
			a.wipeConfirmed = false
			return a, nil
		}

		// From the done screen, `s` re-scans: reset apply state to idle so
		// the scan-key guard below fires.
		if a.applyStage == agentApplyDone && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's' {
			a.applyStage = agentIdle
			a.approved = nil
			a.result = agentApplyDoneMsg{}
		}

		// Scan key: only when not reviewing/applying an existing result.
		if a.applyStage == agentIdle && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's' {
			if a.busy || a.run == nil {
				return a, nil
			}
			a.busy = true
			a.streamBuf = ui.Subtle.Render("[scanning...]\n")
			a.viewport.SetContent(a.streamBuf)
			a.recs = nil
			a.doneErr = nil
			cmd := a.spawnScan()
			return a, cmd
		}

		// Ignore advice ('i'): the TUI face of `agent advise-ignore`.
		// Local heuristics only — no LLM — so it works even without a
		// configured Provider, exactly like the CLI command.
		if a.applyStage == agentIdle && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'i' {
			if a.busy {
				return a, nil
			}
			a.busy = true
			a.streamBuf = ui.Subtle.Render("[collecting ignore advice...]\n")
			a.viewport.SetContent(a.streamBuf)
			deps := a.deps
			return a, func() tea.Msg {
				wopts := walker.Options{}
				if deps.Config != nil {
					wopts.IgnoreFile = deps.Config.Backup.IgnoreFile
					wopts.ExcludeCaches = deps.Config.Backup.ExcludeCaches
					wopts.Concurrency = deps.Config.Backup.Concurrency
				}
				advice, err := heuristics.CollectIgnoreAdvice(
					ctxOrBackground(deps.Ctx), ".", wopts, heuristics.DefaultLargeFileBytes)
				return adviceDoneMsg{advice: advice, err: err}
			}
		}

		// Enter review on `a` when a finished scan produced recs.
		if a.applyStage == agentIdle && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'a' {
			if a.busy || len(a.recs) == 0 {
				return a, nil
			}
			a.applyStage = agentReviewing
			a.cursor = 0
			a.approved = make(map[int]bool, len(a.recs))
			for i, r := range a.recs {
				// "none" is notify-only: it has no side effect, so it is
				// never approvable. Every other verb defaults to approved.
				a.approved[i] = r.Action != "none"
			}
			return a, nil
		}

		if a.applyStage == agentReviewing {
			return a.updateReviewing(msg)
		}
	}
	// Forward other messages to the viewport (for scroll keys); we
	// don't forward to the table because navigation in the empty
	// table would interfere with the parent's tab-switch keys.
	var cmd tea.Cmd
	a.viewport, cmd = a.viewport.Update(msg)
	return a, cmd
}

// updateReviewing handles keystrokes while the operator is toggling
// per-row approval. Up/down move the cursor; space flips approval for
// the current row (except "none" rows, which have no side effect and
// stay unapprovable); esc abandons the apply and returns to the scan
// view. Enter (→ confirming) is wired in a later task.
func (a AgentView) updateReviewing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		a.applyStage = agentIdle
		return a, nil
	case tea.KeyEnter:
		return a.beginConfirm()
	case tea.KeyUp:
		if a.cursor > 0 {
			a.cursor--
		}
		return a, nil
	case tea.KeyDown:
		if a.cursor < len(a.recs)-1 {
			a.cursor++
		}
		return a, nil
	case tea.KeySpace:
		if a.cursor >= 0 && a.cursor < len(a.recs) && a.recs[a.cursor].Action != "none" {
			a.approved[a.cursor] = !a.approved[a.cursor]
		}
		return a, nil
	}
	// Also accept j/k as vim-style movement, matching other views.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'k':
			if a.cursor > 0 {
				a.cursor--
			}
		case 'j':
			if a.cursor < len(a.recs)-1 {
				a.cursor++
			}
		}
	}
	return a, nil
}

// beginConfirm transitions from reviewing into the confirmation walk.
// It builds the queue of approved actionable recs, then re-derives the
// CLI's wipe-guard: seed the remaining-snapshot count from ListSnapshots
// and, if the approved prunes would drive it to zero, require the typed
// "wipe" gate before any per-rec confirm. When nothing is approved the
// flow returns to a done tally rather than confirming an empty set.
func (a AgentView) beginConfirm() (tea.Model, tea.Cmd) {
	queue := make([]int, 0, len(a.recs))
	for i := range a.recs {
		if a.approved[i] && a.recs[i].Action != "none" {
			queue = append(queue, i)
		}
	}
	if len(queue) == 0 {
		// Nothing to apply — go straight to a done tally of all-declined.
		a.applyStage = agentApplyDone
		a.result = agentApplyDoneMsg{declined: a.declinedCount()}
		return a, nil
	}
	a.confirmQueue = queue
	a.confirmCursor = 0

	// Wipe guard: count snapshots that would be pruned and compare with
	// what's in the repo. remaining-prunes >= remaining-snapshots means
	// the sequence empties the repo. Mirrors applyRecommendations'
	// remaining/len(currentSnaps) accounting (cli/agent_apply.go:82-139).
	prunes := 0
	for _, i := range queue {
		if a.recs[i].Action == "prune_snapshot" {
			prunes++
		}
	}
	a.wipePending = false
	// Fresh confirmation each pass: a prior aborted attempt must not leave
	// wipeConfirmed set and silently authorize this batch's emptying prune.
	a.wipeConfirmed = false
	if prunes > 0 && a.deps.Repo != nil {
		ctx, cancel := context.WithTimeout(ctxOrBackground(a.deps.Ctx), hydrateTimeout)
		snaps, err := a.deps.Repo.ListSnapshots(ctx)
		cancel()
		// On a list error we conservatively arm the wipe gate: better to
		// force an explicit "wipe" than to silently allow a destructive
		// sequence we couldn't bound.
		if err != nil || prunes >= len(snaps) {
			a.wipePending = true
		}
	}

	a.applyStage = agentConfirming
	if a.wipePending {
		body := "Applying these recommendations would prune every snapshot in the repo.\nThe repository will be left empty."
		modal := NewTypedConfirmModal("Confirm repo wipe", body, "wipe", agentWipeConfirmID, 80, 24)
		return a, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	return a, a.pushNextConfirm()
}

// pushNextConfirmAt pushes the simple confirm modal for confirmQueue[i].
// Out-of-range i yields nil so the caller can transition to applying.
func (a AgentView) pushNextConfirmAt(i int) tea.Cmd {
	if i < 0 || i >= len(a.confirmQueue) {
		return nil
	}
	rec := a.recs[a.confirmQueue[i]]
	body := fmt.Sprintf("Apply %s on %q?\n\nrationale: %s", rec.Action, rec.Target, rec.Rationale)
	modal := NewConfirmModal("Confirm apply", body, agentConfirmID, 80, 24)
	return func() tea.Msg { return pushModalMsg{modal: modal} }
}

// pushNextConfirm pushes the confirm modal for the current cursor.
func (a AgentView) pushNextConfirm() tea.Cmd { return a.pushNextConfirmAt(a.confirmCursor) }

// declinedCount returns how many actionable recs the operator declined
// during review (approved==false, action != "none"). Used to seed the
// done tally when nothing is applied.
func (a AgentView) declinedCount() int {
	n := 0
	for i, r := range a.recs {
		if r.Action != "none" && !a.approved[i] {
			n++
		}
	}
	return n
}

// startApply enters the applying stage and emits the mutating-op start.
// The run closure iterates the confirmed recs, dispatching each through
// deps.Actions.Dispatch with an Env whose Stdout is a buffer we later
// split into per-action result lines. The wipe-guard is re-checked here
// against a live snapshot count (belt-and-suspenders on top of the
// confirm-time gate): a prune that would empty the repo is refused with
// an error line unless wipePending was explicitly cleared by the typed
// "wipe" modal. Mirrors cli/agent_apply.go's applyRecommendations.
func (a AgentView) startApply() (tea.Model, tea.Cmd) {
	a.applyStage = agentApplying

	// Snapshot the approved-and-confirmed recs by value so the goroutine
	// doesn't read model fields concurrently with the Update loop.
	recs := make([]agent.Recommendation, 0, len(a.confirmQueue))
	for _, i := range a.confirmQueue {
		recs = append(recs, a.recs[i])
	}
	registry := a.deps.Actions
	r := a.deps.Repo
	// wipeAllowed authorizes a repo-emptying prune at apply time. It is
	// true ONLY when the operator actually typed "wipe" in the destructive
	// gate. wipePending can't be used here — it's cleared the instant the
	// modal is satisfied (before startApply runs) — so we read the durable
	// wipeConfirmed signal instead. Capturing it into the run closure keeps
	// the in-loop rail (remaining-1 <= 0 && !wipeAllowed) live: since that
	// rail only trips on the prune that removes the last snapshot, this
	// refuses precisely an unauthorized repo-emptying batch, and because
	// the closure re-reads ListSnapshots for `remaining` it also catches a
	// concurrent deletion the confirm-time count missed.
	wipeAllowed := a.wipeConfirmed
	// declined counts recs the operator turned off during review.
	declined := a.declinedCount()

	start := startOpMsg{
		name: "agent-apply",
		run: func(ctx context.Context) tea.Msg {
			if registry == nil {
				return agentApplyDoneMsg{err: errSentinelApply("no action registry configured"), declined: declined}
			}
			// Seed remaining-snapshot count for the in-loop wipe rail.
			remaining := 0
			if r != nil {
				snaps, err := r.ListSnapshots(ctx)
				if err != nil {
					return agentApplyDoneMsg{err: err, declined: declined}
				}
				remaining = len(snaps)
			}

			var buf strings.Builder
			cwd, _ := os.Getwd() // failure → handler falls back to "."
			env := action.Env{
				Repo:        r,
				Stdout:      &buf,
				Cwd:         cwd,
				FormatBytes: ui.FormatBytes,
			}
			applied, errs := 0, 0
			for _, rec := range recs {
				// In-loop wipe rail: an approved prune that would empty the
				// repo is refused unless the typed gate cleared it.
				if rec.Action == "prune_snapshot" && remaining-1 <= 0 && !wipeAllowed {
					fmt.Fprintf(&buf, "  - %s: refused (would empty the repo)\n", rec.ID)
					errs++
					continue
				}
				derr := registry.Dispatch(ctx, env, action.Action(rec.Action),
					rec.ID, rec.Target, rec.Severity, rec.Rationale)
				if derr != nil {
					fmt.Fprintf(&buf, "  - %s: error: %s\n", rec.ID, derr.Error())
					errs++
					continue
				}
				applied++
				if rec.Action == "prune_snapshot" {
					remaining--
				}
			}
			lines := splitNonEmptyLines(buf.String())
			return agentApplyDoneMsg{lines: lines, applied: applied, declined: declined, errs: errs}
		},
	}
	return a, tea.Batch(func() tea.Msg { return start }, opTick())
}

// errSentinelApply is a tiny error type for the "no registry" guard so
// the run closure doesn't need errors.New.
type errSentinelApply string

func (e errSentinelApply) Error() string { return string(e) }

// splitNonEmptyLines splits s on newlines and drops blank entries so the
// done view renders exactly the per-action lines the handlers emitted.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// spawnScan kicks off the agent runner in a goroutine, stashes the
// stream/done channels on the model, and returns a tea.Cmd that
// waits on the next event.
//
// Channels are returned via *AgentView field updates because the
// caller (Update) returns the modified model — the next tokenMsg
// handler will read a.stream / a.doneCh to re-arm.
func (a *AgentView) spawnScan() tea.Cmd {
	stream := make(chan string, 32)
	doneCh := make(chan agentDoneMsg, 1)
	// Parent is deps.Ctx (App-scoped) so a 'q' quit cancels the
	// scan via App.cleanup. The local cancel is also retained so
	// callers can cancel a scan independently (e.g. user navigates
	// away from the agent view without quitting).
	ctx, cancel := context.WithCancel(ctxOrBackground(a.deps.Ctx))
	a.stream = stream
	a.doneCh = doneCh
	a.cancel = cancel

	go func() {
		recs, err := a.run(ctx, stream)
		close(stream)
		doneCh <- agentDoneMsg{recs: recs, err: err}
	}()

	return waitForAgentEvent(stream, doneCh)
}

// Cleanup cancels any in-flight scan. Safe to call even when no
// scan is running; idempotent. The App invokes this on quit so the
// LLM call doesn't outlive the TUI process by a network round-trip.
//
// Value receiver (not pointer) so the App's cleanup() type assertion
// works against the tea.Model-typed field directly. context.CancelFunc
// is a function value — copying the AgentView still copies the same
// underlying closure pointer, so calling cancel on the copy cancels
// the original context. We don't bother nilling a.cancel here for
// the same reason: the value's copy is throwaway.
func (a AgentView) Cleanup() {
	if a.cancel != nil {
		a.cancel()
	}
}

// waitForAgentEvent returns a cmd that selects on the stream and
// done channels and emits the appropriate message. It's recursive
// in the bubbletea sense: each tokenMsg's cmd kicks off another
// waitForAgentEvent for the next token.
//
// On stream-channel close, we wait for the doneCh result.
func waitForAgentEvent(stream <-chan string, doneCh <-chan agentDoneMsg) tea.Cmd {
	return func() tea.Msg {
		select {
		case tok, ok := <-stream:
			if !ok {
				// Stream closed — wait for the terminal done event.
				return <-doneCh
			}
			return tokenMsg(tok)
		case done := <-doneCh:
			return done
		}
	}
}

// View renders both halves. When no runner is configured, the
// streaming pane is replaced by a placeholder.
//
// The apply-flow stages are rendered before the no-runner placeholder:
// applying recommendations doesn't need a Provider (the recs already
// exist), so review/confirm/apply screens must show even when the scan
// path is unavailable.
func (a AgentView) View() string {
	if a.applyStage == agentReviewing {
		return a.viewReviewing()
	}
	if a.applyStage == agentApplying {
		return a.viewApplying()
	}
	if a.applyStage == agentApplyDone {
		return a.viewApplyDone()
	}

	// Without a Provider the LLM scan is unavailable — but the local
	// ignore-advice pane ('i') still works, so only show the API-key
	// hint while there is nothing collected to display.
	if a.run == nil && a.streamBuf == "" && !a.busy {
		body := ui.Subtle.Render("agent") + "\n" +
			ui.Muted.Render("configure ANTHROPIC_API_KEY and re-run sentra to enable the LLM scan") + "\n" +
			ui.Muted.Render("press i for local .sentraignore advice (no API key needed)")
		return ui.Panel.Render(body) + "\n"
	}

	var top string
	if a.busy {
		top = ui.Panel.Render(a.viewport.View() + "\n" + ui.Subtle.Render("[scanning...]"))
	} else {
		top = ui.Panel.Render(a.viewport.View())
	}

	var bottom string
	switch {
	case a.doneErr != nil:
		bottom = ui.Panel.Render(ui.Danger.Render("error: " + a.doneErr.Error()))
	case len(a.recs) > 0:
		bottom = ui.Panel.Render(a.tbl.View())
	default:
		hint := ui.Muted.Render("press `s` to scan")
		bottom = ui.Panel.Render(ui.Subtle.Render("recommendations") + "\n" + hint)
	}
	return lipgloss.JoinVertical(lipgloss.Left, top, bottom) + "\n"
}

// viewReviewing renders the per-row approve/decline list. The cursor
// row is marked; each row shows [x]/[ ] approval, the verb, and target.
// "none" rows render as informational (no checkbox) so the operator
// isn't invited to "approve" a no-op.
func (a AgentView) viewReviewing() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Review recommendations"))
	fmt.Fprintf(&b, "  %s", ui.Muted.Render("space approve/decline · ⏎ apply · esc cancel"))
	b.WriteString("\n\n")
	for i, r := range a.recs {
		cursor := "  "
		if i == a.cursor {
			cursor = ui.Primary.Render("▸ ")
		}
		var mark string
		switch {
		case r.Action == "none":
			mark = ui.Muted.Render("(fyi)")
		case a.approved[i]:
			mark = ui.Success.Render("[x] approve")
		default:
			mark = ui.Danger.Render("[ ] declined")
		}
		fmt.Fprintf(&b, "%s%s  %s  %s  %s\n",
			cursor, mark, r.ID, r.Action, truncate(r.Target, 24))
	}
	return ui.Panel.Render(b.String()) + "\n"
}

// viewApplying renders a coarse "applying…" panel. Progress is a simple
// N/M counter over the confirmed set — the individual actions (prune+GC,
// ignore write) are fast and don't stream chunk-level progress, so a
// spinner-free counter is honest about the granularity.
func (a AgentView) viewApplying() string {
	total := len(a.confirmQueue)
	body := ui.Primary.Render("Applying recommendations…") + "\n\n" +
		ui.Muted.Render(fmt.Sprintf("dispatching %d action(s)", total))
	return ui.Panel.Render(body) + "\n"
}

// viewApplyDone renders the per-action result lines and the tally.
func (a AgentView) viewApplyDone() string {
	var b strings.Builder
	if a.result.err != nil {
		b.WriteString(ui.Danger.Render("Apply failed"))
		fmt.Fprintf(&b, "\n\n%s", humanizeErr(a.result.err))
	} else {
		b.WriteString(ui.Success.Render("Apply complete"))
		b.WriteString("\n\n")
		for _, ln := range a.result.lines {
			fmt.Fprintf(&b, "%s\n", ln)
		}
		fmt.Fprintf(&b, "\n  applied:  %d\n  declined: %d\n  errors:   %d",
			a.result.applied, a.result.declined, a.result.errs)
	}
	fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("press `s` to re-scan"))
	return ui.Panel.Render(b.String()) + "\n"
}

// recsToRows formats recommendations into bubbles/table rows.
// Truncates rationale to keep columns aligned.
func recsToRows(recs []agent.Recommendation) []table.Row {
	rows := make([]table.Row, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, table.Row{
			r.ID,
			r.Severity,
			r.Action,
			r.Target,
			truncate(r.Rationale, 30),
		})
	}
	return rows
}

// productionHeuristics mirrors cmd/sentra's defaultHeuristics. We
// don't import that package (would be a cycle) so the list is
// duplicated. Keep it in sync with cmd/sentra/main.go.
func productionHeuristics() []heuristics.Heuristic {
	return []heuristics.Heuristic{
		heuristics.NewSecrets(),
		heuristics.NewLargeFiles(),
		heuristics.NewCacheDirs(),
		heuristics.NewStalePaths(),
		heuristics.NewDupPaths(),
		heuristics.NewOrphanBlobs(),
		heuristics.NewRetentionDrift(),
	}
}

// truncate clips s to n characters with an ellipsis. Mirrors the
// CLI's helper rather than depending on it (different package).
func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return s[:n-3] + "..."
}
