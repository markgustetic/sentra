package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
)

// TestAgentView_StartsScan verifies pressing the start key (`s`)
// flips the busy flag and triggers the runner. We synchronize via
// a channel so the test reads `called` only after the runner
// goroutine has signalled completion (avoids the data race a plain
// boolean would create with -race enabled).
func TestAgentView_StartsScan(t *testing.T) {
	calledCh := make(chan struct{}, 1)
	a := NewAgentViewWithRunner(Deps{}, func(_ context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		calledCh <- struct{}{}
		return nil, nil
	})
	updated, cmd := a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !updated.(AgentView).busy {
		t.Errorf("busy flag did not flip on `s` keypress")
	}
	if cmd == nil {
		t.Fatal("expected a non-nil tea.Cmd to drive the scan")
	}
	// Run the cmd in a goroutine; it blocks on the stream/done
	// channels. The runner goroutine fires immediately and signals
	// calledCh; we wait on that signal rather than touching shared
	// state from the test.
	go cmd()
	<-calledCh
}

// TestAgentView_StreamsTokens feeds tokens via the model's tokenMsg
// path and asserts they end up in the rendered viewport. We don't
// exercise the goroutine that drains a channel; we send the token
// messages directly to keep the test deterministic.
func TestAgentView_StreamsTokens(t *testing.T) {
	a := NewAgentViewWithRunner(Deps{}, func(_ context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		return nil, nil
	})
	updated := tea.Model(a)
	for _, tok := range []string{"hello ", "world ", "from agent"} {
		updated, _ = updated.Update(tokenMsg(tok))
	}
	view := updated.(AgentView).View()
	if !strings.Contains(view, "hello") || !strings.Contains(view, "world") || !strings.Contains(view, "agent") {
		t.Errorf("viewport missing streamed tokens: %s", view)
	}
}

// TestAgentView_ShowsRecommendations injects an agentDoneMsg with a
// non-empty recommendation list and asserts the table reflects them.
// The recommendation IDs must appear somewhere in the rendered view.
func TestAgentView_ShowsRecommendations(t *testing.T) {
	a := NewAgentViewWithRunner(Deps{}, func(_ context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		return nil, nil
	})
	recs := []agent.Recommendation{
		{ID: "rec-1", Action: "prune_snapshot", Target: "snap-aaaa", Severity: "warn", Rationale: "old"},
		{ID: "rec-2", Action: "add_to_ignore", Target: "*.log", Severity: "info", Rationale: "noise"},
	}
	updated, _ := a.Update(agentDoneMsg{recs: recs})
	view := updated.(AgentView).View()
	if !strings.Contains(view, "rec-1") {
		t.Errorf("view missing rec-1: %s", view)
	}
	if !strings.Contains(view, "rec-2") {
		t.Errorf("view missing rec-2: %s", view)
	}
	if updated.(AgentView).busy {
		t.Errorf("busy flag still set after agentDoneMsg")
	}
}

// TestAgentView_HandlesError ensures an agentDoneMsg with a non-nil
// error renders the error rather than crashing. The user must be
// able to see what went wrong.
func TestAgentView_HandlesError(t *testing.T) {
	a := NewAgentViewWithRunner(Deps{}, func(_ context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		return nil, nil
	})
	updated, _ := a.Update(agentDoneMsg{err: errSentinel("boom: api key missing")})
	view := updated.(AgentView).View()
	if !strings.Contains(view, "boom") {
		t.Errorf("view did not render error: %s", view)
	}
}

// TestAgentView_RendersConfigureHint asserts the placeholder shown
// when no provider is configured includes the env-var name so the
// user knows what to set.
func TestAgentView_RendersConfigureHint(t *testing.T) {
	a := NewAgentView(Deps{}) // no Provider, no Repo
	view := a.View()
	if !strings.Contains(view, "ANTHROPIC_API_KEY") {
		t.Errorf("agent view did not surface API key hint: %s", view)
	}
}

// errSentinel is a small error type so we don't need errors.New
// in the tests above.
type errSentinel string

func (e errSentinel) Error() string { return string(e) }
