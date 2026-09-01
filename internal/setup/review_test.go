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

func TestReviewTextBackupUserLine(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	base := Plan{Config: cfg, Backend: BackendAWS, PrepareAWS: true}

	tests := []struct {
		name    string
		method  AWSAuthMethod
		on      bool
		profile string
		want    string // substring that must appear
		absent  string // substring that must not appear ("" to skip)
	}{
		{"login on", AWSAuthLogin, true, "sentra", "Backup user: create sentra-backup, keys → ~/.aws/credentials [sentra]", ""},
		{"login on custom profile", AWSAuthLogin, true, "backups", "~/.aws/credentials [backups]", ""},
		{"login on blank profile defaults", AWSAuthLogin, true, "", "[sentra]", ""},
		{"sso off", AWSAuthSSO, false, "", "Backup user: skipped", ""},
		{"existing", AWSAuthExisting, true, "sentra", "AWS sign-in", "Backup user"},
		{"skip", AWSAuthSkip, true, "sentra", "AWS sign-in", "Backup user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.AWSAuthMethod = tc.method
			p.ProvisionBackupUser = tc.on
			p.BackupUserProfile = tc.profile
			got := ReviewText("sentra.yaml", p)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("review text missing %q:\n%s", tc.want, got)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("review text must not mention %q:\n%s", tc.absent, got)
			}
		})
	}
}

// The dangerous condition: nothing secret-shaped may ever reach the review
// screen, whatever the plan holds. An access key ID prefix or a 40-character
// secret in this output would mean a field leaked.
func TestReviewTextBackupUserNeverRendersSecretShapes(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	p := Plan{Config: cfg, Backend: BackendAWS, PrepareAWS: true, AWSAuthMethod: AWSAuthLogin,
		ProvisionBackupUser: true, BackupUserProfile: "sentra"}
	got := ReviewText("sentra.yaml", p)
	if strings.Contains(got, "AKIA") {
		t.Fatalf("review text contains an access key ID shape:\n%s", got)
	}
	isBase64Rune := func(r rune) bool {
		return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/'
	}
	for _, word := range strings.Fields(got) {
		if len(word) == 40 && strings.IndexFunc(word, func(r rune) bool { return !isBase64Rune(r) }) == -1 {
			t.Fatalf("review text contains a 40-char base64 token %q:\n%s", word, got)
		}
	}
	if !strings.Contains(got, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.") {
		t.Fatalf("no-secrets assertion line must remain:\n%s", got)
	}
}
