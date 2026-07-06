package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

// wantInitResult takes setup.InitResult; passing the cli alias to it proves
// setupInitResult is an identity alias of the engine's result type.
func wantInitResult(setup.InitResult) {}

func TestSetupInitResultAliasBindsToSetup(t *testing.T) {
	wantInitResult(setupInitResult{})
	var r setupInitResult
	r.AlreadyInitialized = true
	r.PassphraseSavedToKeyring = true
	r.RepoID = "id"
	if !r.AlreadyInitialized || !r.PassphraseSavedToKeyring || r.RepoID != "id" {
		t.Fatal("setupInitResult field access drifted")
	}
}
