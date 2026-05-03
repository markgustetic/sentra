package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// newTestProvider builds an *anthropicProvider pointed at the given
// HTTP server, suitable for use in unit tests. The Anthropic Client is
// constructed inline rather than via NewAnthropic so we can swap the
// transport without touching the env-var fallback.
func newTestProvider(t *testing.T, server *httptest.Server, model string) *anthropicProvider {
	t.Helper()
	if model == "" {
		model = "claude-sonnet-4-6"
	}
	client := anthropic.NewClient(
		option.WithBaseURL(server.URL),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	return &anthropicProvider{
		client:    &client,
		model:     model,
		maxTokens: 4096,
	}
}

// TestAnthropic_RequiresCredential: NewAnthropic with no APIKey and
// no env var should return a clear error rather than silently
// producing a client that 401s on every request.
func TestAnthropic_RequiresCredential(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	_, err := NewAnthropic(AnthropicConfig{Model: "claude-sonnet-4-6"})
	if err == nil {
		t.Fatal("NewAnthropic with no credential returned nil error")
	}
	msg := strings.ToLower(err.Error())
	// Error should help the user understand which env vars to set.
	if !strings.Contains(msg, "credential") &&
		!strings.Contains(msg, "anthropic_api_key") {
		t.Errorf("error should mention credential or env var, got: %v", err)
	}
}

// TestAnthropic_AcceptsAPIKeyFromEnv: when the env var is set,
// NewAnthropic with an empty config APIKey should succeed.
func TestAnthropic_AcceptsAPIKeyFromEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-from-env")
	p, err := NewAnthropic(AnthropicConfig{Model: "claude-sonnet-4-6"})
	if err != nil {
		t.Fatalf("NewAnthropic from env: %v", err)
	}
	if p == nil {
		t.Fatal("NewAnthropic returned nil provider")
	}
}

// TestAnthropic_RequiresModel: a Model is mandatory — there is no
// sensible default and silently picking one would surprise callers.
func TestAnthropic_RequiresModel(t *testing.T) {
	_, err := NewAnthropic(AnthropicConfig{APIKey: "test"})
	if err == nil {
		t.Fatal("NewAnthropic with no Model returned nil error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "model") {
		t.Errorf("error should mention model, got: %v", err)
	}
}

// TestAnthropic_DefaultMaxTokens: zero MaxTokens should be replaced
// with the documented default rather than passed verbatim (the API
// rejects max_tokens=0).
func TestAnthropic_DefaultMaxTokens(t *testing.T) {
	p, err := NewAnthropic(AnthropicConfig{Model: "claude-sonnet-4-6", APIKey: "test"})
	if err != nil {
		t.Fatalf("NewAnthropic: %v", err)
	}
	if got := p.(*anthropicProvider).maxTokens; got != 4096 {
		t.Errorf("default maxTokens = %d, want 4096", got)
	}
}

// TestAnthropic_BuildRequest_SystemPrompt: a non-empty system prompt
// becomes a TextBlockParam in the System slice.
func TestAnthropic_BuildRequest_SystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	params := p.buildRequest("you are sentra", []Message{{Role: RoleUser, Content: "hi"}}, nil)

	if len(params.System) != 1 || params.System[0].Text != "you are sentra" {
		t.Errorf("System = %+v, want one block with 'you are sentra'", params.System)
	}
}

// TestAnthropic_BuildRequest_EmptySystem: an empty system prompt
// should yield no system block — the API treats nil/empty differently.
func TestAnthropic_BuildRequest_EmptySystem(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	params := p.buildRequest("", []Message{{Role: RoleUser, Content: "hi"}}, nil)

	if len(params.System) != 0 {
		t.Errorf("System = %+v, want empty when no system prompt", params.System)
	}
}

