package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/config"
)

func runSetupAWSAuth(
	ctx context.Context,
	deps SetupDeps,
	method SetupAWSAuthMethod,
	cfg *config.Config,
	out io.Writer,
) (AWSAuthReport, error) {
	switch method {
	case SetupAWSAuthLogin:
		return runSetupAWSLoginAuth(ctx, deps, cfg, out)
	case SetupAWSAuthSSO:
		return runSetupAWSSSOAuth(ctx, deps, cfg, out)
	case SetupAWSAuthExisting:
		return runSetupAWSExistingAuth(ctx, deps, cfg, out)
	default:
		return AWSAuthReport{}, fmt.Errorf("unsupported AWS sign-in method %q", method)
	}
}

func runSetupAWSLoginAuth(ctx context.Context, deps SetupDeps, cfg *config.Config, out io.Writer) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: SetupAWSAuthLogin}
	installReport, err := ensureSetupAWSCLI(ctx, deps, out)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager
	if trySetupAWSSDKIdentity(ctx, deps, cfg, out, "Checking AWS credentials", "AWS credentials ready") {
		report.IdentityVerified = true
		return report, nil
	}

	login := deps.AWSLogin
	if login == nil {
		login = DefaultAWSLogin
	}
	printSetupStep(out, "Opening AWS browser login")
	if err := login(ctx, cfg.Repo.S3.Profile, cfg.Repo.S3.Region); err != nil {
		return AWSAuthReport{}, wrapAWSLoginFlowError(cfg.Repo.S3.Profile, err)
	}
	report.LoginRan = true
	printSetupOK(out, "AWS browser login complete")

	if err := checkSetupAWSSDKIdentity(ctx, deps, cfg, out, SetupAWSAuthLogin, "Verifying AWS credentials", "AWS credentials ready"); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after browser login: %w", err)
	}
	report.IdentityVerified = true
	return report, nil
}

func runSetupAWSSSOAuth(ctx context.Context, deps SetupDeps, cfg *config.Config, out io.Writer) (AWSAuthReport, error) {
	profile := cfg.Repo.S3.Profile
	report := AWSAuthReport{Method: SetupAWSAuthSSO}
	installReport, err := ensureSetupAWSCLI(ctx, deps, out)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager
	if trySetupAWSSDKIdentity(ctx, deps, cfg, out, "Checking AWS credentials", "AWS credentials ready") {
		report.IdentityVerified = true
		return report, nil
	}

	checkConfigured := deps.CheckAWSSSOConfigured
	if checkConfigured == nil {
		checkConfigured = DefaultAWSSSOConfigured
	}
	configure := deps.AWSConfigureSSO
	if configure == nil {
		configure = DefaultAWSConfigureSSO
	}
	login := deps.AWSSSOLogin
	if login == nil {
		login = DefaultAWSSSOLogin
	}

	configured, err := checkConfigured(ctx, profile)
	if err != nil {
		return AWSAuthReport{}, fmt.Errorf("check aws sso profile: %w", err)
	}
	report.SSOConfigured = configured
	if !configured {
		printSetupStep(out, "Configuring AWS SSO profile")
		if err := configure(ctx, profile); err != nil {
			return AWSAuthReport{}, wrapAWSSSOFlowError("aws configure sso", profile, err)
		}
		report.SSOConfigured = true
		report.SSOConfigureRan = true
		printSetupOK(out, "AWS SSO profile configured")
	}

	printSetupStep(out, "Running AWS SSO login")
	if err := login(ctx, profile); err != nil {
		return AWSAuthReport{}, wrapAWSSSOFlowError("aws sso login", profile, err)
	}
	report.SSOLoginRan = true
	printSetupOK(out, "AWS SSO login complete")

	if err := checkSetupAWSSDKIdentity(ctx, deps, cfg, out, SetupAWSAuthSSO, "Verifying AWS credentials", "AWS credentials ready"); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after SSO login: %w", err)
	}
	report.IdentityVerified = true
	return report, nil
}

func runSetupAWSExistingAuth(ctx context.Context, deps SetupDeps, cfg *config.Config, out io.Writer) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: SetupAWSAuthExisting}
	if err := checkSetupAWSSDKIdentity(ctx, deps, cfg, out, SetupAWSAuthExisting, "Checking AWS credentials", "AWS credentials ready"); err != nil {
		return AWSAuthReport{}, err
	}
	report.IdentityVerified = true
	return report, nil
}

func ensureSetupAWSCLI(ctx context.Context, deps SetupDeps, out io.Writer) (AWSCLIInstallReport, error) {
	ensureAWSCLI := deps.EnsureAWSCLI
	if ensureAWSCLI == nil && deps.AWSLogin == nil && deps.AWSConfigureSSO == nil && deps.AWSSSOLogin == nil {
		ensureAWSCLI = DefaultEnsureAWSCLI
	}
	if ensureAWSCLI != nil {
		confirm := deps.ConfirmAWSCLIInstall
		if confirm == nil {
			confirm = HuhAWSCLIInstallConfirm
		}
		installReport, err := ensureAWSCLI(ctx, confirm)
		if err != nil {
			return AWSCLIInstallReport{}, err
		}
		if installReport.Installed {
			printSetupOK(out, "AWS CLI installed")
		}
		return installReport, nil
	}
	return AWSCLIInstallReport{}, nil
}

func trySetupAWSSDKIdentity(
	ctx context.Context,
	deps SetupDeps,
	cfg *config.Config,
	out io.Writer,
	progressLabel string,
	successLabel string,
) bool {
	check := setupAWSSDKIdentityChecker(deps)
	if check == nil {
		printSetupStep(out, progressLabel)
		printSetupOK(out, successLabel)
		return true
	}
	step := startSetupProgress(out, progressLabel)
	if err := check(ctx, cfg); err != nil {
		step.Clear()
		return false
	}
	step.Success(successLabel)
	return true
}

func checkSetupAWSSDKIdentity(
	ctx context.Context,
	deps SetupDeps,
	cfg *config.Config,
	out io.Writer,
	method SetupAWSAuthMethod,
	progressLabel string,
	successLabel string,
) error {
	check := setupAWSSDKIdentityChecker(deps)
	if check == nil {
		printSetupStep(out, progressLabel)
		printSetupOK(out, successLabel)
		return nil
	}
	if err := runSetupProgress(out, progressLabel, successLabel, func() error {
		return check(ctx, cfg)
	}); err != nil {
		return wrapAWSPrepareError(cfg, method, err)
	}
	return nil
}

func setupAWSSDKIdentityChecker(deps SetupDeps) func(context.Context, *config.Config) error {
	if deps.CheckAWSSDKIdentity != nil {
		return deps.CheckAWSSDKIdentity
	}
	if deps.PrepareAWS != nil {
		return nil
	}
	return DefaultAWSCheckSDKIdentity
}
