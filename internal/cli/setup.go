package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

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
	SSOConfigured    bool
	SSOConfigureRan  bool
	SSOLoginRan      bool
}

// SetupDeps wires the side-effecting pieces of `sentra setup`.
type SetupDeps struct {
	Prompt                SetupPrompt
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
		return fmt.Errorf("%s exists - refusing to overwrite (use --force)", cfgPath)
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
			printSetupStep(out, "Checking AWS identity and SSO session")
			report, err := runSetupAWSCLIAuth(cmd.Context(), deps, plan.Config.Repo.S3.Profile)
			if err != nil {
				return err
			}
			awsAuthReport = &report
			printSetupOK(out, "AWS identity ready")
		}

		prepareAWS := deps.PrepareAWS
		if prepareAWS == nil {
			prepareAWS = DefaultAWSPrepare
		}
		printSetupStep(out, "Preparing AWS S3 bucket")
		report, err := prepareAWS(cmd.Context(), &plan.Config, AWSPrepareOptions{
			CreateBucket:      plan.CreateBucket,
			BlockPublicAccess: plan.BlockPublicAccess,
			DefaultEncryption: plan.DefaultEncryption,
		})
		if err != nil {
			return fmt.Errorf("prepare AWS S3: %w", err)
		}
		awsReport = &report
		printSetupOK(out, setupAWSPreparedLabel(&report))
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

func runSetupAWSCLIAuth(ctx context.Context, deps SetupDeps, profile string) (AWSAuthReport, error) {
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

	if err := check(ctx, profile); err == nil {
		return AWSAuthReport{IdentityVerified: true}, nil
	}

	report := AWSAuthReport{}
	configured, err := checkConfigured(ctx, profile)
	if err != nil {
		return AWSAuthReport{}, fmt.Errorf("check aws sso profile: %w", err)
	}
	report.SSOConfigured = configured
	if !configured {
		if err := configure(ctx, profile); err != nil {
			return AWSAuthReport{}, fmt.Errorf("aws configure sso: %w", err)
		}
		report.SSOConfigured = true
		report.SSOConfigureRan = true
	}

	if err := login(ctx, profile); err != nil {
		return AWSAuthReport{}, fmt.Errorf("aws sso login: %w", err)
	}
	if err := check(ctx, profile); err != nil {
		return AWSAuthReport{}, fmt.Errorf("aws identity check after sso login: %w", err)
	}
	report.IdentityVerified = true
	report.SSOLoginRan = true
	return report, nil
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