// TestAnthropic_BuildRequest_TextRoles: plain text user / assistant
// turns become single-block TextBlockParam content.
func TestAnthropic_BuildRequest_TextRoles(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{Role: RoleUser, Content: "hello"},
		{Role: RoleAssistant, Content: "hi there"},
	}
	params := p.buildRequest("", msgs, nil)

	if len(params.Messages) != 2 {
		t.Fatalf("Messages len = %d, want 2", len(params.Messages))
	}
	if params.Messages[0].Role != anthropic.MessageParamRoleUser {
		t.Errorf("first role = %q, want user", params.Messages[0].Role)
	}
	if params.Messages[1].Role != anthropic.MessageParamRoleAssistant {
		t.Errorf("second role = %q, want assistant", params.Messages[1].Role)
	}
	// Each message has a single text block.
	for i, m := range params.Messages {
		if len(m.Content) != 1 {
			t.Errorf("Messages[%d].Content len = %d, want 1", i, len(m.Content))
			continue
		}
		if m.Content[0].OfText == nil {
			t.Errorf("Messages[%d].Content[0] is not a text block", i)
		}
	}
	if got := params.Messages[0].Content[0].OfText.Text; got != "hello" {
		t.Errorf("first text = %q, want hello", got)
	}
}

// TestAnthropic_BuildRequest_AssistantToolUse: an assistant-role
// Message with ToolUse becomes a tool_use content block.
func TestAnthropic_BuildRequest_AssistantToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{
			Role: RoleAssistant,
			ToolUse: &ToolUse{
				ID:    "tool_1",
				Name:  "snapshot_stats",
				Input: map[string]any{"id": "abc"},
			},
		},
	}
	params := p.buildRequest("", msgs, nil)

	if len(params.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(params.Messages))
	}
	m := params.Messages[0]
	if m.Role != anthropic.MessageParamRoleAssistant {
		t.Errorf("role = %q, want assistant", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].OfToolUse == nil {
		t.Fatalf("Content = %+v, want one tool_use block", m.Content)
	}
	tu := m.Content[0].OfToolUse
	if tu.ID != "tool_1" || tu.Name != "snapshot_stats" {
		t.Errorf("tool_use ID/Name = %q/%q, want tool_1/snapshot_stats", tu.ID, tu.Name)
	}
}

// TestAnthropic_BuildRequest_ToolResult: a tool-role Message with
// ToolResult becomes a USER-role MessageParam carrying a tool_result
// content block. (Anthropic API convention: tool results live under
// the user role.)
func TestAnthropic_BuildRequest_ToolResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{
			Role: RoleTool,
			ToolResult: &ToolResult{
				ID:      "tool_1",
				Content: `{"count":3}`,
			},
		},
	}
	params := p.buildRequest("", msgs, nil)

	if len(params.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(params.Messages))
	}
	m := params.Messages[0]
	if m.Role != anthropic.MessageParamRoleUser {
		t.Errorf("role = %q, want user (tool_result lives under user)", m.Role)
	}
	if len(m.Content) != 1 || m.Content[0].OfToolResult == nil {
		t.Fatalf("Content = %+v, want one tool_result block", m.Content)
	}
	tr := m.Content[0].OfToolResult
	if tr.ToolUseID != "tool_1" {
		t.Errorf("ToolUseID = %q, want tool_1", tr.ToolUseID)
	}
}

// TestAnthropic_BuildRequest_AssistantTextPlusToolUse: a real Claude
// "thinking out loud" turn often produces text BEFORE the tool_use
// block; we preserve that ordering so the model's reasoning trace
// survives the round-trip back into the next turn.
func TestAnthropic_BuildRequest_AssistantTextPlusToolUse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{
			Role:    RoleAssistant,
			Content: "Let me check the snapshot count.",
			ToolUse: &ToolUse{ID: "u1", Name: "snapshot_stats", Input: map[string]any{"id": "x"}},
		},
	}
	params := p.buildRequest("", msgs, nil)
	if len(params.Messages) != 1 {
		t.Fatalf("Messages len = %d, want 1", len(params.Messages))
	}
	blocks := params.Messages[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks len = %d, want 2 (text + tool_use)", len(blocks))
	}
	if blocks[0].OfText == nil || blocks[0].OfText.Text != "Let me check the snapshot count." {
		t.Errorf("first block should be text 'Let me check...': %+v", blocks[0])
	}
	if blocks[1].OfToolUse == nil || blocks[1].OfToolUse.ID != "u1" {
		t.Errorf("second block should be tool_use with ID u1: %+v", blocks[1])
	}
}

