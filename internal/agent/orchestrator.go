// Package agent stitches the heuristic registry, LLM provider, and
// read-only tool runner into a single Scan operation.
//
// The flow:
//  1. Run every registered heuristic concurrently → []Finding.
//  2. If zero findings, short-circuit with an empty Recommendation
//     slice (no LLM call needed; nothing to triage).
//  3. Otherwise build the system prompt, an initial user message
//     summarizing the findings, and call Provider.Generate in a loop:
//     - if the model returns tool calls, dispatch each via the Runner,
//     thread the results back as message-history Tool messages, and
//     continue.
//     - if the model returns no tool calls, parse its text as a JSON
//     array of Recommendations and return.
//  4. Cap the loop at Config.MaxToolCalls — the safety rail described
//     in docs/plans/2026-05-02-sentra-design.md → "Safety rails".
//
// The orchestrator never inspects file contents and never executes a
// recommendation — that's `sentra agent scan --apply`'s job (Phase
// 11.3). Apply is gated behind interactive confirms so the agent loop
// stays a pure "advice" surface.
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/agent/tools"
	"github.com/markgustetic/sentra/internal/repo"
)

// ErrBudgetExhausted is returned by Scan when the model exceeded
// Config.MaxToolCalls without ever emitting a final response. Callers
// receive whatever recommendations the loop accumulated along the way
// (currently always empty, since recommendations only land via the
// final-text path — but the contract leaves room for future "partial
// emit" semantics without a breaking signature change).
var ErrBudgetExhausted = errors.New("agent: tool-call budget exhausted")

// ErrInvalidResponse is returned by Scan when the model's final text
// isn't a JSON array of Recommendation. The orchestrator does not
// attempt to repair the output — surfacing the error lets the caller
// decide whether to retry, fall back, or surface the problem to the
// user.
var ErrInvalidResponse = errors.New("agent: model emitted invalid response")

// Recommendation is the structured advice the LLM returns for a finding-
// like situation. Phase 11.3's CLI renders these as a styled table;
// Phase 12's TUI streams them in as the loop progresses.
//
// Action is one of: "prune_snapshot", "add_to_ignore", "flag_secret",
// "none". The CLI's --apply path dispatches each action through a
// small handler map; "none" is a no-op, used by the model to flag
// findings the user should know about but where there's no automatic
// remediation.
type Recommendation struct {
	ID        string `json:"id"`
	Action    string `json:"action"`
	Target    string `json:"target"`
	Severity  string `json:"severity"`
	Rationale string `json:"rationale"`
}

// Config tunes the orchestrator's behavior. The zero value is unusable —
// MaxToolCalls of 0 would budget out before the first call. Tests build
// it inline; production wires it from sentra.yaml via the CLI.
type Config struct {
	// MaxFindingsToLLM caps how many findings are fed into the initial
	// user message. Larger N → more model context cost; defaults to 50
	// per the design doc when zero.
	MaxFindingsToLLM int

	// MaxToolCalls is the per-Scan tool-call budget. Once the loop has
	// dispatched this many tool calls, the next round must produce a
	// final response or Scan fails with ErrBudgetExhausted. Defaults
	// to 10 when zero.
	MaxToolCalls int

	// Model is the LLM model identifier passed through to the provider.
	// The provider is free to ignore this if the value is empty.
	Model string
}

// Defaults fills in sensible default values for any zero-valued Config
// fields. Pulled out so tests can exercise the defaulting behavior
// independent of Scan; CLI wiring also calls it after koanf merge.
func (c Config) Defaults() Config {
	if c.MaxFindingsToLLM == 0 {
		c.MaxFindingsToLLM = 50
	}
	if c.MaxToolCalls == 0 {
		c.MaxToolCalls = 10
	}
	return c
}

// Agent is the orchestrator. Repo and Heuristics are required; Provider
// is required for any non-trivial Scan (no-finding short-circuits skip
// it). Config governs the loop's safety rails.
type Agent struct {
	Repo       *repo.Repo
	Heuristics *heuristics.Registry
	Provider   llm.Provider
	Config     Config

	// Actions is the registered action vocabulary the LLM is told
	// it can emit. The system prompt's "Action is one of: ..." list
	// is generated from this registry, so the model's vocabulary
	// matches the dispatcher's vocabulary by construction. Nil
	// falls back to action.NewDefaultRegistry() — production wires
	// the same registry the CLI's dispatcher uses.
	Actions *action.Registry
}

// actionsOrDefault returns the registry the orchestrator should use
// to build the prompt. Nil falls back to the default registry so
// tests using Agent{} keep working without explicit setup.
func (a *Agent) actionsOrDefault() *action.Registry {
	if a.Actions != nil {
		return a.Actions
	}
	return action.NewDefaultRegistry()
}

