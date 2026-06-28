package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func TestDefaultSetupPlanUsesBrowserLoginByDefault(t *testing.T) {
	plan := defaultSetupPlan(config.Config{})
	if plan.Backend != SetupBackendAWS {
		t.Fatalf("backend: got %q, want AWS", plan.Backend)
	}
	if !plan.PrepareAWS {
		t.Fatal("expected AWS setup to prepare AWS by default")
	}
	if plan.AWSAuthMethod != SetupAWSAuthLogin {
		t.Fatalf("auth method: got %q, want browser login", plan.AWSAuthMethod)
	}
	if !plan.CreateBucket || !plan.BlockPublicAccess || !plan.DefaultEncryption || !plan.InitRepo {
		t.Fatalf("setup actions = %+v, want safe AWS defaults enabled", plan)
	}
}

func TestDefaultSetupPlanUsesCompatibleBackendForEndpoint(t *testing.T) {
	cfg := config.Config{}
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"

	plan := defaultSetupPlan(cfg)
	if plan.Backend != SetupBackendS3Compatible {
		t.Fatalf("backend: got %q, want S3-compatible", plan.Backend)
	}
	if plan.PrepareAWS || plan.AWSAuthMethod != SetupAWSAuthSkip || plan.CreateBucket || plan.BlockPublicAccess || plan.DefaultEncryption {
		t.Fatalf("AWS automation should be disabled for endpoint_url plans: %+v", plan)
	}
}

func TestSetupProgressFallsBackToPlainOutput(t *testing.T) {
	var out bytes.Buffer
	err := runSetupProgress(&out, "Preparing AWS S3 bucket", "AWS S3 bucket verified", func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"... Preparing AWS S3 bucket",
		"ok AWS S3 bucket verified",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("non-terminal progress should not animate, got %q", got)
	}
}

