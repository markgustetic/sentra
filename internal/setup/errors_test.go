package setup

import (
	"errors"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestIsAWSMissingCredentialsError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"failed to refresh cached credentials", true},
		{"no EC2 IMDS role found", true},
		{"no valid credential sources", true},
		{"no credential provider configured", true},
		{"resolve credential providers", true},
		{"ec2imds timeout", true},
		{"access denied", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAWSMissingCredentialsError(errors.New(tt.msg)); got != tt.want {
			t.Fatalf("IsAWSMissingCredentialsError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestWrapAWSPrepareErrorClassifiesByMethod(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Profile = "work"

	missing := errors.New("no valid credential sources")
	got := WrapAWSPrepareError(cfg, AWSAuthLogin, missing)
	if !strings.Contains(got.Error(), "browser login") || !strings.Contains(got.Error(), "AWS profile work") {
		t.Fatalf("login wrap = %v", got)
	}
	if !errors.Is(got, missing) {
		t.Fatalf("wrap must preserve cause chain")
	}

	sso := WrapAWSPrepareError(cfg, AWSAuthSSO, missing)
	if !strings.Contains(sso.Error(), "SSO flow") {
		t.Fatalf("sso wrap = %v", sso)
	}

	other := errors.New("some unrelated failure")
	if got := WrapAWSPrepareError(cfg, AWSAuthExisting, other); !strings.HasPrefix(got.Error(), "prepare AWS S3:") {
		t.Fatalf("non-credential error should be plain prepare wrap, got %v", got)
	}
}

func TestWrapAWSLoginAndSSOFlowErrors(t *testing.T) {
	base := errors.New("boom")
	if got := WrapAWSLoginFlowError("", base); !strings.Contains(got.Error(), "profile default") {
		t.Fatalf("login flow default profile = %v", got)
	}
	if got := WrapAWSSSOFlowError("aws sso login", "work", base); !strings.Contains(got.Error(), "profile work") {
		t.Fatalf("sso flow = %v", got)
	}
	if got := WrapAWSSSOFlowError("aws configure sso", "", base); !strings.Contains(got.Error(), "the default profile") {
		t.Fatalf("sso flow default = %v", got)
	}
}

func TestErrorAdvice(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "my-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	cfg.Repo.S3.Profile = "work"

	advice := ErrorAdvice(errors.New("head bucket: AccessDenied: status code: 403"), cfg)
	joined := strings.Join(advice, "\n")
	if !strings.Contains(joined, "my-bucket") {
		t.Fatalf("advice should mention bucket, got %v", advice)
	}
	if !strings.Contains(joined, "iam-policy") {
		t.Fatalf("access-denied advice should mention iam-policy, got %v", advice)
	}

	if got := ErrorAdvice(nil, cfg); got != nil {
		t.Fatalf("nil error advice = %v, want nil", got)
	}

	fallback := ErrorAdvice(errors.New("totally novel failure"), config.Config{})
	if len(fallback) != 1 {
		t.Fatalf("expected single fallback line, got %v", fallback)
	}
}
