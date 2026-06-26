package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/markgustetic/sentra/internal/config"
)

// HuhSetupPrompt is the production interactive wizard for `sentra setup`.
func HuhSetupPrompt(current config.Config) (SetupPlan, error) {
	plan := SetupPlan{
		Config:            current,
		Backend:           SetupBackendAWS,
		PrepareAWS:        true,
		UseAWSCLIAuth:     true,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		InitRepo:          true,
	}
	if current.Repo.S3.EndpointURL != "" {
		plan.Backend = SetupBackendS3Compatible
		plan.PrepareAWS = false
		plan.CreateBucket = false
		plan.BlockPublicAccess = false
		plan.DefaultEncryption = false
	}

	backendForm := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[SetupBackend]().
				Title("Where should Sentra store backups?").
				Options(
					huh.NewOption("AWS S3", SetupBackendAWS).
						Selected(plan.Backend == SetupBackendAWS),
					huh.NewOption("S3-compatible or existing bucket", SetupBackendS3Compatible).
						Selected(plan.Backend == SetupBackendS3Compatible),
				).
				Value(&plan.Backend),
		),
	)
	if err := backendForm.Run(); err != nil {
		return SetupPlan{}, err
	}

	if plan.Backend == SetupBackendAWS {
		return runHuhAWSSetup(current, plan)
	}
	return runHuhCompatibleSetup(current, plan)
}

func runHuhAWSSetup(current config.Config, plan SetupPlan) (SetupPlan, error) {
	cfg := current
	bucket := cfg.Repo.S3.Bucket
	prefix := cfg.Repo.S3.Prefix
	region := cfg.Repo.S3.Region
	profile := cfg.Repo.S3.Profile
	if region == "" {
		region = "us-east-1"
	}
	if prefix == "" {
		prefix = "sentra/"
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("S3 bucket").
				Description("Required. Use a globally unique bucket name.").
				Value(&bucket).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("bucket is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("S3 key prefix").
				Description("Optional. Useful when sharing one bucket across repos.").
				Value(&prefix),
			huh.NewInput().
				Title("AWS region").
				Description("Region for the bucket and Sentra config.").
				Value(&region).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("region is required for AWS setup")
					}
					return nil
				}),
			huh.NewInput().
				Title("AWS profile").
				Description("Optional shared-config profile name.").
				Placeholder("default").
				Value(&profile),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Configure/check AWS SSO with the AWS CLI if needed?").
				Description("Runs AWS CLI SSO configure/login only if identity is not already available. Skip for env or role credentials.").
				Affirmative("Configure/check").
				Negative("Skip").
				Value(&plan.UseAWSCLIAuth),
			huh.NewConfirm().
				Title("Create the bucket if it does not exist?").
				Affirmative("Create/verify").
				Negative("Verify only").
				Value(&plan.CreateBucket),
			huh.NewConfirm().
				Title("Block public access on the bucket?").
				Affirmative("Block public access").
				Negative("Skip").
				Value(&plan.BlockPublicAccess),
			huh.NewConfirm().
				Title("Enable S3 default encryption?").
				Affirmative("Enable AES256").
				Negative("Skip").
				Value(&plan.DefaultEncryption),
			huh.NewConfirm().
				Title("Initialize the encrypted Sentra repository after writing config?").
				Affirmative("Initialize").
				Negative("Config only").
				Value(&plan.InitRepo),
		),
	)
	if err := form.Run(); err != nil {
		return SetupPlan{}, err
	}

	cfg.Repo.S3.Bucket = bucket
	cfg.Repo.S3.Prefix = prefix
	cfg.Repo.S3.Region = region
	cfg.Repo.S3.Profile = profile
	cfg.Repo.S3.EndpointURL = ""
	plan.Config = cfg
	plan.PrepareAWS = true
	normalizeSetupConfig(&plan.Config)
	return plan, nil
}

func runHuhCompatibleSetup(current config.Config, plan SetupPlan) (SetupPlan, error) {
	cfg := current
	bucket := cfg.Repo.S3.Bucket
	prefix := cfg.Repo.S3.Prefix
	region := cfg.Repo.S3.Region
	profile := cfg.Repo.S3.Profile
	endpointURL := cfg.Repo.S3.EndpointURL

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("S3 bucket").
				Description("Required. Use an existing S3 or S3-compatible bucket.").
				Value(&bucket).
				Validate(func(s string) error {
					if strings.TrimSpace(s) == "" {
						return fmt.Errorf("bucket is required")
					}
					return nil
				}),
			huh.NewInput().
				Title("S3 key prefix").
				Description("Optional. Useful when sharing one bucket across repos.").
				Placeholder("sentra/").
				Value(&prefix),
			huh.NewInput().
				Title("AWS region").
				Description("Optional if your SDK config or S3-compatible store does not require it.").
				Placeholder("us-west-2").
				Value(&region),
			huh.NewInput().
				Title("AWS profile").
				Description("Optional shared-config profile name.").
				Placeholder("default").
				Value(&profile),
			huh.NewInput().
				Title("S3 endpoint URL").
				Description("Leave blank for an existing AWS bucket; set for MinIO or LocalStack.").
				Placeholder("http://localhost:9000").
				Value(&endpointURL),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Initialize the encrypted Sentra repository after writing config?").
				Affirmative("Initialize").
				Negative("Config only").
				Value(&plan.InitRepo),
		),
	)
	if err := form.Run(); err != nil {
		return SetupPlan{}, err
	}

	cfg.Repo.S3.Bucket = bucket
	cfg.Repo.S3.Prefix = prefix
	cfg.Repo.S3.Region = region
	cfg.Repo.S3.Profile = profile
	cfg.Repo.S3.EndpointURL = endpointURL
	plan.Config = cfg
	plan.PrepareAWS = false
	plan.UseAWSCLIAuth = false
	plan.CreateBucket = false
	plan.BlockPublicAccess = false
	plan.DefaultEncryption = false
	normalizeSetupConfig(&plan.Config)
	return plan, nil
}

func normalizeSetupConfig(cfg *config.Config) {
	cfg.Repo.S3.Bucket = strings.TrimSpace(cfg.Repo.S3.Bucket)
	cfg.Repo.S3.Prefix = strings.TrimSpace(cfg.Repo.S3.Prefix)
	cfg.Repo.S3.Region = strings.TrimSpace(cfg.Repo.S3.Region)
	cfg.Repo.S3.Profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	cfg.Repo.S3.EndpointURL = strings.TrimSpace(cfg.Repo.S3.EndpointURL)
}
