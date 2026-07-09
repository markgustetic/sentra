package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/tui"
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
