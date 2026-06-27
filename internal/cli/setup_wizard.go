package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/config"
)

const setupIntroText = "Configure storage, optional AWS automation, and repository initialization in one flow.\n\nSentra only writes non-secret settings to sentra.yaml."

// HuhSetupOverwriteConfirm asks whether an existing setup config may be
// overwritten after the wizard completes.
func HuhSetupOverwriteConfirm(path string) (bool, error) {
	overwrite := false
	form := newSetupForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Config file already exists").
				Description(fmt.Sprintf("%s will be loaded as defaults and overwritten after setup finishes.", path)).
				Affirmative("Review/overwrite").
				Negative("Cancel").
				Value(&overwrite),
		),
	)
	if err := form.Run(); err != nil {
		return false, err
	}
	return overwrite, nil
}

// HuhAWSCLIInstallConfirm asks whether setup may run the detected AWS CLI
// package-manager install command.
func HuhAWSCLIInstallConfirm(plan AWSCLIInstallPlan) (bool, error) {
	install := false
	form := newSetupForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("AWS CLI is not installed").
				Description(fmt.Sprintf("Sentra only needs the AWS CLI when you choose AWS CLI SSO auth. Install with %s?\n\n%s", plan.Manager, strings.Join(plan.Command, " "))).
				Affirmative("Install").
				Negative("Skip").
				Value(&install),
		),
	)
	if err := form.Run(); err != nil {
		return false, err
	}
	return install, nil
}

// HuhSetupPrompt is the production interactive wizard for `sentra setup`.
func HuhSetupPrompt(current config.Config) (SetupPlan, error) {
	plan := defaultSetupPlan(current)
	backendForm := newSetupForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Sentra setup").
				Description(setupIntroText).
				Next(true).
				NextLabel("Start"),
		),
		huh.NewGroup(
			huh.NewSelect[SetupBackend]().
				Title("Storage backend").
				Description("Choose the storage target Sentra should configure.").
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

func defaultSetupPlan(current config.Config) SetupPlan {
	plan := SetupPlan{
		Config:            current,
		Backend:           SetupBackendAWS,
		PrepareAWS:        true,
		UseAWSCLIAuth:     false,
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
	return plan
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

	form := newSetupForm(
		huh.NewGroup(
			huh.NewNote().
				Title("AWS S3").
				Description("Use a globally unique bucket name. Sentra stores encrypted, deduplicated blobs under the prefix you choose.").
				Next(true).
				NextLabel("Continue"),
		),
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
			huh.NewNote().
				Title("Setup actions").
				Description("Sentra can prepare safe bucket defaults and initialize the encrypted repository. AWS CLI SSO is optional and only needed for IAM Identity Center/SSO access.").
				Next(true).
				NextLabel("Review actions"),
		),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Use AWS CLI SSO login?").
				Description("Choose Skip SSO for access keys, environment credentials, normal AWS profiles, or role credentials. If enabled, Sentra may run aws configure sso/login after an identity check fails.").
				Affirmative("Use SSO").
				Negative("Skip SSO").
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
				Affirmative("Enable AES-256").
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

	form := newSetupForm(
		huh.NewGroup(
			huh.NewNote().
				Title("S3-compatible storage").
				Description("Use this path for MinIO, LocalStack, or an existing AWS bucket you want to manage yourself.").
				Next(true).
				NextLabel("Continue"),
		),
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

func newSetupForm(groups ...*huh.Group) *huh.Form {
	return huh.NewForm(groups...).WithTheme(setupHuhTheme())
}

func setupHuhTheme() *huh.Theme {
	theme := huh.ThemeCharm()
	primary := lipgloss.Color("#7C3AED")
	success := lipgloss.Color("#10B981")
	muted := lipgloss.Color("#6B7280")

	theme.Focused.Title = theme.Focused.Title.Foreground(primary).Bold(true)
	theme.Focused.NoteTitle = theme.Focused.NoteTitle.Foreground(primary).Bold(true)
	theme.Focused.SelectSelector = theme.Focused.SelectSelector.Foreground(primary)
	theme.Focused.TextInput.Prompt = theme.Focused.TextInput.Prompt.Foreground(primary)
	theme.Focused.TextInput.Cursor = theme.Focused.TextInput.Cursor.Foreground(success)
	theme.Focused.Description = theme.Focused.Description.Foreground(muted)
	theme.Focused.FocusedButton = theme.Focused.FocusedButton.Background(primary)
	theme.Focused.Next = theme.Focused.Next.Background(primary)

	theme.Blurred = theme.Focused
	theme.Blurred.Base = theme.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	theme.Blurred.NextIndicator = lipgloss.NewStyle()
	theme.Blurred.PrevIndicator = lipgloss.NewStyle()
	return theme
}
