package setup

import (
	"errors"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestDefaultPlanBrowserLoginByDefault(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Backend != BackendAWS {
		t.Fatalf("backend: got %q, want aws", p.Backend)
	}
	if !p.PrepareAWS || p.AWSAuthMethod != AWSAuthLogin {
		t.Fatalf("expected browser login default, got prepare=%v method=%q", p.PrepareAWS, p.AWSAuthMethod)
	}
	if !p.CreateBucket || !p.BlockPublicAccess || !p.DefaultEncryption || !p.InitRepo || !p.SavePassphrase {
		t.Fatalf("safe defaults not all set: %+v", p)
	}
}

func TestDefaultPlanUsesEnvProfileAndRegion(t *testing.T) {
	probe := fakeProbe{env: map[string]string{
		"AWS_PROFILE": "work",
		"AWS_REGION":  "us-west-2",
	}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "work" {
		t.Fatalf("profile: got %q, want work", p.Config.Repo.S3.Profile)
	}
	if p.Config.Repo.S3.Region != "us-west-2" {
		t.Fatalf("region: got %q, want us-west-2", p.Config.Repo.S3.Region)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanFallsBackToConfigProfile(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}, profile: "sentra"}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "sentra" {
		t.Fatalf("profile: got %q, want sentra", p.Config.Repo.S3.Profile)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanUsesExistingForEnvCredentials(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}, envCredentials: true}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "" {
		t.Fatalf("profile: got %q, want blank", p.Config.Repo.S3.Profile)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanRegionFallbackKey(t *testing.T) {
	probe := fakeProbe{env: map[string]string{"AWS_DEFAULT_REGION": "eu-central-1"}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Region != "eu-central-1" {
		t.Fatalf("region: got %q, want eu-central-1", p.Config.Repo.S3.Region)
	}
}

func TestDefaultPlanInfersS3CompatibleFromEndpointWithEnvCredentials(t *testing.T) {
	// A config that already carries a custom endpoint_url AND ambient
	// credentials is a ready-to-use S3-compatible target (the canonical case:
	// `sentra local` points at MinIO with minioadmin creds exported into the
	// environment). It needs no AWS account provisioning, so DefaultPlan must
	// switch the backend and clear every AWS-side step.
	var cfg config.Config
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	probe := fakeProbe{env: map[string]string{}, envCredentials: true}

	p := DefaultPlan(cfg, probe)
	if p.Backend != BackendS3Compatible {
		t.Fatalf("backend: got %q, want s3-compatible", p.Backend)
	}
	if p.PrepareAWS {
		t.Fatal("PrepareAWS should be off for an S3-compatible endpoint")
	}
	if p.CreateBucket || p.BlockPublicAccess || p.DefaultEncryption {
		t.Fatalf("AWS provisioning steps should be off: %+v", p)
	}
	if p.AWSAuthMethod != AWSAuthSkip {
		t.Fatalf("auth method: got %q, want skip", p.AWSAuthMethod)
	}
	if p.Config.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Fatalf("endpoint_url should be preserved, got %q", p.Config.Repo.S3.EndpointURL)
	}
}

func TestDefaultPlanEndpointWithoutCredentialsKeepsAWSBackend(t *testing.T) {
	// A bare endpoint_url with NO ambient credentials stays on the AWS backend:
	// the CLI wizard seeds the endpoint field but the interactive backend select
	// still defaults to AWS S3. This guards the internal/cli oracle
	// (TestDefaultSetupPlanKeepsAWSBackendForEndpointConfig), which delegates to
	// DefaultPlan, from the endpoint→S3-compatible inference.
	var cfg config.Config
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	probe := fakeProbe{env: map[string]string{}}

	p := DefaultPlan(cfg, probe)
	if p.Backend != BackendAWS {
		t.Fatalf("backend: got %q, want aws", p.Backend)
	}
	if p.Config.Repo.S3.EndpointURL != "http://localhost:9000" {
		t.Fatalf("endpoint_url should be preserved, got %q", p.Config.Repo.S3.EndpointURL)
	}
}

func TestNormalizeConfigTrimsS3Fields(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "  b  "
	cfg.Repo.S3.Prefix = " sentra/ "
	cfg.Repo.S3.Region = " us-east-1 "
	cfg.Repo.S3.Profile = " p "
	cfg.Repo.S3.EndpointURL = " http://x "
	NormalizeConfig(&cfg)
	if cfg.Repo.S3.Bucket != "b" || cfg.Repo.S3.Prefix != "sentra/" ||
		cfg.Repo.S3.Region != "us-east-1" || cfg.Repo.S3.Profile != "p" ||
		cfg.Repo.S3.EndpointURL != "http://x" {
		t.Fatalf("normalize did not trim: %+v", cfg.Repo.S3)
	}
}

func TestApplyAWSConfigOnlyDisablesEffects(t *testing.T) {
	p := &Plan{PrepareAWS: true, InitRepo: true, CreateBucket: true, BlockPublicAccess: true, DefaultEncryption: true, AWSAuthMethod: AWSAuthLogin, SavePassphrase: true}
	ApplyAWSConfigOnly(p)
	if p.PrepareAWS || p.InitRepo || p.CreateBucket || p.BlockPublicAccess || p.DefaultEncryption || p.SavePassphrase {
		t.Fatalf("config-only should clear effect flags: %+v", p)
	}
	if p.AWSAuthMethod != AWSAuthSkip {
		t.Fatalf("auth method: got %q, want skip", p.AWSAuthMethod)
	}
}

func TestApplyPassphraseConfigMirrorsSaveToUseKeyring(t *testing.T) {
	p := &Plan{InitRepo: true, SavePassphrase: true}
	ApplyPassphraseConfig(p)
	if !p.Config.Passphrase.UseKeyring {
		t.Fatal("InitRepo+SavePassphrase should set use_keyring=true")
	}
	p2 := &Plan{InitRepo: false, SavePassphrase: true}
	ApplyPassphraseConfig(p2)
	if p2.Config.Passphrase.UseKeyring {
		t.Fatal("no InitRepo should leave use_keyring untouched (false)")
	}
}

func TestResolveAWSAuthMethod(t *testing.T) {
	if ResolveAWSAuthMethod(nil) != AWSAuthExisting {
		t.Fatal("nil plan should resolve to existing")
	}
	if got := ResolveAWSAuthMethod(&Plan{AWSAuthMethod: AWSAuthSSO}); got != AWSAuthSSO {
		t.Fatalf("explicit method: got %q, want sso", got)
	}
	if got := ResolveAWSAuthMethod(&Plan{PrepareAWS: true}); got != AWSAuthExisting {
		t.Fatalf("prepare with empty method: got %q, want existing", got)
	}
	if got := ResolveAWSAuthMethod(&Plan{}); got != AWSAuthSkip {
		t.Fatalf("no prepare, empty method: got %q, want skip", got)
	}
}

func TestDefaultAWSRepairChoice(t *testing.T) {
	// Non-credential failure → existing.
	prep := errors.New(`prepare AWS S3: head bucket "b": AccessDenied`)
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthLogin}, prep); got != AWSRepairExisting {
		t.Fatalf("bucket-prep failure: got %q, want existing", got)
	}
	// Missing-credential failure keeps the plan's method.
	cred := errors.New("failed to refresh cached credentials: no EC2 IMDS role found")
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthLogin}, cred); got != AWSRepairLogin {
		t.Fatalf("missing creds w/ login: got %q, want login", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthSSO}, nil); got != AWSRepairSSO {
		t.Fatalf("sso plan: got %q, want sso", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthExisting}, nil); got != AWSRepairExisting {
		t.Fatalf("existing plan: got %q, want existing", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthSkip}, nil); got != AWSRepairConfig {
		t.Fatalf("skip plan: got %q, want config", got)
	}
}

