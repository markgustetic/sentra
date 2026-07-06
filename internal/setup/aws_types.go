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
