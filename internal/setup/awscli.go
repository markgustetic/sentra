package setup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultEnsureAWSCLI verifies that the AWS CLI is available. When it is
// missing and Homebrew is available, it asks (via confirm) to run
// `brew install awscli` and verifies the install before continuing. This
// brew auto-install path is CLI-only; the TUI wizard must detect a missing
// aws CLI and surface advice instead of driving this confirm.
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if _, err := exec.LookPath("aws"); err == nil {
		return AWSCLIInstallReport{AlreadyInstalled: true}, nil
	}

	plan, ok := DefaultAWSCLIInstallPlan()
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but was not found in PATH. Install it, or rerun setup and choose Existing credentials")
	}
	if confirm == nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but no install confirmation was configured")
	}
	ok, err := confirm(plan)
	if err != nil {
		return AWSCLIInstallReport{}, err
	}
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install canceled; install it manually or rerun setup and choose Existing credentials")
	}

	cmd := exec.CommandContext(ctx, plan.Command[0], plan.Command[1:]...) //nolint:gosec // fixed package-manager command selected by Sentra, confirmed by the operator.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("install AWS CLI with %s: %w", plan.Manager, err)
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install completed with %s, but aws is still not on PATH", plan.Manager)
	}
	return AWSCLIInstallReport{Installed: true, Manager: plan.Manager}, nil
}

// DefaultAWSCLIInstallPlan returns the package-manager command Sentra would run
// to install the AWS CLI, and whether a supported manager was found.
func DefaultAWSCLIInstallPlan() (AWSCLIInstallPlan, bool) {
	if _, err := exec.LookPath("brew"); err == nil {
		return AWSCLIInstallPlan{
			Manager: "Homebrew",
			Command: []string{"brew", "install", "awscli"},
		}, true
	}
	return AWSCLIInstallPlan{}, false
}

// DefaultAWSLogin delegates browser-based AWS CLI sign-in. The AWS CLI stores
// temporary credentials; Sentra never receives or stores them.
func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	args := []string{"login"}
	region = strings.TrimSpace(region)
	if region != "" {
		args = append(args, "--region", region)
	}
	return runAWSCLI(ctx, args, profile, true)
}

// DefaultAWSSSOConfigured checks whether the selected profile has a complete
// AWS CLI SSO profile (newer sso_session or older inline sso_start_url form).
func DefaultAWSSSOConfigured(_ context.Context, profile string) (bool, error) {
	cfg, err := LoadAWSCLIConfig()
	if err != nil {
		return false, err
	}
	if cfg == nil {
		return false, nil
	}
	return AWSSSOProfileConfigured(cfg, profile), nil
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the AWS CLI.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile, true)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the AWS CLI.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sso", "login"}, profile, true)
}

func runAWSCLI(ctx context.Context, args []string, profile string, interactive bool) error {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

func appendAWSProfile(args []string, profile string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return args
	}
	return append(args, "--profile", profile)
}

// AWSCLIConfig is a parsed ~/.aws/config: section name to key/value pairs.
type AWSCLIConfig map[string]map[string]string

// LoadAWSCLIConfig reads and parses the AWS CLI config file. A missing file is
// not an error — it returns (nil, nil) so callers treat it as "nothing
// configured".
func LoadAWSCLIConfig() (AWSCLIConfig, error) {
	path, err := AWSConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // AWS CLI config path comes from AWS_CONFIG_FILE or the current user's home dir.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	defer f.Close()

	cfg := AWSCLIConfig{}
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			end := strings.Index(line, "]")
			section = strings.TrimSpace(line[1:end])
			if section != "" && cfg[section] == nil {
				cfg[section] = map[string]string{}
			}
			continue
		}
		if section == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		cfg[section][key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	return cfg, nil
}

// AWSConfigPath returns the AWS CLI config path, honoring AWS_CONFIG_FILE and
// falling back to ~/.aws/config.
func AWSConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws config: %w", err)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

// AWSSSOProfileConfigured reports whether profile has a complete SSO config in
// cfg, supporting both the modern sso_session form and the legacy inline form.
func AWSSSOProfileConfigured(cfg AWSCLIConfig, profile string) bool {
	profileSection := AWSProfileSection(profile)
	values := cfg[profileSection]
	if len(values) == 0 {
		return false
	}
	if hasAllAWSConfigKeys(values, "sso_start_url", "sso_region", "sso_account_id", "sso_role_name") {
		return true
	}

	session := values["sso_session"]
	if strings.TrimSpace(session) == "" {
		return false
	}
	sessionValues := cfg["sso-session "+strings.TrimSpace(session)]
	if len(sessionValues) == 0 {
		return false
	}
	return hasAllAWSConfigKeys(values, "sso_account_id", "sso_role_name") &&
		hasAllAWSConfigKeys(sessionValues, "sso_region") &&
		hasAnyAWSConfigKey(sessionValues, "sso_start_url", "sso_issuer_url")
}

// AWSProfileSection maps a profile name to its ~/.aws/config section header.
func AWSProfileSection(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "default" {
		return "default"
	}
	return "profile " + profile
}

func hasAllAWSConfigKeys(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(values[key]) == "" {
			return false
		}
	}
	return true
}

func hasAnyAWSConfigKey(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(values[key]) != "" {
			return true
		}
	}
	return false
}
