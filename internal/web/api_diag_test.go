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

func TestDoctor_S3CompatibleSkipsAWS(t *testing.T) {
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "my-bucket"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	srv := testServerCfg(t, testRepo(t), cfg)
	rec := req(t, srv, "GET", "/api/doctor", "", true)
	if rec.Code != 200 {
		t.Fatalf("doctor = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Backend string                           `json:"backend"`
		Checks  []struct{ Label, Status string } `json:"checks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Backend != "s3-compatible" {
		t.Errorf("backend = %q, want s3-compatible", out.Backend)
	}
	found := false
	for _, ck := range out.Checks {
		if strings.Contains(ck.Label, "S3-compatible endpoint") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the S3-compatible endpoint check: %+v", out.Checks)
	}
}

func syncServer(t *testing.T, r *repo.Repo, srcBucket string, dst blobstore.Store) *Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = srcBucket
	return New(Deps{Repo: r, Config: cfg, RepoName: "src", Assets: Assets,
		Unlock:   func([]byte) (*repo.Repo, error) { return r, nil },
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) { return dst, nil }})
}

func writeDstConfig(t *testing.T, bucket string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "dst.yaml")
	if err := os.WriteFile(p, []byte("repo:\n  s3:\n    bucket: "+bucket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestSync_RequiresConfirm(t *testing.T) {
	srv := syncServer(t, testRepo(t), "src", blobstore.NewMemory())
	rec := req(t, srv, "POST", "/api/sync", `{"dstConfigPath":"/x/sentra.yaml"}`, true)
	if rec.Code != 400 {
		t.Errorf("sync without confirm = %d, want 400", rec.Code)
	}
}

func TestSync_RefusesSameLocation(t *testing.T) {
	srv := syncServer(t, testRepo(t), "same", blobstore.NewMemory())
	dst := writeDstConfig(t, "same")
	rec := req(t, srv, "POST", "/api/sync", `{"dstConfigPath":"`+dst+`","confirm":"sync"}`, true)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "same S3 location") {
		t.Errorf("same-location sync = %d: %s", rec.Code, rec.Body)
	}
}

func TestSync_RoundTrip(t *testing.T) {
	r := testRepo(t)
	seedSnapshot(t, r, "s")
	dstMem := blobstore.NewMemory()
	srv := syncServer(t, r, "src-bucket", dstMem)
	dst := writeDstConfig(t, "dst-bucket")
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{"dstConfigPath": dst, "initDest": true, "confirm": "sync"})
	start, _ := http.NewRequest("POST", ts.URL+"/api/sync", strings.NewReader(string(body)))
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
		t.Fatalf("sync SSE never completed:\n%s", stream)
	}
}
