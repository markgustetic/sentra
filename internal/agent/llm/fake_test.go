package llm

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestFake_ReplaysSteps scripts two steps, calls Generate twice, and
// asserts each invocation returns the matching step's outputs in order.
func TestFake_ReplaysSteps(t *testing.T) {
	step1 := FakeStep{
		Text: "first response",
		ToolCalls: []ToolCall{
			{ID: "u1", Name: "list_snapshots", Input: map[string]any{"limit": float64(5)}},
		},
	}
	step2 := FakeStep{
		Text: "second response",
		// no tool calls
	}
	f := &FakeProvider{Steps: []FakeStep{step1, step2}}

	calls, text, err := f.Generate(context.Background(), "sys", nil, nil, nil)
	if err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	if text != "first response" {
		t.Errorf("first text = %q, want %q", text, "first response")
	}
	if !reflect.DeepEqual(calls, step1.ToolCalls) {
		t.Errorf("first calls = %+v, want %+v", calls, step1.ToolCalls)
	}

	calls, text, err = f.Generate(context.Background(), "sys", nil, nil, nil)
	if err != nil {
		t.Fatalf("second Generate: %v", err)
	}
	if text != "second response" {
		t.Errorf("second text = %q, want %q", text, "second response")
	}
	if len(calls) != 0 {
		t.Errorf("second calls = %+v, want empty", calls)
	}
}

// TestFake_RecordsCalls verifies that every Generate invocation lands
// in f.Calls verbatim — system prompt, message slice, tool slice. This
// is the load-bearing test for Phase 11 orchestrator assertions.
func TestFake_RecordsCalls(t *testing.T) {
	f := &FakeProvider{Steps: []FakeStep{{Text: "ok"}, {Text: "ok"}}}

	sysA := "you are agent A"
	msgsA := []Message{{Role: RoleUser, Content: "hello"}}
	toolsA := []Tool{{Name: "stat", Description: "stats tool"}}

	if _, _, err := f.Generate(context.Background(), sysA, msgsA, toolsA, nil); err != nil {
		t.Fatalf("Generate A: %v", err)
	}

	sysB := "you are agent B"
	msgsB := []Message{
		{Role: RoleAssistant, ToolUse: &ToolUse{ID: "u1", Name: "stat"}},
		{Role: RoleTool, ToolResult: &ToolResult{ID: "u1", Content: "{}"}},
	}
	toolsB := []Tool{{Name: "stat", Description: "stats tool"}}

	if _, _, err := f.Generate(context.Background(), sysB, msgsB, toolsB, nil); err != nil {
		t.Fatalf("Generate B: %v", err)
	}

	if len(f.Calls) != 2 {
		t.Fatalf("Calls len = %d, want 2", len(f.Calls))
	}
	if f.Calls[0].System != sysA || !reflect.DeepEqual(f.Calls[0].Msgs, msgsA) || !reflect.DeepEqual(f.Calls[0].Tools, toolsA) {
		t.Errorf("Calls[0] mismatch: got %+v", f.Calls[0])
	}
	if f.Calls[1].System != sysB || !reflect.DeepEqual(f.Calls[1].Msgs, msgsB) || !reflect.DeepEqual(f.Calls[1].Tools, toolsB) {
		t.Errorf("Calls[1] mismatch: got %+v", f.Calls[1])
	}
}

// TestFake_StreamsTokens passes a buffered stream channel and asserts
// that the scripted stream tokens arrive in order before Generate
// returns.
func TestFake_StreamsTokens(t *testing.T) {
	tokens := []string{"hel", "lo ", "world"}
	f := &FakeProvider{Steps: []FakeStep{{Stream: tokens, Text: "hello world"}}}

	stream := make(chan string, len(tokens))
	_, text, err := f.Generate(context.Background(), "sys", nil, nil, stream)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}
	close(stream)

	got := make([]string, 0, len(tokens))
	for tok := range stream {
		got = append(got, tok)
	}
	if !reflect.DeepEqual(got, tokens) {
		t.Errorf("stream tokens = %v, want %v", got, tokens)
	}
}