// TestAnthropic_BuildRequest_ToolResultErrorOnly: when Error is set but
// Content is empty, surface the error text as the tool_result content
// so the model has something to read. Otherwise the model sees an
// empty result and gets confused.
func TestAnthropic_BuildRequest_ToolResultErrorOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{
			Role: RoleTool,
			ToolResult: &ToolResult{
				ID:    "u1",
				Error: "snapshot id not found",
			},
		},
	}
	params := p.buildRequest("", msgs, nil)
	tr := params.Messages[0].Content[0].OfToolResult
	if tr == nil {
		t.Fatal("tool_result block missing")
	}
	if !tr.IsError.Or(false) {
		t.Errorf("IsError = false, want true")
	}
	// Content is a content-block array; the helper produces one text block.
	if len(tr.Content) != 1 || tr.Content[0].OfText == nil || tr.Content[0].OfText.Text != "snapshot id not found" {
		t.Errorf("Content blocks = %+v, want one text block 'snapshot id not found'", tr.Content)
	}
}

// TestAnthropic_BuildRequest_ToolsRequiredAsAnySlice: a Schema built
// from JSON unmarshalling will have "required" as []any, not
// []string. Verify we coerce correctly so JSON-shaped tool schemas
// (the natural shape after koanf/yaml load) don't lose the required
// list.
func TestAnthropic_BuildRequest_ToolsRequiredAsAnySlice(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	tools := []Tool{
		{
			Name: "list_snapshots",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
				// JSON unmarshal produces []any, not []string.
				"required": []any{"limit", "since"},
			},
		},
	}
	params := p.buildRequest("", nil, tools)
	got := params.Tools[0].OfTool
	if got == nil {
		t.Fatal("OfTool is nil")
	}
	if !reflect.DeepEqual(got.InputSchema.Required, []string{"limit", "since"}) {
		t.Errorf("Required = %+v, want [limit since]", got.InputSchema.Required)
	}
}

// TestAnthropic_BuildRequest_ToolResultWithError: a ToolResult with a
// non-empty Error should set IsError=true on the SDK block so the
// model knows the tool failed.
func TestAnthropic_BuildRequest_ToolResultWithError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	msgs := []Message{
		{
			Role: RoleTool,
			ToolResult: &ToolResult{
				ID:      "tool_1",
				Content: "broken",
				Error:   "snapshot not found",
			},
		},
	}
	params := p.buildRequest("", msgs, nil)
	tr := params.Messages[0].Content[0].OfToolResult
	if tr == nil {
		t.Fatal("tool_result block missing")
	}
	if !tr.IsError.Or(false) {
		t.Errorf("IsError = false, want true for errored tool result")
	}
}

// TestAnthropic_BuildRequest_Tools: each Tool advertised becomes a
// ToolParam with name, description, and the input schema.
func TestAnthropic_BuildRequest_Tools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	tools := []Tool{
		{
			Name:        "list_snapshots",
			Description: "List snapshots in the repo.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer"},
				},
				"required": []string{"limit"},
			},
		},
	}
	params := p.buildRequest("", nil, tools)

	if len(params.Tools) != 1 {
		t.Fatalf("Tools len = %d, want 1", len(params.Tools))
	}
	got := params.Tools[0].OfTool
	if got == nil {
		t.Fatalf("Tools[0].OfTool is nil — wrong tool variant")
	}
	if got.Name != "list_snapshots" {
		t.Errorf("Name = %q, want list_snapshots", got.Name)
	}
	if got.Description.Or("") != "List snapshots in the repo." {
		t.Errorf("Description = %q, want 'List snapshots in the repo.'", got.Description.Or(""))
	}
	if !reflect.DeepEqual(got.InputSchema.Required, []string{"limit"}) {
		t.Errorf("Required = %+v, want [limit]", got.InputSchema.Required)
	}
}

// TestAnthropic_BuildRequest_ModelAndMaxTokens: the configured Model
// and MaxTokens flow through to the Anthropic params.
func TestAnthropic_BuildRequest_ModelAndMaxTokens(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "claude-opus-4-5")
	p.maxTokens = 1024

	params := p.buildRequest("", nil, nil)

	if params.Model != "claude-opus-4-5" {
		t.Errorf("Model = %q, want claude-opus-4-5", params.Model)
	}
	if params.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %d, want 1024", params.MaxTokens)
	}
}

