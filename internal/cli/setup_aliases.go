package cli

import (
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// The setup engine lives in internal/setup so both the CLI wizard (this
// package) and the TUI wizard can drive identical logic. These identity
// aliases keep the historical cli names — and the behavior-preservation
// oracle in setup_test.go — meaning exactly the same types and values.

type (
	// SetupBackend names the storage target chosen in the setup wizard.
	SetupBackend = setup.Backend
	// SetupAWSAuthMethod names how setup makes AWS credentials available.
	SetupAWSAuthMethod = setup.AWSAuthMethod
	// SetupPlan is the complete set of actions the setup wizard selected.
	SetupPlan = setup.Plan
	// AWSCLIInstallPlan is the package-manager command to install the AWS CLI.
	AWSCLIInstallPlan = setup.AWSCLIInstallPlan
	// AWSCLIInstallReport summarizes the AWS CLI preflight.
	AWSCLIInstallReport = setup.AWSCLIInstallReport
	// AWSPrepareOptions controls the AWS-side setup work.
	AWSPrepareOptions = setup.AWSPrepareOptions
	// AWSPrepareReport summarizes the AWS setup work for the final CLI output.
	AWSPrepareReport = setup.AWSPrepareReport
	// AWSAuthReport summarizes the optional AWS CLI auth preflight.
	AWSAuthReport = setup.AWSAuthReport
)

const (
	SetupBackendAWS          = setup.BackendAWS
	SetupBackendS3Compatible = setup.BackendS3Compatible

	SetupAWSAuthLogin    = setup.AWSAuthLogin
	SetupAWSAuthSSO      = setup.AWSAuthSSO
	SetupAWSAuthExisting = setup.AWSAuthExisting
	SetupAWSAuthSkip     = setup.AWSAuthSkip
)

// The remaining callback types stay cli-only: they are the huh-facing
// injection seam. Production leaves them nil and falls back to the Huh* forms;
// tests inject deterministic callbacks.

// SetupPrompt collects an updated setup plan from the operator.
type SetupPrompt func(current config.Config) (SetupPlan, error)

// SetupOverwriteConfirm asks whether an existing config file may be overwritten.
type SetupOverwriteConfirm func(path string) (bool, error)

// SetupReviewConfirm asks whether the final non-secret setup plan should apply.
type SetupReviewConfirm func(cfgPath string, plan SetupPlan) (bool, error)

// SetupAWSAuthRepairPrompt asks what to do after AWS auth or bucket prep fails.
type SetupAWSAuthRepairPrompt func(plan SetupPlan, cause error) (SetupPlan, bool, error)

// AWSCLIInstallConfirm asks whether Sentra may run the detected installer.
type AWSCLIInstallConfirm = setup.AWSCLIInstallConfirm
