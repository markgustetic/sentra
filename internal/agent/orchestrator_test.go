package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// stubHeuristic is a deterministic Heuristic test double. Returns the
// preconfigured findings slice (or error) when Run is called. Same
// shape as the registry's own fakeHeuristic, repeated locally so the
// orchestrator tests don't depend on heuristics_test.go's unexported
// types.
type stubHeuristic struct {
	name     string
	findings []heuristics.Finding
	err      error
}

func (s *stubHeuristic) Name() string { return s.name }
func (s *stubHeuristic) Run(ctx context.Context, in heuristics.Input) ([]heuristics.Finding, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]heuristics.Finding, len(s.findings))
	copy(out, s.findings)
	return out, nil
}

// newAgentForTest builds an Agent backed by a memory-store repo, the
// supplied heuristic stubs, and the supplied FakeProvider script.
// Default config keeps the budget tight (5) so tests can exercise
// budget exhaustion without scripting dozens of steps.
func newAgentForTest(t *testing.T, hs []heuristics.Heuristic, provider llm.Provider) *Agent {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	registry := heuristics.NewRegistry(hs...)
	return &Agent{
		Repo:       r,
		Heuristics: registry,
		Provider:   provider,
		Config: Config{
			MaxFindingsToLLM: 50,
			MaxToolCalls:     5,
			Model:            "test-model",
		},
	}
}

// TestScan_NoFindings_ShortCircuits: heuristics emit nothing, so the
// orchestrator returns an empty slice without calling the Provider.
// The stream channel receives the synthetic "all clear" message.
func TestScan_NoFindings_ShortCircuits(t *testing.T) {
	provider := &llm.FakeProvider{} // no Steps; calling Generate would error
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "empty", findings: nil},
	}, provider)

	stream := make(chan string, 4)
	recs, err := agent.Scan(context.Background(), t.TempDir(), stream)
	close(stream)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations, got %d: %v", len(recs), recs)
	}
	if len(provider.Calls) != 0 {
		t.Errorf("Provider should not be called when there are no findings, got %d calls", len(provider.Calls))
	}
	// Drain the stream: should contain exactly one synthetic message
	// with "no findings" / "all clear" wording so downstream renderers
	// (the Phase 12 TUI) have something to display.
	got := drainStream(stream)
	if !strings.Contains(strings.ToLower(got), "no findings") &&
		!strings.Contains(strings.ToLower(got), "all clear") {
		t.Errorf("expected 'no findings' or 'all clear' in stream, got %q", got)
	}
}

// TestScan_HappyPath_LoopWithToolCall: heuristics produce 1 finding,
// the fake provider responds in two rounds — round 1 calls the tool,
// round 2 emits the JSON recommendations array. The orchestrator
// must dispatch the tool, thread the result back, and parse the
// final response.
func TestScan_HappyPath_LoopWithToolCall(t *testing.T) {
	finding := heuristics.Finding{
		ID:        "abc123",
		Category:  "secrets",
		Severity:  heuristics.SeverityCritical,
		Target:    "/repo/.env",
		Heuristic: "secrets",
	}
	provider := &llm.FakeProvider{
		Steps: []llm.FakeStep{
			// Round 1: stream a thought, then ask to inspect the finding.
			{
				Stream: []string{"checking finding..."},
				ToolCalls: []llm.ToolCall{
					{ID: "call_1", Name: "inspect_finding", Input: map[string]any{"id": "abc123"}},
				},
			},
			// Round 2: emit the final recommendation JSON.
			{
				Text: `[{"id":"rec_1","action":"flag_secret","target":"/repo/.env","severity":"critical","rationale":"stop committing .env files"}]`,
			},
		},
	}
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "secrets", findings: []heuristics.Finding{finding}},
	}, provider)

	stream := make(chan string, 16)
	recs, err := agent.Scan(context.Background(), t.TempDir(), stream)
	close(stream)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("got %d recommendations, want 1: %v", len(recs), recs)
	}
	got := recs[0]
	if got.Action != "flag_secret" {
		t.Errorf("Action: got %q, want flag_secret", got.Action)
	}
	if got.Severity != "critical" {
		t.Errorf("Severity: got %q, want critical", got.Severity)
	}
	if got.Target != "/repo/.env" {
		t.Errorf("Target: got %q, want /repo/.env", got.Target)
	}
	// Provider should have been called twice: tool-call round + final.
	if len(provider.Calls) != 2 {
		t.Fatalf("expected 2 Provider.Generate calls, got %d", len(provider.Calls))
	}
	// The second call must include the assistant's tool-use AND the
	// tool-role result message — that's the contract the LLM expects.
	secondMsgs := provider.Calls[1].Msgs
	hasToolResult := false
	for _, m := range secondMsgs {
		if m.Role == llm.RoleTool && m.ToolResult != nil && m.ToolResult.ID == "call_1" {
			hasToolResult = true
		}
	}
	if !hasToolResult {
		t.Errorf("second Generate call should include tool result for call_1; msgs=%v", secondMsgs)
	}
}

