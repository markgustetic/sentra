// SDK pinned at github.com/anthropics/anthropic-sdk-go v1.38.0
// (declared in go.mod). The translation layer below targets that
// release; if the SDK's content-block / streaming-event types change,
// update this file rather than the Provider interface — the whole
// point of llm.Provider is that orchestrator code never sees vendor
// types directly.

package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// defaultMaxTokens caps a single Generate response. 4096 is comfortably
// below all current Claude family model limits while being enough room
// for a multi-finding agent reply.
const defaultMaxTokens = 4096

// AnthropicConfig holds the bits a caller can tune. The zero value
// won't work — Model is mandatory, and either APIKey or the
// ANTHROPIC_API_KEY env var must be set.
type AnthropicConfig struct {
	// Model is the Anthropic model identifier (e.g. "claude-sonnet-4-6").
	// No default: the orchestrator picks one from sentra config.
	Model string

	// MaxTokens caps the response length. Zero is replaced with
	// defaultMaxTokens; the API rejects 0 outright.
	MaxTokens int

	// APIKey is the Anthropic API key. Empty falls back to the
	// ANTHROPIC_API_KEY env var (and ANTHROPIC_AUTH_TOKEN, both honored
	// by the SDK directly). If both are empty, NewAnthropic errors.
	APIKey string
}

// anthropicProvider is the SDK-backed Provider implementation. It is
// unexported because callers should construct it via NewAnthropic;
// only test code in this package needs the struct directly (e.g. to
// point client at an httptest.Server).
type anthropicProvider struct {
	// client is a value, not a pointer in the SDK, so we hold a pointer
	// here to avoid copying the embedded service handles on every call.
	client *anthropic.Client
	model  string

	// maxTokens is the per-request cap. Stored separately from the
	// MessageNewParams so each Generate can set it without rebuilding
	// the whole struct.
	maxTokens int64
}

// NewAnthropic builds a Provider backed by the Anthropic Messages API.
// The returned Provider streams text deltas onto the supplied channel,
// collects tool_use blocks the model emits, and returns the assistant's
// accumulated text plus those tool calls.
//
// API-key resolution order:
//  1. cfg.APIKey (explicit caller value)
//  2. ANTHROPIC_API_KEY env var
//  3. ANTHROPIC_AUTH_TOKEN env var (matches SDK behavior)
//
// If all three are empty, NewAnthropic returns an error rather than
// silently producing a 401-on-every-request client.
func NewAnthropic(cfg AnthropicConfig) (Provider, error) {
	if cfg.Model == "" {
		return nil, errors.New("llm: AnthropicConfig.Model is required")
	}

	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
	}
	if apiKey == "" {
		return nil, errors.New("llm: Anthropic API key not set (provide AnthropicConfig.APIKey or ANTHROPIC_API_KEY)")
	}

	mt := cfg.MaxTokens
	if mt <= 0 {
		mt = defaultMaxTokens
	}

	client := anthropic.NewClient(option.WithAPIKey(apiKey))
	return &anthropicProvider{
		client:    &client,
		model:     cfg.Model,
		maxTokens: int64(mt),
	}, nil
}