// TestAnthropic_Generate_StreamsTextAndExtractsToolUse exercises the
// full Generate happy path against a fake SSE server. We feed canned
// events, assert the stream channel receives the text deltas, and
// verify the returned (toolCalls, text) pair.
func TestAnthropic_Generate_StreamsTextAndExtractsToolUse(t *testing.T) {
	events := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello "}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"snapshot_stats","input":{}}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"id\":\"abc\"}"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":1}`,
		``,
		`event: message_delta`,
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":5}}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("http.Flusher not implemented by test server")
		}
		for _, event := range events {
			fmt.Fprintf(w, "%s\n", event)
		}
		flusher.Flush()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "claude-sonnet-4-6")

	stream := make(chan string, 16)
	calls, text, err := p.Generate(context.Background(), "sys", []Message{{Role: RoleUser, Content: "go"}}, nil, stream)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	close(stream)

	if text != "hello world" {
		t.Errorf("text = %q, want %q", text, "hello world")
	}

	var streamed []string
	for tok := range stream {
		streamed = append(streamed, tok)
	}
	if strings.Join(streamed, "") != "hello world" {
		t.Errorf("streamed tokens concatenated = %q, want 'hello world'", strings.Join(streamed, ""))
	}

	if len(calls) != 1 {
		t.Fatalf("calls len = %d, want 1", len(calls))
	}
	c := calls[0]
	if c.ID != "toolu_1" || c.Name != "snapshot_stats" {
		t.Errorf("call ID/Name = %q/%q, want toolu_1/snapshot_stats", c.ID, c.Name)
	}
	if c.Input["id"] != "abc" {
		t.Errorf("call.Input[id] = %v, want abc", c.Input["id"])
	}
}

// TestAnthropic_Generate_NilStreamIsAllowed: passing nil for the
// stream channel must not panic — the impl should silently drop
// content deltas in that case and still return the accumulated text.
func TestAnthropic_Generate_NilStreamIsAllowed(t *testing.T) {
	events := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "%s\n", e)
		}
		flusher.Flush()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "")
	calls, text, err := p.Generate(context.Background(), "", []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if len(calls) != 0 {
		t.Errorf("calls = %+v, want empty", calls)
	}
}

// TestAnthropic_Generate_ContextCancelMidStream: cancelling the
// context while the stream is in flight should return promptly with
// ctx.Err.
func TestAnthropic_Generate_ContextCancelMidStream(t *testing.T) {
	// Server that flushes one event then sleeps until client
	// disconnects — long enough that we cancel mid-stream.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, `event: message_start
data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}

`)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "")

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, _, err := p.Generate(ctx, "sys", []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err == nil {
		t.Fatal("Generate returned nil error after context cancel")
	}
	if !errors.Is(err, context.Canceled) && !strings.Contains(err.Error(), "context") {
		t.Errorf("err = %v, want context.Canceled-derived", err)
	}
}

// TestAnthropic_Generate_PropagatesAPIError: a non-streaming error
// response from the server bubbles up as an error from Generate.
func TestAnthropic_Generate_PropagatesAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"api_error","message":"boom"}}`))
	}))
	defer server.Close()

	p := newTestProvider(t, server, "")
	_, _, err := p.Generate(context.Background(), "", []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err == nil {
		t.Fatal("Generate did not return an error for 500 response")
	}
}

// TestAnthropic_Generate_BackpressureDropsTokens: an unbuffered stream
// channel with no reader should NOT deadlock the streaming loop; the
// impl should drop deltas it can't deliver while still returning the
// full accumulated text.
func TestAnthropic_Generate_BackpressureDropsTokens(t *testing.T) {
	events := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"abc"}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"def"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "%s\n", e)
		}
		flusher.Flush()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "")

	stream := make(chan string) // unbuffered, no reader
	done := make(chan struct{})
	var (
		gotText string
		gotErr  error
	)
	go func() {
		defer close(done)
		_, gotText, gotErr = p.Generate(context.Background(), "", []Message{{Role: RoleUser, Content: "x"}}, nil, stream)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Generate blocked on unread stream channel")
	}
	if gotErr != nil {
		t.Errorf("Generate err = %v", gotErr)
	}
	if gotText != "abcdef" {
		t.Errorf("text = %q, want abcdef", gotText)
	}
}

