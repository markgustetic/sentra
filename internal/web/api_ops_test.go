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

func testServerCfg(t *testing.T, r *repo.Repo, cfg *config.Config) *Server {
	t.Helper()
	return New(Deps{Repo: r, Config: cfg, RepoName: "test", Assets: Assets,
		Unlock: func([]byte) (*repo.Repo, error) { return r, nil }})
}

func TestRestore_RequiresConfirm(t *testing.T) {
	r := testRepo(t)
	seedSnapshot(t, r, "a")
	srv := testServer(t, r)
	rec := req(t, srv, "POST", "/api/restore", `{"snapshotId":"x","dest":"/tmp/y"}`, true)
	if rec.Code != 400 {
		t.Errorf("restore without confirm = %d, want 400", rec.Code)
	}
}

func TestRestore_RoundTrip(t *testing.T) {
	r := testRepo(t)
	// back up a dir with a known file
	src := t.TempDir()
	_ = os.WriteFile(filepath.Join(src, "hello.txt"), []byte("restore-me"), 0o600)
	info, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "x"})
	if err != nil {
		t.Fatal(err)
	}
	srv := testServer(t, r)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dest := t.TempDir()
	body, _ := json.Marshal(map[string]string{"snapshotId": info.ID, "dest": dest, "confirm": "restore"})
	start, _ := http.NewRequest("POST", ts.URL+"/api/restore", strings.NewReader(string(body)))
	start.Header.Set("Content-Type", "application/json")
	start.Header.Set("Cookie", sessionCookie+"="+srv.token)
	sresp, err := http.DefaultClient.Do(start)
	if err != nil {
		t.Fatal(err)
	}
	var sb struct {
		OpID string `json:"opId"`
	}
	_ = json.NewDecoder(sresp.Body).Decode(&sb)
	sresp.Body.Close()
	if sb.OpID == "" {
		t.Fatalf("no opId")
	}
	ev, _ := http.NewRequest("GET", ts.URL+"/api/op/"+sb.OpID+"/events", nil)
	ev.Header.Set("Cookie", sessionCookie+"="+srv.token)
	eresp, _ := http.DefaultClient.Do(ev)
	stream, _ := io.ReadAll(eresp.Body)
	eresp.Body.Close()
	if !strings.Contains(string(stream), "event: done") {
		t.Fatalf("restore SSE never completed:\n%s", stream)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "hello.txt")); string(got) != "restore-me" {
		t.Errorf("restored file wrong: %q", got)
	}
}

func TestPrune_NoPolicyRefused(t *testing.T) {
	srv := testServer(t, testRepo(t)) // empty config → no retention
	if rec := req(t, srv, "GET", "/api/prune/preview", "", true); rec.Code != 400 {
		t.Errorf("prune preview with no policy = %d, want 400", rec.Code)
	}
}

func TestPrune_PreviewAndApply(t *testing.T) {
	r := testRepo(t)
	seedSnapshot(t, r, "old")
	seedSnapshot(t, r, "new")
	cfg := &config.Config{}
	cfg.Retention.KeepLast = 1
	srv := testServerCfg(t, r, cfg)

	prec := req(t, srv, "GET", "/api/prune/preview", "", true)
	var pv struct {
		DropCount int `json:"dropCount"`
	}
	_ = json.Unmarshal(prec.Body.Bytes(), &pv)
	if pv.DropCount != 1 {
		t.Fatalf("keep_last=1 with 2 snapshots → dropCount %d, want 1", pv.DropCount)
	}
	arec := req(t, srv, "POST", "/api/prune", `{"confirm":"prune"}`, true)
	if arec.Code != 200 {
		t.Fatalf("prune apply = %d: %s", arec.Code, arec.Body)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Errorf("after prune keep_last=1: %d snapshots, want 1", len(snaps))
	}
}

func TestPrune_RequiresConfirm(t *testing.T) {
	cfg := &config.Config{}
	cfg.Retention.KeepLast = 1
	srv := testServerCfg(t, testRepo(t), cfg)
	if rec := req(t, srv, "POST", "/api/prune", `{}`, true); rec.Code != 400 {
		t.Errorf("prune without confirm = %d, want 400", rec.Code)
	}
}

func TestPassword_RotateAndGuards(t *testing.T) {
	srv := testServer(t, testRepo(t))
	// mismatch
	if rec := req(t, srv, "POST", "/api/password", `{"newPassphrase":"longenough1","confirmPassphrase":"different22","confirm":"rotate"}`, true); rec.Code != 400 {
		t.Errorf("mismatch = %d, want 400", rec.Code)
	}
	// too short
	if rec := req(t, srv, "POST", "/api/password", `{"newPassphrase":"short","confirmPassphrase":"short","confirm":"rotate"}`, true); rec.Code != 400 {
		t.Errorf("short = %d, want 400", rec.Code)
	}
	// wrong confirm word
	if rec := req(t, srv, "POST", "/api/password", `{"newPassphrase":"longenough1","confirmPassphrase":"longenough1","confirm":"nope"}`, true); rec.Code != 400 {
		t.Errorf("wrong confirm word = %d, want 400", rec.Code)
	}
	// success
	rec := req(t, srv, "POST", "/api/password", `{"newPassphrase":"longenough1","confirmPassphrase":"longenough1","confirm":"rotate"}`, true)
	if rec.Code != 200 {
		t.Fatalf("rotate = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "\"rotated\":true") {
		t.Errorf("expected rotated:true, got %s", rec.Body)
	}
}
