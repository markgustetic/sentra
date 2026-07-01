package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// SetupBackend names the storage target chosen in the setup wizard.
type SetupBackend string

const (
	SetupBackendAWS          SetupBackend = "aws"
	SetupBackendS3Compatible SetupBackend = "s3-compatible"
)

// SetupAWSAuthMethod names how setup should make AWS credentials available
// before it prepares the bucket.
type SetupAWSAuthMethod string

const (
	SetupAWSAuthLogin    SetupAWSAuthMethod = "login"
	SetupAWSAuthSSO      SetupAWSAuthMethod = "sso"
	SetupAWSAuthExisting SetupAWSAuthMethod = "existing"
	SetupAWSAuthSkip     SetupAWSAuthMethod = "skip"
)

// SetupPlan is the complete set of actions the setup wizard selected.
// Tests return this directly; production builds it from the huh TUI.
type SetupPlan struct {
	Config            config.Config
	Backend           SetupBackend
	PrepareAWS        bool
	AWSAuthMethod     SetupAWSAuthMethod
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
	PrintIAMPolicy    bool
	SavePassphrase    bool
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

// SetupReviewConfirm asks whether the final non-secret setup plan should be
// applied.
type SetupReviewConfirm func(cfgPath string, plan SetupPlan) (bool, error)

// SetupAWSAuthRepairPrompt asks what to do after AWS authentication or bucket
// preparation fails. It returns an updated plan and whether setup should
// continue with that plan.
type SetupAWSAuthRepairPrompt func(plan SetupPlan, cause error) (SetupPlan, bool, error)

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
	Method           SetupAWSAuthMethod
	AWSCLIInstalled  bool
	AWSCLIManager    string
	LoginRan         bool
	SSOConfigured    bool
	SSOConfigureRan  bool
	SSOLoginRan      bool
}