func TestSetup_WritesConfigFromWizard(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "my-bucket"
			current.Repo.S3.Prefix = "sentra/"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "work"
			current.Repo.S3.EndpointURL = "https://s3.example.test"
			return SetupPlan{Config: current, Backend: SetupBackendS3Compatible}, nil
		},
		Stdout: out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cfg, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load(sentra.yaml): %v", err)
	}
	if cfg.Repo.S3.Bucket != "my-bucket" {
		t.Errorf("bucket: got %q", cfg.Repo.S3.Bucket)
	}
	if cfg.Repo.S3.Prefix != "sentra/" {
		t.Errorf("prefix: got %q", cfg.Repo.S3.Prefix)
	}
	if cfg.Repo.S3.Region != "us-east-1" {
		t.Errorf("region: got %q", cfg.Repo.S3.Region)
	}
	if cfg.Repo.S3.Profile != "work" {
		t.Errorf("profile: got %q", cfg.Repo.S3.Profile)
	}
	if cfg.Repo.S3.EndpointURL != "https://s3.example.test" {
		t.Errorf("endpoint: got %q", cfg.Repo.S3.EndpointURL)
	}
	if !strings.Contains(out.String(), "Sentra setup complete") {
		t.Errorf("expected setup summary, got %q", out.String())
	}
	for _, want := range []string{
		"Applying Sentra setup",
		"Writing sentra.yaml",
		"Config written",
		"Configuration",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetup_ExistingConfigCancelLeavesFileUntouched(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	original := []byte("repo:\n  s3:\n    bucket: existing\n")
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), original, 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	called := false
	confirmCalled := false
	deps := SetupDeps{
		ConfirmOverwrite: func(path string) (bool, error) {
			confirmCalled = true
			if path != "sentra.yaml" {
				t.Errorf("confirm path: got %q, want sentra.yaml", path)
			}
			return false, nil
		},
		Prompt: func(current config.Config) (SetupPlan, error) {
			called = true
			return SetupPlan{Config: current}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error on existing sentra.yaml, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected canceled error, got %v", err)
	}
	if !confirmCalled {
		t.Fatal("overwrite confirm should run when sentra.yaml exists")
	}
	if called {
		t.Fatal("wizard should not run when overwrite is canceled")
	}
	body, readErr := os.ReadFile(filepath.Join(dir, "sentra.yaml"))
	if readErr != nil {
		t.Fatalf("read existing: %v", readErr)
	}
	if string(body) != string(original) {
		t.Errorf("existing config was modified:\n%s", body)
	}
}

func TestSetup_ExistingConfigConfirmLoadsAndOverwrites(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	body := []byte(`repo:
  s3:
    bucket: old-bucket
    region: us-west-2
passphrase:
  use_keyring: true
`)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), body, 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	var seen config.Config
	var confirmCalled bool
	deps := SetupDeps{
		ConfirmOverwrite: func(path string) (bool, error) {
			confirmCalled = true
			if path != "sentra.yaml" {
				t.Errorf("confirm path: got %q, want sentra.yaml", path)
			}
			return true, nil
		},
		Prompt: func(current config.Config) (SetupPlan, error) {
			seen = current
			current.Repo.S3.Bucket = "new-bucket"
			return SetupPlan{Config: current, Backend: SetupBackendS3Compatible}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !confirmCalled {
		t.Fatal("overwrite confirm should run when sentra.yaml exists")
	}
	if seen.Repo.S3.Bucket != "old-bucket" {
		t.Errorf("wizard current bucket: got %q", seen.Repo.S3.Bucket)
	}
	if seen.Repo.S3.Region != "us-west-2" {
		t.Errorf("wizard current region: got %q", seen.Repo.S3.Region)
	}
	cfg, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load(sentra.yaml): %v", err)
	}
	if cfg.Repo.S3.Bucket != "new-bucket" {
		t.Errorf("bucket: got %q", cfg.Repo.S3.Bucket)
	}
	if !cfg.Passphrase.UseKeyring {
		t.Error("expected passphrase.use_keyring to be preserved")
	}
}

func TestSetup_ForceLoadsExistingConfig(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	body := []byte(`repo:
  s3:
    bucket: old-bucket
    region: us-west-2
passphrase:
  use_keyring: true
`)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), body, 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	var seen config.Config
	deps := SetupDeps{
		ConfirmOverwrite: func(string) (bool, error) {
			t.Fatal("--force should skip overwrite confirmation")
			return false, nil
		},
		Prompt: func(current config.Config) (SetupPlan, error) {
			seen = current
			current.Repo.S3.Bucket = "new-bucket"
			return SetupPlan{Config: current, Backend: SetupBackendS3Compatible}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if seen.Repo.S3.Bucket != "old-bucket" {
		t.Errorf("wizard current bucket: got %q", seen.Repo.S3.Bucket)
	}
	if seen.Repo.S3.Region != "us-west-2" {
		t.Errorf("wizard current region: got %q", seen.Repo.S3.Region)
	}
	cfg, err := config.Load(filepath.Join(dir, "sentra.yaml"))
	if err != nil {
		t.Fatalf("Load(sentra.yaml): %v", err)
	}
	if cfg.Repo.S3.Bucket != "new-bucket" {
		t.Errorf("bucket: got %q", cfg.Repo.S3.Bucket)
	}
	if !cfg.Passphrase.UseKeyring {
		t.Error("expected passphrase.use_keyring to be preserved")
	}
}

func TestSetup_RequiresBucket(t *testing.T) {
	chDir(t, t.TempDir())
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "   "
			return SetupPlan{Config: current}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected missing bucket error, got nil")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "bucket") {
		t.Errorf("error should mention bucket, got %v", err)
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written on invalid bucket, stat err=%v", statErr)
	}
}

func TestSetup_CustomConfigPath(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "custom-bucket"
			return SetupPlan{Config: current, Backend: SetupBackendS3Compatible}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "custom.yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "sentra.yaml")); !os.IsNotExist(err) {
		t.Fatalf("default sentra.yaml should not exist, stat err=%v", err)
	}
	cfg, err := config.Load(filepath.Join(dir, "custom.yaml"))
	if err != nil {
		t.Fatalf("Load(custom.yaml): %v", err)
	}
	if cfg.Repo.S3.Bucket != "custom-bucket" {
		t.Errorf("bucket: got %q", cfg.Repo.S3.Bucket)
	}
}

func TestSetup_PreparesAWSBeforeWritingConfig(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("no aws")
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{}, wantErr
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected AWS error, got %v", err)
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written when AWS prep fails, stat err=%v", statErr)
	}
}

