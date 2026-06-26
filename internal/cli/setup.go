package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"github.com/charmbracelet/huh"
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

	var (
		awsAuthReport *AWSAuthReport
		awsReport     *AWSPrepareReport
	)
	if plan.PrepareAWS {
		if plan.UseAWSCLIAuth {
			report, err := runSetupAWSCLIAuth(cmd.Context(), deps, plan.Config.Repo.S3.Profile)
			if err != nil {
				return err
			}
			awsAuthReport = &report
		}

		prepareAWS := deps.PrepareAWS
		if prepareAWS == nil {
			prepareAWS = DefaultAWSPrepare
		}
		report, err := prepareAWS(cmd.Context(), &plan.Config, AWSPrepareOptions{
			CreateBucket:      plan.CreateBucket,
			BlockPublicAccess: plan.BlockPublicAccess,
			DefaultEncryption: plan.DefaultEncryption,
		})
		if err != nil {
			return fmt.Errorf("prepare AWS S3: %w", err)
		}
		awsReport = &report
	}

	if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(&plan.Config)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}

	var initResult *setupInitResult
	if plan.InitRepo {
		result, err := runSetupInit(cmd.Context(), deps, &plan.Config)
		if err != nil {
			return err
		}
		initResult = &result
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
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
	fmt.Fprintln(out, ui.Primary.Render("Sentra setup complete"))
	fmt.Fprintf(out, "  config:   %s\n", cfgPath)
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
		if initResult.AlreadyInitialized {
			fmt.Fprintln(out, "  repo:     already initialized")
		} else {
			fmt.Fprintf(out, "  repo id:  %s\n", initResult.RepoID)
		}
	} else {
		fmt.Fprintln(out, ui.Subtle.Render("Run `sentra init` when you are ready to initialize the encrypted repository."))
	}
}

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

// DefaultAWSCheckIdentity verifies that the selected AWS profile can
// resolve a caller identity through the AWS CLI. It captures output so
// a failed preflight can be retried through SSO login without printing
// scary intermediate errors.
func DefaultAWSCheckIdentity(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sts", "get-caller-identity"}, profile, false)
}

// DefaultAWSSSOConfigured checks whether the selected profile already has
// AWS CLI SSO settings. It supports both the newer sso_session form and
// the older inline sso_start_url form.
func DefaultAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	for _, key := range []string{"sso_session", "sso_start_url"} {
		out, err := runAWSCLIOutput(ctx, []string{"configure", "get", key}, profile)
		if err == nil && strings.TrimSpace(out) != "" {
			return true, nil
		}
	}
	return false, nil
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the
// AWS CLI. Sentra does not read or store the configured values.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile, true)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the
// AWS CLI. Sentra never receives or stores the resulting credentials.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sso", "login"}, profile, true)
}

func runAWSCLI(ctx context.Context, args []string, profile string, interactive bool) error {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

func runAWSCLIOutput(ctx context.Context, args []string, profile string) (string, error) {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return "", fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return string(out), nil
}

func appendAWSProfile(args []string, profile string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return args
	}
	return append(args, "--profile", profile)
}

// DefaultAWSPrepare performs the deterministic AWS S3 setup work chosen
// in the wizard. It intentionally does not create or manage IAM users.
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	if cfg.Repo.S3.Region == "" {
		return AWSPrepareReport{}, fmt.Errorf("repo.s3.region is required for AWS setup")
	}

	store, err := blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:  cfg.Repo.S3.Bucket,
		Prefix:  cfg.Repo.S3.Prefix,
		Region:  cfg.Repo.S3.Region,
		Profile: cfg.Repo.S3.Profile,
	})
	if err != nil {
		return AWSPrepareReport{}, err
	}
	client := store.Client()
	bucket := cfg.Repo.S3.Bucket
	report := AWSPrepareReport{}

	if err := headBucket(ctx, client, bucket); err == nil {
		report.BucketExisted = true
	} else if isS3BucketMissing(err) {
		if !opts.CreateBucket {
			return AWSPrepareReport{}, fmt.Errorf("bucket %q does not exist", bucket)
		}
		if err := createBucket(ctx, client, bucket, cfg.Repo.S3.Region); err != nil {
			return AWSPrepareReport{}, err
		}
		report.BucketCreated = true
	} else {
		return AWSPrepareReport{}, err
	}

	if opts.BlockPublicAccess {
		if err := blockBucketPublicAccess(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.PublicAccessBlocked = true
	}
	if opts.DefaultEncryption {
		if err := enableBucketDefaultEncryption(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.DefaultEncryptionEnabled = true
	}
	return report, nil
}

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q: %w", bucket, err)
	}
	return nil
}

func createBucket(ctx context.Context, client *s3.Client, bucket, region string) error {
	input := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	_, err := client.CreateBucket(ctx, input)
	if err == nil {
		return nil
	}
	if isBucketAlreadyOwned(err) {
		return nil
	}
	return fmt.Errorf("create bucket %q: %w", bucket, err)
}

func blockBucketPublicAccess(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("block public access for bucket %q: %w", bucket, err)
	}
	return nil
}

func enableBucketDefaultEncryption(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("enable default encryption for bucket %q: %w", bucket, err)
	}
	return nil
}

func isS3BucketMissing(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchBucket", "404":
		return true
	default:
		return false
	}
}

func isBucketAlreadyOwned(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou"
}

func normalizeSetupConfig(cfg *config.Config) {
	cfg.Repo.S3.Bucket = strings.TrimSpace(cfg.Repo.S3.Bucket)
	cfg.Repo.S3.Prefix = strings.TrimSpace(cfg.Repo.S3.Prefix)
	cfg.Repo.S3.Region = strings.TrimSpace(cfg.Repo.S3.Region)
	cfg.Repo.S3.Profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	cfg.Repo.S3.EndpointURL = strings.TrimSpace(cfg.Repo.S3.EndpointURL)
}
