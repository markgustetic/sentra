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

// Backup-user provisioning names. Constants, not operator inputs: one
// fewer knob in the wizard, and doctor/docs can name them without a
// lookup. Only the credentials profile is chosen by the operator.
const (
	// BackupUserName is the IAM user the wizard creates for day-to-day
	// backups. Each bucket it may reach is granted by its own customer-managed
	// policy, BackupUserPolicyNameFor(bucket), holding
	// BuildIAMPolicy(bucket, prefix) — so buckets accumulate on the user.
	BackupUserName = "sentra-backup"
	// BackupUserPolicyName is the stem of the per-bucket managed policy
	// names (see BackupUserPolicyNameFor) and, on its own, the name of the
	// single inline policy that installs from before per-bucket policies
	// carried on BackupUserName. The provisioner deletes that inline policy
	// once a managed policy covering its grant is attached.
	BackupUserPolicyName = "sentra-s3-backup"
	// DefaultBackupUserProfile is where the minted key lands in
	// ~/.aws/credentials when the operator leaves the field blank. Never
	// "default": that section is the operator's, not Sentra's.
	DefaultBackupUserProfile = "sentra"
)

// BackupUserOptions carries the operator's one choice into the provisioner.
type BackupUserOptions struct {
	// Profile is the ~/.aws/credentials section that receives the key.
	Profile string
}

// BackupUserReport is the NON-SECRET outcome of provisioning. It carries
// the access key ID (an identifier, safe to display) and never the secret:
// the secret exists only inside the Effects implementation, between
// CreateAccessKey and the credentials-file write.
type BackupUserReport struct {
	UserName    string
	UserCreated bool // CreateUser succeeded
	UserExisted bool // EntityAlreadyExists → reused
	// PolicyName is this bucket's managed policy, BackupUserPolicyNameFor(bucket).
	PolicyName    string
	PolicyCreated bool // CreatePolicy succeeded; false when it already existed
	// PolicyUpdated: an existing policy received a new default version — a
	// second prefix merged in, or the canonical policy changed since it was
	// written. False on a rerun that changed nothing.
	PolicyUpdated  bool
	PolicyAttached bool // AttachUserPolicy succeeded (idempotent on rerun)
	// LegacyPolicyRemoved: the inline policy from before per-bucket managed
	// policies was found, fully covered by the managed one, and deleted.
	LegacyPolicyRemoved bool
	AccessKeyID         string
	Profile             string
	CredentialsPath     string
	// ProfileSwitched is set by the engine once the new identity verified
	// and sentra.yaml's profile now names it.
	ProfileSwitched bool
	// Warning is set by the engine on any failure; setup continues on the
	// signed-in session and the wizard shows this text.
	Warning string
}
