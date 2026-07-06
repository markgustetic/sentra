package cli

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/setup"
)

type setupInitResult = setup.InitResult

// runSetupInit resolves the passphrase via the cli's injected resolver, then
// hands repo init (and the verify-before-keyring guard) to the setup engine
// built from these same SetupDeps. It stays in cli so the oracle can drive it
// with the historical SetupDeps closures. The nil-dependency guard errors
// (missing store/passphrase/saver) are preserved here so the oracle's
// nil-dep cases keep their exact messages before the engine is constructed.
func runSetupInit(ctx context.Context, deps SetupDeps, cfg *config.Config, savePassphrase bool) (setupInitResult, error) {
	if deps.NewStore == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing store factory")
	}
	if deps.Passphrase == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing passphrase resolver")
	}
	if savePassphrase && deps.SavePassphrase == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing keyring passphrase saver")
	}

	pass, err := deps.Passphrase()
	if err != nil {
		return setupInitResult{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	// The cli caller owns the resolver's buffer: scrub it on every return path.
	// Engine.InitRepo does NOT zeroize pass — the caller does, per the Part 3
	// security contract.
	defer crypto.Zeroize(pass)

	eng := setup.NewEngine(setupEffects(deps))
	return eng.InitRepo(ctx, cfg, pass, savePassphrase)
}
