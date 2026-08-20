package cli

import (
	"os"
	"testing"
)

// TestMain clears ambient non-interactive passphrase sources before any test
// in this package runs.
//
// `just check` sources the repo's .env (Justfile's `set dotenv-load`), which
// exports SENTRA_PASSPHRASE for the MinIO dev flow. A bare `go test` (and CI,
// which has no .env) runs without it. The setup wizard's launch-routing probe
// resolves SENTRA_PASSPHRASE non-interactively and skips straight past
// interactive stages when a source answers — correct production behavior,
// but it means any test that assumes "no source is configured" observes a
// different flow depending on which command started the process.
// TestRunUI_SetupRoutingMatrix in this package (and three more in
// internal/tui) already broke on this exact trap; per-test t.Setenv fixes
// only the instances caught so far, not the shape of the bug for the next
// test someone writes against the wizard.
//
// Clearing the process environment once, here, means every test starts from
// a known-clean slate regardless of how the suite was invoked. Tests that
// deliberately want the var present still set it with t.Setenv, which
// restores the pre-test value (unset, thanks to this) when the test ends —
// so this does not conflict with tests exercising the "source resolves"
// path.
// Same trap, a second source: config.DiscoverPath falls back to
// $XDG_CONFIG_HOME/sentra/sentra.yaml whenever a test's cwd has no
// sentra.yaml and its --config flag is untouched. Left pointing at
// whatever the host process inherited, any such test resolves to the
// real developer machine's ~/.config — passing or failing depending on
// whether that machine happens to have a ~/.config/sentra/sentra.yaml,
// not on the test's own fixture. Several TestRunUI_* tests in this
// package call runUI directly (bypassing cobra's flag parsing, so
// Flags().Changed is never true) and hit exactly this path. Pinning
// XDG_CONFIG_HOME once, here, to a directory guaranteed empty for the
// life of the test binary removes the machine dependency for all of
// them at once, the same way the SENTRA_PASSPHRASE clear above does for
// its trap. Tests that deliberately exercise discovery still override
// this with their own t.Setenv, which restores this value when they end.
func TestMain(m *testing.M) {
	_ = os.Unsetenv("SENTRA_PASSPHRASE")
	_ = os.Unsetenv("SENTRA_PASSPHRASE_FILE")

	xdg, err := os.MkdirTemp("", "sentra-test-xdg-config-*")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", xdg)

	code := m.Run()
	_ = os.RemoveAll(xdg)
	os.Exit(code)
}
