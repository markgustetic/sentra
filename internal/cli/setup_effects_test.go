package cli

import (
	"context"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupEffectsSatisfiesEngineSeam(t *testing.T) {
	deps := SetupDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error { return nil },
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			return blobstore.NewMemory(), nil
		},
	}
	// setup.NewEngine accepts a setup.Effects, so building an engine from the
	// mapper's result is the compile-time proof the mapper satisfies the seam.
	_ = setup.NewEngine(setupEffects(deps))
}

// Compile-time proof the concrete cli effects type implements the engine seam.
var _ setup.Effects = (*cliSetupEffects)(nil)

// TestCLISetupEffectsResolvesNilConfirm proves plan correction C5: the engine
// passes EnsureAWSCLI(ctx, nil), and the cli decorator resolves that nil into
// a real confirm (deps.ConfirmAWSCLIInstall, falling back to the huh prompt)
// before delegating to the injected EnsureAWSCLI. Without this, the CLI's
// AWS-CLI-install path would invoke its installer with a nil confirm and panic.
func TestCLISetupEffectsResolvesNilConfirm(t *testing.T) {
	var gotConfirm setup.AWSCLIInstallConfirm
	confirmSentinel := func(AWSCLIInstallPlan) (bool, error) { return true, nil }
	deps := SetupDeps{
		ConfirmAWSCLIInstall: confirmSentinel,
		EnsureAWSCLI: func(_ context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
			gotConfirm = confirm
			return AWSCLIInstallReport{}, nil
		},
	}
	eff := setupEffects(deps)
	// The engine always passes nil here; the decorator must substitute a
	// non-nil confirm so the installer is never invoked with nil.
	if _, err := eff.EnsureAWSCLI(context.Background(), nil); err != nil {
		t.Fatalf("EnsureAWSCLI: %v", err)
	}
	if gotConfirm == nil {
		t.Fatal("cliSetupEffects.EnsureAWSCLI passed a nil confirm to the injected EnsureAWSCLI")
	}
	// The resolved confirm must be usable without panicking.
	if _, err := gotConfirm(AWSCLIInstallPlan{}); err != nil {
		t.Fatalf("resolved confirm returned error: %v", err)
	}
}
