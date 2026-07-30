package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

// wantIAMDoc/Statement take the setup.* policy types; passing the cli aliases
// to them proves the cli names are identity aliases of the setup package, so
// setup_test.go's json.Unmarshal is checking the engine's own shape. Identity
// aliases make an explicit conversion unnecessary, so the helpers accept the
// value directly.
func wantIAMDoc(setup.IAMPolicyDocument)        {}
func wantIAMStatement(setup.IAMPolicyStatement) {}

func TestSetupIAMPolicyAliasBindsToSetup(t *testing.T) {
	wantIAMDoc(setupIAMPolicyDocument{})
	wantIAMStatement(setupIAMPolicyStatement{})
	doc := buildSetupIAMPolicy("bkt", "p/")
	if doc.Version != "2012-10-17" {
		t.Fatalf("policy version drifted: %q", doc.Version)
	}
}
