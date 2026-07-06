package setup

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// DefaultPlan builds the wizard's starting plan from the current config and
// the ambient AWS environment. Ported from the CLI wizard's
// defaultSetupPlan + applySetupSmartDefaults (internal/cli/setup_wizard.go:208-237);
// the os.Getenv / ~/.aws/config reads now go through probe so the transform is
// testable without touching the real environment.
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

func firstNonEmpty(probe EnvProbe, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(probe.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// NormalizeConfig trims the S3 fields so equal-but-padded values compare and
// serialize identically. Ported from internal/cli/setup_wizard.go:580-586.
func NormalizeConfig(cfg *config.Config) {
	cfg.Repo.S3.Bucket = strings.TrimSpace(cfg.Repo.S3.Bucket)
	cfg.Repo.S3.Prefix = strings.TrimSpace(cfg.Repo.S3.Prefix)
	cfg.Repo.S3.Region = strings.TrimSpace(cfg.Repo.S3.Region)
	cfg.Repo.S3.Profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	cfg.Repo.S3.EndpointURL = strings.TrimSpace(cfg.Repo.S3.EndpointURL)
}

// ApplyAWSConfigOnly turns a plan into a write-config-only plan: no AWS side
// effects, no repo init, no keyring save. Ported from
// internal/cli/setup.go:419-427.
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
// use_keyring flag, but only when the repo is being initialized. Ported from
// internal/cli/setup.go:429-433.
func ApplyPassphraseConfig(p *Plan) {
	if p.InitRepo {
		p.Config.Passphrase.UseKeyring = p.SavePassphrase
	}
}

// ResolveAWSAuthMethod picks the effective auth method for a plan, defaulting
// an empty method to existing credentials (when preparing AWS) or skip.
// Ported from internal/cli/setup.go:435-446.
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
// credential failure keeps the plan's chosen sign-in method. Ported from
// internal/cli/setup_wizard.go:158-172.
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

// ValidatePlan enforces the guards runSetup applied inline before writing
// anything: a bucket is required, its name must be valid, and AWS preparation
// is incompatible with a custom endpoint_url. Ported from
// internal/cli/setup.go:210-228.
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
