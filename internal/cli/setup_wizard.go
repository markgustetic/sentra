package cli

import (
	"fmt"
	"os"
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
				Description(fmt.Sprintf("Sentra needs the AWS CLI for the selected AWS sign-in method. Install with %s?\n\n%s", plan.Manager, strings.Join(plan.Command, " "))).
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

// HuhSetupReviewConfirm shows the final non-secret setup plan before any
// AWS or repository side effects run.
func HuhSetupReviewConfirm(cfgPath string, plan SetupPlan) (bool, error) {
	apply := true
	form := newSetupForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Review setup").
				Description(setupPlanReviewText(cfgPath, plan)).
				Affirmative("Apply setup").
				Negative("Cancel").
				Value(&apply),
		),
	)
	if err := form.Run(); err != nil {
		return false, err
	}
	return apply, nil
}

type setupAWSRepairChoice string

const (
	setupAWSRepairLogin    setupAWSRepairChoice = "login"
	setupAWSRepairSSO      setupAWSRepairChoice = "sso"
	setupAWSRepairExisting setupAWSRepairChoice = "existing"
	setupAWSRepairConfig   setupAWSRepairChoice = "config"
	setupAWSRepairCancel   setupAWSRepairChoice = "cancel"
)

// HuhSetupAWSAuthRepairPrompt offers a recovery path after AWS auth or bucket
// preparation fails.
func HuhSetupAWSAuthRepairPrompt(plan SetupPlan, cause error) (SetupPlan, bool, error) {
	choice := setupAWSRepairLogin
	switch plan.AWSAuthMethod {
	case SetupAWSAuthSSO:
		choice = setupAWSRepairSSO
	case SetupAWSAuthExisting:
		choice = setupAWSRepairExisting
	case SetupAWSAuthSkip:
		choice = setupAWSRepairConfig
	}
	cfg := plan.Config
	region := cfg.Repo.S3.Region
	profile := cfg.Repo.S3.Profile
	if region == "" {
		region = "us-east-1"
	}

	form := newSetupForm(
		huh.NewGroup(
			huh.NewNote().
				Title("AWS setup needs attention").
				Description(fmt.Sprintf("%v\n\nYou can edit the profile or region, try another sign-in method, or write config only.", cause)).
				Next(true).
				NextLabel("Choose next step"),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("AWS region").
				Description("Used for AWS sign-in and bucket operations.").
				Value(&region),
			huh.NewInput().
				Title("AWS profile").
				Description("Leave blank to use environment credentials or an attached role.").
				Placeholder("default").
				Value(&profile),
			huh.NewSelect[setupAWSRepairChoice]().
				Title("Next step").
				Options(
					huh.NewOption("Browser login with AWS CLI", setupAWSRepairLogin).
						Selected(choice == setupAWSRepairLogin),
					huh.NewOption("IAM Identity Center / SSO", setupAWSRepairSSO).
						Selected(choice == setupAWSRepairSSO),
					huh.NewOption("Existing profile, environment, or role credentials", setupAWSRepairExisting).
						Selected(choice == setupAWSRepairExisting),
					huh.NewOption("Write config only", setupAWSRepairConfig).
						Selected(choice == setupAWSRepairConfig),
					huh.NewOption("Cancel setup", setupAWSRepairCancel).
						Selected(choice == setupAWSRepairCancel),
				).
				Value(&choice),
		),
	)
	if err := form.Run(); err != nil {
		return plan, false, err
	}

	cfg.Repo.S3.Region = region
	cfg.Repo.S3.Profile = profile
	plan.Config = cfg
	switch choice {
	case setupAWSRepairLogin:
		plan.AWSAuthMethod = SetupAWSAuthLogin
		plan.PrepareAWS = true
	case setupAWSRepairSSO:
		plan.AWSAuthMethod = SetupAWSAuthSSO
		plan.PrepareAWS = true
	case setupAWSRepairExisting:
		plan.AWSAuthMethod = SetupAWSAuthExisting
		plan.PrepareAWS = true
	case setupAWSRepairConfig:
		applySetupAWSConfigOnly(&plan)
	case setupAWSRepairCancel:
		return plan, false, nil
	}
	normalizeSetupConfig(&plan.Config)
	return plan, true, nil
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
		return runHuhAWSSetup(plan.Config, plan)
	}
	return runHuhCompatibleSetup(plan.Config, plan)
}