// TestScan_BudgetExhausted: the fake provider keeps asking for tool
// calls forever; the orchestrator must stop after MaxToolCalls and
// return ErrBudgetExhausted with whatever recommendations it managed
// to emit (none, in this case).
func TestScan_BudgetExhausted(t *testing.T) {
	finding := heuristics.Finding{
		ID:       "abc123",
		Category: "secrets", Severity: "critical", Target: "/x",
	}
	steps := make([]llm.FakeStep, 10)
	for i := range steps {
		steps[i] = llm.FakeStep{
			ToolCalls: []llm.ToolCall{
				{ID: "loop", Name: "inspect_finding", Input: map[string]any{"id": "abc123"}},
			},
		}
	}
	provider := &llm.FakeProvider{Steps: steps}
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "secrets", findings: []heuristics.Finding{finding}},
	}, provider)
	agent.Config.MaxToolCalls = 3 // tight budget for the test

	stream := make(chan string, 16)
	_, err := agent.Scan(context.Background(), t.TempDir(), stream)
	close(stream)
	if !errors.Is(err, ErrBudgetExhausted) {
		t.Fatalf("expected ErrBudgetExhausted, got %v", err)
	}
	// Provider was called at most MaxToolCalls + 1 times (initial + each
	// of MaxToolCalls retries). The orchestrator should stop AFTER the
	// budget is hit, not before.
	if len(provider.Calls) > agent.Config.MaxToolCalls+1 {
		t.Errorf("Provider called too many times: %d, budget=%d",
			len(provider.Calls), agent.Config.MaxToolCalls)
	}
}

// TestScan_InvalidJSONResponse: the fake provider returns plain text
// that isn't a JSON array. The orchestrator must surface
// ErrInvalidResponse rather than panicking or returning the raw text.
func TestScan_InvalidJSONResponse(t *testing.T) {
	finding := heuristics.Finding{
		ID:       "abc123",
		Category: "secrets", Severity: "critical", Target: "/x",
	}
	provider := &llm.FakeProvider{
		Steps: []llm.FakeStep{
			{Text: "I have decided not to provide JSON today."},
		},
	}
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "secrets", findings: []heuristics.Finding{finding}},
	}, provider)

	stream := make(chan string, 4)
	_, err := agent.Scan(context.Background(), t.TempDir(), stream)
	close(stream)
	if !errors.Is(err, ErrInvalidResponse) {
		t.Fatalf("expected ErrInvalidResponse, got %v", err)
	}
}

// TestScan_StreamReceivesText: tokens from the fake provider's Stream
// must reach the caller's stream channel.
func TestScan_StreamReceivesText(t *testing.T) {
	finding := heuristics.Finding{
		ID:       "abc123",
		Category: "secrets", Severity: "critical", Target: "/x",
	}
	provider := &llm.FakeProvider{
		Steps: []llm.FakeStep{
			{
				Stream: []string{"thinking", " about", " findings"},
				Text:   "[]",
			},
		},
	}
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "secrets", findings: []heuristics.Finding{finding}},
	}, provider)

	stream := make(chan string, 16)
	_, err := agent.Scan(context.Background(), t.TempDir(), stream)
	close(stream)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	got := drainStream(stream)
	for _, want := range []string{"thinking", "findings"} {
		if !strings.Contains(got, want) {
			t.Errorf("stream missing %q: %q", want, got)
		}
	}
}

// TestScan_HeuristicError_Propagates: a failing heuristic surfaces as
// a Scan error; the LLM is never called.
func TestScan_HeuristicError_Propagates(t *testing.T) {
	provider := &llm.FakeProvider{} // would error if invoked
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "broken", err: errors.New("disk on fire")},
	}, provider)

	_, err := agent.Scan(context.Background(), t.TempDir(), nil)
	if err == nil {
		t.Fatal("expected error from broken heuristic")
	}
	if !strings.Contains(err.Error(), "disk on fire") {
		t.Errorf("error should propagate heuristic message, got %v", err)
	}
	if len(provider.Calls) != 0 {
		t.Errorf("Provider must not be called when heuristics fail")
	}
}

// TestScan_NilStream_OK: a caller that doesn't care about streaming
// passes nil. The orchestrator must not panic.
func TestScan_NilStream_OK(t *testing.T) {
	provider := &llm.FakeProvider{} // no findings → no Generate
	agent := newAgentForTest(t, []heuristics.Heuristic{
		&stubHeuristic{name: "empty"},
	}, provider)
	if _, err := agent.Scan(context.Background(), t.TempDir(), nil); err != nil {
		t.Fatalf("Scan with nil stream: %v", err)
	}
}

// drainStream collects everything sent on stream until close.
func drainStream(stream <-chan string) string {
	var sb strings.Builder
	for s := range stream {
		sb.WriteString(s)
	}
	return sb.String()
}
