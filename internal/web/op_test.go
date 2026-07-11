package web

import (
	"context"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestOp_RecoversPanic proves a panic inside an op's run func is turned into an
// error instead of crashing the process, and the one-op guard is still cleared.
func TestOp_RecoversPanic(t *testing.T) {
	srv := testServer(t, testRepo(t))
	opID, err := srv.startOp("boom", func(context.Context, progress.Reporter, *repo.Repo) (any, error) {
		panic("kaboom")
	})
	if err != nil {
		t.Fatalf("startOp: %v", err)
	}
	srv.mu.Lock()
	o := srv.ops[opID]
	srv.mu.Unlock()
	<-o.done // finishOp closes this even on a recovered panic

	o.mu.Lock()
	res := o.result
	o.mu.Unlock()
	if res.err == nil || !strings.Contains(res.err.Error(), "internal error") {
		t.Errorf("panic was not recovered as an error: %+v", res)
	}
	srv.mu.Lock()
	running := srv.opRunning
	srv.mu.Unlock()
	if running != "" {
		t.Errorf("opRunning not cleared after a panicking op: %q", running)
	}
}

func TestOpReporter_TotalZeroResetsDone(t *testing.T) {
	o := &op{progress: make(chan progressMsg, 8)}
	r := &opReporter{op: o}
	r.Total(100)
	r.Add(60)
	r.Total(0) // start of a new sub-operation (e.g. path 2 of a policy run)
	r.mu.Lock()
	done, total := r.done, r.total
	r.mu.Unlock()
	if done != 0 || total != 0 {
		t.Errorf("after Total(0): done=%d total=%d, want 0/0 (reporter did not reset)", done, total)
	}
}

func TestSameS3Location_NormalizesPrefix(t *testing.T) {
	mk := func(bucket, prefix, ep string) *config.Config {
		c := &config.Config{}
		c.Repo.S3.Bucket = bucket
		c.Repo.S3.Prefix = prefix
		c.Repo.S3.EndpointURL = ep
		return c
	}
	// "backup" and "backup/" key into the same namespace (path.Join) → same repo.
	if !sameS3Location(mk("b", "backup", "http://x"), mk("b", "backup/", "http://x")) {
		t.Error("a trailing-slash prefix difference must still count as the same location")
	}
	if sameS3Location(mk("b", "p1", "http://x"), mk("b", "p2", "http://x")) {
		t.Error("genuinely different prefixes must not match")
	}
}

func TestSecurity_SetsAntiFramingHeaders(t *testing.T) {
	srv := testServer(t, testRepo(t))
	rec := req(t, srv, "GET", "/api/session", "", false)
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
		t.Errorf("CSP = %q, want frame-ancestors 'none'", got)
	}
}
