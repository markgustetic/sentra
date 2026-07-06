package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// fakeEffects is a fully injectable Effects for engine unit tests. Unset
// func fields default to permissive no-ops so each test overrides only what
// it exercises.
type fakeEffects struct {
	ensureAWSCLI  func(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	awsLogin      func(ctx context.Context, profile, region string) error
	ssoConfigured func(ctx context.Context, profile string) (bool, error)
	configureSSO  func(ctx context.Context, profile string) error
	ssoLogin      func(ctx context.Context, profile string) error
	checkIdentity func(ctx context.Context, cfg *config.Config) error
	prepareAWS    func(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	newStore      func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	savePass      func(cfg *config.Config, passphrase []byte) error
}

func (f fakeEffects) EnsureAWSCLI(ctx context.Context, c AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if f.ensureAWSCLI != nil {
		return f.ensureAWSCLI(ctx, c)
	}
	return AWSCLIInstallReport{AlreadyInstalled: true}, nil
}
func (f fakeEffects) AWSLogin(ctx context.Context, p, r string) error {
	if f.awsLogin != nil {
		return f.awsLogin(ctx, p, r)
	}
	return nil
}
func (f fakeEffects) CheckAWSSSOConfigured(ctx context.Context, p string) (bool, error) {
	if f.ssoConfigured != nil {
		return f.ssoConfigured(ctx, p)
	}
	return true, nil
}
func (f fakeEffects) AWSConfigureSSO(ctx context.Context, p string) error {
	if f.configureSSO != nil {
		return f.configureSSO(ctx, p)
	}
	return nil
}
func (f fakeEffects) AWSSSOLogin(ctx context.Context, p string) error {
	if f.ssoLogin != nil {
		return f.ssoLogin(ctx, p)
	}
	return nil
}
func (f fakeEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	if f.checkIdentity != nil {
		return f.checkIdentity(ctx, cfg)
	}
	return nil
}
func (f fakeEffects) PrepareAWS(ctx context.Context, cfg *config.Config, o AWSPrepareOptions) (AWSPrepareReport, error) {
	if f.prepareAWS != nil {
		return f.prepareAWS(ctx, cfg, o)
	}
	return AWSPrepareReport{BucketExisted: true}, nil
}
func (f fakeEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if f.newStore != nil {
		return f.newStore(ctx, cfg)
	}
	return blobstore.NewMemory(), nil
}
func (f fakeEffects) SavePassphrase(cfg *config.Config, p []byte) error {
	if f.savePass != nil {
		return f.savePass(cfg, p)
	}
	return nil
}

func awsPlan() Plan {
	var p Plan
	p.Backend = BackendAWS
	p.PrepareAWS = true
	p.CreateBucket = true
	p.Config.Repo.S3.Bucket = "example-bucket"
	p.Config.Repo.S3.Region = "us-east-1"
	return p
}

// Existing-credentials happy path: identity verifies, prepare runs, both
// reports returned, no error. Mirrors runSetupAWSExistingAuth
// (internal/cli/setup_auth.go:117-124) + the loop success path
// (internal/cli/setup.go:288-291).
func TestEnginePrepareAWSExistingSuccess(t *testing.T) {
	var gotOpts AWSPrepareOptions
	eff := fakeEffects{
		prepareAWS: func(_ context.Context, cfg *config.Config, o AWSPrepareOptions) (AWSPrepareReport, error) {
			gotOpts = o
			return AWSPrepareReport{BucketCreated: true}, nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthExisting
	auth, prep, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !auth.IdentityVerified {
		t.Fatalf("auth.IdentityVerified = false, want true")
	}
	if auth.Method != AWSAuthExisting {
		t.Fatalf("auth.Method = %q, want %q", auth.Method, AWSAuthExisting)
	}
	if !prep.BucketCreated {
		t.Fatalf("prep.BucketCreated = false, want true")
	}
	if !gotOpts.CreateBucket {
		t.Fatalf("prepare opts.CreateBucket = false, want true")
	}
}

// Login sub-machine: identity fails first, login runs, identity then
// verifies. Mirrors runSetupAWSLoginAuth (internal/cli/setup_auth.go:30-59).
func TestEnginePrepareAWSLoginRunsWhenIdentityMissing(t *testing.T) {
	calls := 0
	loginRan := false
	eff := fakeEffects{
		checkIdentity: func(context.Context, *config.Config) error {
			calls++
			if calls == 1 {
				return errors.New("no valid credential sources")
			}
			return nil
		},
		awsLogin: func(context.Context, string, string) error {
			loginRan = true
			return nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	auth, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !loginRan {
		t.Fatal("AWSLogin did not run after identity failed")
	}
	if !auth.LoginRan || !auth.IdentityVerified {
		t.Fatalf("auth = %+v, want LoginRan && IdentityVerified", auth)
	}
}

// Failure classification: PrepareAWS returning a missing-credentials error
// must be wrapped via WrapAWSPrepareError (substring-detectable), and the
// engine must NOT prompt/repair — it returns the classified error so the
// caller (cli driver or TUI) owns the repair decision.
func TestEnginePrepareAWSClassifiesPrepareError(t *testing.T) {
	eff := fakeEffects{
		prepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{}, errors.New("operation error S3: HeadBucket, no valid credential sources")
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthExisting
	_, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err == nil {
		t.Fatal("PrepareAWS: got nil error, want classified prepare error")
	}
	if !IsAWSMissingCredentialsError(err) {
		t.Fatalf("PrepareAWS error not classified as missing-credentials: %v", err)
	}
}

// C5: Engine.PrepareAWS's login/SSO auth paths call eff.EnsureAWSCLI(ctx,
// nil) — resolving that nil confirm into a real callback is the Effects
// implementation's job (Part 4's cliSetupEffects.EnsureAWSCLI substitutes
// deps.ConfirmAWSCLIInstall, falling back to the huh prompt, before calling
// the real DefaultEnsureAWSCLI). This test stands in for that Part 4
// decorator with a fake Effects that resolves a nil confirm itself, and
// asserts the engine's call reaches it: the confirm path is reachable end to
// end through Engine.PrepareAWS, not just callable in isolation.
func TestEnginePrepareAWSEnsureAWSCLIConfirmPathReachable(t *testing.T) {
	confirmCalled := false
	resolvingEff := fakeEffects{
		ensureAWSCLI: func(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
			// Stand-in for cliSetupEffects.EnsureAWSCLI (Part 4): substitute a
			// real confirm when the engine passes nil, then invoke it.
			if confirm == nil {
				confirm = func(AWSCLIInstallPlan) (bool, error) {
					confirmCalled = true
					return true, nil
				}
			}
			ok, err := confirm(AWSCLIInstallPlan{Manager: "Homebrew", Command: []string{"brew", "install", "awscli"}})
			if err != nil {
				return AWSCLIInstallReport{}, err
			}
			if !ok {
				return AWSCLIInstallReport{}, errors.New("install declined")
			}
			return AWSCLIInstallReport{Installed: true, Manager: "Homebrew"}, nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	if _, _, err := NewEngine(resolvingEff).PrepareAWS(context.Background(), &p); err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !confirmCalled {
		t.Fatal("confirm callback was never invoked — EnsureAWSCLI confirm path is not reachable from Engine.PrepareAWS")
	}
}
