package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// setupEffects adapts the cli's SetupDeps injection seam onto the setup
// engine's Effects interface. A nil dep field falls back to the production
// default, matching the historical per-field fallbacks in setup_auth.go and
// runSetup so the behavior-preservation oracle keeps passing.
func setupEffects(deps SetupDeps) setup.Effects {
	def := setup.DefaultEffects()
	return &cliSetupEffects{deps: deps, def: def}
}

type cliSetupEffects struct {
	deps SetupDeps
	def  setup.Effects
}

// EnsureAWSCLI resolves the nil confirm the engine passes (plan correction C5):
// the engine stays huh-free and calls EnsureAWSCLI(ctx, nil), so the cli
// decorator supplies the real confirm — deps.ConfirmAWSCLIInstall, falling back
// to the huh install prompt — before delegating. Without this the CLI's
// AWS-CLI-install path would run its installer with a nil confirm and panic.
// The historical rule from setup_auth.go:126-146 also holds: the default
// AWS-CLI preflight runs only when NONE of the interactive AWS effects are
// injected; otherwise a test that stubs login/sso must not trigger a real brew
// probe.
func (e *cliSetupEffects) EnsureAWSCLI(ctx context.Context, confirm setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error) {
	if confirm == nil {
		confirm = e.deps.ConfirmAWSCLIInstall
		if confirm == nil {
			confirm = HuhAWSCLIInstallConfirm
		}
	}
	if e.deps.EnsureAWSCLI == nil &&
		e.deps.AWSLogin == nil && e.deps.AWSConfigureSSO == nil && e.deps.AWSSSOLogin == nil {
		return e.def.EnsureAWSCLI(ctx, confirm)
	}
	if e.deps.EnsureAWSCLI == nil {
		return setup.AWSCLIInstallReport{}, nil
	}
	return e.deps.EnsureAWSCLI(ctx, confirm)
}

func (e *cliSetupEffects) AWSLogin(ctx context.Context, profile, region string) error {
	if e.deps.AWSLogin == nil {
		return e.def.AWSLogin(ctx, profile, region)
	}
	return e.deps.AWSLogin(ctx, profile, region)
}

func (e *cliSetupEffects) CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	if e.deps.CheckAWSSSOConfigured == nil {
		return e.def.CheckAWSSSOConfigured(ctx, profile)
	}
	return e.deps.CheckAWSSSOConfigured(ctx, profile)
}

func (e *cliSetupEffects) AWSConfigureSSO(ctx context.Context, profile string) error {
	if e.deps.AWSConfigureSSO == nil {
		return e.def.AWSConfigureSSO(ctx, profile)
	}
	return e.deps.AWSConfigureSSO(ctx, profile)
}

func (e *cliSetupEffects) AWSSSOLogin(ctx context.Context, profile string) error {
	if e.deps.AWSSSOLogin == nil {
		return e.def.AWSSSOLogin(ctx, profile)
	}
	return e.deps.AWSSSOLogin(ctx, profile)
}

func (e *cliSetupEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	if e.deps.CheckAWSSDKIdentity != nil {
		return e.deps.CheckAWSSDKIdentity(ctx, cfg)
	}
	// Historical nil rule (setup_auth.go:194-202): when PrepareAWS is injected
	// but no identity checker is, skip the SDK identity call (treat as
	// verified). This let tests inject only PrepareAWS and have the identity
	// check be a no-op success.
	if e.deps.PrepareAWS != nil {
		return nil
	}
	return e.def.CheckAWSSDKIdentity(ctx, cfg)
}

func (e *cliSetupEffects) PrepareAWS(ctx context.Context, cfg *config.Config, opts setup.AWSPrepareOptions) (setup.AWSPrepareReport, error) {
	if e.deps.PrepareAWS == nil {
		return e.def.PrepareAWS(ctx, cfg, opts)
	}
	return e.deps.PrepareAWS(ctx, cfg, opts)
}

func (e *cliSetupEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if e.deps.NewStore == nil {
		return e.def.NewStore(ctx, cfg)
	}
	return e.deps.NewStore(ctx, cfg)
}

func (e *cliSetupEffects) SavePassphrase(cfg *config.Config, pass []byte) error {
	if e.deps.SavePassphrase == nil {
		return e.def.SavePassphrase(cfg, pass)
	}
	return e.deps.SavePassphrase(cfg, pass)
}
