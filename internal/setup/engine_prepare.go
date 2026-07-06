package setup

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
)

// PrepareAWS runs one pass of the AWS auth + bucket-prep sequence for p and
// returns the auth report, the prepare report, and any error. It is the
// headless body of the cli loop at internal/cli/setup.go:244-292 MINUS the
// huh repair prompt and stdout progress: on failure it classifies and
// returns the error rather than prompting. Callers own the retry decision
// (the cli driver keeps its huh repair loop; the TUI wizard mutates the plan
// and calls PrepareAWS again).
func (e *Engine) PrepareAWS(ctx context.Context, p *Plan) (AWSAuthReport, AWSPrepareReport, error) {
	method := ResolveAWSAuthMethod(p)
	auth, err := e.runAWSAuth(ctx, method, &p.Config)
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, err
	}

	prep, err := e.eff.PrepareAWS(ctx, &p.Config, AWSPrepareOptions{
		CreateBucket:      p.CreateBucket,
		BlockPublicAccess: p.BlockPublicAccess,
		DefaultEncryption: p.DefaultEncryption,
	})
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, WrapAWSPrepareError(p.Config, method, err)
	}
	return auth, prep, nil
}

// runAWSAuth dispatches the selected sign-in method. Headless port of
// runSetupAWSAuth (internal/cli/setup_auth.go:11-28).
func (e *Engine) runAWSAuth(ctx context.Context, method AWSAuthMethod, cfg *config.Config) (AWSAuthReport, error) {
	switch method {
	case AWSAuthLogin:
		return e.runAWSLoginAuth(ctx, cfg)
	case AWSAuthSSO:
		return e.runAWSSSOAuth(ctx, cfg)
	case AWSAuthExisting:
		return e.runAWSExistingAuth(ctx, cfg)
	default:
		return AWSAuthReport{}, fmt.Errorf("unsupported AWS sign-in method %q", method)
	}
}

// runAWSLoginAuth is the headless port of runSetupAWSLoginAuth
// (internal/cli/setup_auth.go:30-59). The EnsureAWSCLI confirm passed here is
// nil: per plan correction C5, resolving a nil confirm into a real
// callback (deps.ConfirmAWSCLIInstall, falling back to the huh prompt) is the
// responsibility of the Effects implementation the cli driver injects
// (cliSetupEffects.EnsureAWSCLI in Part 4), so the engine stays huh-free and
// the same call works unchanged for the TUI, which never installs the CLI
// and instead surfaces an ErrorAdvice modal for a missing binary.
func (e *Engine) runAWSLoginAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: AWSAuthLogin}
	installReport, err := e.eff.EnsureAWSCLI(ctx, nil)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager

	if e.eff.CheckAWSSDKIdentity(ctx, cfg) == nil {
		report.IdentityVerified = true
		return report, nil
	}

	if err := e.eff.AWSLogin(ctx, cfg.Repo.S3.Profile, cfg.Repo.S3.Region); err != nil {
		return AWSAuthReport{}, WrapAWSLoginFlowError(cfg.Repo.S3.Profile, err)
	}
	report.LoginRan = true

	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after browser login: %w", WrapAWSPrepareError(*cfg, AWSAuthLogin, err))
	}
	report.IdentityVerified = true
	return report, nil
}

// runAWSSSOAuth is the headless port of runSetupAWSSSOAuth
// (internal/cli/setup_auth.go:61-115). See runAWSLoginAuth's doc comment for
// why the EnsureAWSCLI confirm is nil here (C5).
func (e *Engine) runAWSSSOAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	profile := cfg.Repo.S3.Profile
	report := AWSAuthReport{Method: AWSAuthSSO}
	installReport, err := e.eff.EnsureAWSCLI(ctx, nil)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager

	if e.eff.CheckAWSSDKIdentity(ctx, cfg) == nil {
		report.IdentityVerified = true
		return report, nil
	}

	configured, err := e.eff.CheckAWSSSOConfigured(ctx, profile)
	if err != nil {
		return AWSAuthReport{}, fmt.Errorf("check aws sso profile: %w", err)
	}
	report.SSOConfigured = configured
	if !configured {
		if err := e.eff.AWSConfigureSSO(ctx, profile); err != nil {
			return AWSAuthReport{}, WrapAWSSSOFlowError("aws configure sso", profile, err)
		}
		report.SSOConfigured = true
		report.SSOConfigureRan = true
	}

	if err := e.eff.AWSSSOLogin(ctx, profile); err != nil {
		return AWSAuthReport{}, WrapAWSSSOFlowError("aws sso login", profile, err)
	}
	report.SSOLoginRan = true

	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after SSO login: %w", WrapAWSPrepareError(*cfg, AWSAuthSSO, err))
	}
	report.IdentityVerified = true
	return report, nil
}

// runAWSExistingAuth is the headless port of runSetupAWSExistingAuth
// (internal/cli/setup_auth.go:117-124).
func (e *Engine) runAWSExistingAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: AWSAuthExisting}
	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, WrapAWSPrepareError(*cfg, AWSAuthExisting, err)
	}
	report.IdentityVerified = true
	return report, nil
}
