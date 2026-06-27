package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultEnsureAWSCLI_AlreadyInstalled(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "aws"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)

	report, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		t.Fatal("confirm should not run when aws is already installed")
		return false, nil
	})
	if err != nil {
		t.Fatalf("ensure aws cli: %v", err)
	}
	if !report.AlreadyInstalled || report.Installed {
		t.Fatalf("report = %+v, want already installed only", report)
	}
}

func TestDefaultEnsureAWSCLI_InstallsWithHomebrew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake package manager is unix-only")
	}
	dir := t.TempDir()
	chmodPath, err := exec.LookPath("chmod")
	if err != nil {
		t.Fatalf("locate chmod: %v", err)
	}
	awsPath := filepath.Join(dir, "aws")
	writeExecutable(t, filepath.Join(dir, "brew"), fmt.Sprintf(`#!/bin/sh
if [ "$1" != "install" ] || [ "$2" != "awscli" ]; then
  exit 2
fi
{
  printf '%%s\n' '#!/bin/sh'
  printf '%%s\n' 'exit 0'
} > %q
%q +x %q
`, awsPath, chmodPath, awsPath))
	t.Setenv("PATH", dir)

	var seen AWSCLIInstallPlan
	report, err := DefaultEnsureAWSCLI(context.Background(), func(plan AWSCLIInstallPlan) (bool, error) {
		seen = plan
		return true, nil
	})
	if err != nil {
		t.Fatalf("ensure aws cli: %v", err)
	}
	if seen.Manager != "Homebrew" || strings.Join(seen.Command, " ") != "brew install awscli" {
		t.Fatalf("install plan = %+v", seen)
	}
	if !report.Installed || report.Manager != "Homebrew" {
		t.Fatalf("report = %+v, want installed with Homebrew", report)
	}
}

func TestDefaultEnsureAWSCLI_InstallDeclined(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "brew"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", dir)

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected declined install error, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestDefaultEnsureAWSCLI_ConfirmError(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "brew"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", dir)
	wantErr := errors.New("no thanks")

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		return false, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestDefaultEnsureAWSCLI_NoSupportedInstaller(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		t.Fatal("confirm should not run without a supported installer")
		return false, nil
	})
	if err == nil {
		t.Fatal("expected missing installer error, got nil")
	}
	if !strings.Contains(err.Error(), "AWS CLI is only required") {
		t.Fatalf("error = %v, want optional AWS CLI guidance", err)
	}
}

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

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // test helper creates fake executables in t.TempDir.
		t.Fatalf("chmod executable %s: %v", path, err)
	}
}
