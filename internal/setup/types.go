package setup

import "github.com/markgustetic/sentra/internal/config"

// Backend names the storage target chosen in the setup wizard.
type Backend string

const (
	BackendAWS          Backend = "aws"
	BackendS3Compatible Backend = "s3-compatible"
)

// Plan is the complete set of actions the setup wizard selected. The TUI
// wizard builds this and hands it to the engine; the engine never re-reads
// the terminal.
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

	// PassphraseSource names where a non-interactive passphrase came from
	// (config.PassphraseSourceFile / config.PassphraseSourceEnv), empty when the
	// operator typed it into the wizard. It is a LABEL for the review screen —
	// never the passphrase, never a file's contents, and never a path, so it is
	// safe everywhere a plan is rendered. The engine ignores it: the secret
	// itself is passed to InitRepo as an argument.
	PassphraseSource string

	// ProvisionBackupUser asks PrepareAWS to create the scoped IAM user and
	// switch the config to its static-key profile after a login/SSO sign-in.
	// Ignored for existing-credentials and skip (see ShouldProvisionBackupUser).
	ProvisionBackupUser bool
	// BackupUserProfile is the ~/.aws/credentials section for the minted
	// key; empty means DefaultBackupUserProfile.
	BackupUserProfile string

	// ProvisionedBackupUserProfile names the ~/.aws/credentials section an
	// earlier attempt in the same wizard run already filled: the backup user
	// exists, its key is saved there, and the engine verified the identity
	// before a later step failed. The TUI wizard sets it when it adopts that
	// switch on the way to its failure screen, so the retry's review can say
	// the user is already there instead of "skipped" — a word that invites
	// the operator to go back and ask for it again. A label only: the engine
	// ignores it, and an explicit ProvisionBackupUser still wins.
	ProvisionedBackupUserProfile string
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
	// BackupUser is nil when provisioning was not attempted (gate false);
	// otherwise the non-secret outcome, Warning set on failure.
	BackupUser *BackupUserReport
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
