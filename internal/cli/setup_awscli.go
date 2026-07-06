package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/setup"
)

func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	report, err := setup.DefaultEnsureAWSCLI(ctx, func(p setup.AWSCLIInstallPlan) (bool, error) {
		return confirm(AWSCLIInstallPlan{Manager: p.Manager, Command: p.Command})
	})
	return AWSCLIInstallReport{
		AlreadyInstalled: report.AlreadyInstalled,
		Installed:        report.Installed,
		Manager:          report.Manager,
	}, err
}

func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	return setup.DefaultAWSLogin(ctx, profile, region)
}

func DefaultAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return setup.DefaultAWSSSOConfigured(ctx, profile)
}

func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return setup.DefaultAWSConfigureSSO(ctx, profile)
}

func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return setup.DefaultAWSSSOLogin(ctx, profile)
}

func loadAWSCLIConfig() (setup.AWSCLIConfig, error) {
	return setup.LoadAWSCLIConfig()
}

func awsProfileSection(profile string) string {
	return setup.AWSProfileSection(profile)
}
