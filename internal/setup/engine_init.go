package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// InitRepo initializes (or, if already present, verifies) the encrypted
// repository for cfg using pass, optionally saving pass to the OS keyring.
// The caller owns pass and its zeroization — the TUI wizard zeroizes its
// masked-input buffer. Headless port of the deleted CLI wizard's runSetupInit,
// minus its optional-dep nil guards: Effects methods are always present.
func (e *Engine) InitRepo(ctx context.Context, cfg *config.Config, pass []byte, save bool) (InitResult, error) {
	store, err := e.eff.NewStore(ctx, cfg)
	if err != nil {
		return InitResult{}, fmt.Errorf("open blobstore: %w", err)
	}

	r, err := repo.Init(ctx, store, pass)
	if err != nil {
		if errors.Is(err, repo.ErrAlreadyInitialized) {
			result := InitResult{AlreadyInitialized: true}
			// The repo already exists, but the user still asked to save the
			// passphrase to the OS keyring. repo.Init does not verify the
			// passphrase against an existing repo, so open it to confirm the
			// passphrase is correct before populating the keyring — otherwise
			// we'd either leave use_keyring:true dangling with an empty keyring
			// or store a wrong passphrase. Both silently break later
			// non-interactive runs.
			if save {
				existing, oerr := repo.Open(ctx, store, pass)
				if oerr != nil {
					return InitResult{}, fmt.Errorf("repository already initialized, but the provided passphrase did not open it (keyring not updated): %w", oerr)
				}
				result.RepoID = existing.Config().ID
				existing.Close()
				if serr := e.eff.SavePassphrase(cfg, pass); serr != nil {
					return InitResult{}, fmt.Errorf("save passphrase to keyring: %w", serr)
				}
				result.PassphraseSavedToKeyring = true
			}
			return result, nil
		}
		return InitResult{}, fmt.Errorf("init repo: %w", err)
	}
	defer r.Close()

	result := InitResult{RepoID: r.Config().ID}
	if save {
		if err := e.eff.SavePassphrase(cfg, pass); err != nil {
			return InitResult{}, fmt.Errorf("save passphrase to keyring: %w", err)
		}
		result.PassphraseSavedToKeyring = true
	}
	return result, nil
}
