package cli

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/tui"
	"github.com/markgustetic/sentra/internal/web"
)

// localFixture builds LocalDeps whose UI runner captures the App and whose
// EnsureMinIO stub records its invocation instead of touching docker. It mirrors
// uiFixture: the first-run path (no .sentra-local.yaml in cwd) must never open a
// repo, so NewStore fails the test if called.
func localFixture(t *testing.T) (LocalDeps, *tui.App, *bool) {
	t.Helper()
	var captured tui.App
	ensured := false
	deps := LocalDeps{
		UI: UIDeps{
			RepoDeps: RepoDeps{
				NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
					t.Fatal("NewStore must not be called on the first-run local path")
					return nil, nil
				},
			},
			Run: func(app tui.App) error { captured = app; return nil },
		},
		EnsureMinIO: func(_ context.Context) error {
			ensured = true
			return nil
		},
	}
	return deps, &captured, &ensured
}

// webLocalFixture builds LocalDeps whose Web.Serve stub inspects the running
// server (via httptest) instead of listening, so the test can assert the MinIO
// seed reached the web wizard. The TUI runner fails the test if called.
func webLocalFixture(t *testing.T) (LocalDeps, *bool, *string) {
	t.Helper()
	served := false
	setupBody := ""
	deps := LocalDeps{
		UI: UIDeps{
			Run: func(tui.App) error { t.Fatal("--web must launch the web server, not the TUI"); return nil },
		},
		Web: WebDeps{
			RepoDeps: RepoDeps{
				NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
					return blobstore.NewMemory(), nil
				},
			},
			OpenBrowser: func(string) error { return nil },
			Serve: func(_ context.Context, srv *web.Server, ln net.Listener) error {
				served = true
				_ = ln.Close() // the stub inspects via httptest instead of listening
				ts := httptest.NewServer(srv.Handler())
				defer ts.Close()
				resp, err := http.Get(ts.URL + "/") // establishes the session cookie
				if err != nil {
					return err
				}
				cookie := resp.Header.Get("Set-Cookie")
				resp.Body.Close()
				req, _ := http.NewRequest("GET", ts.URL+"/api/setup", nil)
				req.Header.Set("Cookie", cookie)
				sresp, err := http.DefaultClient.Do(req)
				if err != nil {
					return err
				}
				b, _ := io.ReadAll(sresp.Body)
				sresp.Body.Close()
				setupBody = string(b)
				return nil
			},
		},
		EnsureMinIO: func(context.Context) error { return nil },
	}
	return deps, &served, &setupBody
}

// TestLocal_WebFlagLaunchesSeededWebWizard proves `sentra local --web` boots the
// browser UI (not the TUI) with the wizard pre-filled for MinIO: the seed
// coordinates reach /api/setup and the S3-compatible backend is inferred+locked.
func TestLocal_WebFlagLaunchesSeededWebWizard(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no .sentra-local.yaml → first-run wizard
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	deps, served, setupBody := webLocalFixture(t)
	cmd := NewLocal(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--web", "--no-open"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !*served {
		t.Fatal("--web did not launch the web server")
	}
	if !strings.Contains(*setupBody, "http://localhost:9000") || !strings.Contains(*setupBody, "sentra-test") {
		t.Errorf("web wizard not seeded with MinIO coordinates: %s", *setupBody)
	}
	// endpoint + minioadmin creds → the wizard infers and locks S3-compatible.
	if !strings.Contains(*setupBody, "s3-compatible") || !strings.Contains(*setupBody, `"endpointLocked":true`) {
		t.Errorf("web wizard should lock S3-compatible: %s", *setupBody)
	}
	// The real sentra.yaml is never touched.
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml must not be created by `sentra local --web`, stat err=%v", statErr)
	}
}

// TestLocal_EnsuresMinIOSeedsWizardAndSetsCreds is the heart of `sentra local`:
// it ensures MinIO, exports minioadmin credentials, then launches the first-run
// wizard pre-filled with the MinIO seed and pointed at .sentra-local.yaml.
func TestLocal_EnsuresMinIOSeedsWizardAndSetsCreds(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no .sentra-local.yaml → first run
	// Start from unset credentials so the command must populate them.
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")

	deps, captured, ensured := localFixture(t)
	cmd := NewLocal(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// (a) EnsureMinIO ran.
	if !*ensured {
		t.Fatal("EnsureMinIO was not invoked")
	}
	// (b) AWS credentials exported to minioadmin.
	if got := os.Getenv("AWS_ACCESS_KEY_ID"); got != "minioadmin" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want minioadmin", got)
	}
	if got := os.Getenv("AWS_SECRET_ACCESS_KEY"); got != "minioadmin" {
		t.Errorf("AWS_SECRET_ACCESS_KEY = %q, want minioadmin", got)
	}

	d := captured.Deps()
	// (c) runUI drove against the dedicated local config path.
	if base := filepath.Base(d.ConfigPath); base != ".sentra-local.yaml" {
		t.Errorf("ConfigPath base = %q, want .sentra-local.yaml (full=%q)", base, d.ConfigPath)
	}
	// (d) first-run wizard carries the MinIO seed.
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want setup", d.InitialView)
	}
	if d.Config == nil {
		t.Fatal("wizard Deps.Config is nil; MinIO seed did not reach the wizard")
	}
	if d.Config.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Errorf("seed endpoint: got %q, want http://localhost:9000", d.Config.Repo.S3.EndpointURL)
	}
	if d.Config.Repo.S3.Bucket != "sentra-test" {
		t.Errorf("seed bucket: got %q, want sentra-test", d.Config.Repo.S3.Bucket)
	}
	if d.Config.Repo.S3.Region != "us-east-1" {
		t.Errorf("seed region: got %q, want us-east-1", d.Config.Repo.S3.Region)
	}
	// The real sentra.yaml is never touched.
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml must not be created by `sentra local`, stat err=%v", statErr)
	}
}

// TestLocal_DoesNotClobberExistingAWSCreds proves the command respects a user's
// pre-set AWS credentials rather than overwriting them with minioadmin.
func TestLocal_DoesNotClobberExistingAWSCreds(t *testing.T) {
	chDir(t, t.TempDir())
	t.Setenv("AWS_ACCESS_KEY_ID", "user-key")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "user-secret")

	deps, _, _ := localFixture(t)
	cmd := NewLocal(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := os.Getenv("AWS_ACCESS_KEY_ID"); got != "user-key" {
		t.Errorf("AWS_ACCESS_KEY_ID = %q, want preserved user-key", got)
	}
	if got := os.Getenv("AWS_SECRET_ACCESS_KEY"); got != "user-secret" {
		t.Errorf("AWS_SECRET_ACCESS_KEY = %q, want preserved user-secret", got)
	}
}

// TestLocal_EnsureMinIOFailureStopsBeforeLaunch: if MinIO can't be reached or
// started, the command must return that error and never launch the UI.
func TestLocal_EnsureMinIOFailureStopsBeforeLaunch(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("could not reach or start local MinIO")
	runCalled := false
	deps := LocalDeps{
		UI: UIDeps{
			Run: func(tui.App) error { runCalled = true; return nil },
		},
		EnsureMinIO: func(_ context.Context) error { return wantErr },
	}
	cmd := NewLocal(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected MinIO error, got %v", err)
	}
	if runCalled {
		t.Fatal("UI must not launch when EnsureMinIO fails")
	}
}
