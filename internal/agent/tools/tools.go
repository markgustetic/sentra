// Package tools implements the read-only investigation toolset the
// Sentra agent advertises to the LLM. The orchestrator (see
// internal/agent) stitches the LLM's tool-use blocks back into this
// package via Runner.Run; the LLM never sees a file's contents — only
// the metadata each tool returns.
//
// Safety rail (per docs/plans/2026-05-02-sentra-design.md → "Safety
// rails"): NONE of the tools in this package read a file's contents,
// not even indirectly. They operate on snapshot manifests, blob
// listings, and the precomputed Findings slice. If a future tool needs
// content, it goes in a NEW package gated behind explicit user opt-in
// — keeping this package on a strict no-content diet is the architectural
// invariant that lets Sentra promise "the model never sees your data."
//
// The tool surface intentionally tracks the design's enumeration:
//
//   - list_snapshots(limit, since) → []SnapshotSummary
//   - snapshot_stats(id)           → {id, files, bytes, new_bytes, file_type_histogram}
//   - diff_snapshots(a, b)         → {added, removed, changed}
//   - inspect_finding(id)          → heuristics.Finding (with Details)
//
// Each tool returns a JSON-encoded string so the orchestrator can
// thread results back as ToolResult messages without re-marshalling.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/repo"
)

// Runner is the bridge between the LLM's tool_use blocks and the
// orchestrator. The orchestrator looks up Schema(name) when building the
// tool advertisement for the model, calls All() to enumerate everything
// available, and dispatches Run(ctx, name, input) when the model asks
// for a tool to be invoked. Returns a JSON-encoded string so the
// caller can thread it directly into a ToolResult.Content without
// further marshalling.
//
// Production has exactly one implementation (*RepoRunner). The
// interface is the orchestrator's actual contract — typed at the
// call site so future fake runners (for testing tool-error paths,
// tool-call latency, or specific tool outputs) can drop in without
// edits to the orchestrator.
type Runner interface {
	Schema(name string) (Tool, bool)
	All() []Tool
	Run(ctx context.Context, name string, input map[string]any) (string, error)
}

// Compile-time assertion that *RepoRunner satisfies Runner. A future
// addition to the Runner interface (or a refactor that drops a method
// from RepoRunner) will fail the build here rather than at the
// orchestrator's call site.
var _ Runner = (*RepoRunner)(nil)

// Tool is the public schema record advertised to the model. The Schema
// field is a JSON-schema map (matches llm.Tool.Schema verbatim) so the
// Anthropic provider can pass it through without translation.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any
}

// AsLLMTool returns the llm.Tool view of this tool. Provided so the
// orchestrator can advertise the toolset to the Provider without
// reaching into the field set itself.
func (t Tool) AsLLMTool() llm.Tool {
	return llm.Tool{
		Name:        t.Name,
		Description: t.Description,
		Schema:      t.Schema,
	}
}

// RepoRunner is the production Runner: it dispatches tool calls
// against a real *repo.Repo, plus an in-memory Findings slice
// populated by the most recent heuristic run.
//
// Findings is captured by the orchestrator BEFORE the LLM loop starts
// and remains constant for the duration of a single Scan — that way
// `inspect_finding(id)` returns the same record the LLM saw in the
// initial summary. Mutating Findings mid-run is undefined behavior;
// the orchestrator owns the slice's lifetime.
type RepoRunner struct {
	// Repo is the open repository the snapshot tools dispatch against.
	// May be nil only for tools that don't touch repo state (currently
	// only inspect_finding); tests that exercise the snapshot tools
	// must supply a non-nil Repo.
	Repo *repo.Repo

	// Heuristics is retained for potential future re-runs from inside
	// the agent loop (e.g. re-scanning after the LLM proposes a new
	// .sentraignore entry). Today's tools don't use it.
	Heuristics *heuristics.Registry

	// Findings is the precomputed finding set the orchestrator passed
	// in when it built this runner. Looked up by ID by inspect_finding.
	Findings []heuristics.Finding
}

