package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/ui"
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
		a := &agent.Agent{
			Repo:       deps.Repo,
			Heuristics: hs,
			Provider:   deps.Provider,
			Config:     agent.Config{}.Defaults(),
		}
		return a.Scan(ctx, ".", stream)
	}
	return baseAgentView(deps, runner)
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

	case tea.KeyMsg:
		if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's' {
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
	}
	// Forward other messages to the viewport (for scroll keys); we
	// don't forward to the table because navigation in the empty
	// table would interfere with the parent's tab-switch keys.
	var cmd tea.Cmd
	a.viewport, cmd = a.viewport.Update(msg)
	return a, cmd
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
func (a AgentView) View() string {
	if a.run == nil {
		body := ui.Subtle.Render("agent") + "\n" +
			ui.Muted.Render("configure ANTHROPIC_API_KEY and re-run sentra to enable the agent")
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
