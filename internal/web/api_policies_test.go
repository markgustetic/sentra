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

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func policyServer(t *testing.T, r *repo.Repo, cfgPath string) *Server {
	t.Helper()
	return New(Deps{
		Repo:       r,
		Config:     &config.Config{},
		RepoName:   "test-repo",
		ConfigPath: cfgPath,
		Assets:     Assets,
		Unlock:     func([]byte) (*repo.Repo, error) { return r, nil },
	})
}

func tempConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "sentra.yaml")
}

// TestPolicies_ListReadsFreshFromDisk proves the list handler reloads the file
// rather than serving the stale in-memory Config: the policy is written to disk
// directly (not through the server), then must appear in the list.
func TestPolicies_ListReadsFreshFromDisk(t *testing.T) {
	cfgPath := tempConfigPath(t)
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies["nightly"] = config.PolicyConfig{
			Paths:       []string{"/tmp/data"},
			Tags:        []string{"home"},
			Schedule:    config.PolicySchedule{Cadence: "daily", At: "02:30"},
			AfterBackup: config.PolicyAfterBackup{Check: true, Prune: "off"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := policyServer(t, testRepo(t), cfgPath)
	rec := req(t, srv, "GET", "/api/policies", "", true)
	if rec.Code != 200 {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var out []policyDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out) != 1 {
		t.Fatalf("want 1 policy, got %d: %+v", len(out), out)
	}
	p := out[0]
	if p.Name != "nightly" || p.ScheduleSpec != "daily@02:30" || !p.Valid || !p.Check {
		t.Errorf("policy DTO = %+v", p)
	}
}

func TestPolicies_CreateValidateAndDuplicate(t *testing.T) {
	cfgPath := tempConfigPath(t)
	srv := policyServer(t, testRepo(t), cfgPath)

	ok := `{"name":"docs","paths":["/tmp/docs"],"scheduleSpec":"daily@01:00","prune":"off"}`
	if rec := req(t, srv, "POST", "/api/policies", ok, true); rec.Code != 200 {
		t.Fatalf("create = %d: %s", rec.Code, rec.Body)
	}
	// It lands on disk and lists.
	if rec := req(t, srv, "GET", "/api/policies", "", true); !strings.Contains(rec.Body.String(), "docs") {
		t.Errorf("created policy not listed: %s", rec.Body)
	}
	// Bad schedule: daily requires @HH:MM.
	bad := `{"name":"x","paths":["/tmp/x"],"scheduleSpec":"daily"}`
	if rec := req(t, srv, "POST", "/api/policies", bad, true); rec.Code != 400 {
		t.Errorf("bad schedule = %d, want 400", rec.Code)
	}
	// No paths: validation rejects.
	nopaths := `{"name":"y","paths":[],"scheduleSpec":"manual"}`
	if rec := req(t, srv, "POST", "/api/policies", nopaths, true); rec.Code != 400 {
		t.Errorf("no-paths = %d, want 400", rec.Code)
	}
	// Duplicate without replace: 409.
	dup := `{"name":"docs","paths":["/tmp/other"],"scheduleSpec":"manual"}`
	if rec := req(t, srv, "POST", "/api/policies", dup, true); rec.Code != 409 {
		t.Errorf("duplicate = %d, want 409", rec.Code)
	}
	// Duplicate with replace: 200 and the paths change.
	rep := `{"name":"docs","paths":["/tmp/other"],"scheduleSpec":"manual","replace":true}`
	if rec := req(t, srv, "POST", "/api/policies", rep, true); rec.Code != 200 {
		t.Errorf("replace = %d, want 200", rec.Code)
	}
	cfg, _ := config.Load(cfgPath)
	if got := cfg.Policies["docs"].Paths; len(got) != 1 || got[0] != "/tmp/other" {
		t.Errorf("replace did not overwrite paths: %+v", got)
	}
}

func TestPolicies_Delete(t *testing.T) {
	cfgPath := tempConfigPath(t)
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies["gone"] = config.PolicyConfig{Paths: []string{"/tmp/x"}, Schedule: config.PolicySchedule{Cadence: "manual"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := policyServer(t, testRepo(t), cfgPath)
	if rec := req(t, srv, "DELETE", "/api/policies/gone", "", true); rec.Code != 200 {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if rec := req(t, srv, "DELETE", "/api/policies/missing", "", true); rec.Code != 404 {
		t.Errorf("delete missing = %d, want 404", rec.Code)
	}
	cfg, _ := config.Load(cfgPath)
	if _, ok := cfg.Policies["gone"]; ok {
		t.Error("policy still present after delete")
	}
}

func TestPolicies_RunRequiresConfirmWhenPruneApply(t *testing.T) {
	cfgPath := tempConfigPath(t)
	dir := t.TempDir()
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies["p"] = config.PolicyConfig{
			Paths:       []string{dir},
			Schedule:    config.PolicySchedule{Cadence: "manual"},
			AfterBackup: config.PolicyAfterBackup{Prune: "apply"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	srv := policyServer(t, testRepo(t), cfgPath)
	// Without the "run" confirm word, an apply-pruning policy is refused.
	if rec := req(t, srv, "POST", "/api/policies/p/run", `{}`, true); rec.Code != 400 {
		t.Errorf("run without confirm = %d, want 400", rec.Code)
	}
}

// TestPolicies_RunRoundTrip runs a prune:off policy over a live server and drains
// the SSE stream, then confirms the snapshot landed with the policy tag.
func TestPolicies_RunRoundTrip(t *testing.T) {
	cfgPath := tempConfigPath(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies["job"] = config.PolicyConfig{
			Paths:       []string{dir},
			Tags:        []string{"weekly"},
			Schedule:    config.PolicySchedule{Cadence: "manual"},
			AfterBackup: config.PolicyAfterBackup{Prune: "off"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	r := testRepo(t)
	srv := policyServer(t, r, cfgPath)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	start, _ := http.NewRequest("POST", ts.URL+"/api/policies/job/run", strings.NewReader(`{}`))
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
		t.Fatalf("no opId (error=%q)", sb.Error)
	}
	ev, _ := http.NewRequest("GET", ts.URL+"/api/op/"+sb.OpID+"/events", nil)
	ev.Header.Set("Cookie", sessionCookie+"="+srv.token)
	eresp, _ := http.DefaultClient.Do(ev)
	stream, _ := io.ReadAll(eresp.Body)
	eresp.Body.Close()
	if !strings.Contains(string(stream), "event: done") {
		t.Fatalf("policy-run SSE never completed:\n%s", stream)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 || snaps[0].Tag != "policy:job weekly" {
		t.Fatalf("policy run snapshots = %+v", snaps)
	}
}
