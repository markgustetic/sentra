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
	"github.com/markgustetic/sentra/internal/setup"
)

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
		if err := setup.WriteIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix); err != nil {
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

	eng := setup.NewEngine(setupEffects(deps))
	var (
		awsAuthReport *AWSAuthReport
		awsReport     *AWSPrepareReport
	)
	for plan.PrepareAWS {
		// The engine runs AWS auth + bucket prep headlessly and classifies any
		// failure; the cli keeps the progress spinner and the huh repair loop.
		step := startSetupProgress(out, "Preparing AWS S3 bucket")
		auth, prep, perr := eng.PrepareAWS(cmd.Context(), &plan)
		if perr != nil {
			step.Fail()
			printSetupErrorDetail(out, perr, &plan.Config)
			updated, retry, repairErr := promptSetupAWSRepairIfNeeded(deps, plan, perr)
			if repairErr != nil {
				return repairErr
			}
			if !retry {
				return perr
			}
			if err := continueSetupAfterAWSRepair(cfgPath, out, &plan, updated); err != nil {
				return err
			}
			continue
		}
		printSetupAuthProgress(out, auth)
		step.Success(setupAWSPreparedLabel(&prep))
		a := auth
		p := prep
		awsAuthReport = &a
		awsReport = &p
		break
	}

	printSetupStep(out, "Writing "+cfgPath)
	if err := eng.WriteConfig(cfgPath, &plan); err != nil {
		return err
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
	if err := config.Write(draftPath, cfg); err != nil {
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

func applySetupAWSConfigOnly(plan *SetupPlan)    { setup.ApplyAWSConfigOnly(plan) }
func applySetupPassphraseConfig(plan *SetupPlan) { setup.ApplyPassphraseConfig(plan) }
func resolveSetupAWSAuthMethod(plan *SetupPlan) SetupAWSAuthMethod {
	return setup.ResolveAWSAuthMethod(plan)
}