// SetupDeps wires the side-effecting pieces of `sentra setup`.
type SetupDeps struct {
	Prompt                SetupPrompt
	ConfirmOverwrite      SetupOverwriteConfirm
	ConfirmReview         SetupReviewConfirm
	PromptAWSAuthRepair   SetupAWSAuthRepairPrompt
	EnsureAWSCLI          func(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	ConfirmAWSCLIInstall  AWSCLIInstallConfirm
	CheckAWSSDKIdentity   func(ctx context.Context, cfg *config.Config) error
	AWSLogin              func(ctx context.Context, profile string, region string) error
	CheckAWSSSOConfigured func(ctx context.Context, profile string) (bool, error)
	AWSConfigureSSO       func(ctx context.Context, profile string) error
	AWSSSOLogin           func(ctx context.Context, profile string) error
	PrepareAWS            func(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	NewStore              func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase            func() ([]byte, error)
	SavePassphrase        func(cfg *config.Config, passphrase []byte) error
	Stdout                io.Writer
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
			"The wizard can sign in with AWS CLI browser login, run AWS SSO " +
			"profile setup when selected, verify credentials, prepare an AWS S3 " +
			"bucket, write sentra.yaml, and initialize the encrypted repository " +
			"in one flow.",
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
	cmd.AddCommand(newSetupIAMPolicy(deps.Stdout))
	return cmd
}

func runSetup(cmd *cobra.Command, deps SetupDeps, cfgPath string, force bool) error {
	cmd.SilenceUsage = true
	if cfgPath == "" {
		cfgPath = configFileName
	}
	out := cmdStdout(cmd, deps.Stdout)

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

	cfg, err := loadSetupConfigForWizard(cfgPath, yamlExists, out)
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
	if err := validateSetupBucketName(plan.Config.Repo.S3.Bucket); err != nil {
		return err
	}
	if plan.PrintIAMPolicy {
		if err := writeSetupIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix); err != nil {
			return err
		}
		return nil
	}
	authMethod := resolveSetupAWSAuthMethod(&plan)
	if plan.Backend == SetupBackendAWS && authMethod == SetupAWSAuthSkip {
		applySetupAWSConfigOnly(&plan)
	}
	applySetupPassphraseConfig(&plan)
	if plan.PrepareAWS && plan.Config.Repo.S3.EndpointURL != "" {
		return fmt.Errorf("AWS setup does not support endpoint_url - choose S3-compatible/manual setup for MinIO or LocalStack")
	}
	if err := confirmSetupReviewIfNeeded(deps, cfgPath, &plan); err != nil {
		return err
	}

	if err := writeSetupDraft(cfgPath, &plan.Config); err != nil {
		return err
	}

	printSetupApplyHeader(out, cfgPath, &plan)

	var (
		awsAuthReport *AWSAuthReport
		awsReport     *AWSPrepareReport
	)
	for plan.PrepareAWS {
		authMethod = resolveSetupAWSAuthMethod(&plan)
		report, err := runSetupAWSAuth(cmd.Context(), deps, authMethod, &plan.Config, out)
		if err != nil {
			printSetupErrorDetail(out, err, &plan.Config)
			updated, retry, repairErr := promptSetupAWSRepairIfNeeded(deps, plan, err)
			if repairErr != nil {
				return repairErr
			}
			if !retry {
				return err
			}
			if err := continueSetupAfterAWSRepair(cfgPath, out, &plan, updated); err != nil {
				return err
			}
			continue
		}
		awsAuthReport = &report

		prepareAWS := deps.PrepareAWS
		if prepareAWS == nil {
			prepareAWS = DefaultAWSPrepare
		}
		awsStep := startSetupProgress(out, "Preparing AWS S3 bucket")
		prepareReport, err := prepareAWS(cmd.Context(), &plan.Config, AWSPrepareOptions{
			CreateBucket:      plan.CreateBucket,
			BlockPublicAccess: plan.BlockPublicAccess,
			DefaultEncryption: plan.DefaultEncryption,
		})
		if err != nil {
			awsStep.Fail()
			wrappedErr := wrapAWSPrepareError(&plan.Config, authMethod, err)
			printSetupErrorDetail(out, wrappedErr, &plan.Config)
			updated, retry, repairErr := promptSetupAWSRepairIfNeeded(deps, plan, wrappedErr)
			if repairErr != nil {
				return repairErr
			}
			if !retry {
				return wrappedErr
			}
			if err := continueSetupAfterAWSRepair(cfgPath, out, &plan, updated); err != nil {
				return err
			}
			continue
		}
		awsReport = &prepareReport
		awsStep.Success(setupAWSPreparedLabel(&prepareReport))
		break
	}

	printSetupStep(out, "Writing "+cfgPath)
	if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(&plan.Config)), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	printSetupOK(out, "Config written")

	var initResult *setupInitResult
	if plan.InitRepo {
		printSetupStep(out, "Initializing encrypted repository")
		result, err := runSetupInit(cmd.Context(), deps, &plan.Config, plan.SavePassphrase)
		if err != nil {
			return err
		}
		initResult = &result
		if result.AlreadyInitialized {
			printSetupOK(out, "Repository already initialized")
		} else {
			printSetupOK(out, "Repository initialized")
		}
		if result.PassphraseSavedToKeyring {
			printSetupOK(out, "Passphrase saved to OS keyring")
		}
	}

	printSetupSummary(out, cfgPath, &plan, awsAuthReport, awsReport, initResult)
	removeSetupDraft(cfgPath)
	return nil
}

func loadSetupConfigForWizard(cfgPath string, yamlExists bool, out io.Writer) (*config.Config, error) {
	if !yamlExists {
		draftPath := setupDraftPath(cfgPath)
		if _, err := os.Stat(draftPath); err == nil {
			cfg, loadErr := config.Load(draftPath)
			if loadErr != nil {
				return nil, fmt.Errorf("load setup draft %s: %w", draftPath, loadErr)
			}
			printSetupOK(out, "Loaded previous setup draft")
			return cfg, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat setup draft %s: %w", draftPath, err)
		}
	}
	return config.Load(cfgPath)
}