func TestSetup_AWSPrepareMissingCredentialsAddsGuidance(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New(`head bucket "sentra-test": operation error S3: HeadBucket, get identity: get credentials: failed to refresh cached credentials, no EC2 IMDS role found, operation error ec2imds`)
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "sentra-test"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthExisting,
			}, nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{}, wantErr
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped credential error, got %v", err)
	}
	for _, want := range []string{
		"AWS credentials were not found",
		"AWS profile sentra",
		"aws configure --profile sentra",
		"choose Browser login",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written when AWS credentials fail, stat err=%v", statErr)
	}
}

func TestSetup_AWSPrepareMissingCredentialsAfterSSOAddsGuidance(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("failed to refresh cached credentials")
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "sentra-test"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthSSO,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{}, wantErr
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected wrapped credential error, got %v", err)
	}
	for _, want := range []string{
		"AWS credentials were not available",
		"after the SSO flow",
		"aws configure --profile sentra",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error missing %q:\n%v", want, err)
		}
	}
}

func TestSetup_PreparesAWSAndInitializesRepo(t *testing.T) {
	chDir(t, t.TempDir())
	store := blobstore.NewMemory()
	var gotAWS bool
	var gotOpts AWSPrepareOptions
	var captured *config.Config
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
				InitRepo:          true,
			}, nil
		},
		PrepareAWS: func(_ context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
			gotAWS = true
			gotOpts = opts
			if cfg.Repo.S3.Bucket != "aws-bucket" {
				t.Errorf("PrepareAWS bucket: got %q", cfg.Repo.S3.Bucket)
			}
			return AWSPrepareReport{
				BucketCreated:            true,
				PublicAccessBlocked:      true,
				DefaultEncryptionEnabled: true,
			}, nil
		},
		NewStore: func(_ context.Context, cfg *config.Config) (blobstore.Store, error) {
			captured = cfg
			return store, nil
		},
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !gotAWS {
		t.Fatal("PrepareAWS not called")
	}
	if !gotOpts.CreateBucket || !gotOpts.BlockPublicAccess || !gotOpts.DefaultEncryption {
		t.Fatalf("PrepareAWS opts = %+v", gotOpts)
	}
	if captured == nil {
		t.Fatal("NewStore not called")
	}
	if captured.Repo.S3.Profile != "sentra" {
		t.Errorf("NewStore config profile: got %q", captured.Repo.S3.Profile)
	}
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Open: %v", err)
	}
	r.Close()
	if !strings.Contains(out.String(), "repo id:") {
		t.Errorf("expected repo id in output, got %q", out.String())
	}
	for _, want := range []string{
		"Preparing AWS S3 bucket",
		"AWS S3 bucket created",
		"Initializing encrypted repository",
		"Repository initialized",
		"AWS bucket",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetup_AWSSSOAuthSkipsLoginWhenCredentialsWork(t *testing.T) {
	chDir(t, t.TempDir())
	var checks int
	var loginCalled bool
	var configureCalled bool
	var prepareCalled bool
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				AWSAuthMethod:     SetupAWSAuthSSO,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSSDKIdentity: func(_ context.Context, cfg *config.Config) error {
			checks++
			if cfg.Repo.S3.Profile != "sentra" {
				t.Errorf("profile: got %q", cfg.Repo.S3.Profile)
			}
			return nil
		},
		CheckAWSSSOConfigured: func(context.Context, string) (bool, error) {
			t.Fatal("SSO profile check should not run when identity check succeeds")
			return false, nil
		},
		AWSConfigureSSO: func(context.Context, string) error {
			configureCalled = true
			return nil
		},
		AWSSSOLogin: func(context.Context, string) error {
			loginCalled = true
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			prepareCalled = true
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if checks != 1 {
		t.Fatalf("identity checks: got %d, want 1", checks)
	}
	if loginCalled {
		t.Fatal("SSO login should not run when identity check succeeds")
	}
	if configureCalled {
		t.Fatal("SSO configure should not run when identity check succeeds")
	}
	if !prepareCalled {
		t.Fatal("PrepareAWS not called")
	}
}

func TestSetup_AWSSSOAuthInstallsMissingAWSCLI(t *testing.T) {
	chDir(t, t.TempDir())
	var ensureCalled bool
	var confirmCalled bool
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthSSO,
			}, nil
		},
		EnsureAWSCLI: func(_ context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
			ensureCalled = true
			ok, err := confirm(AWSCLIInstallPlan{
				Manager: "Homebrew",
				Command: []string{"brew", "install", "awscli"},
			})
			if err != nil {
				return AWSCLIInstallReport{}, err
			}
			if !ok {
				return AWSCLIInstallReport{}, errors.New("install declined")
			}
			return AWSCLIInstallReport{Installed: true, Manager: "Homebrew"}, nil
		},
		ConfirmAWSCLIInstall: func(plan AWSCLIInstallPlan) (bool, error) {
			confirmCalled = true
			if plan.Manager != "Homebrew" {
				t.Errorf("install manager: got %q, want Homebrew", plan.Manager)
			}
			return true, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		Stdout: out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !ensureCalled {
		t.Fatal("AWS CLI preflight should run")
	}
	if !confirmCalled {
		t.Fatal("AWS CLI install confirm should run")
	}
	for _, want := range []string{"AWS CLI installed", "aws cli installed with Homebrew"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetup_AWSSSOAuthInstallFailureStopsBeforeWrite(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("install failed")
	prepareCalled := false
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthSSO,
			}, nil
		},
		EnsureAWSCLI: func(context.Context, AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
			return AWSCLIInstallReport{}, wantErr
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			t.Fatal("identity check should not run after AWS CLI install failure")
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			prepareCalled = true
			return AWSPrepareReport{}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected install error, got %v", err)
	}
	if prepareCalled {
		t.Fatal("PrepareAWS should not run after AWS CLI install failure")
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written on install failure, stat err=%v", statErr)
	}
}

func TestSetup_AWSBrowserLoginRunsWhenIdentityMissing(t *testing.T) {
	chDir(t, t.TempDir())
	var checks int
	var loginProfile string
	var loginRegion string
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				AWSAuthMethod:     SetupAWSAuthLogin,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			checks++
			if checks == 1 {
				return errors.New("credentials missing")
			}
			return nil
		},
		AWSLogin: func(_ context.Context, profile string, region string) error {
			loginProfile = profile
			loginRegion = region
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		Stdout: out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if checks != 2 {
		t.Fatalf("identity checks: got %d, want 2", checks)
	}
	if loginProfile != "sentra" {
		t.Fatalf("login profile: got %q, want sentra", loginProfile)
	}
	if loginRegion != "us-east-1" {
		t.Fatalf("login region: got %q, want us-east-1", loginRegion)
	}
	for _, want := range []string{"AWS browser login complete", "browser login completed"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("setup output missing %q:\n%s", want, out.String())
		}
	}
}

