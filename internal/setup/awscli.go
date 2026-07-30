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
// missing and the caller supplied a confirm, it asks to run
// `brew install awscli` and verifies the install before continuing.
//
// NO PRODUCTION CALLER SUPPLIES A CONFIRM. Engine.PrepareAWS passes nil, and
// the TUI wizard passes nil by design (huh cannot run inside a live
// tea.Program), so since the huh-based CLI wizard's deletion the install branch
// below is reachable only from tests. It is kept rather than deleted because
// re-enabling it is cheap: a TUI confirm modal whose "yes" runs the plan the way
// interactiveAWSAuthCommand already suspends the program to run `aws login`
// would restore brew auto-install without changing anything here. That is a
// feature, not a fix, so it is tracked rather than built.
//
// A missing binary with no confirm and a missing binary with no supported
// package manager are therefore the same situation from the operator's side —
// nothing here can install it — so they share one actionable message. Naming the
// absent confirm instead would describe internal wiring the operator cannot act
// on, and ErrorAdvice has no case for it, so the TUI's modal would fall through
// to its generic line and never say "install the AWS CLI".
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if _, err := exec.LookPath("aws"); err == nil {
		return AWSCLIInstallReport{AlreadyInstalled: true}, nil
	}

	plan, ok := DefaultAWSCLIInstallPlan()
	if !ok || confirm == nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but was not found in PATH. Install it, or rerun setup and choose Existing credentials")
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
// to install the AWS CLI, and whether a supported manager was found. Its result
// only reaches an actual install through DefaultEnsureAWSCLI's confirm branch,
// which no production caller currently arms — see the note there.
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
	return runAWSCLI(ctx, args, profile)
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
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the AWS CLI.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sso", "login"}, profile)
}

// runAWSCLI runs the aws CLI wired to the caller's terminal: login and SSO
// flows open a browser and prompt on stdin, so the child must own the TTY.
// The TUI wizard cannot use this — it builds its own exec.Cmd via
// tea.ExecProcess so the running program can suspend around the child.
func runAWSCLI(ctx context.Context, args []string, profile string) error {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
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