func TestValidatePlan(t *testing.T) {
	base := func() Plan {
		var p Plan
		p.Config.Repo.S3.Bucket = "good-bucket"
		return p
	}
	if err := ValidatePlan(base()); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}

	empty := Plan{}
	if err := ValidatePlan(empty); err == nil || !errIsBucketNotSet(err) {
		t.Fatalf("empty bucket: got %v, want bucket-not-set error", err)
	}

	bad := base()
	bad.Config.Repo.S3.Bucket = "Bad_Bucket"
	if err := ValidatePlan(bad); err == nil {
		t.Fatal("invalid bucket name should error")
	}

	ep := base()
	ep.PrepareAWS = true
	ep.Config.Repo.S3.EndpointURL = "http://localhost:9000"
	if err := ValidatePlan(ep); err == nil {
		t.Fatal("PrepareAWS with endpoint_url should error")
	}
}

func errIsBucketNotSet(err error) bool {
	return err != nil && err.Error() == "repo.s3.bucket not set - enter a bucket name"
}

// TestDefaultPlanS3CompatibleDoesNotInheritDiscoveredProfile pins the bug that
// broke `sentra local`: an S3-compatible endpoint must never inherit an AWS
// shared-config profile the user did not choose.
//
// aws-sdk-go-v2's resolveCredentialChain checks `sharedProfileSet` BEFORE
// `envConfig.Credentials.HasKeys()`, so passing a profile to
// WithSharedConfigProfile makes the SDK resolve that profile's credentials and
// ignore the static keys entirely. DefaultProfileFromConfig prefers a profile
// literally named "sentra", so a user with an SSO profile of that name had
// their MinIO run silently redirected to expired SSO credentials.
func TestDefaultPlanS3CompatibleDoesNotInheritDiscoveredProfile(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	probe := fakeProbe{env: map[string]string{}, profile: "sentra", envCredentials: true}

	p := DefaultPlan(cfg, probe)
	if p.Backend != BackendS3Compatible {
		t.Fatalf("backend: got %q, want s3-compatible", p.Backend)
	}
	if got := p.Config.Repo.S3.Profile; got != "" {
		t.Errorf("s3-compatible plan inherited AWS profile %q; it must stay empty so the "+
			"ambient endpoint credentials resolve", got)
	}
	if p.AWSAuthMethod != AWSAuthSkip {
		t.Errorf("auth method: got %q, want skip", p.AWSAuthMethod)
	}
}

