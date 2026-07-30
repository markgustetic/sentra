package setup

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// DefaultPlan builds the wizard's starting plan from the current config and
// the ambient AWS environment. Ported from the CLI wizard's defaultSetupPlan
// + applySetupSmartDefaults; the os.Getenv / ~/.aws/config reads now go through
// probe so the transform is testable without touching the real environment.
func DefaultPlan(cfg config.Config, probe EnvProbe) Plan {
	p := Plan{
		Config:            cfg,
		Backend:           BackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     AWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		SavePassphrase:    true,
		InitRepo:          true,
	}
	applySmartDefaults(&p, probe)
	return p
}

func applySmartDefaults(p *Plan, probe EnvProbe) {
	if p.Config.Repo.S3.Region == "" {
		p.Config.Repo.S3.Region = firstNonEmpty(probe, "AWS_REGION", "AWS_DEFAULT_REGION")
	}

	// Settle the backend BEFORE inferring an AWS profile. An S3-compatible
	// endpoint authenticates with whatever credentials the environment already
	// carries, and a profile it never asked for is not inert: blobstore.NewS3
	// passes a non-empty Profile to awsconfig.WithSharedConfigProfile, and
	// aws-sdk-go-v2's resolveCredentialChain tests `sharedProfileSet` BEFORE
	// `envConfig.Credentials.HasKeys()`. The profile's credentials therefore win
	// and the endpoint's are never consulted. Since DefaultProfileFromConfig
	// prefers a profile literally named "sentra", a user with an SSO profile of
	// that name had `sentra local` silently redirected at their AWS account.
	//
	// Only inference is skipped. A profile the user wrote into their config
	// survives — MinIO, R2 and Wasabi credentials all legitimately live in one.
	inferS3CompatibleFromEndpoint(p, probe)
	if p.Backend == BackendS3Compatible {
		return
	}

	if p.Config.Repo.S3.Profile == "" {
		p.Config.Repo.S3.Profile = firstNonEmpty(probe, "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	}
	if p.Config.Repo.S3.Profile == "" {
		p.Config.Repo.S3.Profile = probe.DefaultProfileFromConfig()
	}
	if probe.HasEnvCredentials() || p.Config.Repo.S3.Profile != "" {
		p.AWSAuthMethod = AWSAuthExisting
	}
}

// inferS3CompatibleFromEndpoint switches the plan to the S3-compatible backend
// when the config already carries a custom endpoint_url AND ambient credentials
// are present. A config with an endpoint_url is inherently S3-compatible and
// needs none of the AWS account provisioning; the canonical trigger is
// `sentra local`, which points at MinIO and exports minioadmin credentials into
// the environment before the wizard builds its plan.
//
// The credential guard is deliberate: a bare endpoint_url with no credentials
// is a target the operator has named but not made reachable, so the plan stays
// on the AWS backend and lets the wizard's own backend stage decide. Inferring
// S3-compatible from the endpoint alone would clear every AWS provisioning flag
// for a config that still needs them.
func inferS3CompatibleFromEndpoint(p *Plan, probe EnvProbe) {
	if strings.TrimSpace(p.Config.Repo.S3.EndpointURL) == "" || !probe.HasEnvCredentials() {
		return
	}
	p.Backend = BackendS3Compatible
	p.PrepareAWS = false
	p.CreateBucket = false
	p.BlockPublicAccess = false
	p.DefaultEncryption = false
	p.AWSAuthMethod = AWSAuthSkip
}

// ApplyBackendChoice settles a plan once the operator picks a backend by hand
// in the TUI wizard — its only production caller. DefaultPlan's inference
// path (inferS3CompatibleFromEndpoint, above) does not call it: it upholds
// the same invariant its own way, by settling the backend before inferring a
// profile and inferring none for S3-compatible targets. Both mechanisms must
// keep agreeing on what they drop — only an *inferred* profile, never one
// the operator wrote into their own config.
//
// Two invariants:
//
//   - AWS forbids endpoint_url.
//   - An S3-compatible target must not carry an AWS shared-config profile the
//     operator never chose. blobstore.NewS3 hands a non-empty Profile to
//     awsconfig.WithSharedConfigProfile, and aws-sdk-go-v2's
//     resolveCredentialChain tests `sharedProfileSet` BEFORE
//     `envConfig.Credentials.HasKeys()` — so the profile's credentials win and
//     the endpoint's are never consulted. DefaultProfileFromConfig prefers a
//     profile literally named "sentra", which is how `sentra local` ended up
//     authenticating against a real AWS account.
//
// configuredProfile is the profile from the operator's own sentra.yaml, empty
// when they never set one. Only an inferred profile is dropped: R2 and Wasabi
// credentials legitimately live in a named profile.
//
// It deliberately does NOT touch the provisioning flags (PrepareAWS,
// CreateBucket, …). Those are settled later, once the operator has seen the
// actions stage.
func ApplyBackendChoice(p *Plan, backend Backend, configuredProfile string) {
	p.Backend = backend
	switch backend {
	case BackendAWS:
		p.Config.Repo.S3.EndpointURL = ""
	case BackendS3Compatible:
		if strings.TrimSpace(configuredProfile) == "" {
			p.Config.Repo.S3.Profile = ""
		}
	}
}

func firstNonEmpty(probe EnvProbe, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(probe.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// NormalizeConfig trims the S3 fields so equal-but-padded values compare and
// serialize identically.
func NormalizeConfig(cfg *config.Config) {
	cfg.Repo.S3.Bucket = strings.TrimSpace(cfg.Repo.S3.Bucket)
	cfg.Repo.S3.Prefix = strings.TrimSpace(cfg.Repo.S3.Prefix)
	cfg.Repo.S3.Region = strings.TrimSpace(cfg.Repo.S3.Region)
	cfg.Repo.S3.Profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	cfg.Repo.S3.EndpointURL = strings.TrimSpace(cfg.Repo.S3.EndpointURL)
}

// ApplyAWSConfigOnly turns a plan into a write-config-only plan: no AWS side
// effects, no repo init, no keyring save.
func ApplyAWSConfigOnly(p *Plan) {
	p.PrepareAWS = false
	p.InitRepo = false
	p.CreateBucket = false
	p.BlockPublicAccess = false
	p.DefaultEncryption = false
	p.AWSAuthMethod = AWSAuthSkip
	p.SavePassphrase = false
}

// ApplyPassphraseConfig mirrors the SavePassphrase decision into the persisted
// use_keyring flag, but only when the repo is being initialized.
func ApplyPassphraseConfig(p *Plan) {
	if p.InitRepo {
		p.Config.Passphrase.UseKeyring = p.SavePassphrase
	}
}

// ResolveAWSAuthMethod picks the effective auth method for a plan, defaulting
// an empty method to existing credentials (when preparing AWS) or skip.
func ResolveAWSAuthMethod(p *Plan) AWSAuthMethod {
	if p == nil {
		return AWSAuthExisting
	}
	if p.AWSAuthMethod != "" {
		return p.AWSAuthMethod
	}
	if p.PrepareAWS {
		return AWSAuthExisting
	}
	return AWSAuthSkip
}

// DefaultAWSRepairChoice picks the pre-selected recovery option after AWS auth
// or bucket preparation fails. A non-credential failure (e.g. AccessDenied on
// an existing bucket) suggests switching to existing credentials; a missing-
// credential failure keeps the plan's chosen sign-in method.
func DefaultAWSRepairChoice(p Plan, cause error) AWSRepairChoice {
	if cause != nil && !IsAWSMissingCredentialsError(cause) {
		return AWSRepairExisting
	}
	switch p.AWSAuthMethod {
	case AWSAuthSSO:
		return AWSRepairSSO
	case AWSAuthExisting:
		return AWSRepairExisting
	case AWSAuthSkip:
		return AWSRepairConfig
	default:
		return AWSRepairLogin
	}
}

// ValidatePlan enforces the pre-write guards the deleted CLI wizard applied
// inline: a bucket is required, its name must be valid, and AWS preparation is
// incompatible with a custom endpoint_url.
//
// NOTE: it currently has no production caller. The live equivalents are the
// TUI wizard's own inline checks in commitDetails. Either route the wizard
// through here or drop it — a validator nothing calls will drift from the
// checks that actually run.
func ValidatePlan(p Plan) error {
	if strings.TrimSpace(p.Config.Repo.S3.Bucket) == "" {
		return fmt.Errorf("repo.s3.bucket not set - enter a bucket name")
	}
	if err := diag.ValidateBucketName(p.Config.Repo.S3.Bucket); err != nil {
		return err
	}
	if p.PrepareAWS && p.Config.Repo.S3.EndpointURL != "" {
		return fmt.Errorf("AWS setup does not support endpoint_url - choose S3-compatible/manual setup for MinIO or LocalStack")
	}
	return nil
}

// ValidateBucketName re-exports diag's bucket-name validation so both
// ValidatePlan and the TUI wizard's inline field validation share one rule
// set without the TUI importing internal/diag directly.
func ValidateBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
