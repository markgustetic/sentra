package setup

import (
	"context"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// DefaultEffects must satisfy the Effects interface and wire the moved
// Default* drivers. We only assert the wiring is complete (non-nil,
// implements Effects) — the subprocess/AWS bodies are exercised by the
// Default* unit tests in awscli_test.go and by AWS integration tests.
func TestDefaultEffectsImplementsInterface(t *testing.T) {
	eff := DefaultEffects()
	if eff == nil {
		t.Fatal("DefaultEffects returned nil")
	}
}

// CheckAWSSDKIdentity must delegate to diag.CheckSDKIdentity. With no AWS
// credentials configured in the test environment it must return a non-nil
// error rather than panicking, proving the delegation is wired.
func TestDefaultEffectsCheckIdentityDelegates(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/nonexistent-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/nonexistent-creds")
	eff := DefaultEffects()
	cfg := &config.Config{}
	cfg.Repo.S3.Region = "us-east-1"
	if err := eff.CheckAWSSDKIdentity(context.Background(), cfg); err == nil {
		t.Fatal("CheckAWSSDKIdentity: got nil error with no credentials, want non-nil")
	}
}

// DefaultAWSPrepare must reject a config with no region before touching AWS,
// preserving the guard moved from the old internal/cli/setup_awss3.go.
func TestDefaultAWSPrepareRequiresRegion(t *testing.T) {
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "example-bucket"
	if _, err := DefaultAWSPrepare(context.Background(), cfg, AWSPrepareOptions{}); err == nil {
		t.Fatal("DefaultAWSPrepare: got nil error with empty region, want non-nil")
	}
}

// NewStore must build a live blobstore.Store (no network at construction).
func TestDefaultEffectsNewStore(t *testing.T) {
	eff := DefaultEffects()
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "example-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	store, err := eff.NewStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if store == nil {
		t.Fatal("NewStore returned a nil Store")
	}
}

// The seam must expose provisioning, and the production seam must route it
// to DefaultProvisionBackupUser (a nil config is the cheapest observable
// path through that driver).
func TestDefaultEffectsProvisionBackupUserDelegates(t *testing.T) {
	eff := DefaultEffects()
	_, err := eff.ProvisionBackupUser(context.Background(), nil, BackupUserOptions{})
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("expected the default driver's nil-config error, got %v", err)
	}
}