// buildRequest translates Sentra's (sys, msgs, tools) tuple into the
// Anthropic SDK's MessageNewParams. Translation rules:
//
//   - The system prompt rides on the top-level System field. Anthropic
//     does NOT accept a system-role message in the messages slice, so a
//     RoleSystem entry in msgs is rejected by Generate; here we just
//     handle the sys arg.
//   - A plain (text-only) Message becomes a single TextBlock under the
//     matching role. We use the SDK's NewUserMessage / NewAssistantMessage
//     helpers so any future schema changes flow through one code path.
//   - An assistant Message with a non-nil ToolUse becomes a tool_use
//     content block.
//   - A RoleTool Message with ToolResult becomes a USER-role message
//     carrying a single tool_result block (Anthropic's convention).
//   - Each Tool advertised becomes a ToolParam under the OfTool variant
//     of ToolUnionParam — we don't use the server-side tools (bash,
//     web_search, etc).
//
// Validation is intentionally light: this method assumes Generate has
// already screened malformed Messages (RoleTool without ToolResult,
// RoleSystem in the slice). buildRequest never errors so it's easy to
// unit-test in isolation.
func (p *anthropicProvider) buildRequest(sys string, msgs []Message, tools []Tool) anthropic.MessageNewParams {
	params := anthropic.MessageNewParams{
		Model:     p.model,
		MaxTokens: p.maxTokens,
	}

	if sys != "" {
		params.System = []anthropic.TextBlockParam{{Text: sys}}
	}

	out := make([]anthropic.MessageParam, 0, len(msgs))
	for _, m := range msgs {
		switch m.Role {
		case RoleUser:
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))

		case RoleAssistant:
			if m.ToolUse != nil {
				// Assistant turn requesting a tool call. The SDK accepts
				// `any` for the input arg and JSON-marshals it; our
				// map[string]any flows through naturally.
				blocks := []anthropic.ContentBlockParamUnion{
					anthropic.NewToolUseBlock(m.ToolUse.ID, m.ToolUse.Input, m.ToolUse.Name),
				}
				// If the assistant turn ALSO had text content alongside
				// the tool_use, prepend it. Real Claude responses commonly
				// emit "thinking out loud" text before the tool_use block.
				if m.Content != "" {
					blocks = append([]anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}, blocks...)
				}
				out = append(out, anthropic.NewAssistantMessage(blocks...))
			} else {
				out = append(out, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
			}

		case RoleTool:
			// RoleTool messages translate to a user-role MessageParam
			// with a tool_result block. The SDK helper handles the
			// IsError flag for us.
			isErr := m.ToolResult.Error != ""
			content := m.ToolResult.Content
			if isErr && content == "" {
				// Some callers may set only Error — surface it as content
				// so the model has something to read.
				content = m.ToolResult.Error
			}
			out = append(out, anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(m.ToolResult.ID, content, isErr),
			))

		case RoleSystem:
			// Should be unreachable — Generate rejects RoleSystem in msgs
			// up-front. If we somehow get here, pretend it's a user turn
			// rather than panicking; the API will surface the actual
			// constraint via its 400 response.
			out = append(out, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		}
	}
	params.Messages = out

	if len(tools) > 0 {
		ts := make([]anthropic.ToolUnionParam, 0, len(tools))
		for _, t := range tools {
			tp := anthropic.ToolParam{
				Name: t.Name,
				InputSchema: anthropic.ToolInputSchemaParam{
					Properties: t.Schema["properties"],
				},
			}
			if t.Description != "" {
				tp.Description = param.NewOpt(t.Description)
			}
			if req, ok := t.Schema["required"].([]string); ok {
				tp.InputSchema.Required = req
			} else if reqAny, ok := t.Schema["required"].([]any); ok {
				// Defensive: if a caller built the schema from JSON, the
				// "required" field will be []any. Coerce to []string.
				strs := make([]string, 0, len(reqAny))
				for _, v := range reqAny {
					if s, ok := v.(string); ok {
						strs = append(strs, s)
					}
				}
				tp.InputSchema.Required = strs
			}
			ts = append(ts, anthropic.ToolUnionParam{OfTool: &tp})
		}
		params.Tools = ts
	}

	return params
}

// validateMessages screens the inbound Messages for shapes Generate
// can't translate. Returning an error here is friendlier than letting
// the SDK 400 with an opaque "invalid_request_error" — the orchestrator
// (Phase 11) needs clear feedback when its prompt-construction logic
// is wrong.
func validateMessages(msgs []Message) error {
	for i, m := range msgs {
		switch m.Role {
		case RoleSystem:
			return fmt.Errorf("llm: messages[%d] has Role=system; pass the system prompt via the sys argument instead", i)
		case RoleTool:
			if m.ToolResult == nil {
				return fmt.Errorf("llm: messages[%d] has Role=tool but ToolResult is nil", i)
			}
		case RoleUser, RoleAssistant:
			// no constraints beyond what buildRequest enforces
		default:
			return fmt.Errorf("llm: messages[%d] has unknown Role %q", i, m.Role)
		}
	}
	return nil
}

