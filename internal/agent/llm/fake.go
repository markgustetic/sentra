package llm

import (
	"context"
	"errors"
	"sync"
)

// ErrFakeProviderExhausted is returned by FakeProvider.Generate when
// the scripted Steps slice has been fully consumed. Orchestrator tests
// rely on this to fail fast when a buggy retry loop calls Generate
// more times than the script anticipates.
var ErrFakeProviderExhausted = errors.New("llm: fake provider script exhausted")

// FakeProvider is a deterministic Provider implementation for tests.
// It replays a scripted sequence of FakeSteps: each call to Generate
// consumes one Step from the front of the queue. Calls past the end
// of the script return ErrFakeProviderExhausted.
//
// FakeProvider is safe for concurrent use — Steps consumption and
// Calls recording are guarded by an internal mutex. The race detector
// is the canonical test for this invariant; see TestFake_ConcurrentSafe.
//
// FakeProvider is intentionally a struct rather than constructed via a
// New func: tests build it inline with `&FakeProvider{Steps: ...}`,
// which is more readable than a constructor for a test double.
type FakeProvider struct {
	// Steps is the scripted output queue. Set this before calling
	// Generate. It is consumed in order; concurrent calls interleave.
	Steps []FakeStep

	// Calls records every Generate invocation in order. Tests assert
	// on this slice to verify that the orchestrator built the right
	// request — system prompt, message history, advertised tools.
	//
	// Concurrent calls land in Calls in completion order, not
	// invocation order; tests with concurrent Generate should assert
	// on `len(Calls)` and per-element invariants rather than ordering.
	Calls []FakeCall

	// mu guards both the Steps cursor and Calls appends. We use a
	// single mutex for simplicity; the contention surface is small
	// (orchestrator tests issue ~10 calls).
	mu sync.Mutex

	// cursor is the index of the NEXT Step to consume.
	cursor int
}

// FakeStep is one scripted response. All fields are optional:
//   - Stream tokens are forwarded onto the stream channel before
//     Generate returns. If stream is nil, tokens are silently dropped.
//   - ToolCalls are returned as the assistant's tool invocations.
//   - Text is returned as the assistant's accumulated text.
//   - Err is returned as the Generate error. Returning a non-nil Err
//     with non-empty Text/ToolCalls models "the model said something,
//     then we failed to fully receive the response" — orchestrator
//     tests use this to verify error-paths still propagate context.
type FakeStep struct {
	Stream    []string
	ToolCalls []ToolCall
	Text      string
	Err       error
}

// FakeCall captures the arguments a single Generate invocation was
// called with. The struct mirrors the relevant Provider.Generate
// parameters so test code can use reflect.DeepEqual against expected
// values directly.
type FakeCall struct {
	System string
	Msgs   []Message
	Tools  []Tool
}

// Generate implements Provider. The flow is:
//  1. Honor context cancellation up front. If ctx is already done,
//     return its error without consuming a step or recording a call —
//     this matches the behavior of a real network round-trip aborted
//     before send.
//  2. Pop the next FakeStep under the mutex. If none remain, return
//     ErrFakeProviderExhausted.
//  3. Record the call. Doing this AFTER popping (rather than before)
//     keeps Calls and consumed-Steps in sync 1:1 even when a step is
//     scripted to error.
//  4. Forward Stream tokens onto the stream channel non-blocking — if
//     the caller's reader is slow, drop rather than stall. This mirrors
//     the Anthropic impl's contract and keeps a misbehaving consumer
//     from deadlocking the orchestrator under test.
//  5. Return the step's outputs (including Err, if set).
func (f *FakeProvider) Generate(ctx context.Context, sys string, msgs []Message, tools []Tool, stream chan<- string) ([]ToolCall, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}

	f.mu.Lock()
	if f.cursor >= len(f.Steps) {
		f.mu.Unlock()
		return nil, "", ErrFakeProviderExhausted
	}
	step := f.Steps[f.cursor]
	f.cursor++
	f.Calls = append(f.Calls, FakeCall{
		System: sys,
		Msgs:   msgs,
		Tools:  tools,
	})
	f.mu.Unlock()

	// Stream tokens. We use a non-blocking select with a ctx.Done()
	// fast-path so a cancelled context aborts streaming mid-flight.
	if stream != nil {
		for _, tok := range step.Stream {
			select {
			case <-ctx.Done():
				return step.ToolCalls, step.Text, ctx.Err()
			case stream <- tok:
				// delivered
			default:
				// backpressure — drop token (consumer is slow). The
				// accumulated Text is still returned in full so callers
				// don't need the stream channel to recover the message.
			}
		}
	}

	return step.ToolCalls, step.Text, step.Err
}
