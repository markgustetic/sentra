package setup

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestReviewTextMentionsPassphraseSourceForInit(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "review-bucket"
	p := Plan{Config: cfg, InitRepo: true, SavePassphrase: true}

	got := ReviewText("sentra.yaml", p)
	for _, want := range []string{
		"Config: sentra.yaml",
		"Bucket: review-bucket",
		"Repository: initialize after config",
		"Passphrase: save to OS keyring after repo initialization",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review text missing %q:\n%s", want, got)
		}
	}

	p.SavePassphrase = false
	got = ReviewText("sentra.yaml", p)
	if !strings.Contains(got, "Passphrase: prompted or read from --passphrase-file or SENTRA_PASSPHRASE") {
		t.Fatalf("review text should mention prompt/file/env path:\n%s", got)
	}
}

// TestReviewTextNamesResolvedPassphraseSource: when a non-interactive source
// already supplied the passphrase, the wizard skips its entry stage, so review
// is the last screen before the repository is initialized under a secret the
// operator never typed. It must name the source — and only ever the source.
func TestReviewTextNamesResolvedPassphraseSource(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	const secret = "correcthorsebatterystaple"

	tests := []struct {
		name   string
		source string
		save   bool
		want   string
	}{
		{"env", config.PassphraseSourceEnv, false, "Passphrase: read from SENTRA_PASSPHRASE"},
		{"file", config.PassphraseSourceFile, false, "Passphrase: read from --passphrase-file"},
		{"file plus keyring", config.PassphraseSourceFile, true, "Passphrase: read from --passphrase-file, saved to OS keyring after repo initialization"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Plan{
				Config:           cfg,
				InitRepo:         true,
				SavePassphrase:   tc.save,
				PassphraseSource: tc.source,
			}
			got := ReviewText("sentra.yaml", p)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("review text missing %q:\n%s", tc.want, got)
			}
			if strings.Contains(got, secret) {
				t.Fatalf("review text must never contain the passphrase:\n%s", got)
			}
		})
	}
}

func TestReviewTextAssertsNoSecrets(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	got := ReviewText("sentra.yaml", Plan{Config: cfg})
	if !strings.Contains(got, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.") {
		t.Fatalf("review text must keep the no-secrets assertion:\n%s", got)
	}
}

func TestReviewTextEmptyBucketShowsDash(t *testing.T) {
	got := ReviewText("sentra.yaml", Plan{})
	if !strings.Contains(got, "Bucket: -") {
		t.Fatalf("empty bucket should render as dash:\n%s", got)
	}
}

func TestReviewTextAWSPrepareBlock(t *testing.T) {
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
	}
	got := ReviewText("sentra.yaml", p)
	for _, want := range []string{
		"AWS sign-in: browser login",
		"Create missing bucket: true",
		"Block public access: true",
		"Enable default encryption: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review text missing %q:\n%s", want, got)
		}
	}

	p.PrepareAWS = false
	if !strings.Contains(ReviewText("sentra.yaml", p), "AWS setup: skipped") {
		t.Fatalf("no-prepare plan should say AWS setup: skipped")
	}
}

func TestLabelMaps(t *testing.T) {
	if BackendLabel(BackendAWS) != "AWS S3" {
		t.Fatalf("BackendLabel(aws) = %q", BackendLabel(BackendAWS))
	}
	if BackendLabel(BackendS3Compatible) != "S3-compatible or existing bucket" {
		t.Fatalf("BackendLabel(s3c) = %q", BackendLabel(BackendS3Compatible))
	}
	if AWSAuthMethodLabel(AWSAuthLogin) != "browser login" {
		t.Fatalf("AWSAuthMethodLabel(login) = %q", AWSAuthMethodLabel(AWSAuthLogin))
	}
	if AWSAuthMethodLabel(AWSAuthSkip) != "config only" {
		t.Fatalf("AWSAuthMethodLabel(skip) = %q", AWSAuthMethodLabel(AWSAuthSkip))
	}
}
