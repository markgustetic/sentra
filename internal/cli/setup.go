package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// SetupBackend names the storage target chosen in the setup wizard.
type SetupBackend string

const (
	SetupBackendAWS          SetupBackend = "aws"
	SetupBackendS3Compatible SetupBackend = "s3-compatible"
)

// SetupPlan is the complete set of actions the setup wizard selected.
// Tests return this directly; production builds it from the huh TUI.
type SetupPlan struct {
	Config            config.Config
	Backend           SetupBackend
	PrepareAWS        bool
	UseAWSCLIAuth     bool
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
	InitRepo          bool
}

// SetupPrompt collects an updated setup plan from the operator.
// Production wires this to HuhSetupPrompt; tests inject a deterministic
// callback.
type SetupPrompt func(current config.Config) (SetupPlan, error)

// SetupOverwriteConfirm asks whether an existing config file may be
// overwritten. Production wires this to HuhSetupOverwriteConfirm; tests
// inject a deterministic callback.
type SetupOverwriteConfirm func(path string) (bool, error)

// AWSCLIInstallPlan is the package-manager command Sentra can run to install
// the AWS CLI for setup's SSO flow.
type AWSCLIInstallPlan struct {
	Manager string
	Command []string
}

// AWSCLIInstallConfirm asks whether Sentra may run the detected package
// manager command. Production wires this to HuhAWSCLIInstallConfirm; tests
// inject a deterministic callback.
type AWSCLIInstallConfirm func(plan AWSCLIInstallPlan) (bool, error)

// AWSCLIInstallReport summarizes the AWS CLI preflight.
type AWSCLIInstallReport struct {
	AlreadyInstalled bool
	Installed        bool
	Manager          string
}

// AWSPrepareOptions controls the AWS-side setup work. Bucket existence
// is always checked; CreateBucket decides whether a missing bucket is
// created or reported as an error.
type AWSPrepareOptions struct {
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
}

// AWSPrepareReport summarizes the AWS setup work for the final CLI output.
type AWSPrepareReport struct {
	BucketExisted            bool
	BucketCreated            bool
	PublicAccessBlocked      bool
	DefaultEncryptionEnabled bool
}

// AWSAuthReport summarizes the optional AWS CLI auth preflight.
type AWSAuthReport struct {
	IdentityVerified bool
	AWSCLIInstalled  bool
	AWSCLIManager    string
	SSOConfigured    bool
	SSOConfigureRan  bool
	SSOLoginRan      bool
}

