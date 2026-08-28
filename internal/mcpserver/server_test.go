package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// secretBody is a file body planted in every fixture snapshot. The
// never-contents rule is pinned by asserting NO tool result ever carries
// it — metadata only, always.
const secretBody = "TOP-SECRET-FILE-BODY-a9f3"

// newFixture builds an in-memory repo holding two real snapshots and a
// connected MCP client session against the real server.
func newFixture(t *testing.T) (*mcp.ClientSession, *Server, *repo.Repo) {
	t.Helper()
	ctx := context.Background()
	store := blobstore.NewMemory()
	r, err := repo.Init(ctx, store, []byte("hunter2"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })

	dir := t.TempDir()
	writeFile := func(rel string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(secretBody+" "+rel), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeFile("docs/taxes-2025.pdf")
	writeFile("docs/notes.md")
	if _, err := r.CreateSnapshot(ctx, dir, repo.SnapshotOptions{Tag: "first"}); err != nil {
		t.Fatal(err)
	}
	writeFile("code/main.go")
	if _, err := r.CreateSnapshot(ctx, dir, repo.SnapshotOptions{Tag: "second"}); err != nil {
		t.Fatal(err)
	}

	srv := New(r, "test")
	ct, st := mcp.NewInMemoryTransports()
	go func() { _ = srv.Run(ctx, st) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	sess, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, srv, r
}

// call invokes a tool and returns the raw result plus its combined text.
func call(t *testing.T, sess *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	var text strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			text.WriteString(tc.Text)
		}
	}
	if sc := res.StructuredContent; sc != nil {
		b, _ := json.Marshal(sc)
		text.Write(b)
	}
	return res, text.String()
}

func TestServer_AdvertisesTheToolSet(t *testing.T) {
	sess, _, _ := newFixture(t)
	res, err := sess.ListTools(context.Background(), &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, tool := range res.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"list_snapshots", "snapshot_files", "find", "diff_snapshots",
		"repo_stats", "plan_backup", "confirm_backup", "plan_restore", "confirm_restore",
	} {
		if !got[want] {
			t.Errorf("tool %q not advertised; have %v", want, got)
		}
	}
}

func TestListSnapshots_NewestFirstAndTagFilter(t *testing.T) {
	sess, _, _ := newFixture(t)
	_, text := call(t, sess, "list_snapshots", nil)
	if !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Fatalf("both snapshots must appear:\n%s", text)
	}
	if strings.Index(text, "second") > strings.Index(text, "first") {
		t.Errorf("newest (second) must come first:\n%s", text)
	}
	_, text = call(t, sess, "list_snapshots", map[string]any{"tag": "first"})
	if strings.Contains(text, "second") {
		t.Errorf("tag filter leaked other snapshots:\n%s", text)
	}
}

func TestSnapshotFiles_ListsMetadataWithPrefix(t *testing.T) {
	sess, _, r := newFixture(t)
	snaps, _ := r.ListSnapshots(context.Background())
	newest := snaps[0].ID
	_, text := call(t, sess, "snapshot_files", map[string]any{"snapshot_id": newest, "prefix": "docs/"})
	if !strings.Contains(text, "taxes-2025.pdf") || !strings.Contains(text, "notes.md") {
		t.Fatalf("docs/ entries missing:\n%s", text)
	}
	if strings.Contains(text, "main.go") {
		t.Errorf("prefix filter leaked code/:\n%s", text)
	}
}

func TestFind_MatchesAcrossSnapshots(t *testing.T) {
	sess, _, _ := newFixture(t)
	_, text := call(t, sess, "find", map[string]any{"pattern": "taxes"})
	if !strings.Contains(text, "taxes-2025.pdf") {
		t.Fatalf("find missed the file:\n%s", text)
	}
	if !strings.Contains(text, "first") || !strings.Contains(text, "second") {
		t.Errorf("find should report every snapshot containing the match:\n%s", text)
	}
}

func TestDiffSnapshots_ReportsAdded(t *testing.T) {
	sess, _, r := newFixture(t)
	snaps, _ := r.ListSnapshots(context.Background())
	_, text := call(t, sess, "diff_snapshots", map[string]any{"a": snaps[1].ID, "b": snaps[0].ID})
	if !strings.Contains(text, "main.go") {
		t.Fatalf("added file missing from diff:\n%s", text)
	}
}

func TestRepoStats_ReturnsTotals(t *testing.T) {
	sess, _, _ := newFixture(t)
	_, text := call(t, sess, "repo_stats", nil)
	for _, want := range []string{"snapshots", "logical_bytes", "stored_bytes"} {
		if !strings.Contains(text, want) {
			t.Errorf("stats missing %q:\n%s", want, text)
		}
	}
}

