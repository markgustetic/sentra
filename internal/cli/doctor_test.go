package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/repo"
)

// acceptsDiagAWSReport and acceptsAWSInspectReport only both compile if
// AWSInspectReport is a type alias for diag.AWSReport (not merely an
// identical struct) — a defined type would fail to satisfy the other's
// parameter without an explicit conversion.
func acceptsDiagAWSReport(diag.AWSReport)      {}
func acceptsAWSInspectReport(AWSInspectReport) {}

func TestAWSInspectReportIsDiagAlias(t *testing.T) {
	acceptsDiagAWSReport(AWSInspectReport{})
	acceptsAWSInspectReport(diag.AWSReport{})
}

func TestDoctor_AWSAndRepoHealthy(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.Region = "us-east-1"
	if err := config.Write(filepath.Join(dir, "sentra.yaml"), &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	r.Close()
	out := &bytes.Buffer{}
	deps := DoctorDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			return nil
		},
		InspectAWS: func(context.Context, *config.Config) (AWSInspectReport, error) {
			return AWSInspectReport{
				BucketAccessible:          true,
				PublicAccessReadable:      true,
				PublicAccessBlocked:       true,
				DefaultEncryptionReadable: true,
				DefaultEncryptionEnabled:  true,
			}, nil
		},
		NewStore:             func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		PassphraseWithConfig: func(*config.Config) ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:               out,
	}

	cmd := NewDoctor(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"AWS identity verified",
		"Bucket public access is blocked",
		"Bucket default encryption is enabled",
		"Repository check healthy",
		"Doctor: healthy",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor output missing %q:\n%s", want, out.String())
		}
	}
}

func TestDoctor_InvalidBucketFailsBeforeAWS(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "Bad_Bucket"
	if err := config.Write(filepath.Join(dir, "sentra.yaml"), &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out := &bytes.Buffer{}
	deps := DoctorDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			t.Fatal("AWS identity check should not run for invalid bucket")
			return nil
		},
		Stdout: out,
	}

	cmd := NewDoctor(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--skip-repo"})
	err := cmd.Execute()
	if !errors.Is(err, ErrDoctorFailed) {
		t.Fatalf("error: got %v, want ErrDoctorFailed", err)
	}
	if !strings.Contains(out.String(), "lowercase") {
		t.Fatalf("doctor output should explain bucket naming:\n%s", out.String())
	}
}

func TestDoctor_NotInitializedIsGuidance(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "local-bucket"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	if err := config.Write(filepath.Join(dir, "sentra.yaml"), &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	out := &bytes.Buffer{}
	deps := DoctorDeps{
		NewStore:             func(context.Context, *config.Config) (blobstore.Store, error) { return blobstore.NewMemory(), nil },
		PassphraseWithConfig: func(*config.Config) ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:               out,
	}

	cmd := NewDoctor(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "Repository is not initialized yet") {
		t.Fatalf("expected not initialized guidance:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "Doctor: healthy") {
		t.Fatalf("expected doctor to remain healthy for pre-init config:\n%s", out.String())
	}
}

func TestDoctor_RegisteredOnRoot(t *testing.T) {
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewDoctor(DoctorDeps{Stdout: io.Discard}))
	found := false
	for _, c := range root.Commands() {
		if c.Name() == "doctor" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("doctor command not registered on root")
	}
}

// TestDoctor_JSON: --json emits the summary schema; probe prose stays
// off stdout so the output parses.
func TestDoctor_JSON(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.Region = "us-east-1"
	if err := config.Write(filepath.Join(dir, "sentra.yaml"), &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	r.Close()
	out := &bytes.Buffer{}
	deps := DoctorDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error { return nil },
		InspectAWS: func(context.Context, *config.Config) (AWSInspectReport, error) {
			return AWSInspectReport{BucketAccessible: true}, nil
		},
		NewStore:             func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		PassphraseWithConfig: func(*config.Config) ([]byte, error) { return []byte("hunter2"), nil },
		Stdout:               out,
	}
	cmd := NewDoctor(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var report struct {
		Status string `json:"status"`
		Issues int    `json:"issues"`
	}
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if report.Status != "healthy" || report.Issues != 0 {
		t.Errorf("unexpected report: %+v", report)
	}
}
