package setup

import (
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestConstValuesMatchLegacyStrings(t *testing.T) {
	// A slice (not a map) because these values are not all distinct; a map
	// keyed on the string value would collide.
	cases := []struct {
		got  string
		want string
	}{
		{string(BackendAWS), "aws"},
		{string(BackendS3Compatible), "s3-compatible"},
		{string(AWSAuthLogin), "login"},
		{string(AWSAuthSSO), "sso"},
		{string(AWSAuthExisting), "existing"},
		{string(AWSAuthSkip), "skip"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("const value = %q, want %q", c.got, c.want)
		}
	}
}

func TestPlanCarriesConfigAndFlags(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	p := Plan{
		Config:            cfg,
		Backend:           BackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     AWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		PrintIAMPolicy:    false,
		SavePassphrase:    true,
		InitRepo:          true,
	}
	if p.Config.Repo.S3.Bucket != "b" {
		t.Fatalf("config not carried: %q", p.Config.Repo.S3.Bucket)
	}
	if !p.PrepareAWS || !p.SavePassphrase || !p.InitRepo {
		t.Fatalf("flags not set: %+v", p)
	}
}

func TestReportZeroValues(t *testing.T) {
	if (AWSPrepareReport{}).BucketCreated {
		t.Fatal("zero AWSPrepareReport.BucketCreated should be false")
	}
	if (AWSAuthReport{}).Method != "" {
		t.Fatal("zero AWSAuthReport.Method should be empty")
	}
	if (InitResult{}).AlreadyInitialized {
		t.Fatal("zero InitResult.AlreadyInitialized should be false")
	}
	if (AWSCLIInstallReport{}).AlreadyInstalled {
		t.Fatal("zero AWSCLIInstallReport.AlreadyInstalled should be false")
	}
}