// The mutating flow is two-phase: plan returns a single-use token, confirm
// executes exactly that plan. A backup through it must actually land.
func TestBackup_TwoPhaseConfirm(t *testing.T) {
	sess, _, r := newFixture(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "new.txt"), []byte("fresh"), 0o600); err != nil {
		t.Fatal(err)
	}

	res, text := call(t, sess, "plan_backup", map[string]any{"path": src, "tag": "via-mcp"})
	if res.IsError {
		t.Fatalf("plan_backup errored: %s", text)
	}
	token := extractToken(t, text)

	res, text = call(t, sess, "confirm_backup", map[string]any{"token": token})
	if res.IsError {
		t.Fatalf("confirm_backup errored: %s", text)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	found := false
	for _, s := range snaps {
		if s.Tag == "via-mcp" {
			found = true
		}
	}
	if !found {
		t.Fatal("confirmed backup did not create a snapshot")
	}

	// Single-use: the same token must be refused the second time.
	res, _ = call(t, sess, "confirm_backup", map[string]any{"token": token})
	if !res.IsError {
		t.Fatal("a token must be single-use")
	}
}

func TestConfirm_RefusesUnknownAndExpiredTokens(t *testing.T) {
	sess, srv, _ := newFixture(t)
	res, _ := call(t, sess, "confirm_backup", map[string]any{"token": "nope"})
	if !res.IsError {
		t.Fatal("unknown token must be refused")
	}

	src := t.TempDir()
	_, text := call(t, sess, "plan_backup", map[string]any{"path": src})
	token := extractToken(t, text)
	srv.now = func() time.Time { return time.Now().Add(tokenTTL + time.Minute) }
	res, _ = call(t, sess, "confirm_backup", map[string]any{"token": token})
	if !res.IsError {
		t.Fatal("expired token must be refused")
	}
}

func TestRestore_TwoPhaseRoundTrip(t *testing.T) {
	sess, _, r := newFixture(t)
	snaps, _ := r.ListSnapshots(context.Background())
	dest := filepath.Join(t.TempDir(), "out")

	res, text := call(t, sess, "plan_restore", map[string]any{
		"snapshot_id": snaps[0].ID, "dest": dest})
	if res.IsError {
		t.Fatalf("plan_restore errored: %s", text)
	}
	token := extractToken(t, text)
	res, text = call(t, sess, "confirm_restore", map[string]any{"token": token})
	if res.IsError {
		t.Fatalf("confirm_restore errored: %s", text)
	}
	body, err := os.ReadFile(filepath.Join(dest, "docs", "notes.md"))
	if err != nil {
		t.Fatalf("restored file missing: %v", err)
	}
	if !strings.Contains(string(body), "notes.md") {
		t.Fatalf("restored content wrong: %q", body)
	}
}

// A confirm tool must never accept the OTHER flow's token: a plan is
// bound to exactly the operation it described.
func TestConfirm_TokensAreFlowBound(t *testing.T) {
	sess, _, _ := newFixture(t)
	src := t.TempDir()
	_, text := call(t, sess, "plan_backup", map[string]any{"path": src})
	token := extractToken(t, text)
	res, _ := call(t, sess, "confirm_restore", map[string]any{"token": token})
	if !res.IsError {
		t.Fatal("a backup token must not confirm a restore")
	}
}

// The invariant behind every read tool: metadata only. No tool result may
// ever contain a file body.
func TestReadTools_NeverLeakFileContents(t *testing.T) {
	sess, _, r := newFixture(t)
	snaps, _ := r.ListSnapshots(context.Background())
	calls := []struct {
		name string
		args map[string]any
	}{
		{"list_snapshots", nil},
		{"snapshot_files", map[string]any{"snapshot_id": snaps[0].ID}},
		{"find", map[string]any{"pattern": "."}},
		{"diff_snapshots", map[string]any{"a": snaps[1].ID, "b": snaps[0].ID}},
		{"repo_stats", nil},
	}
	for _, c := range calls {
		_, text := call(t, sess, c.name, c.args)
		if strings.Contains(text, secretBody) {
			t.Errorf("%s leaked file contents", c.name)
		}
	}
}

func extractToken(t *testing.T, text string) string {
	t.Helper()
	var payload struct {
		Token string `json:"token"`
	}
	// The structured content is appended as JSON; find the token field.
	idx := strings.Index(text, `"token"`)
	if idx == -1 {
		t.Fatalf("no token in result: %s", text)
	}
	start := strings.LastIndex(text[:idx], "{")
	dec := json.NewDecoder(strings.NewReader(text[start:]))
	if err := dec.Decode(&payload); err != nil || payload.Token == "" {
		// fall back: scan a JSON object containing token
		t.Fatalf("could not parse token from: %s (err=%v)", text, err)
	}
	return payload.Token
}

var _ = fmt.Sprintf // keep fmt for future debugging edits
