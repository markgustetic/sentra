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
	"cmp"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sync"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/agent/tools"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/walker"
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
// like situation. The CLI renders these as a styled table; the TUI
// streams them in as the loop progresses.
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

	// Walker controls the filesystem walk used to populate
	// heuristics.Input.Walked. The zero value uses walker defaults.
	Walker walker.Options

	// InputConfig carries heuristic thresholds and policies sourced
	// from the CLI config. Zero values preserve each heuristic's
	// documented defaults.
	InputConfig heuristics.InputConfig

	// LocalOnly skips the LLM loop and converts heuristic findings
	// directly into conservative recommendations.
	LocalOnly bool

	// Categories limits findings to matching Finding.Category or
	// Finding.Heuristic values before local or LLM triage.
	Categories []string
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
	// Walked entries are populated from root so filesystem heuristics
	// (cache_dirs, secrets, large_files, stale_paths, dup_paths) audit
	// the same tree the user asked `agent scan` to inspect.
	walked, err := collectWalked(ctx, root, cfg.Walker)
	if err != nil {
		return nil, fmt.Errorf("agent: walk: %w", err)
	}

	snaps, err := a.Repo.ListSnapshots(ctx)
	if err != nil {
		return nil, fmt.Errorf("agent: list snapshots: %w", err)
	}
	liveBlobs, err := computeLiveBlobs(ctx, a.Repo, snaps)
	if err != nil {
		return nil, fmt.Errorf("agent: compute live blobs: %w", err)
	}

	in := heuristics.Input{
		Walked:    walked,
		Repo:      a.Repo,
		Snapshots: snaps,
		LiveBlobs: liveBlobs,
		Config:    cfg.InputConfig,
	}
	findings, err := a.Heuristics.Run(ctx, in)
	if err != nil {
		return nil, fmt.Errorf("agent: heuristics: %w", err)
	}
	findings = filterFindingsByCategory(findings, cfg.Categories)

	if len(findings) == 0 {
		writeStream(stream, "no findings — all clear\n")
		return []Recommendation{}, nil
	}
	if cfg.LocalOnly {
		writeStream(stream, "local-only scan: converted heuristic findings to recommendations\n")
		return localRecommendations(findings), nil
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

// collectWalked runs the shared walker over root and returns a
// deterministic entry list for heuristics. The walker invokes callbacks
// concurrently, so a mutex guards the append; sorting restores stable
// prompt/test output.
func collectWalked(ctx context.Context, root string, opts walker.Options) ([]walker.Entry, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	var (
		mu      sync.Mutex
		entries []walker.Entry
	)
	if err := walker.Walk(ctx, absRoot, opts, func(e walker.Entry) error {
		mu.Lock()
		entries = append(entries, e)
		mu.Unlock()
		return nil
	}); err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b walker.Entry) int {
		return cmp.Compare(a.RelPath, b.RelPath)
	})
	return entries, nil
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