// staticTools is the design-defined set of tools advertised to the
// model. Defined as a package-level slice rather than rebuilt on every
// All() call so the schemas live in one grep-able place; All() returns
// a defensive copy so callers can't mutate the canonical set.
var staticTools = []Tool{
	{
		Name:        "list_snapshots",
		Description: "List snapshots in the repository, newest-first. Returns id, created_at, tag, files, and bytes for each. Useful for surveying what's stored before deciding which snapshots to investigate further.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{
					"type":        "integer",
					"description": "Maximum number of snapshots to return. Defaults to 50.",
				},
				"since": map[string]any{
					"type":        "string",
					"description": "RFC3339 timestamp; only snapshots created at or after this time are returned. Optional.",
				},
			},
		},
	},
	{
		Name:        "snapshot_stats",
		Description: "Return summary statistics for a single snapshot: file count, total bytes, new bytes uploaded, and a histogram of file extensions in the manifest tree. Does NOT read file contents.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "The snapshot ID, as returned by list_snapshots.",
				},
			},
			"required": []any{"id"},
		},
	},
	{
		Name:        "diff_snapshots",
		Description: "Compare two snapshots by ID and return the lists of paths added, removed, or changed (size or mtime). Path-only — no file contents.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"a": map[string]any{
					"type":        "string",
					"description": "ID of the older snapshot.",
				},
				"b": map[string]any{
					"type":        "string",
					"description": "ID of the newer snapshot.",
				},
			},
			"required": []any{"a", "b"},
		},
	},
	{
		Name:        "inspect_finding",
		Description: "Look up a finding by its ID and return the full record (category, severity, target, structured details). Does NOT include file contents.",
		Schema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"id": map[string]any{
					"type":        "string",
					"description": "Finding ID, as it appeared in the initial findings summary.",
				},
			},
			"required": []any{"id"},
		},
	},
}

// All returns a defensive copy of the static tool set so callers can't
// mutate the package-level slice. Order matches staticTools.
func (r *RepoRunner) All() []Tool {
	out := make([]Tool, len(staticTools))
	copy(out, staticTools)
	return out
}

// Schema looks up a tool by name. Returns the zero Tool and false if
// the name isn't registered — callers that branch on ok stay symmetric
// with the map-lookup idiom users expect.
func (r *RepoRunner) Schema(name string) (Tool, bool) {
	for _, t := range staticTools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// Run dispatches a tool call by name. Unknown names return a clear
// error rather than silently no-op'ing — the orchestrator can surface
// that error back to the model so it can correct its tool name.
//
// Each tool's input/output shape is documented at the dispatch site
// below. Output is always a JSON-encoded string so the caller can
// thread it directly into a ToolResult without re-marshalling.
func (r *RepoRunner) Run(ctx context.Context, name string, input map[string]any) (string, error) {
	switch name {
	case "list_snapshots":
		return r.runListSnapshots(ctx, input)
	case "snapshot_stats":
		return r.runSnapshotStats(ctx, input)
	case "diff_snapshots":
		return r.runDiffSnapshots(ctx, input)
	case "inspect_finding":
		return r.runInspectFinding(input)
	default:
		return "", fmt.Errorf("unknown tool %q", name)
	}
}

// listSnapshotsRow is the per-snapshot wire shape for the list_snapshots
// output. Pulled out as a named type so the JSON tags are stable across
// repo.SnapshotInfo refactors.
type listSnapshotsRow struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Tag       string    `json:"tag"`
	Files     int       `json:"files"`
	Bytes     int64     `json:"bytes"`
}

const defaultListSnapshotsLimit = 50

// runListSnapshots implements the list_snapshots tool. Inputs:
//
//   - limit: optional integer cap on rows (default 50)
//   - since: optional RFC3339 timestamp; rows older than this are dropped
//
// Returns a JSON array. Empty repository → "[]" (not "null") so the
// LLM can iterate without a nil check.
func (r *RepoRunner) runListSnapshots(ctx context.Context, input map[string]any) (string, error) {
	limit := defaultListSnapshotsLimit
	if v, ok := input["limit"]; ok {
		// JSON numbers unmarshal as float64; tolerate both the float
		// path and an explicit int (e.g. a test that built the map
		// directly). A non-numeric "limit" is the user's mistake to
		// surface rather than silently drop.
		switch n := v.(type) {
		case float64:
			limit = int(n)
		case int:
			limit = n
		case int64:
			limit = int(n)
		default:
			return "", fmt.Errorf("list_snapshots: limit must be a number, got %T", v)
		}
		if limit < 0 {
			return "", fmt.Errorf("list_snapshots: limit must be non-negative")
		}
	}

	var since *time.Time
	if v, ok := input["since"]; ok {
		s, ok := v.(string)
		if !ok {
			return "", fmt.Errorf("list_snapshots: since must be an RFC3339 string, got %T", v)
		}
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "", fmt.Errorf("list_snapshots: parse since: %w", err)
		}
		since = &t
	}

	if r.Repo == nil {
		return "", fmt.Errorf("list_snapshots: repo not configured")
	}

	snaps, err := r.Repo.ListSnapshots(ctx)
	if err != nil {
		return "", fmt.Errorf("list_snapshots: %w", err)
	}

	rows := make([]listSnapshotsRow, 0, len(snaps))
	for _, s := range snaps {
		if since != nil && s.CreatedAt.Before(*since) {
			continue
		}
		// Check the cap BEFORE appending so limit=0 yields an empty list
		// rather than one row (the append-then-break order returned one
		// row too many at the boundary).
		if len(rows) >= limit {
			break
		}
		rows = append(rows, listSnapshotsRow{
			ID:        s.ID,
			CreatedAt: s.CreatedAt,
			Tag:       s.Tag,
			Files:     s.Stats.Files,
			Bytes:     s.Stats.Bytes,
		})
	}

	return marshalJSON(rows)
}

