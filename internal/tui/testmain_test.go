package tui

import (
	"os"
	"testing"
)

// TestMain clears ambient non-interactive passphrase sources before any test
// in this package runs.
//
// `just check` sources the repo's .env (Justfile's `set dotenv-load`), which
// exports SENTRA_PASSPHRASE for the MinIO dev flow. A bare `go test` (and CI,
// which has no .env) runs without it. SetupWizardView.enterPassphraseStage
// resolves SENTRA_PASSPHRASE non-interactively and skips its passphrase entry
// stage entirely when a source answers — correct production behavior, but it
// means any test that assumes "no source is configured" observes a different
// flow depending on which command started the process. Three tests already
// broke on this exact trap; per-test t.Setenv fixes only the instances caught
// so far, not the shape of the bug for the next test someone writes against
// the wizard.
//
// Clearing the process environment once, here, means every test starts from
// a known-clean slate regardless of how the suite was invoked. Tests that
// deliberately want the var present still set it with t.Setenv, which
// restores the pre-test value (unset, thanks to this) when the test ends —
// so this does not conflict with tests exercising the "source resolves" path
// (e.g. TestSetupWizard_NonInteractivePassphraseSkipsEntryStage).
func TestMain(m *testing.M) {
	_ = os.Unsetenv("SENTRA_PASSPHRASE")
	_ = os.Unsetenv("SENTRA_PASSPHRASE_FILE")
	os.Exit(m.Run())
}
