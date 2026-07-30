package setup

import (
	"context"
	"errors"
	"strings"
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
// reports returned, no error.
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
// verifies.
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
// caller (the TUI wizard) owns the repair decision.
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

// ssoPlan is an SSO-auth plan carrying the named profile the SSO sub-machine
// threads through CheckAWSSSOConfigured / AWSConfigureSSO / AWSSSOLogin.
func ssoPlan() Plan {
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthSSO
	p.Config.Repo.S3.Profile = "sentra"
	return p
}

// TestEnginePrepareAWSSSOSkipsFlowWhenIdentityVerifies: the SSO sub-machine
// must short-circuit the moment the SDK credential chain already works. If it
// did not, every `sentra setup` run on a healthy SSO profile would re-open a
// browser and re-run `aws sso login` for credentials it already has.
func TestEnginePrepareAWSSSOSkipsFlowWhenIdentityVerifies(t *testing.T) {
	checks := 0
	prepareCalled := false
	eff := fakeEffects{
		checkIdentity: func(context.Context, *config.Config) error { checks++; return nil },
		prepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			prepareCalled = true
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		ssoConfigured: func(context.Context, string) (bool, error) {
			t.Fatal("SSO profile check must not run when the identity check succeeds")
			return false, nil
		},
		configureSSO: func(context.Context, string) error {
			t.Fatal("aws configure sso must not run when the identity check succeeds")
			return nil
		},
		ssoLogin: func(context.Context, string) error {
			t.Fatal("aws sso login must not run when the identity check succeeds")
			return nil
		},
	}
	p := ssoPlan()
	auth, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if checks != 1 {
		t.Fatalf("identity checks = %d, want exactly 1 (no re-check after a skipped flow)", checks)
	}
	if !prepareCalled {
		t.Fatal("skipping the SSO flow must still run bucket work, not short-circuit PrepareAWS")
	}
	if !auth.IdentityVerified || auth.SSOLoginRan || auth.SSOConfigureRan {
		t.Fatalf("auth = %+v, want IdentityVerified only", auth)
	}
}

// TestEnginePrepareAWSSSOLoginRunsWhenProfileConfigured: identity fails but the
// profile already has a complete SSO block, so only `aws sso login` runs — an
// `aws configure sso` here would walk the operator through re-entering a
// start URL and region they already have.
func TestEnginePrepareAWSSSOLoginRunsWhenProfileConfigured(t *testing.T) {
	checks := 0
	configuredChecks := 0
	loginProfile := ""
	eff := fakeEffects{
		checkIdentity: func(context.Context, *config.Config) error {
			checks++
			if checks == 1 {
				return errors.New("expired sso token")
			}
			return nil
		},
		ssoConfigured: func(context.Context, string) (bool, error) { configuredChecks++; return true, nil },
		configureSSO: func(context.Context, string) error {
			t.Fatal("aws configure sso must not run for an already-configured profile")
			return nil
		},
		ssoLogin: func(_ context.Context, profile string) error { loginProfile = profile; return nil },
	}
	p := ssoPlan()
	auth, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if checks != 2 {
		t.Fatalf("identity checks = %d, want 2 (once before the flow, once to confirm it worked)", checks)
	}
	if configuredChecks != 1 {
		t.Fatalf("SSO profile checks = %d, want 1", configuredChecks)
	}
	if loginProfile != "sentra" {
		t.Fatalf("sso login profile = %q, want sentra", loginProfile)
	}
	if !auth.SSOLoginRan || auth.SSOConfigureRan || !auth.IdentityVerified {
		t.Fatalf("auth = %+v, want SSOLoginRan && IdentityVerified without SSOConfigureRan", auth)
	}
}

// TestEnginePrepareAWSSSOConfiguresProfileWhenMissing: with no SSO block on the
// profile, `aws configure sso` must run first and `aws sso login` second — the
// login alone would fail with an unhelpful AWS CLI error.
func TestEnginePrepareAWSSSOConfiguresProfileWhenMissing(t *testing.T) {
	checks := 0
	var order []string
	eff := fakeEffects{
		checkIdentity: func(context.Context, *config.Config) error {
			checks++
			if checks == 1 {
				return errors.New("profile not ready")
			}
			return nil
		},
		ssoConfigured: func(context.Context, string) (bool, error) { return false, nil },
		configureSSO: func(_ context.Context, profile string) error {
			order = append(order, "configure:"+profile)
			return nil
		},
		ssoLogin: func(_ context.Context, profile string) error {
			order = append(order, "login:"+profile)
			return nil
		},
	}
	p := ssoPlan()
	auth, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if len(order) != 2 || order[0] != "configure:sentra" || order[1] != "login:sentra" {
		t.Fatalf("SSO call order = %v, want [configure:sentra login:sentra]", order)
	}
	if !auth.SSOConfigureRan || !auth.SSOConfigured || !auth.SSOLoginRan || !auth.IdentityVerified {
		t.Fatalf("auth = %+v, want the full configure+login+verify report", auth)
	}
}

// TestEnginePrepareAWSSSOFlowFailureStopsBeforePrepare: neither SSO step may
// fall through to bucket work. PrepareAWS touching S3 after a failed sign-in
// would report a credential problem as a bucket problem, and the caller would
// go on to write a config for a repo it never reached.
func TestEnginePrepareAWSSSOFlowFailureStopsBeforePrepare(t *testing.T) {
	wantErr := errors.New("flow failed")
	tests := []struct {
		name string
		eff  func(*bool) fakeEffects
	}{
		{
			name: "configure fails",
			eff: func(prepared *bool) fakeEffects {
				return fakeEffects{
					checkIdentity: func(context.Context, *config.Config) error { return errors.New("identity missing") },
					ssoConfigured: func(context.Context, string) (bool, error) { return false, nil },
					configureSSO:  func(context.Context, string) error { return wantErr },
					prepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
						*prepared = true
						return AWSPrepareReport{}, nil
					},
				}
			},
		},
		{
			name: "login fails",
			eff: func(prepared *bool) fakeEffects {
				return fakeEffects{
					checkIdentity: func(context.Context, *config.Config) error { return errors.New("identity missing") },
					ssoConfigured: func(context.Context, string) (bool, error) { return true, nil },
					ssoLogin:      func(context.Context, string) error { return wantErr },
					prepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
						*prepared = true
						return AWSPrepareReport{}, nil
					},
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prepared := false
			p := ssoPlan()
			_, _, err := NewEngine(tc.eff(&prepared)).PrepareAWS(context.Background(), &p)
			if !errors.Is(err, wantErr) {
				t.Fatalf("PrepareAWS error = %v, want the SSO flow cause wrapped", err)
			}
			if prepared {
				t.Fatal("PrepareAWS ran bucket work after a failed SSO sign-in")
			}
			// The wrap must keep the operator's recovery paths attached.
			for _, want := range []string{"IAM Identity Center / SSO", "Existing credentials", "profile sentra"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("SSO flow error missing %q:\n%v", want, err)
				}
			}
		})
	}
}

// Engine.PrepareAWS's login/SSO auth paths must call eff.EnsureAWSCLI with a
// NIL confirm. That is what keeps the engine huh-free: a real confirm here
// would be a form fighting the running tea.Program for os.Stdin. The nil is
// also what makes DefaultEnsureAWSCLI return its actionable missing-binary
// error instead of attempting an install (TestDefaultEnsureAWSCLI_NilConfirm).
//
// The assertion is on the argument the engine passes, which is the only part a
// change to the engine can break. An earlier version of this test had the fake
// substitute its own confirm for the nil and then invoke it, asserting the
// confirm ran — it would have passed just as happily if PrepareAWS dropped the
// parameter altogether, since the fake supplied both sides.
func TestEnginePrepareAWSPassesNilAWSCLIConfirm(t *testing.T) {
	called := false
	var got AWSCLIInstallConfirm
	eff := fakeEffects{
		ensureAWSCLI: func(_ context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
			called, got = true, confirm
			return AWSCLIInstallReport{AlreadyInstalled: true}, nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	if _, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p); err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !called {
		t.Fatal("login auth must preflight the AWS CLI")
	}
	if got != nil {
		t.Fatal("the engine must pass a nil confirm — a real one would run a form inside the live tea.Program")
	}
}
