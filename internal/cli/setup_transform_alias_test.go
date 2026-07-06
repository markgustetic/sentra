package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupTransformsDelegateToEngine(t *testing.T) {
	clearAWSSetupEnv(t)
	// defaultSetupPlan must equal setup.DefaultPlan under the production probe.
	got := defaultSetupPlan(config.Config{})
	want := setup.DefaultPlan(config.Config{}, setup.DefaultEnvProbe())
	if got.Backend != want.Backend || got.AWSAuthMethod != want.AWSAuthMethod ||
		got.SavePassphrase != want.SavePassphrase {
		t.Fatalf("defaultSetupPlan drifted from setup.DefaultPlan: got %+v want %+v", got, want)
	}
	// resolveSetupAWSAuthMethod must equal setup.ResolveAWSAuthMethod.
	p := SetupPlan{PrepareAWS: true}
	if resolveSetupAWSAuthMethod(&p) != setup.ResolveAWSAuthMethod(&p) {
		t.Fatal("resolveSetupAWSAuthMethod drifted")
	}
}