// systemPrompt is the agent's role prompt. It describes Sentra, the
// agent's job, and — crucially — the safety rails: never request file
// contents, always emit recommendations as a JSON array.
//
// We rebuild this on every Scan rather than storing it on the Agent
// struct because the available tools depend on the runner, and the
// action vocabulary depends on the registry — both injectable. The
// two %s placeholders are filled with: (1) the tool-list fragment
// produced by formatToolsForPrompt, (2) the action-list fragment
// produced by action.Registry.PromptFragment. Generating from the
// live registry guarantees the LLM is told about exactly the verbs
// the dispatcher knows how to handle.
const systemPromptTemplate = `You are the Sentra repository auditor. Sentra is an encrypted, ` +
	`versioned S3 backup tool. Your job is to triage local heuristic findings ` +
	`(secrets, large files, stale paths, retention drift, ...) and emit a JSON ` +
	`array of recommendations the operator can review.

Tools available (read-only metadata; you must NEVER request file contents):
%s

Hard rules:
- You never see file contents. Don't ask for them; the tools won't return them.
- Use the tools to investigate findings before recommending action. Be parsimonious — most findings need 0-1 tool calls.
- Final response MUST be a single JSON array of Recommendation objects:
  [{"id":"...","action":"...","target":"...","severity":"...","rationale":"..."}]
  Action must be one of:
%s
  Severity is one of: "info", "warn", "critical".
- If there is nothing to do, respond with "[]".
- Do NOT include prose outside the JSON array. The array IS your response.`

// Scan runs the heuristics, then drives the LLM loop until the model
// emits a final JSON array of recommendations or the tool-call budget
// is exhausted. The stream channel receives the model's text as it
// arrives; pass nil to disable. Scan owns no goroutines that outlive
// the call — when Scan returns, the stream channel won't see any more
// writes.
//
// On no findings, Scan short-circuits with an empty result and writes
// a synthetic "no findings" message to stream so the TUI's tail
// viewport has something to display.
func (a *Agent) Scan(ctx context.Context, root string, stream chan<- string) ([]Recommendation, error) {
	cfg := a.Config.Defaults()

	// Phase 1: assemble the heuristic Input.
	//
	// Snapshots and LiveBlobs are populated here so OrphanBlobs (and any
	// other heuristic that needs the live set) can run without each
	// caller re-deriving the same data. Critically, OrphanBlobs treats a
	// nil LiveBlobs as "empty live set" and would flag every chunk in
	// data/ as orphaned — thousands of false positives on a real repo.
	// Computing it once here keeps the orchestrator the single source
	// of truth for that side of the input contract.
	//
	// Walked entries are intentionally left empty for v1: the
	// orchestrator doesn't yet have a working-directory context, and
	// the heuristics that need a Walked tree (cache_dirs, secrets,
	// large_files, stale_paths, dup_paths) gracefully no-op on empty
	// input. Wiring in a walk is a Phase 12 concern.
	snaps, err := a.Repo.ListSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: list snapshots: %w", err)
	}
	liveBlobs, err := computeLiveBlobs(ctx, a.Repo, snaps)
	if err != nil {
		return nil, fmt.Errorf("agent: compute live blobs: %w", err)
	}

	in := heuristics.Input{
		Repo:      a.Repo,
		Snapshots: snaps,
		LiveBlobs: liveBlobs,
	}
	findings, err := a.Heuristics.Run(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("agent: heuristics: %w", err)
	}

	if len(findings) == 0 {
		writeStream(stream, "no findings — all clear\n")
		return []Recommendation{}, nil
	}

	// Cap the findings going to the LLM so prompts don't balloon.
	// Truncating means the LLM only triages the top-N; that's fine for
	// v1, the user can re-run after addressing the top issues. A future
	// pass could add prioritization (severity-then-newest) but for now
	// we accept the registry's deterministic order.
	capped := findings
	if len(capped) > cfg.MaxFindingsToLLM {
		capped = capped[:cfg.MaxFindingsToLLM]
	}

	// Type the local as tools.Runner (interface) rather than the
	// concrete *tools.RepoRunner so the orchestrator's actual contract
	// is locked in code: it needs All() + Run() + Schema(). A future
	// fake runner for testing tool-error paths can drop in here
	// without changing this call site.
	var runner tools.Runner = &tools.RepoRunner{
		Repo:       a.Repo,
		Heuristics: a.Heuristics,
		Findings:   findings, // pass the FULL set so inspect_finding can look up tail entries
	}

	llmTools := make([]llm.Tool, 0, len(runner.All()))
	for _, t := range runner.All() {
		llmTools = append(llmTools, t.AsLLMTool())
	}

	sys := fmt.Sprintf(systemPromptTemplate,
		formatToolsForPrompt(runner.All()),
		a.actionsOrDefault().PromptFragment(),
	)

	initialMsg, err := buildInitialMessage(capped)
	if err != nil {
		return nil, fmt.Errorf("agent: build initial message: %w", err)
	}
	msgs := []llm.Message{{Role: llm.RoleUser, Content: initialMsg}}

	// Phase 2: drive the LLM loop. Each iteration is one Provider.Generate
	// round trip. Tool calls extend the message history; a final text
	// response terminates.
	toolCallsUsed := 0
	for {
		calls, text, gerr := a.Provider.Generate(ctx, sys, msgs, llmTools, stream)
		if gerr != nil {
			return nil, fmt.Errorf("agent: generate: %w", gerr)
		}

		// Final response: no tool calls. Parse the text as a JSON array
		// of Recommendation. If parsing fails, surface ErrInvalidResponse
		// rather than silently returning an empty list.
		if len(calls) == 0 {
			recs, perr := parseRecommendations(text)
			if perr != nil {
				return nil, fmt.Errorf("%w: %v (got %q)", ErrInvalidResponse, perr, truncate(text, 120))
			}
			return recs, nil
		}

		// Append the assistant's tool-use turn to the message history.
		// The Anthropic SDK requires the assistant's tool_use block to
		// precede the corresponding tool_result, so we serialize each
		// requested call as its own assistant Message with ToolUse set.
		// Simpler models (and our fake) tolerate the same shape.
		for _, call := range calls {
			msgs = append(msgs, llm.Message{
				Role: llm.RoleAssistant,
				ToolUse: &llm.ToolUse{
					ID:    call.ID,
					Name:  call.Name,
					Input: call.Input,
				},
			})
		}

		// Dispatch each call. Tool errors are NOT fatal — we surface
		// them as ToolResult.Error so the model can react. A truly
		// catastrophic failure in the runner would still bubble up via
		// a non-tool error path, but that's not what a missing-finding
		// or malformed-input lookup should do.
		for _, call := range calls {
			if toolCallsUsed >= cfg.MaxToolCalls {
				return []Recommendation{}, ErrBudgetExhausted
			}
			toolCallsUsed++

			out, terr := runner.Run(ctx, call.Name, call.Input)
			result := llm.ToolResult{ID: call.ID, Content: out}
			if terr != nil {
				result.Error = terr.Error()
			}
			msgs = append(msgs, llm.Message{
				Role:       llm.RoleTool,
				ToolResult: &result,
			})
		}
	}
}

