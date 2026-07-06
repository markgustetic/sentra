package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/setup"
)

type setupInitResult = setup.InitResult

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

	store, err := deps.NewStore(ctx, cfg)
	if err != nil {
		return setupInitResult{}, fmt.Errorf("open blobstore: %w", err)
	}

	pass, err := deps.Passphrase()
	if err != nil {
		return setupInitResult{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Init(ctx, store, pass)
	if err != nil {
		if errors.Is(err, repo.ErrAlreadyInitialized) {
			result := setupInitResult{AlreadyInitialized: true}
			// The repo already exists, but the user still asked to save the
			// passphrase to the OS keyring. repo.Init does not verify the
			// passphrase against an existing repo, so open it to confirm the
			// passphrase is correct before populating the keyring — otherwise
			// we'd either leave use_keyring:true dangling with an empty keyring
			// or store a wrong passphrase. Both silently break later
			// non-interactive runs.
			if savePassphrase {
				existing, oerr := repo.Open(ctx, store, pass)
				if oerr != nil {
					return setupInitResult{}, fmt.Errorf("repository already initialized, but the provided passphrase did not open it (keyring not updated): %w", oerr)
				}
				result.RepoID = existing.Config().ID
				existing.Close()
				if serr := deps.SavePassphrase(cfg, pass); serr != nil {
					return setupInitResult{}, fmt.Errorf("save passphrase to keyring: %w", serr)
				}
				result.PassphraseSavedToKeyring = true
			}
			return result, nil
		}
		return setupInitResult{}, fmt.Errorf("init repo: %w", err)
	}
	defer r.Close()

	result := setupInitResult{RepoID: r.Config().ID}
	if savePassphrase {
		if err := deps.SavePassphrase(cfg, pass); err != nil {
			return setupInitResult{}, fmt.Errorf("save passphrase to keyring: %w", err)
		}
		result.PassphraseSavedToKeyring = true
	}
	return result, nil
}
