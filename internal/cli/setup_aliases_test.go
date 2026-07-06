package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

// wantSetupPlan/Backend/etc. take the setup.* types; passing the cli aliases
// to them is the compile-time proof the cli names are identity aliases of the
// setup package, so the oracle's field access and const comparisons keep
// meaning the same thing. Identity aliases make an explicit conversion
// unnecessary (that is the point), so these helpers accept the value directly.
func wantSetupPlan(setup.Plan)                          {}
func wantSetupBackend(setup.Backend)                    {}
func wantSetupAuthMethod(setup.AWSAuthMethod)           {}
func wantAWSPrepareOptions(setup.AWSPrepareOptions)     {}
func wantAWSPrepareReport(setup.AWSPrepareReport)       {}
func wantAWSAuthReport(setup.AWSAuthReport)             {}
func wantAWSCLIInstallPlan(setup.AWSCLIInstallPlan)     {}
func wantAWSCLIInstallReport(setup.AWSCLIInstallReport) {}

func TestSetupAliasesBindToSetupPackage(t *testing.T) {
	wantSetupPlan(SetupPlan{})
	wantSetupBackend(SetupBackendAWS)
	wantSetupBackend(SetupBackendS3Compatible)
	wantSetupAuthMethod(SetupAWSAuthLogin)
	wantSetupAuthMethod(SetupAWSAuthSSO)
	wantSetupAuthMethod(SetupAWSAuthExisting)
	wantSetupAuthMethod(SetupAWSAuthSkip)
	wantAWSPrepareOptions(AWSPrepareOptions{})
	wantAWSPrepareReport(AWSPrepareReport{})
	wantAWSAuthReport(AWSAuthReport{})
	wantAWSCLIInstallPlan(AWSCLIInstallPlan{})
	wantAWSCLIInstallReport(AWSCLIInstallReport{})
	if string(SetupBackendAWS) != "aws" {
		t.Fatalf("SetupBackendAWS value drifted: %q", SetupBackendAWS)
	}
	if string(SetupAWSAuthLogin) != "login" {
		t.Fatalf("SetupAWSAuthLogin value drifted: %q", SetupAWSAuthLogin)
	}
}
