package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// newTestRunner builds a RepoRunner backed by a fresh memory store and
// the supplied number of snapshots. Each snapshot has a single uniquely
// named file so dedup doesn't collapse them. Returns the runner plus
// the snapshot IDs in creation order (oldest-first).
//
// We construct a real *repo.Repo here rather than a mock because the
// tools package is a thin JSON wrapper around repo methods — the
// integration value of the test comes from exercising the real repo
// path, not from stubbing it.
func newTestRunner(t *testing.T, n int, findings []heuristics.Finding) (*RepoRunner, []string) {
	t.Helper()
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := repo.Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		root := t.TempDir()
		// Each snapshot gets two files with distinct extensions so the
		// histogram tests have something interesting to assert on.
		mustWrite(t, filepath.Join(root, "file.go"), strings.Repeat("a", 50+i))
		mustWrite(t, filepath.Join(root, "notes.md"), strings.Repeat("b", 30+i))
		s, err := r.CreateSnapshot(ctx, root, repo.SnapshotOptions{Tag: "snap" + string(rune('a'+i))})
		if err != nil {
			t.Fatalf("create snapshot %d: %v", i, err)
		}
		ids = append(ids, s.ID)
	}

	return &RepoRunner{Repo: r, Findings: findings}, ids
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestRunner_AllReturnsAllRegisteredTools verifies the static set of
// tools advertised to the LLM matches the design's enumeration.
func TestRunner_AllReturnsAllRegisteredTools(t *testing.T) {
	r := &RepoRunner{}
	got := r.All()
	want := map[string]bool{
		"list_snapshots":  true,
		"snapshot_stats":  true,
		"diff_snapshots":  true,
		"inspect_finding": true,
	}
	if len(got) != len(want) {
		t.Fatalf("All(): got %d tools, want %d", len(got), len(want))
	}
	for _, tool := range got {
		if !want[tool.Name] {
			t.Errorf("unexpected tool %q in All()", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %q missing description", tool.Name)
		}
		if tool.Schema == nil {
			t.Errorf("tool %q missing schema", tool.Name)
		}
	}
}

// TestRunner_SchemaLookup confirms Schema(name) round-trips for every
// registered tool and returns false for unknown names.
func TestRunner_SchemaLookup(t *testing.T) {
	r := &RepoRunner{}
	for _, name := range []string{"list_snapshots", "snapshot_stats", "diff_snapshots", "inspect_finding"} {
		got, ok := r.Schema(name)
		if !ok {
			t.Errorf("Schema(%q) returned ok=false", name)
		}
		if got.Name != name {
			t.Errorf("Schema(%q).Name = %q", name, got.Name)
		}
	}
	if _, ok := r.Schema("not_a_tool"); ok {
		t.Errorf("Schema(unknown) returned ok=true")
	}
}

func TestTool_AsLLMTool(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id": map[string]any{"type": "string"},
		},
	}
	tool := Tool{
		Name:        "inspect_finding",
		Description: "Inspect a finding",
		Schema:      schema,
	}
	got := tool.AsLLMTool()
	if got.Name != tool.Name {
		t.Fatalf("Name = %q, want %q", got.Name, tool.Name)
	}
	if got.Description != tool.Description {
		t.Fatalf("Description = %q, want %q", got.Description, tool.Description)
	}
	if got.Schema["type"] != "object" {
		t.Fatalf("Schema = %+v, want original JSON schema", got.Schema)
	}
}

// TestRunner_RunUnknownToolFails ensures the dispatcher rejects names
// that aren't in the registered set rather than silently no-op'ing.
func TestRunner_RunUnknownToolFails(t *testing.T) {
	r, _ := newTestRunner(t, 0, nil)
	_, err := r.Run(context.Background(), "make_coffee", map[string]any{})
	if err == nil {
		t.Fatal("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("error should mention 'unknown tool', got %v", err)
	}
}

// --- list_snapshots --------------------------------------------------

func TestRunner_ListSnapshots_HappyPath(t *testing.T) {
	r, ids := newTestRunner(t, 3, nil)
	got, err := r.Run(context.Background(), "list_snapshots", map[string]any{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, got)
	}
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3; output: %s", len(rows), got)
	}
	// Each row carries the repo.SnapshotInfo fields we promised in the
	// schema. Verify by reading by name rather than positional index.
	for _, row := range rows {
		if _, ok := row["id"]; !ok {
			t.Errorf("row missing id: %v", row)
		}
		if _, ok := row["created_at"]; !ok {
			t.Errorf("row missing created_at: %v", row)
		}
		if _, ok := row["files"]; !ok {
			t.Errorf("row missing files: %v", row)
		}
		if _, ok := row["bytes"]; !ok {
			t.Errorf("row missing bytes: %v", row)
		}
	}
	// The first ID we created should appear somewhere in the output.
	if !strings.Contains(got, ids[0]) {
		t.Errorf("output missing snapshot %q: %s", ids[0], got)
	}
}

