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

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func testRepo(t *testing.T) *repo.Repo {
	t.Helper()
	r, err := repo.Init(context.Background(), blobstore.NewMemory(), []byte("web-test-pass"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func testServer(t *testing.T, r *repo.Repo) *Server {
	t.Helper()
	return New(Deps{
		Repo:     r,
		Config:   &config.Config{},
		RepoName: "test-repo",
		Assets:   Assets,
		Unlock:   func([]byte) (*repo.Repo, error) { return r, nil },
	})
}

// seedSnapshot backs up a throwaway dir so the repo has one snapshot.
func seedSnapshot(t *testing.T, r *repo.Repo, tag string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello-"+tag), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), dir, repo.SnapshotOptions{Tag: tag}); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
}

// req builds a loopback request; withCookie attaches the server's session token.
func req(t *testing.T, srv *Server, method, path, body string, withCookie bool) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	} else {
		r = httptest.NewRequest(method, path, nil)
	}
	r.Host = "127.0.0.1:54321" // pass the loopback guard
	if withCookie {
		r.Header.Set("Cookie", sessionCookie+"="+srv.token)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	return rec
}

func TestShell_ServesHTMLAndSetsSession(t *testing.T) {
	srv := testServer(t, testRepo(t))
	rec := req(t, srv, "GET", "/", "", false)
	if rec.Code != 200 {
		t.Fatalf("GET / = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "S E N T R A") {
		t.Error("shell missing the wordmark")
	}
	if !strings.Contains(rec.Header().Get("Set-Cookie"), sessionCookie) {
		t.Error("shell must set the session cookie")
	}
}

func TestSession_ReportsUnlocked(t *testing.T) {
	srv := testServer(t, testRepo(t))
	rec := req(t, srv, "GET", "/api/session", "", false)
	var out struct {
		Locked   bool   `json:"locked"`
		RepoName string `json:"repoName"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Locked || out.RepoName != "test-repo" {
		t.Fatalf("session = %+v", out)
	}
}

func TestDashboardAndSnapshots_LiveData(t *testing.T) {
	r := testRepo(t)
	seedSnapshot(t, r, "nightly")
	srv := testServer(t, r)

	rec := req(t, srv, "GET", "/api/dashboard", "", true)
	if rec.Code != 200 {
		t.Fatalf("dashboard = %d: %s", rec.Code, rec.Body)
	}
	var dash struct {
		SnapshotCount int `json:"snapshotCount"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &dash)
	if dash.SnapshotCount != 1 {
		t.Errorf("snapshotCount = %d, want 1", dash.SnapshotCount)
	}

	rec = req(t, srv, "GET", "/api/snapshots", "", true)
	var list []snapshotDTO
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 || list[0].Tag != "nightly" {
		t.Fatalf("snapshots = %+v", list)
	}
}

func TestAuth_RequiresSessionCookie(t *testing.T) {
	srv := testServer(t, testRepo(t))
	if rec := req(t, srv, "GET", "/api/dashboard", "", false); rec.Code != 401 {
		t.Errorf("no-cookie dashboard = %d, want 401", rec.Code)
	}
}

func TestSecurity_RejectsNonLoopbackHost(t *testing.T) {
	srv := testServer(t, testRepo(t))
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.Host = "evil.example.com" // a rebinding attempt
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Errorf("non-loopback host = %d, want 403", rec.Code)
	}
}

func TestSecurity_RejectsCrossOrigin(t *testing.T) {
	srv := testServer(t, testRepo(t))
	r := httptest.NewRequest("GET", "/api/session", nil)
	r.Host = "127.0.0.1:54321"
	r.Header.Set("Origin", "http://evil.example.com")
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, r)
	if rec.Code != 403 {
		t.Errorf("cross-origin = %d, want 403", rec.Code)
	}
}

func TestUnlock_WrongPassphraseIsGeneric(t *testing.T) {
	// A locked server whose Unlock always fails as wrong passphrase.
	srv := New(Deps{
		Config:   &config.Config{},
		RepoName: "x",
		Assets:   Assets,
		Unlock:   func([]byte) (*repo.Repo, error) { return nil, repo.ErrWrongPassphrase },
	})
	rec := req(t, srv, "POST", "/api/unlock", `{"passphrase":"nope"}`, false)
	if rec.Code != 401 {
		t.Fatalf("unlock = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "wrong passphrase") {
		t.Errorf("expected generic wrong-passphrase, got %s", rec.Body)
	}
	// still locked
	if srv.currentRepo() != nil {
		t.Error("a failed unlock must leave the repo locked")
	}
}

func TestBackup_OneOpGuard(t *testing.T) {
	srv := testServer(t, testRepo(t))
	srv.opRunning = "backup" // simulate an in-flight op
	dir := t.TempDir()
	rec := req(t, srv, "POST", "/api/backup", `{"root":"`+dir+`"}`, true)
	if rec.Code != 409 {
		t.Errorf("second backup = %d, want 409", rec.Code)
	}
}

// TestBackup_RoundTripSSE runs a real backup over a live server and drains the
// SSE stream to its terminal event, then confirms the snapshot landed.
func TestBackup_RoundTripSSE(t *testing.T) {
	r := testRepo(t)
	srv := testServer(t, r)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("payload"), 0o600)

	body, _ := json.Marshal(map[string]string{"root": dir, "tag": "web"})
	start, _ := http.NewRequest("POST", ts.URL+"/api/backup", strings.NewReader(string(body)))
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
		t.Fatal("no opId returned")
	}

	ev, _ := http.NewRequest("GET", ts.URL+"/api/backup/"+sb.OpID+"/events", nil)
	ev.Header.Set("Cookie", sessionCookie+"="+srv.token)
	eresp, err := http.DefaultClient.Do(ev)
	if err != nil {
		t.Fatal(err)
	}
	stream, _ := io.ReadAll(eresp.Body) // blocks until the stream closes on done
	eresp.Body.Close()
	if !strings.Contains(string(stream), "event: done") {
		t.Fatalf("SSE stream never completed:\n%s", stream)
	}

	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("backup did not persist: %d snapshots", len(snaps))
	}
}