func defaultSetupPlan(current config.Config) SetupPlan {
	plan := SetupPlan{
		Config:            current,
		Backend:           SetupBackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     SetupAWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		InitRepo:          true,
	}
	applySetupSmartDefaults(&plan)
	return plan
}

func applySetupSmartDefaults(plan *SetupPlan) {
	if plan.Config.Repo.S3.Region == "" {
		plan.Config.Repo.S3.Region = firstNonEmptyEnv("AWS_REGION", "AWS_DEFAULT_REGION")
	}
	if plan.Config.Repo.S3.Profile == "" {
		plan.Config.Repo.S3.Profile = firstNonEmptyEnv("AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	}
	if plan.Config.Repo.S3.Profile == "" {
		plan.Config.Repo.S3.Profile = defaultAWSProfileFromConfig()
	}
	if hasAWSEnvironmentCredentials() || plan.Config.Repo.S3.Profile != "" {
		plan.AWSAuthMethod = SetupAWSAuthExisting
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func hasAWSEnvironmentCredentials() bool {
	if strings.TrimSpace(os.Getenv("AWS_ROLE_ARN")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "" ||
		strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN")) != ""
}

func defaultAWSProfileFromConfig() string {
	cfg, err := loadAWSCLIConfig()
	if err != nil || cfg == nil {
		return ""
	}
	for _, profile := range []string{"sentra", "default"} {
		if len(cfg[awsProfileSection(profile)]) > 0 {
			return profile
		}
	}
	for section := range cfg {
		if profile := awsProfileNameFromSection(section); profile != "" {
			return profile
		}
	}
	return ""
}

func awsProfileNameFromSection(section string) string {
	section = strings.TrimSpace(section)
	if section == "default" {
		return "default"
	}
	if strings.HasPrefix(section, "profile ") {
		return strings.TrimSpace(strings.TrimPrefix(section, "profile "))
	}
	return ""
}

func runHuhAWSSetup(current config.Config, plan SetupPlan) (SetupPlan, error) {
	cfg := current
	bucket := cfg.Repo.S3.Bucket
	prefix := cfg.Repo.S3.Prefix
	region := cfg.Repo.S3.Region
	profile := cfg.Repo.S3.Profile
	printIAMPolicy := plan.PrintIAMPolicy
	if region == "" {
		region = "us-east-1"
	}
	if profile == "" && plan.AWSAuthMethod != SetupAWSAuthExisting {
		profile = "sentra"
	}
	if prefix == "" {
		prefix = "sentra/"
	}

	detailsForm := newSetupForm(
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
					return validateSetupBucketName(s)
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
			huh.NewConfirm().
				Title("Need IAM policy before setup?").
				Description("Print least-privilege IAM JSON for this bucket/prefix and stop before AWS or local files change.").
				Affirmative("Print policy").
				Negative("Continue setup").
				Value(&printIAMPolicy),
		),
	)
	if err := detailsForm.Run(); err != nil {
		return SetupPlan{}, err
	}

	cfg.Repo.S3.Bucket = bucket
	cfg.Repo.S3.Prefix = prefix
	cfg.Repo.S3.Region = region
	cfg.Repo.S3.Profile = profile
	cfg.Repo.S3.EndpointURL = ""
	plan.Config = cfg
	plan.PrintIAMPolicy = printIAMPolicy
	if printIAMPolicy {
		plan.PrepareAWS = false
		plan.CreateBucket = false
		plan.BlockPublicAccess = false
		plan.DefaultEncryption = false
		plan.InitRepo = false
		normalizeSetupConfig(&plan.Config)
		return plan, nil
	}

	actionsForm := newSetupForm(
		huh.NewGroup(
			huh.NewNote().
				Title("Setup actions").
				Description("Sentra verifies AWS credentials before bucket setup. Browser login uses temporary AWS CLI credentials and is the easiest local path.").
				Next(true).
				NextLabel("Review actions"),
		),
		huh.NewGroup(
			huh.NewSelect[SetupAWSAuthMethod]().
				Title("AWS sign-in method").
				Description("Sentra will verify credentials before touching S3. Choose config-only if you want to set credentials up later.").
				Options(
					huh.NewOption("Browser login with AWS CLI (Recommended)", SetupAWSAuthLogin).
						Selected(plan.AWSAuthMethod == SetupAWSAuthLogin),
					huh.NewOption("IAM Identity Center / SSO", SetupAWSAuthSSO).
						Selected(plan.AWSAuthMethod == SetupAWSAuthSSO),
					huh.NewOption("Existing profile, environment, or role credentials", SetupAWSAuthExisting).
						Selected(plan.AWSAuthMethod == SetupAWSAuthExisting),
					huh.NewOption("Skip AWS setup and write config only", SetupAWSAuthSkip).
						Selected(plan.AWSAuthMethod == SetupAWSAuthSkip),
				).
				Value(&plan.AWSAuthMethod),
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
				Description("If initialized now, Sentra will prompt for the repository passphrase unless --passphrase-file, SENTRA_PASSPHRASE, or keyring supplies it. The passphrase is not written to sentra.yaml.").
				Affirmative("Initialize").
				Negative("Config only").
				Value(&plan.InitRepo),
		),
	)
	if err := actionsForm.Run(); err != nil {
		return SetupPlan{}, err
	}

	plan.PrepareAWS = true
	if plan.AWSAuthMethod == SetupAWSAuthSkip {
		plan.PrepareAWS = false
		plan.CreateBucket = false
		plan.BlockPublicAccess = false
		plan.DefaultEncryption = false
		plan.InitRepo = false
	}
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
					return validateSetupBucketName(s)
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
	plan.AWSAuthMethod = SetupAWSAuthSkip
	plan.CreateBucket = false
	plan.BlockPublicAccess = false
	plan.DefaultEncryption = false
	normalizeSetupConfig(&plan.Config)
	return plan, nil
}

func setupPlanReviewText(cfgPath string, plan SetupPlan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Config: %s\n", cfgPath)
	fmt.Fprintf(&b, "Storage: %s\n", setupBackendLabel(plan.Backend))
	fmt.Fprintf(&b, "Bucket: %s\n", emptyDash(plan.Config.Repo.S3.Bucket))
	if plan.Config.Repo.S3.Prefix != "" {
		fmt.Fprintf(&b, "Prefix: %s\n", plan.Config.Repo.S3.Prefix)
	}
	if plan.Config.Repo.S3.Region != "" {
		fmt.Fprintf(&b, "Region: %s\n", plan.Config.Repo.S3.Region)
	}
	if plan.Config.Repo.S3.Profile != "" {
		fmt.Fprintf(&b, "Profile: %s\n", plan.Config.Repo.S3.Profile)
	}
	if plan.Config.Repo.S3.EndpointURL != "" {
		fmt.Fprintf(&b, "Endpoint: %s\n", plan.Config.Repo.S3.EndpointURL)
	}
	if plan.PrepareAWS {
		fmt.Fprintf(&b, "AWS sign-in: %s\n", setupAWSAuthMethodLabel(plan.AWSAuthMethod))
		fmt.Fprintf(&b, "Create missing bucket: %t\n", plan.CreateBucket)
		fmt.Fprintf(&b, "Block public access: %t\n", plan.BlockPublicAccess)
		fmt.Fprintf(&b, "Enable default encryption: %t\n", plan.DefaultEncryption)
	} else {
		fmt.Fprintln(&b, "AWS setup: skipped")
	}
	if plan.InitRepo {
		fmt.Fprintln(&b, "Repository: initialize after config")
		fmt.Fprintln(&b, "Passphrase: prompted or read from --passphrase-file, SENTRA_PASSPHRASE, or keyring")
	} else {
		fmt.Fprintln(&b, "Repository: config only")
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.")
	return b.String()
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
