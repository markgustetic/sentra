package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// fakeHeuristic returns a fixed finding set so a local-only scan is deterministic
// without an LLM.
type fakeHeuristic struct {
	name     string
	findings []heuristics.Finding
}

func (f fakeHeuristic) Name() string { return f.name }
func (f fakeHeuristic) Run(context.Context, heuristics.Input) ([]heuristics.Finding, error) {
	return f.findings, nil
}

func agentServer(t *testing.T, r *repo.Repo, cfgPath string, hs ...heuristics.Heuristic) *Server {
	t.Helper()
	return New(Deps{
		Repo:       r,
		Config:     &config.Config{},
		RepoName:   "test-repo",
		ConfigPath: cfgPath,
		Assets:     Assets,
		Unlock:     func([]byte) (*repo.Repo, error) { return r, nil },
		Actions:    action.NewDefaultRegistry(),
		Heuristics: hs,
	})
}

// sseDoneData extracts the JSON payload of the terminal "done" event.
func sseDoneData(t *testing.T, stream string) []byte {
	t.Helper()
	for _, block := range strings.Split(stream, "\n\n") {
		if strings.Contains(block, "event: done") {
			for _, line := range strings.Split(block, "\n") {
				if data, ok := strings.CutPrefix(line, "data: "); ok {
					return []byte(data)
				}
			}
		}
	}
	t.Fatalf("no done event in stream:\n%s", stream)
	return nil
}

// drainScan POSTs a scan, opens the SSE stream, and returns the raw stream text.
func drainScan(t *testing.T, srv *Server, reqBody string) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	start, _ := http.NewRequest("POST", ts.URL+"/api/agent/scan", strings.NewReader(reqBody))
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("Cookie", sessionCookie+"="+srv.token)
	sresp, err := http.DefaultClient.Do(start)
	if err != nil {
		t.Fatal(err)
	}
	var sb struct {
		OpID  string `json:"opId"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(sresp.Body).Decode(&sb)
	sresp.Body.Close()
	if sb.OpID == "" {
		t.Fatalf("scan returned no opId (error=%q)", sb.Error)
	}
	ev, _ := http.NewRequest("GET", ts.URL+"/api/op/"+sb.OpID+"/events", nil)
	ev.Header.Set("Cookie", sessionCookie+"="+srv.token)
	eresp, _ := http.DefaultClient.Do(ev)
	stream, _ := io.ReadAll(eresp.Body)
	eresp.Body.Close()
	return string(stream)
}

func TestAgent_StatusReportsLLMKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "")
	srv := agentServer(t, testRepo(t), tempConfigPath(t))
	rec := req(t, srv, "GET", "/api/agent", "", true)
	if rec.Code != 200 {
		t.Fatalf("agent status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		LLMConfigured bool `json:"llmConfigured"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.LLMConfigured {
		t.Error("llmConfigured should be false with no API key in env")
	}
}

func TestAgent_ScanLocalOnlyProducesRecommendations(t *testing.T) {
	hz := fakeHeuristic{name: "fake", findings: []heuristics.Finding{
		{ID: "f1", Category: "cache_dirs", Target: "node_modules", Severity: "warn", Heuristic: "fake"},
	}}
	srv := agentServer(t, testRepo(t), tempConfigPath(t), hz)
	stream := drainScan(t, srv, `{"root":"`+t.TempDir()+`","localOnly":true}`)

	data := sseDoneData(t, stream)
	var done struct {
		Recommendations []struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			Target string `json:"target"`
		} `json:"recommendations"`
	}
	_ = json.Unmarshal(data, &done)
	if len(done.Recommendations) != 1 {
		t.Fatalf("want 1 recommendation, got %+v", done.Recommendations)
	}
	got := done.Recommendations[0]
	if got.ID != "local-f1" || got.Action != "add_to_ignore" || got.Target != "node_modules" {
		t.Errorf("recommendation = %+v", got)
	}
}

func TestAgent_ApplyRequiresConfirm(t *testing.T) {
	srv := agentServer(t, testRepo(t), tempConfigPath(t))
	if rec := req(t, srv, "POST", "/api/agent/apply", `{"ids":["x"]}`, true); rec.Code != 400 {
		t.Errorf("apply without confirm = %d, want 400", rec.Code)
	}
}

func TestAgent_ApplyRejectsUnknownID(t *testing.T) {
	srv := agentServer(t, testRepo(t), tempConfigPath(t))
	// lastRecs is empty, so any id is "unknown" — the browser can't inject a rec.
	rec := req(t, srv, "POST", "/api/agent/apply", `{"ids":["forged"],"confirm":"apply"}`, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "unknown recommendation") {
		t.Errorf("forged id = %d: %s", rec.Code, rec.Body)
	}
}

func TestAgent_ApplyAddToIgnoreWritesFile(t *testing.T) {
	cfgPath := tempConfigPath(t)
	srv := agentServer(t, testRepo(t), cfgPath)
	// Seed a server-held recommendation, as a prior scan would have.
	srv.lastRecs = map[string]agent.Recommendation{
		"local-f1": {ID: "local-f1", Action: "add_to_ignore", Target: "node_modules", Severity: "warn"},
	}
	stream := drainApply(t, srv, `{"ids":["local-f1"],"confirm":"apply"}`)
	if !strings.Contains(stream, "event: done") {
		t.Fatalf("apply SSE never completed:\n%s", stream)
	}
	// add_to_ignore writes .sentraignore next to sentra.yaml.
	ignore := filepath.Join(filepath.Dir(cfgPath), ".sentraignore")
	b, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatalf("expected .sentraignore written: %v", err)
	}
	if !strings.Contains(string(b), "node_modules") {
		t.Errorf(".sentraignore = %q", b)
	}
}

func TestAgent_ApplyWipeGuardRefusesEmptying(t *testing.T) {
	r := testRepo(t)
	seedSnapshot(t, r, "only")
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("want 1 seeded snapshot, got %d", len(snaps))
	}
	srv := agentServer(t, r, tempConfigPath(t))
	srv.lastRecs = map[string]agent.Recommendation{
		"local-p": {ID: "local-p", Action: "prune_snapshot", Target: snaps[0].ID, Severity: "warn"},
	}
	// Pruning the only snapshot would empty the repo — refused without a wipe.
	rec := req(t, srv, "POST", "/api/agent/apply", `{"ids":["local-p"],"confirm":"apply"}`, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "wipe") {
		t.Errorf("wipe-guard = %d: %s", rec.Code, rec.Body)
	}
}

// drainApply POSTs an apply and drains the resulting op SSE stream.
func drainApply(t *testing.T, srv *Server, reqBody string) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	start, _ := http.NewRequest("POST", ts.URL+"/api/agent/apply", strings.NewReader(reqBody))
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("Cookie", sessionCookie+"="+srv.token)
	sresp, err := http.DefaultClient.Do(start)
	if err != nil {
		t.Fatal(err)
	}
	var sb struct {
		OpID  string `json:"opId"`
		Error string `json:"error"`
	}
	_ = json.NewDecoder(sresp.Body).Decode(&sb)
	sresp.Body.Close()
	if sb.OpID == "" {
		t.Fatalf("apply returned no opId (error=%q)", sb.Error)
	}
	ev, _ := http.NewRequest("GET", ts.URL+"/api/op/"+sb.OpID+"/events", nil)
	ev.Header.Set("Cookie", sessionCookie+"="+srv.token)
	eresp, _ := http.DefaultClient.Do(ev)
	stream, _ := io.ReadAll(eresp.Body)
	eresp.Body.Close()
	return string(stream)
}
