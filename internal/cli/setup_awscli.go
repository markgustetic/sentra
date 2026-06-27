package cli

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
// missing and Homebrew is available, it asks for permission to run
// `brew install awscli` and verifies the install before continuing.
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if _, err := exec.LookPath("aws"); err == nil {
		return AWSCLIInstallReport{AlreadyInstalled: true}, nil
	}

	plan, ok := defaultAWSCLIInstallPlan()
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for AWS SSO setup but was not found in PATH. Install it, or rerun setup and skip AWS CLI SSO auth")
	}
	if confirm == nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for AWS SSO setup but no install confirmation was configured")
	}
	ok, err := confirm(plan)
	if err != nil {
		return AWSCLIInstallReport{}, err
	}
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install canceled; install it manually or rerun setup and skip AWS CLI SSO auth")
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

func defaultAWSCLIInstallPlan() (AWSCLIInstallPlan, bool) {
	if _, err := exec.LookPath("brew"); err == nil {
		return AWSCLIInstallPlan{
			Manager: "Homebrew",
			Command: []string{"brew", "install", "awscli"},
		}, true
	}
	return AWSCLIInstallPlan{}, false
}

// DefaultAWSCheckIdentity verifies that the selected AWS profile can
// resolve a caller identity through the AWS CLI. It captures output so
// a failed preflight can be retried through SSO login without printing
// scary intermediate errors.
func DefaultAWSCheckIdentity(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sts", "get-caller-identity"}, profile, false)
}

// DefaultAWSSSOConfigured checks whether the selected profile has a complete
// AWS CLI SSO profile. It supports both the newer sso_session form and the
// older inline sso_start_url form.
func DefaultAWSSSOConfigured(_ context.Context, profile string) (bool, error) {
	cfg, err := loadAWSCLIConfig()
	if err != nil {
		return false, err
	}
	if cfg == nil {
		return false, nil
	}
	return awsSSOProfileConfigured(cfg, profile), nil
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the
// AWS CLI. Sentra does not store the configured values.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile, true)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the
// AWS CLI. Sentra never receives or stores the resulting credentials.
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

type awsCLIConfig map[string]map[string]string

func loadAWSCLIConfig() (awsCLIConfig, error) {
	path, err := awsConfigPath()
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

	cfg := awsCLIConfig{}
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

func awsConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws config: %w", err)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

func awsSSOProfileConfigured(cfg awsCLIConfig, profile string) bool {
	profileSection := awsProfileSection(profile)
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

func awsProfileSection(profile string) string {
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
