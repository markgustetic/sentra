package setup

// AWSAuthMethod names how setup makes AWS credentials available before it
// prepares the bucket. The string values are stable and match the CLI wizard's
// SetupAWSAuthMethod so config and reports read the same across both drivers.
type AWSAuthMethod string

const (
	AWSAuthLogin    AWSAuthMethod = "login"
	AWSAuthSSO      AWSAuthMethod = "sso"
	AWSAuthExisting AWSAuthMethod = "existing"
	AWSAuthSkip     AWSAuthMethod = "skip"
)

// AWSCLIInstallPlan is the package-manager command Sentra can run to install
// the AWS CLI for setup's SSO flow.
type AWSCLIInstallPlan struct {
	Manager string
	Command []string
}

// AWSCLIInstallConfirm asks whether Sentra may run the detected package
// manager command.
type AWSCLIInstallConfirm func(plan AWSCLIInstallPlan) (bool, error)

// AWSCLIInstallReport summarizes the AWS CLI preflight.
type AWSCLIInstallReport struct {
	AlreadyInstalled bool
	Installed        bool
	Manager          string
}
