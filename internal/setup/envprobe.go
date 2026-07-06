package setup

import (
	"os"
	"strings"
)

// EnvProbe reads the ambient AWS environment so DefaultPlan stays a pure
// transform in tests: production wires DefaultEnvProbe, tests inject a fake.
// It never reads secrets — only presence of credentials and profile/region
// hints used to pre-fill the wizard.
type EnvProbe interface {
	Getenv(key string) string
	DefaultProfileFromConfig() string
	HasEnvCredentials() bool
}

// DefaultEnvProbe reads the real process environment and ~/.aws/config.
func DefaultEnvProbe() EnvProbe { return osEnvProbe{} }

type osEnvProbe struct{}

// Getenv returns the trimmed value of key, matching the wizard's historical
// firstNonEmptyEnv trimming so a whitespace-only var counts as unset.
func (osEnvProbe) Getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// HasEnvCredentials reports whether static or web-identity AWS credentials are
// present in the environment. Ported from the CLI wizard's
// hasAWSEnvironmentCredentials (internal/cli/setup_wizard.go:248-258).
func (osEnvProbe) HasEnvCredentials() bool {
	if strings.TrimSpace(os.Getenv("AWS_ROLE_ARN")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "" ||
		strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN")) != ""
}

// DefaultProfileFromConfig picks a sensible default AWS profile from
// ~/.aws/config, preferring "sentra" then "default", else the first profile.
// Ported from the CLI wizard's defaultAWSProfileFromConfig
// (internal/cli/setup_wizard.go:260-287). Reuses this package's exported
// LoadAWSCLIConfig/AWSProfileSection (defined in awscli.go) rather than
// duplicating the parser.
func (osEnvProbe) DefaultProfileFromConfig() string {
	cfg, err := LoadAWSCLIConfig()
	if err != nil || cfg == nil {
		return ""
	}
	for _, profile := range []string{"sentra", "default"} {
		if len(cfg[AWSProfileSection(profile)]) > 0 {
			return profile
		}
	}
	for section := range cfg {
		if profile := awsProfileNameFromSection(section); profile != "" {
			return profile
		}
	}
	return ""
}

func awsProfileNameFromSection(section string) string {
	section = strings.TrimSpace(section)
	if section == "default" {
		return "default"
	}
	if strings.HasPrefix(section, "profile ") {
		return strings.TrimSpace(strings.TrimPrefix(section, "profile "))
	}
	return ""
}
