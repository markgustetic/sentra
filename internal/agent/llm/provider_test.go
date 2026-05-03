package llm

import (
	"context"
	"testing"
)

// stubProvider is a compile-time witness that the Provider interface is
// implementable. It also gives the tests a no-op Generate to call.
type stubProvider struct {
	gotSys   string
	gotMsgs  []Message
	gotTools []Tool
	calls    []ToolCall
	text     string
	err      error
}

func (s *stubProvider) Generate(_ context.Context, sys string, msgs []Message, tools []Tool, _ chan<- string) ([]ToolCall, string, error) {
	s.gotSys = sys
	s.gotMsgs = msgs
	s.gotTools = tools
	return s.calls, s.text, s.err
}

// TestProvider_InterfaceShape pins the interface signature so refactors
// have to be deliberate. If the Provider contract changes, this fails
// at compile time before any other test gets a chance.
func TestProvider_InterfaceShape(t *testing.T) {
	var _ Provider = (*stubProvider)(nil)
}

// TestProvider_GenerateBasicCall exercises a stub through the interface
// to confirm the message/tool/role types flow as designed: a single
// system prompt, a user message, and a tool advertised. We don't care
// about the LLM behavior here — only that the types compose.
func TestProvider_GenerateBasicCall(t *testing.T) {
	stub := &stubProvider{
		text: "ok",
		calls: []ToolCall{
			{ID: "call_1", Name: "list_snapshots", Input: map[string]any{"limit": float64(10)}},
		},
	}
	var p Provider = stub

	msgs := []Message{
		{Role: RoleUser, Content: "what's the snapshot count?"},
	}
	tools := []Tool{
		{
			Name:        "list_snapshots",
			Description: "List snapshots in the repo.",
			Schema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"limit": map[string]any{"type": "integer"},
				},
			},
		},
	}

	calls, text, err := p.Generate(context.Background(), "you are a helpful agent", msgs, tools, nil)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if text != "ok" {
		t.Errorf("text = %q, want %q", text, "ok")
	}
	if len(calls) != 1 || calls[0].Name != "list_snapshots" {
		t.Errorf("calls = %+v, want one list_snapshots call", calls)
	}
	if stub.gotSys != "you are a helpful agent" {
		t.Errorf("system prompt not threaded: got %q", stub.gotSys)
	}
	if len(stub.gotMsgs) != 1 || stub.gotMsgs[0].Role != RoleUser {
		t.Errorf("messages not threaded: got %+v", stub.gotMsgs)
	}
	if len(stub.gotTools) != 1 || stub.gotTools[0].Name != "list_snapshots" {
		t.Errorf("tools not threaded: got %+v", stub.gotTools)
	}
}

// TestProvider_RoleConstants pins the wire-level string values. Some
// downstream callers (and the Anthropic translation in 10.3) depend on
// these strings, so flipping them silently would break the SDK mapping.
func TestProvider_RoleConstants(t *testing.T) {
	cases := []struct {
		role Role
		want string
	}{
		{RoleSystem, "system"},
		{RoleUser, "user"},
		{RoleAssistant, "assistant"},
		{RoleTool, "tool"},
	}
	for _, c := range cases {
		if string(c.role) != c.want {
			t.Errorf("Role(%v) = %q, want %q", c.role, string(c.role), c.want)
		}
	}
}

// TestMessage_ToolUseAndResult sanity-checks the assistant- and
// tool-side payload shapes. The orchestrator threads these through the
// conversation and we don't want a refactor to silently rename the
// fields under it.
func TestMessage_ToolUseAndResult(t *testing.T) {
	use := &ToolUse{ID: "u1", Name: "snapshot_stats", Input: map[string]any{"id": "abc"}}
	res := &ToolResult{ID: "u1", Content: `{"count":3}`}

	msgAssistant := Message{Role: RoleAssistant, ToolUse: use}
	msgTool := Message{Role: RoleTool, ToolResult: res}

	if msgAssistant.ToolUse == nil || msgAssistant.ToolUse.ID != "u1" {
		t.Errorf("assistant ToolUse not preserved: %+v", msgAssistant)
	}
	if msgTool.ToolResult == nil || msgTool.ToolResult.ID != "u1" {
		t.Errorf("tool ToolResult not preserved: %+v", msgTool)
	}
	if msgTool.ToolResult.Error != "" {
		t.Errorf("ToolResult.Error should default empty, got %q", msgTool.ToolResult.Error)
	}
}
