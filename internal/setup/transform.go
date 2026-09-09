package setup

import (
	"errors"
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

// ValidateBucketName re-exports diag's bucket-name validation so the TUI
// wizard's inline field validation (commitDetails) shares one rule set with
// doctor's probes without internal/tui importing internal/diag directly. It is
// the only live bucket-name gate in the product.
func ValidateBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}

// ErrBackupUserProfileDefault is returned when the operator names the
// "default" credentials profile for the backup user. That section is the
// operator's everyday identity; Sentra must never write into it.
var ErrBackupUserProfileDefault = errors.New("backup user profile must not be \"default\"")

// validateBackupUserProfileName checks a ~/.aws/credentials section name.
// The rules are the INI file's, not AWS's: the name becomes a "[name]"
// header line, so brackets and whitespace would corrupt the file the
// operator's other tools read. Unexported on purpose: it is the
// session-blind half of ValidateBackupUserProfileFor, and a caller outside
// the package that reached it directly would skip the one rule that needs
// the plan — the collision with the sign-in profile.
func validateBackupUserProfileName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("backup user profile is required")
	}
	if name == "default" {
		return ErrBackupUserProfileDefault
	}
	if strings.ContainsAny(name, "[] \t\r\n") {
		return fmt.Errorf("backup user profile %q must not contain brackets or whitespace", name)
	}
	return nil
}

// ErrBackupUserProfileIsSession is returned when the backup user's
// credentials profile is the very profile setup signs in with
// (Repo.S3.Profile). aws-sdk-go-v2 resolves one profile's static keys
// BEFORE its SSO or assume-role settings (resolveCredsFromProfile tests
// Credentials.HasKeys() first), so a key written under that name would
// make every tool using the profile authenticate as the least-privilege
// backup user, and the operator's own identity would be unreachable under
// the name they know it by. The text carries that reason: on a machine
// with a [profile sentra] this is the refusal the default path meets, and
// a bare "must differ" reads as a rule to work around rather than a trap
// to avoid. It is a whole sentence on its own — errors.Is callers may
// print it bare — and the wrap adds only the name.
var ErrBackupUserProfileIsSession = errors.New("backup user profile is the profile setup signs in with; a static key under it would shadow that sign-in for every tool using the profile")

// ValidateBackupUserProfileFor is the package's only exported profile-name
// gate: the section-name rules plus the one that needs the plan — the name
// must not be sessionProfile. The wizard, the drivers and the engine all
// call this form, so the refusal is the same wherever the operator meets
// it and no caller can validate a name without the session rule.
func ValidateBackupUserProfileFor(name, sessionProfile string) error {
	name = strings.TrimSpace(name)
	if err := validateBackupUserProfileName(name); err != nil {
		return err
	}
	if name == strings.TrimSpace(sessionProfile) {
		return fmt.Errorf("%q: %w", name, ErrBackupUserProfileIsSession)
	}
	return nil
}

// DefaultBackupUserProfileFor is DefaultBackupUserProfile made safe for the
// plan at hand: when the session profile is itself called "sentra" — this
// is common, since DefaultPlan prefers a [profile sentra] from ~/.aws/config
// — the default steps aside to "sentra-backup" rather than failing the
// happy path with the collision it exists to prevent. One step is enough:
// the session profile is a single name, so it cannot equal both. Pure, so
// the review line and the engine can agree on the name before anything
// runs.
func DefaultBackupUserProfileFor(sessionProfile string) string {
	if strings.TrimSpace(sessionProfile) == DefaultBackupUserProfile {
		return DefaultBackupUserProfile + "-backup"
	}
	return DefaultBackupUserProfile
}

// ResolveBackupUserProfile is the single reading of Plan.BackupUserProfile:
// the operator's explicit name, else the plan-derived default. Every
// consumer goes through it so "blank" cannot resolve to two different
// sections on the review screen and in the credentials file.
func ResolveBackupUserProfile(p *Plan) string {
	if profile := strings.TrimSpace(p.BackupUserProfile); profile != "" {
		return profile
	}
	return DefaultBackupUserProfileFor(p.Config.Repo.S3.Profile)
}

// ShouldProvisionBackupUser is the single gate for the IAM provisioning
// stage. Existing-credentials and skip never provision: the operator already
// chose a durable identity, and an IAM mutation they did not ask for is the
// worst surprise a setup wizard can spring.
func ShouldProvisionBackupUser(p *Plan) bool {
	if p == nil || !p.ProvisionBackupUser || !p.PrepareAWS {
		return false
	}
	m := ResolveAWSAuthMethod(p)
	return m == AWSAuthLogin || m == AWSAuthSSO
}