// snapshotStatsResult is the wire shape for the snapshot_stats tool's
// output. The histogram is keyed by file extension (lowercased,
// including the dot — e.g. ".go") to match what users see in their
// editor's outline.
type snapshotStatsResult struct {
	ID                string         `json:"id"`
	Files             int            `json:"files"`
	Bytes             int64          `json:"bytes"`
	NewBytes          int64          `json:"new_bytes"`
	FileTypeHistogram map[string]int `json:"file_type_histogram"`
}

// runSnapshotStats implements the snapshot_stats tool. Inputs:
//
//   - id (required): the snapshot ID to inspect
//
// Output is a single JSON object. The file_type_histogram is built
// from filename extensions; files without an extension are bucketed
// under the empty string ("") so they're still represented.
func (r *RepoRunner) runSnapshotStats(ctx context.Context, input map[string]any) (string, error) {
	id, err := requireString(input, "id", "snapshot_stats")
	if err != nil {
		return "", err
	}

	if r.Repo == nil {
		return "", fmt.Errorf("snapshot_stats: repo not configured")
	}

	m, err := r.Repo.LoadSnapshot(ctx, id)
	if err != nil {
		return "", fmt.Errorf("snapshot_stats: %w", err)
	}

	hist := make(map[string]int)
	for _, fe := range m.Tree {
		ext := strings.ToLower(filepath.Ext(fe.Path))
		hist[ext]++
	}

	return marshalJSON(snapshotStatsResult{
		ID:                m.ID,
		Files:             m.Stats.Files,
		Bytes:             m.Stats.Bytes,
		NewBytes:          m.Stats.NewBytes,
		FileTypeHistogram: hist,
	})
}

// diffSnapshotsResult is the wire shape for the diff_snapshots tool's
// output. Slices are guaranteed non-nil (empty arrays, not null) so
// the LLM can iterate uniformly.
type diffSnapshotsResult struct {
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
	Changed []string `json:"changed"`
}

// runDiffSnapshots implements the diff_snapshots tool. Inputs:
//
//   - a (required): older snapshot ID
//   - b (required): newer snapshot ID
//
// Delegates to repo.Diff and projects the result into a JSON object
// with stable shape.
func (r *RepoRunner) runDiffSnapshots(ctx context.Context, input map[string]any) (string, error) {
	a, err := requireString(input, "a", "diff_snapshots")
	if err != nil {
		return "", err
	}
	b, err := requireString(input, "b", "diff_snapshots")
	if err != nil {
		return "", err
	}

	if r.Repo == nil {
		return "", fmt.Errorf("diff_snapshots: repo not configured")
	}

	d, err := r.Repo.Diff(ctx, a, b)
	if err != nil {
		return "", fmt.Errorf("diff_snapshots: %w", err)
	}

	// Normalize nil slices to empty arrays so the JSON output is
	// always valid for downstream iteration.
	out := diffSnapshotsResult{
		Added:   d.Added,
		Removed: d.Removed,
		Changed: d.Changed,
	}
	if out.Added == nil {
		out.Added = []string{}
	}
	if out.Removed == nil {
		out.Removed = []string{}
	}
	if out.Changed == nil {
		out.Changed = []string{}
	}
	return marshalJSON(out)
}

// runInspectFinding implements the inspect_finding tool. Inputs:
//
//   - id (required): the finding ID
//
// Returns the matching heuristics.Finding (with Details preserved).
// Unknown IDs return an error mentioning "not found" so the
// orchestrator can surface that back to the model verbatim.
func (r *RepoRunner) runInspectFinding(input map[string]any) (string, error) {
	id, err := requireString(input, "id", "inspect_finding")
	if err != nil {
		return "", err
	}

	for _, f := range r.Findings {
		if f.ID == id {
			return marshalJSON(f)
		}
	}
	return "", fmt.Errorf("inspect_finding: finding %q not found", id)
}

// requireString extracts a required string field from the input map.
// The op argument is used for error context so a missing or wrong-typed
// field surfaces against the right tool name. Empty strings count as
// missing — a caller asking inspect_finding(id="") almost certainly
// meant "I don't have an ID."
func requireString(input map[string]any, field, op string) (string, error) {
	v, ok := input[field]
	if !ok {
		return "", fmt.Errorf("%s: missing required field %q", op, field)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s: %q must be a string, got %T", op, field, v)
	}
	if s == "" {
		return "", fmt.Errorf("%s: %q must not be empty", op, field)
	}
	return s, nil
}

// marshalJSON wraps json.Marshal with the package's wrapped error
// pattern so call sites stay one-liner readable.
func marshalJSON(v any) (string, error) {
	out, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("marshal: %w", err)
	}
	return string(out), nil
}