// TestAnthropic_Generate_RejectsToolMessageWithoutResult: a Message
// with Role=RoleTool but ToolResult=nil is malformed; we surface that
// as an error rather than translating it into a confusing empty
// tool_result block.
func TestAnthropic_Generate_RejectsToolMessageWithoutResult(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	_, _, err := p.Generate(context.Background(), "", []Message{{Role: RoleTool}}, nil, nil)
	if err == nil {
		t.Fatal("Generate accepted RoleTool message without ToolResult")
	}
}

// TestAnthropic_Generate_RejectsAssistantToolUseWithoutToolUse: an
// assistant Message that claims a ToolUse but has neither ToolUse set
// nor Content set is malformed; mirror the Tool-without-Result check.
func TestAnthropic_Generate_RejectsSystemRoleInMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	defer server.Close()
	p := newTestProvider(t, server, "")

	_, _, err := p.Generate(context.Background(), "", []Message{{Role: RoleSystem, Content: "should not be here"}}, nil, nil)
	if err == nil {
		t.Fatal("Generate accepted RoleSystem in messages slice (should use sys arg instead)")
	}
}

// TestAnthropic_Generate_TextThenStop_NoTools confirms a pure-text
// response with no tool_use blocks returns an empty calls slice.
func TestAnthropic_Generate_TextThenStop_NoTools(t *testing.T) {
	events := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"final answer"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "%s\n", e)
		}
		flusher.Flush()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "")
	stream := make(chan string, 4)
	calls, text, err := p.Generate(context.Background(), "", []Message{{Role: RoleUser, Content: "x"}}, nil, stream)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	close(stream)
	if text != "final answer" {
		t.Errorf("text = %q, want 'final answer'", text)
	}
	if len(calls) != 0 {
		t.Errorf("calls = %+v, want empty", calls)
	}
}

// TestAnthropic_Generate_RequestPayloadShape: assert that the JSON the
// client actually sends to the API contains the expected system
// prompt, model, and tools — a single round-trip integration check
// that the entire pipeline (buildRequest + SDK marshal) is wired up.
func TestAnthropic_Generate_RequestPayloadShape(t *testing.T) {
	var captured map[string]any

	events := []string{
		`event: message_start`,
		`data: {"type":"message_start","message":{"id":"m","type":"message","role":"assistant","model":"x","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":0}}}`,
		``,
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		``,
		`event: content_block_delta`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`,
		``,
		`event: content_block_stop`,
		`data: {"type":"content_block_stop","index":0}`,
		``,
		`event: message_stop`,
		`data: {"type":"message_stop"}`,
		``,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Record body
		_ = json.NewDecoder(r.Body).Decode(&captured)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		for _, e := range events {
			fmt.Fprintf(w, "%s\n", e)
		}
		flusher.Flush()
	}))
	defer server.Close()

	p := newTestProvider(t, server, "claude-sonnet-4-6")
	tools := []Tool{
		{
			Name:        "list_snapshots",
			Description: "List snapshots.",
			Schema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
	if _, _, err := p.Generate(context.Background(), "sys-prompt", []Message{{Role: RoleUser, Content: "x"}}, tools, nil); err != nil {
		t.Fatalf("Generate: %v", err)
	}

	if captured["model"] != "claude-sonnet-4-6" {
		t.Errorf("model = %v, want claude-sonnet-4-6", captured["model"])
	}
	if captured["stream"] != true {
		t.Errorf("stream = %v, want true", captured["stream"])
	}
	sys, ok := captured["system"].([]any)
	if !ok || len(sys) != 1 {
		t.Fatalf("system field = %+v, want one-block array", captured["system"])
	}
	caps, _ := captured["tools"].([]any)
	if len(caps) != 1 {
		t.Errorf("tools = %+v, want 1", caps)
	}
}

// suppress unused import warnings if a refactor drops a call site.
var _ = os.Getenv
