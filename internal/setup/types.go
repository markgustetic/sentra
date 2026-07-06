package setup

import "github.com/markgustetic/sentra/internal/config"

// Backend names the storage target chosen in the setup wizard.
type Backend string

const (
	BackendAWS          Backend = "aws"
	BackendS3Compatible Backend = "s3-compatible"
)

// AWSRepairChoice is the recovery path chosen after AWS auth or bucket
// preparation fails.
type AWSRepairChoice string

const (
	AWSRepairLogin    AWSRepairChoice = "login"
	AWSRepairSSO      AWSRepairChoice = "sso"
	AWSRepairExisting AWSRepairChoice = "existing"
	AWSRepairConfig   AWSRepairChoice = "config"
	AWSRepairCancel   AWSRepairChoice = "cancel"
)

// Plan is the complete set of actions the setup wizard selected. Both the
// CLI wizard (thin huh driver) and the TUI wizard build this and hand it to
// the engine; the engine never re-reads the terminal.
type Plan struct {
	Config            config.Config
	Backend           Backend
	PrepareAWS        bool
	AWSAuthMethod     AWSAuthMethod
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
	PrintIAMPolicy    bool
	SavePassphrase    bool
	InitRepo          bool
}

// AWSPrepareOptions controls the AWS-side setup work. Bucket existence is
// always checked; CreateBucket decides whether a missing bucket is created
// or reported as an error.
type AWSPrepareOptions struct {
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
}

// AWSPrepareReport summarizes the AWS setup work for the final output.
type AWSPrepareReport struct {
	BucketExisted            bool
	BucketCreated            bool
	PublicAccessBlocked      bool
	DefaultEncryptionEnabled bool
}

// AWSAuthReport summarizes the optional AWS CLI auth preflight.
type AWSAuthReport struct {
	IdentityVerified bool
	Method           AWSAuthMethod
	AWSCLIInstalled  bool
	AWSCLIManager    string
	LoginRan         bool
	SSOConfigured    bool
	SSOConfigureRan  bool
	SSOLoginRan      bool
}

// InitResult reports the outcome of initializing the encrypted repository.
type InitResult struct {
	RepoID                   string
	AlreadyInitialized       bool
	PassphraseSavedToKeyring bool
}