func TestSetup_AWSSSOAuthRunsSSOLoginWhenCredentialsFail(t *testing.T) {
	chDir(t, t.TempDir())
	var checks int
	var configureCalled bool
	var loginProfile string
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				AWSAuthMethod:     SetupAWSAuthSSO,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			checks++
			if checks == 1 {
				return errors.New("expired sso token")
			}
			return nil
		},
		CheckAWSSSOConfigured: func(context.Context, string) (bool, error) {
			return true, nil
		},
		AWSConfigureSSO: func(context.Context, string) error {
			configureCalled = true
			return nil
		},
		AWSSSOLogin: func(_ context.Context, profile string) error {
			loginProfile = profile
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		Stdout: out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if checks != 2 {
		t.Fatalf("identity checks: got %d, want 2", checks)
	}
	if loginProfile != "sentra" {
		t.Fatalf("login profile: got %q, want sentra", loginProfile)
	}
	if configureCalled {
		t.Fatal("SSO configure should not run when profile is already configured")
	}
	if !strings.Contains(out.String(), "sso login completed") {
		t.Errorf("expected sso login summary, got %q", out.String())
	}
}

func TestSetup_AWSSSOAuthConfiguresSSOWhenProfileMissing(t *testing.T) {
	chDir(t, t.TempDir())
	var checks int
	var configuredChecks int
	var configureProfile string
	var loginProfile string
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:            current,
				Backend:           SetupBackendAWS,
				PrepareAWS:        true,
				AWSAuthMethod:     SetupAWSAuthSSO,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			checks++
			if checks == 1 {
				return errors.New("profile not ready")
			}
			return nil
		},
		CheckAWSSSOConfigured: func(context.Context, string) (bool, error) {
			configuredChecks++
			return false, nil
		},
		AWSConfigureSSO: func(_ context.Context, profile string) error {
			configureProfile = profile
			return nil
		},
		AWSSSOLogin: func(_ context.Context, profile string) error {
			loginProfile = profile
			return nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		Stdout: out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if checks != 2 {
		t.Fatalf("identity checks: got %d, want 2", checks)
	}
	if configuredChecks != 1 {
		t.Fatalf("configured checks: got %d, want 1", configuredChecks)
	}
	if configureProfile != "sentra" {
		t.Fatalf("configure profile: got %q, want sentra", configureProfile)
	}
	if loginProfile != "sentra" {
		t.Fatalf("login profile: got %q, want sentra", loginProfile)
	}
	got := out.String()
	if !strings.Contains(got, "sso profile configured") {
		t.Errorf("expected configure summary, got %q", got)
	}
	if !strings.Contains(got, "sso login completed") {
		t.Errorf("expected login summary, got %q", got)
	}
}