func TestRunner_ListSnapshots_LimitClips(t *testing.T) {
	r, _ := newTestRunner(t, 5, nil)
	got, err := r.Run(context.Background(), "list_snapshots", map[string]any{"limit": float64(2)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows []any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("limit=2 should clip to 2 rows, got %d", len(rows))
	}
}

func TestRunner_ListSnapshots_ZeroLimitReturnsNoRows(t *testing.T) {
	r, _ := newTestRunner(t, 3, nil)
	got, err := r.Run(context.Background(), "list_snapshots", map[string]any{"limit": float64(0)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows []any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("limit=0 should return 0 rows, got %d", len(rows))
	}
}

func TestRunner_ListSnapshots_AcceptsIntegerLimit(t *testing.T) {
	r, _ := newTestRunner(t, 3, nil)
	got, err := r.Run(context.Background(), "list_snapshots", map[string]any{"limit": int64(1)})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows []any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("limit=1 should clip to 1 row, got %d", len(rows))
	}
}

func TestRunner_ListSnapshots_SinceFiltersRows(t *testing.T) {
	r, _ := newTestRunner(t, 2, nil)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	got, err := r.Run(context.Background(), "list_snapshots", map[string]any{"since": future})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var rows []any
	if err := json.Unmarshal([]byte(got), &rows); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("future since should filter all rows, got %d: %s", len(rows), got)
	}
}

func TestRunner_ListSnapshots_RejectsBadInputs(t *testing.T) {
	r, _ := newTestRunner(t, 1, nil)
	cases := []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"negative limit", map[string]any{"limit": -1}, "non-negative"},
		{"bad since type", map[string]any{"since": 42}, "since"},
		{"bad since value", map[string]any{"since": "yesterday"}, "parse since"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := r.Run(context.Background(), "list_snapshots", tc.input)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want to contain %q", err, tc.want)
			}
		})
	}
}

func TestRunner_ListSnapshots_RequiresRepo(t *testing.T) {
	r := &RepoRunner{}
	_, err := r.Run(context.Background(), "list_snapshots", map[string]any{})
	if err == nil {
		t.Fatal("expected error for nil repo")
	}
	if !strings.Contains(err.Error(), "repo not configured") {
		t.Fatalf("error = %v, want repo not configured", err)
	}
}

func TestRunner_ListSnapshots_BadLimitType(t *testing.T) {
	r, _ := newTestRunner(t, 1, nil)
	_, err := r.Run(context.Background(), "list_snapshots", map[string]any{"limit": "not a number"})
	if err == nil {
		t.Fatal("expected error for non-numeric limit")
	}
	if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error should mention 'limit', got %v", err)
	}
}

// --- snapshot_stats --------------------------------------------------

func TestRunner_SnapshotStats_HappyPath(t *testing.T) {
	r, ids := newTestRunner(t, 1, nil)
	got, err := r.Run(context.Background(), "snapshot_stats", map[string]any{"id": ids[0]})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var stats map[string]any
	if err := json.Unmarshal([]byte(got), &stats); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, got)
	}
	if stats["id"] != ids[0] {
		t.Errorf("id mismatch: got %v, want %s", stats["id"], ids[0])
	}
	// Two files were written per snapshot (.go + .md), so files == 2.
	if files, ok := stats["files"].(float64); !ok || files != 2 {
		t.Errorf("files: got %v, want 2", stats["files"])
	}
	hist, ok := stats["file_type_histogram"].(map[string]any)
	if !ok {
		t.Fatalf("file_type_histogram missing or wrong type: %T", stats["file_type_histogram"])
	}
	if hist[".go"] == nil || hist[".md"] == nil {
		t.Errorf("histogram should include .go and .md, got %v", hist)
	}
}

func TestRunner_SnapshotStats_MissingID(t *testing.T) {
	r, _ := newTestRunner(t, 1, nil)
	_, err := r.Run(context.Background(), "snapshot_stats", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing id")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error should mention 'id', got %v", err)
	}
}

