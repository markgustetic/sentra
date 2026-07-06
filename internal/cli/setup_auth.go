package cli

import (
	"io"

	"github.com/markgustetic/sentra/internal/setup"
)

// printSetupAuthProgress renders the engine's AWS auth report to stdout,
// reproducing the historical per-step "ok" lines the setup oracle asserts.
// The AWS auth + bucket-prep sequencing now lives in setup.Engine.PrepareAWS
// (the headless port of the former runSetupAWSAuth family); this render is the
// cli-only stdout side the engine deliberately omits. Keep these strings
// byte-identical to the old setup_auth.go so the oracle's Contains checks pass.
func printSetupAuthProgress(out io.Writer, report setup.AWSAuthReport) {
	if report.AWSCLIInstalled {
		printSetupOK(out, "AWS CLI installed")
	}
	if report.LoginRan {
		printSetupOK(out, "AWS browser login complete")
	}
	if report.SSOConfigureRan {
		printSetupOK(out, "AWS SSO profile configured")
	}
	if report.SSOLoginRan {
		printSetupOK(out, "AWS SSO login complete")
	}
	if report.IdentityVerified {
		printSetupOK(out, "AWS credentials ready")
	}
}
