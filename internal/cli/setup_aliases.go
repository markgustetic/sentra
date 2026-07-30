package cli

import (
	"github.com/markgustetic/sentra/internal/setup"
)

// The setup engine lives in internal/setup, and since `sentra setup` became a
// launcher for the TUI wizard the engine has exactly one driver. These identity
// aliases keep the historical cli names pointing at the same types and values,
// so anything still written against them cannot silently diverge from the
// engine's own definitions.

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
