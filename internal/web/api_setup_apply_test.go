package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// fakeSetupEffects backs the setup engine with an in-memory store so apply can
// be exercised end-to-end without live AWS. NewStore hands back the SAME store
// each call, so InitRepo's write and the post-setup re-open see one repository.
type fakeSetupEffects struct{ store blobstore.Store }

func (f *fakeSetupEffects) EnsureAWSCLI(context.Context, setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error) {
	return setup.AWSCLIInstallReport{}, nil
}
func (f *fakeSetupEffects) AWSLogin(context.Context, string, string) error { return nil }
func (f *fakeSetupEffects) CheckAWSSSOConfigured(context.Context, string) (bool, error) {
	return true, nil
}
func (f *fakeSetupEffects) AWSConfigureSSO(context.Context, string) error             { return nil }
func (f *fakeSetupEffects) AWSSSOLogin(context.Context, string) error                 { return nil }
func (f *fakeSetupEffects) CheckAWSSDKIdentity(context.Context, *config.Config) error { return nil }
func (f *fakeSetupEffects) PrepareAWS(context.Context, *config.Config, setup.AWSPrepareOptions) (setup.AWSPrepareReport, error) {
	return setup.AWSPrepareReport{BucketExisted: false, BucketCreated: true, PublicAccessBlocked: true, DefaultEncryptionEnabled: true}, nil
}
func (f *fakeSetupEffects) NewStore(context.Context, *config.Config) (blobstore.Store, error) {
	return f.store, nil
}
func (f *fakeSetupEffects) SavePassphrase(*config.Config, []byte) error { return nil }

func setupServerWithEffects(t *testing.T, eff setup.Effects) *Server {
	t.Helper()
	return New(Deps{
		Config:       &config.Config{},
		RepoName:     "sentra",
		ConfigPath:   tempConfigPath(t),
		Assets:       Assets,
		SetupNeeded:  true,
		SetupEffects: eff,
	})
}

// drainSetupApply POSTs the apply and drains its op SSE stream to completion.
func drainSetupApply(t *testing.T, srv *Server, body string) string {
	t.Helper()
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	start, _ := http.NewRequest("POST", ts.URL+"/api/setup/apply", strings.NewReader(body))
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

func TestSetup_ApplyS3CompatibleUnlocksServer(t *testing.T) {
	srv := setupServerWithEffects(t, &fakeSetupEffects{store: blobstore.NewMemory()})
	const pass = "correct-horse-battery"
	body := `{"backend":"s3-compatible","bucket":"sentra-web","endpointUrl":"http://localhost:9000","initRepo":true,"passphrase":"` + pass + `","savePassphrase":false}`

	stream := drainSetupApply(t, srv, body)
	if !strings.Contains(stream, "event: done") || !strings.Contains(stream, "repo-initialized") {
		t.Fatalf("apply SSE did not complete with init:\n%s", stream)
	}
	// The passphrase must never appear in the stream.
	if strings.Contains(stream, pass) {
		t.Fatal("passphrase leaked into the setup SSE stream")
	}

	// The server is now unlocked and out of setup mode.
	var sess struct {
		Locked      bool `json:"locked"`
		SetupNeeded bool `json:"setupNeeded"`
	}
	rec := req(t, srv, "GET", "/api/session", "", false)
	_ = json.Unmarshal(rec.Body.Bytes(), &sess)
	if sess.Locked || sess.SetupNeeded {
		t.Fatalf("server not unlocked after setup: %+v", sess)
	}
	// A repo-backed route now works.
	if rec := req(t, srv, "GET", "/api/dashboard", "", true); rec.Code != 200 {
		t.Errorf("dashboard after setup = %d, want 200", rec.Code)
	}
	// Setup is now refused.
	if rec := req(t, srv, "GET", "/api/setup", "", true); rec.Code != 409 {
		t.Errorf("setup after apply = %d, want 409", rec.Code)
	}
}

func TestSetup_ApplyRejectsShortPassphrase(t *testing.T) {
	srv := setupServerWithEffects(t, &fakeSetupEffects{store: blobstore.NewMemory()})
	body := `{"backend":"s3-compatible","bucket":"sentra-web","endpointUrl":"http://x:9000","initRepo":true,"passphrase":"short"}`
	if rec := req(t, srv, "POST", "/api/setup/apply", body, true); rec.Code != 400 {
		t.Errorf("short passphrase apply = %d, want 400", rec.Code)
	}
}

func TestSetup_ApplyRejectsBadBucket(t *testing.T) {
	srv := setupServerWithEffects(t, &fakeSetupEffects{store: blobstore.NewMemory()})
	body := `{"backend":"s3-compatible","bucket":"ab","initRepo":true,"passphrase":"correct-horse-battery"}`
	if rec := req(t, srv, "POST", "/api/setup/apply", body, true); rec.Code != 400 {
		t.Errorf("bad-bucket apply = %d, want 400", rec.Code)
	}
}
