package setup

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
)

// PrepareAWS runs one pass of the AWS auth + bucket-prep + backup-user
// sequence for p and returns the auth report, the prepare report, and any
// error. It is the headless body of the deleted CLI wizard's auth+prepare
// loop MINUS the huh repair prompt and stdout progress: on failure it
// classifies and returns the error rather than prompting. Callers own the
// retry decision (the TUI wizard mutates the plan and calls PrepareAWS
// again).
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
	// Bucket prep ran on the session identity that just signed in; the scoped
	// user only has to USE the bucket. Provisioning comes last so a failure
	// here can never undo a prepared bucket, and never fails setup.
	if ShouldProvisionBackupUser(p) {
		prep.BackupUser = e.provisionBackupUser(ctx, p)
	}
	return auth, prep, nil
}

// runAWSAuth dispatches the selected sign-in method.
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

// runAWSLoginAuth is the headless port of the CLI wizard's browser-login
// sub-machine. The EnsureAWSCLI confirm passed here is nil, and now that
// `sentra setup` is a launcher for the TUI wizard, nil is the only value it
// ever takes in production: substituting a real prompt was the job of the
// deleted huh wizard's Effects decorator. Keeping it nil is what makes the
// engine huh-free — a huh form here would fight the running tea.Program for
// os.Stdin. DefaultEnsureAWSCLI therefore treats nil as "cannot install" and
// returns actionable guidance, which the wizard surfaces as an ErrorAdvice
// modal (TestDefaultEnsureAWSCLI_NilConfirm).
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

// runAWSSSOAuth is the headless port of the CLI wizard's SSO sub-machine. See
// runAWSLoginAuth's doc comment for why the EnsureAWSCLI confirm is nil here.
//
// The two short-circuits are load bearing. A working credential chain returns
// before any AWS CLI call, so a healthy SSO profile is not made to re-open a
// browser on every run; and an already-configured profile skips `aws configure
// sso`, which would otherwise walk the operator back through a start URL and
// region they already have.
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

// runAWSExistingAuth verifies the ambient credential chain and nothing else.
func (e *Engine) runAWSExistingAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: AWSAuthExisting}
	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, WrapAWSPrepareError(*cfg, AWSAuthExisting, err)
	}
	report.IdentityVerified = true
	return report, nil
}
