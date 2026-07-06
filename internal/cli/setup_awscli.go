package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/setup"
)

func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	return setup.DefaultEnsureAWSCLI(ctx, confirm)
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