// TestFake_StreamsTokensRespectsBackpressure: an unbuffered channel
// with no reader must not deadlock Generate. The fake should drop
// tokens rather than block — same contract as the Anthropic impl.
func TestFake_StreamsTokensRespectsBackpressure(t *testing.T) {
	tokens := []string{"a", "b", "c"}
	f := &FakeProvider{Steps: []FakeStep{{Stream: tokens, Text: "abc"}}}

	stream := make(chan string) // unbuffered, no reader
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = f.Generate(context.Background(), "sys", nil, nil, stream)
	}()
	select {
	case <-done:
		// success — Generate returned without a reader
	case <-time.After(2 * time.Second):
		t.Fatalf("Generate blocked on unread stream channel")
	}
}

// TestFake_NilStreamIsAllowed: callers commonly pass nil to disable
// streaming. The fake must tolerate that even when the step has
// scripted tokens.
func TestFake_NilStreamIsAllowed(t *testing.T) {
	f := &FakeProvider{Steps: []FakeStep{{Stream: []string{"x", "y"}, Text: "xy"}}}
	_, text, err := f.Generate(context.Background(), "sys", nil, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "xy" {
		t.Errorf("text = %q, want %q", text, "xy")
	}
}

// TestFake_ExhaustedReturnsError calls past the end of the script and
// asserts the sentinel error. Orchestrator tests use this to fail fast
// when a buggy loop calls Generate one too many times.
func TestFake_ExhaustedReturnsError(t *testing.T) {
	f := &FakeProvider{Steps: []FakeStep{{Text: "only one"}}}
	if _, _, err := f.Generate(context.Background(), "sys", nil, nil, nil); err != nil {
		t.Fatalf("first Generate: %v", err)
	}
	_, _, err := f.Generate(context.Background(), "sys", nil, nil, nil)
	if !errors.Is(err, ErrFakeProviderExhausted) {
		t.Errorf("err = %v, want ErrFakeProviderExhausted", err)
	}
}

// TestFake_PropagatesContextCancel: a cancelled context must short
// circuit Generate before any step is consumed. Both the returned
// error and the (un-incremented) cursor must reflect that.
func TestFake_PropagatesContextCancel(t *testing.T) {
	f := &FakeProvider{Steps: []FakeStep{{Text: "should not fire"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err := f.Generate(ctx, "sys", nil, nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(f.Calls) != 0 {
		t.Errorf("Calls len = %d, want 0 (cancel before consume)", len(f.Calls))
	}
}

// TestFake_PropagatesStepError: a scripted Err is returned alongside
// any text/tool calls the step also defines, so tests can simulate
// "model returned text, then errored mid-stream."
func TestFake_PropagatesStepError(t *testing.T) {
	want := errors.New("rate limited")
	f := &FakeProvider{Steps: []FakeStep{{Text: "partial", Err: want}}}
	_, text, err := f.Generate(context.Background(), "sys", nil, nil, nil)
	if !errors.Is(err, want) {
		t.Errorf("err = %v, want %v", err, want)
	}
	if text != "partial" {
		t.Errorf("text = %q, want %q", text, "partial")
	}
}

// TestFake_ConcurrentSafe runs many Generate calls in parallel against
// a single provider with enough scripted steps to satisfy them. Race
// detector + atomic counter pin down the locking contract.
func TestFake_ConcurrentSafe(t *testing.T) {
	const n = 64
	steps := make([]FakeStep, n)
	for i := range steps {
		steps[i] = FakeStep{Text: "ok"}
	}
	f := &FakeProvider{Steps: steps}

	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := f.Generate(context.Background(), "sys", nil, nil, nil); err == nil {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != int64(n) {
		t.Errorf("successful calls = %d, want %d", ok.Load(), n)
	}
	if len(f.Calls) != n {
		t.Errorf("recorded calls = %d, want %d", len(f.Calls), n)
	}
}
