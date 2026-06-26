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
}

func TestSetup_RefusesExistingConfigWithoutForce(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	original := []byte("repo:\n  s3:\n    bucket: existing\n")
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), original, 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}
	called := false
	deps := SetupDeps{
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
	if called {
		t.Fatal("wizard should not run when refusing an existing config")
	}
	body, readErr := os.ReadFile(filepath.Join(dir, "sentra.yaml"))
	if readErr != nil {
		t.Fatalf("read existing: %v", readErr)
	}
	if string(body) != string(original) {
		t.Errorf("existing config was modified:\n%s", body)
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
}

func TestSetup_AWSCLIAuthSkipsLoginWhenIdentityWorks(t *testing.T) {
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
				UseAWSCLIAuth:     true,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSIdentity: func(_ context.Context, profile string) error {
			checks++
			if profile != "sentra" {
				t.Errorf("profile: got %q", profile)
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

func TestSetup_AWSCLIAuthRunsSSOLoginWhenIdentityFails(t *testing.T) {
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
				UseAWSCLIAuth:     true,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSIdentity: func(context.Context, string) error {
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

func TestSetup_AWSCLIAuthConfiguresSSOWhenProfileMissing(t *testing.T) {
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
				UseAWSCLIAuth:     true,
				CreateBucket:      true,
				BlockPublicAccess: true,
				DefaultEncryption: true,
			}, nil
		},
		CheckAWSIdentity: func(context.Context, string) error {
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

func TestSetup_AWSCLIAuthConfigureFailureStopsBeforeWrite(t *testing.T) {
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
				UseAWSCLIAuth: true,
			}, nil
		},
		CheckAWSIdentity: func(context.Context, string) error {
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
	if prepareCalled {
		t.Fatal("PrepareAWS should not run after configure failure")
	}
	if _, statErr := os.Stat("sentra.yaml"); !os.IsNotExist(statErr) {
		t.Fatalf("sentra.yaml should not be written on configure failure, stat err=%v", statErr)
	}
}

func TestSetup_AWSCLIAuthLoginFailureStopsBeforeWrite(t *testing.T) {
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
				UseAWSCLIAuth: true,
			}, nil
		},
		CheckAWSIdentity: func(context.Context, string) error {
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