// buildInitialMessage formats the heuristic findings as a JSON array
// for the model's initial user-turn input. JSON keeps the parse cost
// low for the model and lines up with the recommendation output shape.
func buildInitialMessage(findings []heuristics.Finding) (string, error) {
	out, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Heuristic findings (%d total):\n\n%s\n\nReview these and emit recommendations as a JSON array.",
		len(findings), string(out)), nil
}

// formatToolsForPrompt renders the toolset into a human-readable list
// for the system prompt. Each line is "- name: description" so the LLM
// has both the name (for tool-use) and a one-liner of intent.
func formatToolsForPrompt(ts []tools.Tool) string {
	var sb strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&sb, "- %s: %s\n", t.Name, t.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parseRecommendations decodes the model's final text as a list of
// Recommendation. Real LLM output rarely matches the prompt exactly:
// models prepend prose, wrap arrays in code fences, return a single
// object instead of an array, or trail off with "Hope this helps!" —
// so the parser tries multiple shapes in order and accepts the first
// one that yields a valid Recommendation slice.
//
// Precedence (each step falls through to the next on failure):
//  1. Direct json.Unmarshal as []Recommendation.
//  2. Strip a single ``` … ``` (or ```json … ```) code fence and retry
//     direct array unmarshal.
//  3. Locate the substring from the first '[' to the last ']' and
//     unmarshal that as []Recommendation.
//  4. Try parsing the whole (or the fence-stripped) text as a single
//     Recommendation object and wrap it in a one-element slice.
//  5. Locate the substring from the first '{' to the last '}' and
//     parse that as a single Recommendation.
//
// If everything fails, returns an error wrapping a snippet of the
// actual response (first 200 chars) so logs and the CLI surface point
// the operator at what came back. Empty input is a clear error,
// distinct from the empty-array "nothing to recommend" case which
// step (1) handles cleanly.
func parseRecommendations(text string) ([]Recommendation, error) {
	original := text
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, fmt.Errorf("empty response (got %q)", truncate(original, 200))
	}

	// Step 1: direct array.
	if recs, ok := tryUnmarshalArray(t); ok {
		return recs, nil
	}

	// Step 2: strip code fences (with or without language tag) and retry.
	stripped := stripFences(t)
	if stripped != t {
		if recs, ok := tryUnmarshalArray(stripped); ok {
			return recs, nil
		}
	}

	// Step 3: scan for the first '[' to the last ']' (handles prose
	// prefixes/suffixes around an inline array).
	if sub, ok := bracketSubstring(stripped, '[', ']'); ok {
		if recs, ok := tryUnmarshalArray(sub); ok {
			return recs, nil
		}
	}

	// Step 4: try a single Recommendation object — both on the
	// fence-stripped text and on a '{'-'}' substring scan.
	if rec, ok := tryUnmarshalObject(stripped); ok {
		return []Recommendation{rec}, nil
	}
	if sub, ok := bracketSubstring(stripped, '{', '}'); ok {
		if rec, ok := tryUnmarshalObject(sub); ok {
			return []Recommendation{rec}, nil
		}
	}

	return nil, fmt.Errorf("could not parse JSON recommendations from response (got %q)",
		truncate(original, 200))
}

