package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
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

// TestParseRecommendations_PlainArray: the canonical happy path —
// the model emits a bare JSON array, no wrapping. Must parse cleanly.
func TestParseRecommendations_PlainArray(t *testing.T) {
	in := `[{"id":"r1","action":"flag_secret","target":"/x","severity":"warn","rationale":"hi"}]`
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r1" || recs[0].Action != "flag_secret" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_FencedJSON: the model wraps the array in a
// ```json … ``` code fence (Anthropic's Sonnet does this often, even
// after explicit instructions not to). Must strip the fence and parse.
func TestParseRecommendations_FencedJSON(t *testing.T) {
	in := "```json\n[{\"id\":\"r1\",\"action\":\"none\",\"target\":\"/x\",\"severity\":\"info\",\"rationale\":\"hi\"}]\n```"
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r1" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_FencedNoLang: the model emits a generic
// ``` … ``` fence with no language tag.
func TestParseRecommendations_FencedNoLang(t *testing.T) {
	in := "```\n[{\"id\":\"r2\",\"action\":\"none\",\"target\":\"/y\",\"severity\":\"info\",\"rationale\":\"k\"}]\n```"
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r2" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_ProsePrefix: the model prepends an English
// sentence to the array. The parser scans for the first '[' and parses
// from there.
func TestParseRecommendations_ProsePrefix(t *testing.T) {
	in := "Here are my findings:\n[{\"id\":\"r3\",\"action\":\"none\",\"target\":\"/z\",\"severity\":\"info\",\"rationale\":\"k\"}]"
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r3" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_ProseSuffix: the model appends prose after
// the array. The parser scans to the LAST ']' and trims.
func TestParseRecommendations_ProseSuffix(t *testing.T) {
	in := "[{\"id\":\"r4\",\"action\":\"none\",\"target\":\"/q\",\"severity\":\"info\",\"rationale\":\"k\"}]\nHope this helps!"
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r4" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_SingleObject: a single Recommendation
// object (not an array) is auto-wrapped to a one-element slice.
// Anthropic's Haiku occasionally returns this shape.
func TestParseRecommendations_SingleObject(t *testing.T) {
	in := `{"id":"r5","action":"none","target":"/s","severity":"info","rationale":"k"}`
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r5" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_GarbageReturnsClearError: random text with
// no JSON shape returns ErrInvalidResponse, with a snippet of the
// response body for debugging. Empty body still maps to a clear error.
func TestParseRecommendations_GarbageReturnsClearError(t *testing.T) {
	cases := []string{
		"this is not json at all",
		"",
		"   \n  ",
		"{not even valid",
	}
	for _, in := range cases {
		_, err := parseRecommendations(in)
		if err == nil {
			t.Errorf("expected error for input %q, got nil", in)
		}
	}
}

// TestParseRecommendations_GarbageIncludesSnippet: the error message
// for clearly-garbage input includes a snippet of the response so the
// CLI/log surface points the operator at what went wrong.
func TestParseRecommendations_GarbageIncludesSnippet(t *testing.T) {
	in := "totally not json: I forgot how to format things"
	_, err := parseRecommendations(in)
	if err == nil {
		t.Fatal("expected an error")
	}
	// The snippet should include enough characters to identify the
	// payload when read back from a log.
	if !strings.Contains(err.Error(), "totally not json") {
		t.Errorf("expected snippet of the input in error, got %v", err)
	}
}

// TestParseRecommendations_FencedSingleObject: a fenced single object
// (Sonnet's other favorite shape) should also work.
func TestParseRecommendations_FencedSingleObject(t *testing.T) {
	in := "```json\n{\"id\":\"r6\",\"action\":\"none\",\"target\":\"/o\",\"severity\":\"info\",\"rationale\":\"k\"}\n```"
	recs, err := parseRecommendations(in)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 1 || recs[0].ID != "r6" {
		t.Fatalf("unexpected recs: %+v", recs)
	}
}

// TestParseRecommendations_EmptyArray: the explicit "nothing to
// recommend" response — must remain a valid no-op result, not an error.
func TestParseRecommendations_EmptyArray(t *testing.T) {
	recs, err := parseRecommendations("[]")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected empty slice, got %+v", recs)
	}
}

// captureFindingsHeuristic is a Heuristic that records the Input it
// received on Run, so tests can assert on what the orchestrator
// populated. Returns no findings of its own.
type captureFindingsHeuristic struct {
	gotInput heuristics.Input
}

func (c *captureFindingsHeuristic) Name() string { return "capture" }
func (c *captureFindingsHeuristic) Run(_ context.Context, in heuristics.Input) ([]heuristics.Finding, error) {
	c.gotInput = in
	return nil, nil
}

// agentWithRealOrphanHeuristic builds an Agent with a real OrphanBlobs
// heuristic and a (default) fake provider. Callers swap the provider
// when they need scripted output; otherwise an unscripted Provider
// will error if invoked, which is what the no-false-orphans test wants.
func agentWithRealOrphanHeuristic(t *testing.T) (*Agent, *repo.Repo, *llm.FakeProvider) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	registry := heuristics.NewRegistry(heuristics.NewOrphanBlobs())
	provider := &llm.FakeProvider{}
	return &Agent{
		Repo:       r,
		Heuristics: registry,
		Provider:   provider,
		Config: Config{
			MaxFindingsToLLM: 50,
			MaxToolCalls:     5,
			Model:            "test-model",
		},
	}, r, provider
}

// TestScan_PopulatesLiveBlobs_NoFalseOrphans:
// production's defaultHeuristics() includes OrphanBlobs, which treats
// nil LiveBlobs as "empty live set" — that flags every blob in data/
// as orphaned. The orchestrator must populate Input.LiveBlobs from
// the repo's snapshots so a real-world scan against a freshly created
// snapshot does NOT generate spurious orphan findings.
//
// We use an UNSCRIPTED FakeProvider: if the orchestrator forgot to
// populate LiveBlobs, the heuristic emits findings, the orchestrator
// calls the provider, and Generate fails with ErrFakeProviderExhausted.
// That failure mode is exactly the bug we want to surface.
func TestScan_PopulatesLiveBlobs_NoFalseOrphans(t *testing.T) {
	a, r, provider := agentWithRealOrphanHeuristic(t)

	// Build a directory with a real file and snapshot it. Every chunk
	// the snapshot uploads MUST end up in Input.LiveBlobs, otherwise
	// orphan_blobs flags it.
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body for chunking"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "test"}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Sanity: the snapshot uploaded at least one chunk; otherwise the
	// "no false orphans" claim is vacuous.
	dataEntries, err := r.Store().List(context.Background(), "data/")
	if err != nil {
		t.Fatalf("list data: %v", err)
	}
	if len(dataEntries) == 0 {
		t.Fatal("expected at least one data/ blob from the snapshot")
	}

	recs, err := a.Scan(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("expected 0 recommendations (no findings), got %d: %+v", len(recs), recs)
	}
	// Provider must NOT have been called: zero findings → no LLM round trip.
	if len(provider.Calls) != 0 {
		t.Errorf("Provider must not be called when there are no findings, got %d calls — bug: orchestrator missing LiveBlobs caused false orphan findings",
			len(provider.Calls))
	}
}

// TestScan_PopulatesLiveBlobs_SurfacesRealOrphan: the orchestrator
// must NOT mask real orphans. Set up a repo, snapshot it normally,
// then add an extra unreferenced data/ blob directly to the store.
// orphan_blobs should surface a finding for that key, and (since the
// initial Generate sees that finding's payload) the provider's first
// call must include the orphan key in the user message.
func TestScan_PopulatesLiveBlobs_SurfacesRealOrphan(t *testing.T) {
	a, r, _ := agentWithRealOrphanHeuristic(t)

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "test"}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Drop an unreferenced blob into data/ — the perfect orphan.
	orphanKey := "data/zz/zz" + strings.Repeat("0", 62)
	if err := r.Store().Put(context.Background(), orphanKey, bytes.NewReader([]byte("unreferenced"))); err != nil {
		t.Fatalf("put orphan: %v", err)
	}

	provider := &llm.FakeProvider{Steps: []llm.FakeStep{
		{Text: `[{"id":"r1","action":"none","target":"` + orphanKey + `","severity":"warn","rationale":"orphan"}]`},
	}}
	a.Provider = provider

	recs, err := a.Scan(context.Background(), src, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("expected 1 recommendation surfacing the orphan, got %d: %+v", len(recs), recs)
	}
	if recs[0].Target != orphanKey {
		t.Errorf("orphan target mismatch: got %s, want %s", recs[0].Target, orphanKey)
	}
	// And the initial user message that went to the LLM should contain
	// the orphan key — verifies the orchestrator passed the right
	// findings to the provider.
	if len(provider.Calls) != 1 {
		t.Fatalf("expected 1 provider call, got %d", len(provider.Calls))
	}
	hasOrphanInMsgs := false
	for _, m := range provider.Calls[0].Msgs {
		if strings.Contains(m.Content, orphanKey) {
			hasOrphanInMsgs = true
		}
	}
	if !hasOrphanInMsgs {
		t.Errorf("expected the initial user message to mention orphan key %s; msgs=%+v",
			orphanKey, provider.Calls[0].Msgs)
	}

	// The findings sent to the LLM should reflect EXACTLY ONE orphan —
	// the one we deliberately seeded. If LiveBlobs were missing/empty,
	// the snapshot's chunks would also surface as "orphans" and the
	// total finding count would be > 1.
	initialUser := ""
	for _, m := range provider.Calls[0].Msgs {
		if m.Role == llm.RoleUser {
			initialUser = m.Content
			break
		}
	}
	wantPrefix := "Heuristic findings (1 total):"
	if !strings.Contains(initialUser, wantPrefix) {
		t.Errorf("expected exactly one finding (the seeded orphan); user msg did not contain %q.\nGot: %s",
			wantPrefix, initialUser)
	}
}

// TestScan_PopulatesInput_SnapshotsAndLiveBlobs is a unit-level check
// that verifies the orchestrator hands the heuristic an Input
// populated with both Snapshots and LiveBlobs (the fields that, if
// missing, cause the C1 false-orphan bug).
func TestScan_PopulatesInput_SnapshotsAndLiveBlobs(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body for chunking"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "t"}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	cap := &captureFindingsHeuristic{}
	provider := &llm.FakeProvider{}
	a := &Agent{
		Repo:       r,
		Heuristics: heuristics.NewRegistry(cap),
		Provider:   provider,
		Config: Config{
			MaxFindingsToLLM: 50,
			MaxToolCalls:     5,
			Model:            "test",
		},
	}
	if _, err := a.Scan(context.Background(), src, nil); err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(cap.gotInput.Snapshots) != 1 {
		t.Errorf("Input.Snapshots: got %d, want 1", len(cap.gotInput.Snapshots))
	}
	if cap.gotInput.LiveBlobs == nil {
		t.Fatal("Input.LiveBlobs is nil; orchestrator must populate it")
	}
	// Sanity: at least one live blob keyed under data/.
	if len(cap.gotInput.LiveBlobs) == 0 {
		t.Errorf("Input.LiveBlobs is empty; expected ≥1 chunk reference")
	}
	for k := range cap.gotInput.LiveBlobs {
		if !strings.HasPrefix(k, "data/") {
			t.Errorf("LiveBlobs key %q not prefixed with data/", k)
		}
	}
}