// SetupDeps wires the side-effecting pieces of `sentra setup`.
type SetupDeps struct {
	Prompt                SetupPrompt
	ConfirmOverwrite      SetupOverwriteConfirm
	EnsureAWSCLI          func(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	ConfirmAWSCLIInstall  AWSCLIInstallConfirm
	CheckAWSIdentity      func(ctx context.Context, profile string) error
	CheckAWSSSOConfigured func(ctx context.Context, profile string) (bool, error)
	AWSConfigureSSO       func(ctx context.Context, profile string) error
	AWSSSOLogin           func(ctx context.Context, profile string) error
	PrepareAWS            func(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	NewStore              func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase            func() ([]byte, error)
	Stdout                io.Writer
}

type setupInitResult struct {
	RepoID             string
	AlreadyInitialized bool
}

// NewSetup returns the cobra command for `sentra setup`. The command
// launches a guided TUI wizard, writes sentra.yaml, and can optionally
// prepare AWS S3 and initialize the encrypted repository.
func NewSetup(deps SetupDeps) *cobra.Command {
	var (
		cfgPath string
		force   bool
	)
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run the guided Sentra setup wizard",
		Long: "Open an interactive terminal wizard for configuring Sentra. " +
			"The wizard can check AWS CLI identity, run AWS SSO profile setup " +
			"or login if needed, prepare an AWS S3 bucket, write sentra.yaml, " +
			"and initialize the encrypted repository in one flow.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd, deps, cfgPath, force)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to write sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().BoolVar(&force, "force", false,
		"overwrite an existing sentra.yaml")
	return cmd
}

func runSetup(cmd *cobra.Command, deps SetupDeps, cfgPath string, force bool) error {
	cmd.SilenceUsage = true
	if cfgPath == "" {
		cfgPath = configFileName
	}

	yamlExists := false
	if _, err := os.Stat(cfgPath); err == nil {
		yamlExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}
	if yamlExists && !force {
		confirmOverwrite := deps.ConfirmOverwrite
		if confirmOverwrite == nil {
			confirmOverwrite = HuhSetupOverwriteConfirm
		}
		overwrite, err := confirmOverwrite(cfgPath)
		if err != nil {
			return fmt.Errorf("confirm overwrite %s: %w", cfgPath, err)
		}
		if !overwrite {
			return fmt.Errorf("%s exists - setup canceled", cfgPath)
		}
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	prompt := deps.Prompt
	if prompt == nil {
		prompt = HuhSetupPrompt
	}
	plan, err := prompt(*cfg)
	if err != nil {
		return fmt.Errorf("run setup wizard: %w", err)
	}
	normalizeSetupConfig(&plan.Config)
	if plan.Config.Repo.S3.Bucket == "" {
		return fmt.Errorf("repo.s3.bucket not set - enter a bucket name")
	}
	if plan.PrepareAWS && plan.Config.Repo.S3.EndpointURL != "" {
		return fmt.Errorf("AWS setup does not support endpoint_url - choose S3-compatible/manual setup for MinIO or LocalStack")
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}
	printSetupApplyHeader(out, cfgPath, &plan)

	var (
		awsAuthReport *AWSAuthReport
		awsReport     *AWSPrepareReport
	)
	if plan.PrepareAWS {
		if plan.UseAWSCLIAuth {
			report, err := runSetupAWSCLIAuth(cmd.Context(), deps, plan.Config.Repo.S3.Profile, out)
			if err != nil {
				return err
			}
			awsAuthReport = &report
		}

		prepareAWS := deps.PrepareAWS
		if prepareAWS == nil {
			prepareAWS = DefaultAWSPrepare
		}
		awsStep := startSetupProgress(out, "Preparing AWS S3 bucket")
		report, err := prepareAWS(cmd.Context(), &plan.Config, AWSPrepareOptions{
			CreateBucket:      plan.CreateBucket,
			BlockPublicAccess: plan.BlockPublicAccess,
			DefaultEncryption: plan.DefaultEncryption,
		})
		if err != nil {
			awsStep.Fail()
			return wrapAWSPrepareError(&plan.Config, plan.UseAWSCLIAuth, err)
		}
		awsReport = &report
		awsStep.Success(setupAWSPreparedLabel(&report))
	}

	printSetupStep(out, "Writing "+cfgPath)
	if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(&plan.Config)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	printSetupOK(out, "Config written")

	var initResult *setupInitResult
	if plan.InitRepo {
		printSetupStep(out, "Initializing encrypted repository")
		result, err := runSetupInit(cmd.Context(), deps, &plan.Config)
		if err != nil {
			return err
		}
		initResult = &result
		if result.AlreadyInitialized {
			printSetupOK(out, "Repository already initialized")
		} else {
			printSetupOK(out, "Repository initialized")
		}
	}

	printSetupSummary(out, cfgPath, &plan, awsAuthReport, awsReport, initResult)
	return nil
}

func runSetupAWSCLIAuth(ctx context.Context, deps SetupDeps, profile string, out io.Writer) (AWSAuthReport, error) {
	report := AWSAuthReport{}
	ensureAWSCLI := deps.EnsureAWSCLI
	if ensureAWSCLI == nil && deps.CheckAWSIdentity == nil {
		ensureAWSCLI = DefaultEnsureAWSCLI
	}
	if ensureAWSCLI != nil {
		confirm := deps.ConfirmAWSCLIInstall
		if confirm == nil {
			confirm = HuhAWSCLIInstallConfirm
		}
		installReport, err := ensureAWSCLI(ctx, confirm)
		if err != nil {
			return AWSAuthReport{}, err
		}
		report.AWSCLIInstalled = installReport.Installed
		report.AWSCLIManager = installReport.Manager
		if report.AWSCLIInstalled {
			printSetupOK(out, "AWS CLI installed")
		}
	}

	check := deps.CheckAWSIdentity
	if check == nil {
		check = DefaultAWSCheckIdentity
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

	identityStep := startSetupProgress(out, "Checking AWS identity")
	if err := check(ctx, profile); err == nil {
		identityStep.Success("AWS identity ready")
		report.IdentityVerified = true
		return report, nil
	} else {
		identityStep.Clear()
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

	if err := runSetupProgress(out, "Verifying AWS identity", "AWS identity ready", func() error {
		return check(ctx, profile)
	}); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS identity is still unavailable after SSO login. SSO is optional; rerun `sentra setup` and choose Skip SSO to use access keys, environment credentials, a normal AWS profile, or role credentials instead: %w", err)
	}
	report.IdentityVerified = true
	return report, nil
}

func wrapAWSSSOFlowError(command string, profile string, err error) error {
	profile = strings.TrimSpace(profile)
	profileLabel := "the default profile"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	return fmt.Errorf("%s did not complete for %s. SSO is optional; rerun `sentra setup` and choose Skip SSO to use access keys, environment credentials, a normal AWS profile, or role credentials instead. To use a non-SSO AWS CLI profile, run `%s` first: %w", command, profileLabel, configureCommand, err)
}

func wrapAWSPrepareError(cfg *config.Config, usedSSO bool, err error) error {
	if !isAWSMissingCredentialsError(err) {
		return fmt.Errorf("prepare AWS S3: %w", err)
	}

	profile := ""
	if cfg != nil {
		profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	}
	profileLabel := "the default AWS credential chain"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "AWS profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	if usedSSO {
		return fmt.Errorf("prepare AWS S3: AWS credentials were not available for %s after the SSO flow. Rerun `sentra setup` and choose Use SSO again, or configure non-SSO credentials with `%s`: %w", profileLabel, configureCommand, err)
	}
	return fmt.Errorf("prepare AWS S3: AWS credentials were not found for %s. Configure them with `%s`, export AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, use role credentials, or rerun `sentra setup` and choose Use SSO if your AWS account uses IAM Identity Center: %w", profileLabel, configureCommand, err)
}

func isAWSMissingCredentialsError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"failed to refresh cached credentials",
		"no ec2 imds role found",
		"no valid credential",
		"no credential provider",
		"credential providers",
		"ec2imds",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func runSetupInit(ctx context.Context, deps SetupDeps, cfg *config.Config) (setupInitResult, error) {
	if deps.NewStore == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing store factory")
	}
	if deps.Passphrase == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing passphrase resolver")
	}

	store, err := deps.NewStore(ctx, cfg)
	if err != nil {
		return setupInitResult{}, fmt.Errorf("open blobstore: %w", err)
	}

	pass, err := deps.Passphrase()
	if err != nil {
		return setupInitResult{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Init(ctx, store, pass)
	if err != nil {
		if errors.Is(err, repo.ErrAlreadyInitialized) {
			return setupInitResult{AlreadyInitialized: true}, nil
		}
		return setupInitResult{}, fmt.Errorf("init repo: %w", err)
	}
	defer r.Close()

	return setupInitResult{RepoID: r.Config().ID}, nil
}

func printSetupSummary(
	out io.Writer,
	cfgPath string,
	plan *SetupPlan,
	awsAuthReport *AWSAuthReport,
	awsReport *AWSPrepareReport,
	initResult *setupInitResult,
) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Success.Bold(true).Render("Sentra setup complete"))
	fmt.Fprintln(out, ui.Subtle.Render("Configuration"))
	fmt.Fprintf(out, "  config:   %s\n", cfgPath)
	fmt.Fprintf(out, "  storage:  %s\n", setupBackendLabel(plan.Backend))
	fmt.Fprintf(out, "  bucket:   %s\n", plan.Config.Repo.S3.Bucket)
	if plan.Config.Repo.S3.Prefix != "" {
		fmt.Fprintf(out, "  prefix:   %s\n", plan.Config.Repo.S3.Prefix)
	}
	if plan.Config.Repo.S3.Region != "" {
		fmt.Fprintf(out, "  region:   %s\n", plan.Config.Repo.S3.Region)
	}
	if plan.Config.Repo.S3.Profile != "" {
		fmt.Fprintf(out, "  profile:  %s\n", plan.Config.Repo.S3.Profile)
	}
	if plan.Config.Repo.S3.EndpointURL != "" {
		fmt.Fprintf(out, "  endpoint: %s\n", plan.Config.Repo.S3.EndpointURL)
	}
	if awsAuthReport != nil {
		fmt.Fprintln(out, ui.Subtle.Render("AWS authentication"))
		if awsAuthReport.AWSCLIInstalled {
			fmt.Fprintf(out, "  aws auth: aws cli installed with %s\n", awsAuthReport.AWSCLIManager)
		}
		if awsAuthReport.SSOConfigureRan {
			fmt.Fprintln(out, "  aws auth: sso profile configured")
		}
		if awsAuthReport.SSOLoginRan {
			fmt.Fprintln(out, "  aws auth: sso login completed")
		} else if awsAuthReport.IdentityVerified {
			fmt.Fprintln(out, "  aws auth: identity verified")
		}
	}
	if awsReport != nil {
		fmt.Fprintln(out, ui.Subtle.Render("AWS bucket"))
		switch {
		case awsReport.BucketCreated:
			fmt.Fprintln(out, "  aws:      bucket created")
		case awsReport.BucketExisted:
			fmt.Fprintln(out, "  aws:      bucket verified")
		default:
			fmt.Fprintln(out, "  aws:      bucket checked")
		}
		if awsReport.PublicAccessBlocked {
			fmt.Fprintln(out, "  aws:      public access blocked")
		}
		if awsReport.DefaultEncryptionEnabled {
			fmt.Fprintln(out, "  aws:      default encryption enabled")
		}
	}
	if initResult != nil {
		fmt.Fprintln(out, ui.Subtle.Render("Repository"))
		if initResult.AlreadyInitialized {
			fmt.Fprintln(out, "  repo:     already initialized")
		} else {
			fmt.Fprintf(out, "  repo id:  %s\n", initResult.RepoID)
		}
	} else {
		fmt.Fprintln(out, ui.Subtle.Render("Next"))
		fmt.Fprintln(out, "  Run `sentra init` when you are ready to initialize the encrypted repository.")
	}
}

func printSetupApplyHeader(out io.Writer, cfgPath string, plan *SetupPlan) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Primary.Render("Applying Sentra setup"))
	fmt.Fprintf(out, "  config:  %s\n", cfgPath)
	fmt.Fprintf(out, "  storage: %s\n", setupBackendLabel(plan.Backend))
	if plan.InitRepo {
		fmt.Fprintln(out, "  repo:    initialize after config")
	} else {
		fmt.Fprintln(out, "  repo:    config only")
	}
	fmt.Fprintln(out)
}

func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

func setupBackendLabel(backend SetupBackend) string {
	switch backend {
	case SetupBackendAWS:
		return "AWS S3"
	case SetupBackendS3Compatible:
		return "S3-compatible or existing bucket"
	default:
		return string(backend)
	}
}

func setupAWSPreparedLabel(report *AWSPrepareReport) string {
	switch {
	case report == nil:
		return "AWS S3 checked"
	case report.BucketCreated:
		return "AWS S3 bucket created"
	case report.BucketExisted:
		return "AWS S3 bucket verified"
	default:
		return "AWS S3 bucket checked"
	}
}