// Generate runs a single round-trip: build the request, open the SDK
// streaming connection, walk the event stream forwarding text deltas
// to the stream channel and accumulating tool_use blocks, then return
// the (toolCalls, text, err) triple.
//
// Streaming model:
//   - Each ContentBlockStartEvent for type=tool_use seeds a partial
//     ToolCall (ID + Name + empty input buffer).
//   - Each ContentBlockDeltaEvent of type input_json_delta appends to
//     the active tool call's JSON buffer.
//   - Each text_delta appends to the accumulated text and is forwarded
//     onto stream (non-blocking — drop on backpressure to avoid
//     stalling the upstream HTTP read on a slow consumer).
//   - On message_stop, parse each tool call's JSON buffer into Input.
//
// Error handling:
//   - Context cancellation propagates: the SDK's stream.Err() returns
//     ctx.Err() once the underlying request is cancelled.
//   - HTTP errors (4xx/5xx) are surfaced via stream.Err() too.
//   - Malformed input_json buffers (rare — only happens if the model
//     emits non-JSON) are returned as a wrapped error.
func (p *anthropicProvider) Generate(ctx context.Context, sys string, msgs []Message, tools []Tool, stream chan<- string) ([]ToolCall, string, error) {
	if err := validateMessages(msgs); err != nil {
		return nil, "", err
	}

	params := p.buildRequest(sys, msgs, tools)
	sseStream := p.client.Messages.NewStreaming(ctx, params)
	defer func() { _ = sseStream.Close() }()

	// Per-content-block accumulation state. The Anthropic stream
	// interleaves blocks by index; index N's deltas can arrive before
	// block N+1 ever starts. We key both maps by index to keep them
	// independently addressable.
	type pendingTool struct {
		id   string
		name string
		raw  []byte // partial input_json
	}
	tools_ := make(map[int64]*pendingTool)

	var (
		text  []byte // accumulated assistant text
		calls []ToolCall
	)

	for sseStream.Next() {
		// Honor context cancellation between events. The SDK already
		// surfaces ctx errors via Err() at end-of-stream, but checking
		// here lets us short-circuit a slow stream without waiting for
		// the next event to arrive.
		if err := ctx.Err(); err != nil {
			return calls, string(text), err
		}

		evt := sseStream.Current()
		switch evt.Type {
		case "content_block_start":
			cbs := evt.AsContentBlockStart()
			if cbs.ContentBlock.Type == "tool_use" {
				tools_[cbs.Index] = &pendingTool{
					id:   cbs.ContentBlock.ID,
					name: cbs.ContentBlock.Name,
				}
			}

		case "content_block_delta":
			cbd := evt.AsContentBlockDelta()
			switch cbd.Delta.Type {
			case "text_delta":
				td := cbd.Delta.AsTextDelta()
				if td.Text != "" {
					text = append(text, td.Text...)
					if stream != nil {
						// Non-blocking: if the consumer is slow, drop the
						// token. The Provider contract guarantees the
						// accumulated text is returned in full, so a slow
						// reader can still recover the full message —
						// they just lose the per-token streaming view.
						select {
						case stream <- td.Text:
						default:
						}
					}
				}

			case "input_json_delta":
				ijd := cbd.Delta.AsInputJSONDelta()
				if pt, ok := tools_[cbd.Index]; ok {
					pt.raw = append(pt.raw, ijd.PartialJSON...)
				}
			}

		case "content_block_stop":
			// Nothing to do: we finalize tool calls in message_stop so
			// the slice is built in a single deterministic place.

		case "message_stop":
			// End of response — fall through to post-loop finalization.
		}
	}

	if err := sseStream.Err(); err != nil {
		// Even on error, return whatever text/calls we accumulated so
		// the orchestrator can decide whether the partial response is
		// useful (e.g. a streaming abort mid-text).
		return calls, string(text), err
	}

	// Finalize tool calls in index order so the returned slice is
	// stable across runs (orchestrator tests assert on it directly).
	indices := make([]int64, 0, len(tools_))
	for idx := range tools_ {
		indices = append(indices, idx)
	}
	// Tiny insertion-sort — n is small (typically 0–3 tools per turn).
	for i := 1; i < len(indices); i++ {
		for j := i; j > 0 && indices[j-1] > indices[j]; j-- {
			indices[j-1], indices[j] = indices[j], indices[j-1]
		}
	}

	for _, idx := range indices {
		pt := tools_[idx]
		input := map[string]any{}
		if len(pt.raw) > 0 {
			if err := json.Unmarshal(pt.raw, &input); err != nil {
				return calls, string(text), fmt.Errorf("llm: tool %q (id=%s) emitted invalid JSON input: %w", pt.name, pt.id, err)
			}
		}
		calls = append(calls, ToolCall{
			ID:    pt.id,
			Name:  pt.name,
			Input: input,
		})
	}

	return calls, string(text), nil
}