// tryUnmarshalArray attempts to parse t as a JSON []Recommendation.
// Returns (recs, true) on success; (nil, false) on any failure. The
// boolean ok pattern avoids leaking parse errors up the chain — at
// each step we just want to know "did this shape work?".
func tryUnmarshalArray(t string) ([]Recommendation, bool) {
	var recs []Recommendation
	if err := json.Unmarshal([]byte(t), &recs); err != nil {
		return nil, false
	}
	return recs, true
}

// tryUnmarshalObject attempts to parse t as a single Recommendation.
// Returns (rec, true) on success; (Recommendation{}, false) otherwise.
func tryUnmarshalObject(t string) (Recommendation, bool) {
	var rec Recommendation
	if err := json.Unmarshal([]byte(t), &rec); err != nil {
		return Recommendation{}, false
	}
	return rec, true
}

// stripFences removes a single Markdown code-fence wrapper from t.
// Recognizes "```\n…\n```" and "```json\n…\n```" (and other language
// tags). If no leading fence is found, returns t unchanged so callers
// can detect "no fence stripping happened" by identity comparison.
func stripFences(t string) string {
	t = strings.TrimSpace(t)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Drop everything up to and including the first newline (the
	// opening-fence line, optionally with a language tag).
	if nl := strings.IndexByte(t, '\n'); nl > 0 {
		t = t[nl+1:]
	} else {
		// Single-line "```json[…]```" pathology: just trim the
		// leading backticks.
		t = strings.TrimPrefix(t, "```")
	}
	// Drop the closing fence and anything after it.
	if end := strings.LastIndex(t, "```"); end >= 0 {
		t = t[:end]
	}
	return strings.TrimSpace(t)
}

// bracketSubstring returns the substring from the first occurrence
// of open to the last occurrence of close (both inclusive). Returns
// (s, true) when both delimiters were found in valid order, or
// ("", false) otherwise. Enables "salvage the JSON out of prose
// noise" without parsing the prose itself.
func bracketSubstring(t string, open, close byte) (string, bool) {
	start := strings.IndexByte(t, open)
	if start < 0 {
		return "", false
	}
	end := strings.LastIndexByte(t, close)
	if end < 0 || end <= start {
		return "", false
	}
	return t[start : end+1], true
}

// writeStream sends s to stream non-blocking; nil and full channels
// are silently no-op'd. We intentionally drop rather than block so a
// slow consumer (e.g. a test that hasn't drained yet) doesn't stall
// the agent loop. The contract matches the Provider.Generate stream
// invariant.
func writeStream(stream chan<- string, s string) {
	if stream == nil {
		return
	}
	select {
	case stream <- s:
	default:
	}
}

// truncate clips s to at most n bytes (with an ellipsis), used for
// error context where we don't want to dump a multi-KB model response.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// computeLiveBlobs loads each snapshot's manifest and unions every
// chunk hash into a chunk-key set the heuristics consume. Keys are the
// "data/<aa>/<hex>" form returned by blobstore.List, matching the
// shape OrphanBlobs expects so membership tests are direct map
// lookups (no per-key translation step).
//
// Uses repo.ChunkKey so the on-disk format has a single source of
// truth: any future change to chunk sharding propagates here
// automatically rather than silently desynchronizing.
func computeLiveBlobs(ctx context.Context, r *repo.Repo, snaps []repo.SnapshotInfo) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	for _, s := range snaps {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m, err := r.LoadSnapshot(ctx, s.ID)
		if err != nil {
			return nil, fmt.Errorf("load snapshot %s: %w", s.ID, err)
		}
		for _, fe := range m.Tree {
			for _, hexHash := range fe.Chunks {
				out[repo.ChunkKey(hexHash)] = struct{}{}
			}
		}
	}
	return out, nil
}