// TestDefaultPlanS3CompatibleKeepsExplicitProfile: only *inference* is skipped.
// A profile the user actually wrote into sentra.yaml is theirs to keep — MinIO,
// R2 and Wasabi credentials all legitimately live in a named profile.
func TestDefaultPlanS3CompatibleKeepsExplicitProfile(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	cfg.Repo.S3.Profile = "wasabi"
	probe := fakeProbe{env: map[string]string{}, profile: "sentra", envCredentials: true}

	p := DefaultPlan(cfg, probe)
	if got := p.Config.Repo.S3.Profile; got != "wasabi" {
		t.Errorf("explicit profile: got %q, want wasabi", got)
	}
}

// TestDefaultPlanS3CompatibleIgnoresAWSProfileEnv: AWS_PROFILE must not leak in
// either. Unlike a programmatic profile it does not outrank static env keys in
// the SDK chain, but writing it into sentra.yaml would make the next run pass it
// to WithSharedConfigProfile, where it would.
func TestDefaultPlanS3CompatibleIgnoresAWSProfileEnv(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	probe := fakeProbe{env: map[string]string{"AWS_PROFILE": "work"}, envCredentials: true}

	p := DefaultPlan(cfg, probe)
	if got := p.Config.Repo.S3.Profile; got != "" {
		t.Errorf("s3-compatible plan inherited AWS_PROFILE %q, want empty", got)
	}
}