func TestRunner_SnapshotStats_BadIDType(t *testing.T) {
	r, _ := newTestRunner(t, 1, nil)
	_, err := r.Run(context.Background(), "snapshot_stats", map[string]any{"id": 42})
	if err == nil {
		t.Fatal("expected error for non-string id")
	}
}

// --- diff_snapshots --------------------------------------------------

func TestRunner_DiffSnapshots_HappyPath(t *testing.T) {
	r, ids := newTestRunner(t, 2, nil)
	got, err := r.Run(context.Background(), "diff_snapshots", map[string]any{
		"a": ids[0],
		"b": ids[1],
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var diff map[string]any
	if err := json.Unmarshal([]byte(got), &diff); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, got)
	}
	// Each snapshot has its own root with its own files (different temp
	// dirs), so all paths in B are "added" relative to A and vice versa.
	// What we really care about for the smoke test is that the schema
	// shape is correct.
	for _, key := range []string{"added", "removed", "changed"} {
		if _, ok := diff[key]; !ok {
			t.Errorf("diff missing %q: %v", key, diff)
		}
	}
}

func TestRunner_DiffSnapshots_EmptyDiffUsesArrays(t *testing.T) {
	r, ids := newTestRunner(t, 1, nil)
	got, err := r.Run(context.Background(), "diff_snapshots", map[string]any{
		"a": ids[0],
		"b": ids[0],
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var diff map[string][]string
	if err := json.Unmarshal([]byte(got), &diff); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, got)
	}
	for _, key := range []string{"added", "removed", "changed"} {
		if diff[key] == nil {
			t.Fatalf("%s decoded as nil from %s", key, got)
		}
		if len(diff[key]) != 0 {
			t.Fatalf("%s = %v, want empty", key, diff[key])
		}
	}
}

func TestRunner_DiffSnapshots_MissingArgs(t *testing.T) {
	r, ids := newTestRunner(t, 2, nil)
	_, err := r.Run(context.Background(), "diff_snapshots", map[string]any{"a": ids[0]})
	if err == nil {
		t.Fatal("expected error for missing 'b'")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("error should mention 'b', got %v", err)
	}
}

func TestRunner_DiffSnapshots_RejectsBadArgAndNilRepo(t *testing.T) {
	r, ids := newTestRunner(t, 2, nil)
	_, err := r.Run(context.Background(), "diff_snapshots", map[string]any{"a": 42, "b": ids[1]})
	if err == nil {
		t.Fatal("expected bad arg error")
	}
	if !strings.Contains(err.Error(), `"a"`) {
		t.Fatalf("error = %v, want to mention a", err)
	}

	_, err = (&RepoRunner{}).Run(context.Background(), "diff_snapshots", map[string]any{
		"a": ids[0],
		"b": ids[1],
	})
	if err == nil {
		t.Fatal("expected nil repo error")
	}
	if !strings.Contains(err.Error(), "repo not configured") {
		t.Fatalf("error = %v, want repo not configured", err)
	}
}

// --- inspect_finding -------------------------------------------------

func TestRunner_InspectFinding_HappyPath(t *testing.T) {
	findings := []heuristics.Finding{
		{
			ID:        "abc123",
			Category:  "secrets",
			Severity:  heuristics.SeverityCritical,
			Target:    "/repo/.env",
			Heuristic: "secrets",
			Details: map[string]any{
				"pattern": "aws_access_key",
			},
		},
	}
	r, _ := newTestRunner(t, 0, findings)
	got, err := r.Run(context.Background(), "inspect_finding", map[string]any{"id": "abc123"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var f heuristics.Finding
	if err := json.Unmarshal([]byte(got), &f); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, got)
	}
	if f.ID != "abc123" {
		t.Errorf("id: got %q, want abc123", f.ID)
	}
	// Details must round-trip — that's the whole point of inspect_finding.
	if f.Details["pattern"] != "aws_access_key" {
		t.Errorf("details didn't survive round-trip: %v", f.Details)
	}
}

func TestRunner_InspectFinding_NotFound(t *testing.T) {
	r, _ := newTestRunner(t, 0, nil)
	_, err := r.Run(context.Background(), "inspect_finding", map[string]any{"id": "no_such"})
	if err == nil {
		t.Fatal("expected error for missing finding")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got %v", err)
	}
}

func TestRunner_InspectFinding_MissingID(t *testing.T) {
	r, _ := newTestRunner(t, 0, nil)
	_, err := r.Run(context.Background(), "inspect_finding", map[string]any{})
	if err == nil {
		t.Fatal("expected error for missing id")
	}
	if !strings.Contains(err.Error(), "id") {
		t.Errorf("error should mention 'id', got %v", err)
	}
}