func confirmSetupReviewIfNeeded(deps SetupDeps, cfgPath string, plan *SetupPlan) error {
	confirm := deps.ConfirmReview
	if confirm == nil && deps.Prompt == nil {
		confirm = HuhSetupReviewConfirm
	}
	if confirm == nil {
		return nil
	}
	ok, err := confirm(cfgPath, *plan)
	if err != nil {
		return fmt.Errorf("confirm setup plan: %w", err)
	}
	if !ok {
		return fmt.Errorf("setup canceled")
	}
	return nil
}

func promptSetupAWSRepairIfNeeded(deps SetupDeps, plan SetupPlan, cause error) (SetupPlan, bool, error) {
	prompt := deps.PromptAWSAuthRepair
	if prompt == nil && deps.Prompt == nil {
		prompt = HuhSetupAWSAuthRepairPrompt
	}
	if prompt == nil {
		return plan, false, nil
	}
	updated, retry, err := prompt(plan, cause)
	if err != nil {
		return plan, false, fmt.Errorf("repair AWS setup: %w", err)
	}
	if !retry {
		return plan, false, nil
	}
	normalizeSetupConfig(&updated.Config)
	if updated.Backend == SetupBackendAWS && resolveSetupAWSAuthMethod(&updated) == SetupAWSAuthSkip {
		applySetupAWSConfigOnly(&updated)
	}
	if updated.Config.Repo.S3.Bucket == "" {
		return updated, false, fmt.Errorf("repo.s3.bucket not set - enter a bucket name")
	}
	if err := validateSetupBucketName(updated.Config.Repo.S3.Bucket); err != nil {
		return updated, false, err
	}
	return updated, true, nil
}

func continueSetupAfterAWSRepair(cfgPath string, out io.Writer, plan *SetupPlan, updated SetupPlan) error {
	*plan = updated
	normalizeSetupConfig(&plan.Config)
	applySetupPassphraseConfig(plan)
	if err := writeSetupDraft(cfgPath, &plan.Config); err != nil {
		return err
	}
	printSetupRepairContinue(out, plan)
	return nil
}

func writeSetupDraft(cfgPath string, cfg *config.Config) error {
	draftPath := setupDraftPath(cfgPath)
	if err := os.WriteFile(draftPath, []byte(renderConfigYAML(cfg)), 0o600); err != nil {
		return fmt.Errorf("write setup draft %s: %w", draftPath, err)
	}
	return nil
}

func removeSetupDraft(cfgPath string) {
	if err := os.Remove(setupDraftPath(cfgPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		// Best effort cleanup; leaving a non-secret draft is less harmful than
		// turning a successful setup into a failure.
		return
	}
}

func setupDraftPath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	base := filepath.Base(cfgPath)
	return filepath.Join(dir, "."+base+".setup-draft")
}

func applySetupAWSConfigOnly(plan *SetupPlan) {
	plan.PrepareAWS = false
	plan.InitRepo = false
	plan.CreateBucket = false
	plan.BlockPublicAccess = false
	plan.DefaultEncryption = false
	plan.AWSAuthMethod = SetupAWSAuthSkip
	plan.SavePassphrase = false
}

func applySetupPassphraseConfig(plan *SetupPlan) {
	if plan.InitRepo {
		plan.Config.Passphrase.UseKeyring = plan.SavePassphrase
	}
}

func resolveSetupAWSAuthMethod(plan *SetupPlan) SetupAWSAuthMethod {
	if plan == nil {
		return SetupAWSAuthExisting
	}
	if plan.AWSAuthMethod != "" {
		return plan.AWSAuthMethod
	}
	if plan.PrepareAWS {
		return SetupAWSAuthExisting
	}
	return SetupAWSAuthSkip
}
