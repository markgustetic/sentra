package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultAWSSSOConfigured_ModernSessionProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_session = work
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
region = us-east-1

[sso-session work]
sso_issuer_url = https://identitycenter.example/start
sso_region = us-east-1
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete modern SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_LegacyInlineProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete legacy SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_DefaultProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[default]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete default SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_RejectsPartialModernProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_session = work
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected profile missing its sso-session section to be unconfigured")
	}
}

func TestDefaultAWSSSOConfigured_RejectsPartialLegacyProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected legacy profile missing role name to be unconfigured")
	}
}

func TestDefaultAWSSSOConfigured_MissingConfigFile(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing-config"))

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected missing AWS config file to be unconfigured")
	}
}

func writeAWSConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write aws config: %v", err)
	}
	return path
}
