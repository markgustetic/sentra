package setup

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeProbe is a deterministic EnvProbe for the transform tests in this
// package; kept here so DefaultPlan tests never touch the real environment.
type fakeProbe struct {
	env            map[string]string
	profile        string
	envCredentials bool
}

func (f fakeProbe) Getenv(key string) string         { return f.env[key] }
func (f fakeProbe) DefaultProfileFromConfig() string { return f.profile }
func (f fakeProbe) HasEnvCredentials() bool          { return f.envCredentials }

func clearAWSEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing-aws-config"))
}

func TestDefaultEnvProbeGetenvTrimsToRaw(t *testing.T) {
	clearAWSEnv(t)
	t.Setenv("AWS_REGION", "us-west-2")
	probe := DefaultEnvProbe()
	if got := probe.Getenv("AWS_REGION"); got != "us-west-2" {
		t.Fatalf("Getenv: got %q, want us-west-2", got)
	}
}

func TestDefaultEnvProbeHasEnvCredentials(t *testing.T) {
	clearAWSEnv(t)
	probe := DefaultEnvProbe()
	if probe.HasEnvCredentials() {
		t.Fatal("no credentials set, HasEnvCredentials should be false")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	if !DefaultEnvProbe().HasEnvCredentials() {
		t.Fatal("access key + secret set, HasEnvCredentials should be true")
	}
}

func TestDefaultEnvProbeHasEnvCredentialsWebIdentity(t *testing.T) {
	clearAWSEnv(t)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::1:role/x")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/tmp/token")
	if !DefaultEnvProbe().HasEnvCredentials() {
		t.Fatal("role arn + web identity token set, HasEnvCredentials should be true")
	}
}

func TestDefaultEnvProbeDefaultProfileFromConfig(t *testing.T) {
	clearAWSEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("[profile sentra]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	if got := DefaultEnvProbe().DefaultProfileFromConfig(); got != "sentra" {
		t.Fatalf("DefaultProfileFromConfig: got %q, want sentra", got)
	}
}
