// Package llm defines the provider abstraction the Sentra agent uses
// to talk to a language model. It is deliberately small: a Provider
// takes a system prompt, a message history, and a set of advertised
// tools, and returns the assistant's text plus any tool calls it
// emitted. Streaming is opt-in via the stream channel.
//
// Provider implementations live alongside this file:
//   - fake.go       — deterministic scripted impl used by orchestrator tests
//   - anthropic.go  — Claude-backed impl used in production
//
// The interface is intentionally narrower than any single SDK so the
// orchestrator (Phase 11) doesn't leak vendor types into its core.
package llm

import "context"

// Role identifies the speaker of a Message. The string values are
// stable wire-level identifiers — Anthropic and OpenAI both use
// "system" / "user" / "assistant" with a "tool" extension, so we
// match that vocabulary directly. Tests in provider_test.go pin these
// strings so flipping them is a deliberate, breaking change.
type Role string

// Role constants. Use these instead of string literals at call sites
// to keep the typing tight.
const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message is one entry in the conversation. Content holds the text;
// ToolUse and ToolResult are mutually exclusive optional payloads
// describing a tool invocation (assistant turn) or its result (tool
// turn) respectively. A Message with neither is a plain text turn.
//
// We don't enforce the exclusivity at the type level because Go's
// struct ergonomics for "one of" payloads are awkward; the Anthropic
// translator (anthropic.go) and the orchestrator (Phase 11) treat
// ToolUse as taking precedence when both are non-nil.
type Message struct {
	Role       Role
	Content    string
	ToolUse    *ToolUse    // assistant-side tool invocation
	ToolResult *ToolResult // tool-side result of the most recent call
}

// Tool advertises a callable function to the model. Schema is a JSON
// schema document for the input — Anthropic and OpenAI both consume
// the same shape (with minor field-naming differences). The provider
// is responsible for translating into the vendor-specific structure.
//
// We use map[string]any rather than a typed schema struct because the
// schema's authoritative form is JSON and re-typing the (small) set of
// schema features we actually use would just create a parallel
// vocabulary for callers to learn.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// ToolCall is what the orchestrator extracts from the assistant's
// response and dispatches to its tool implementations. ID is the
// provider-assigned correlation token: the corresponding ToolResult
// must echo the same ID so the next assistant turn can match them up.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any // unmarshalled JSON input arguments
}

// ToolUse mirrors ToolCall but lives inside a Message — specifically,
// an assistant-role Message representing a turn in which the model
// asked for a tool to be called. Keeping ToolUse and ToolCall as
// distinct types lets the Provider.Generate signature return calls
// without exposing the message-history shape to callers.
type ToolUse struct {
	ID    string
	Name  string
	Input map[string]any
}

// ToolResult is the tool runner's reply, threaded back into the
// next assistant turn via a tool-role Message. ID must match the
// originating ToolUse.ID so the model can correlate request and
// response. Error is non-empty when the tool itself failed; the
// model decides what to do with that information.
type ToolResult struct {
	ID      string
	Content string
	Error   string
}

// Provider is the abstraction every LLM backend implements.
//
// Generate runs a single round-trip: build the request from sys + msgs
// + tools, optionally stream content tokens onto stream (callers may
// pass nil to disable streaming), collect any ToolCalls the assistant
// emitted, and return the assistant's accumulated text plus those
// tool calls.
//
// Implementation contract:
//   - The stream channel is OWNED BY THE CALLER. Implementations must
//     not close it. Implementations should also be tolerant of
//     backpressure (drop tokens rather than blocking the network read)
//     so a slow consumer can't stall the whole call.
//   - On context cancellation, Generate must return ctx.Err() promptly.
//   - The returned text is the full accumulated assistant message,
//     even when streaming is enabled — callers shouldn't have to
//     reconstruct it from the channel.
type Provider interface {
	Generate(ctx context.Context,
		sys string,
		msgs []Message,
		tools []Tool,
		stream chan<- string,
	) (calls []ToolCall, text string, err error)
}
