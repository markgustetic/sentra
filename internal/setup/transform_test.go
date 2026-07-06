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