func TestSetup_AWSSSOAuthConfigureFailureStopsBeforeWrite(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("configure failed")
	prepareCalled := false
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthSSO,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			return errors.New("identity missing")
		},
		CheckAWSSSOConfigured: func(context.Context, string) (bool, error) {
			return false, nil
		},
		AWSConfigureSSO: func(context.Context, string) error {
			return wantErr
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			prepareCalled = true
			return AWSPrepareReport{}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected configure error, got %v", err)
	}
	if !strings.Contains(err.Error(), "IAM Identity Center / SSO") || !strings.Contains(err.Error(), "Existing credentials") {
		t.Fatalf("configure error missing SSO guidance: %v", err)
	}
	if prepareCalled {
		t.Fatal("PrepareAWS should not run after configure failure")
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written on configure failure, stat err=%v", statErr)
	}
}

func TestSetup_AWSSSOAuthLoginFailureStopsBeforeWrite(t *testing.T) {
	chDir(t, t.TempDir())
	wantErr := errors.New("login failed")
	prepareCalled := false
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "aws-bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.Profile = "sentra"
			return SetupPlan{
				Config:        current,
				Backend:       SetupBackendAWS,
				PrepareAWS:    true,
				AWSAuthMethod: SetupAWSAuthSSO,
			}, nil
		},
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			return errors.New("identity missing")
		},
		CheckAWSSSOConfigured: func(context.Context, string) (bool, error) {
			return true, nil
		},
		AWSSSOLogin: func(context.Context, string) error {
			return wantErr
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			prepareCalled = true
			return AWSPrepareReport{}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected login error, got %v", err)
	}
	if !strings.Contains(err.Error(), "IAM Identity Center / SSO") || !strings.Contains(err.Error(), "Existing credentials") {
		t.Fatalf("login error missing SSO guidance: %v", err)
	}
	if prepareCalled {
		t.Fatal("PrepareAWS should not run after login failure")
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written on login failure, stat err=%v", statErr)
	}
}

func TestSetup_AWSPrepareRejectsEndpointURL(t *testing.T) {
	chDir(t, t.TempDir())
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "bucket"
			current.Repo.S3.Region = "us-east-1"
			current.Repo.S3.EndpointURL = "http://localhost:9000"
			return SetupPlan{Config: current, Backend: SetupBackendAWS, PrepareAWS: true}, nil
		},
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			t.Fatal("PrepareAWS should not be called for endpoint_url")
			return AWSPrepareReport{}, nil
		},
		Stdout: io.Discard,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected endpoint_url guard error, got nil")
	}
	if !strings.Contains(err.Error(), "endpoint_url") {
		t.Errorf("error should mention endpoint_url, got %v", err)
	}
}

func TestSetup_InitAlreadyInitializedIsSummaryNotError(t *testing.T) {
	chDir(t, t.TempDir())
	store := blobstore.NewMemory()
	first, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	first.Close()
	out := &bytes.Buffer{}
	deps := SetupDeps{
		Prompt: func(current config.Config) (SetupPlan, error) {
			current.Repo.S3.Bucket = "bucket"
			return SetupPlan{Config: current, Backend: SetupBackendS3Compatible, InitRepo: true}, nil
		},
		NewStore:   func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:     out,
	}

	cmd := NewSetup(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), "already initialized") {
		t.Errorf("expected already initialized summary, got %q", out.String())
	}
}

func TestSetup_RegisteredOnRoot(t *testing.T) {
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewSetup(SetupDeps{Stdout: io.Discard}))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "setup" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("setup command not registered on root")
	}
}
