package main

import (
	"fmt"
	"os"

	"github.com/markgustetic/sentra/internal/cli"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
)

const minPassphraseLen = 8

// loadConfigBestEffort is used by startup-time helpers. A missing file is
// fine; a malformed file is surfaced as a warning and callers fall back to
// their own defaults.
func loadConfigBestEffort(path, where string) *config.Config {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sentra: warning: %s: %v (using defaults)\n", where, err)
		return nil
	}
	return cfg
}

// promptNewRepoPassphrase returns the new-passphrase callback for `sentra
// password`. SENTRA_PASSPHRASE is intentionally not a source for the new
// secret; non-interactive rotation uses --new-passphrase-file instead.
func promptNewRepoPassphrase() func(passphraseFile string) ([]byte, error) {
	return func(passphraseFile string) ([]byte, error) {
		if passphraseFile != "" {
			return config.Resolve(config.ResolveOptions{PassphraseFile: passphraseFile})
		}
		return ui.PromptPassphraseWithConfirm("Set new repository passphrase", minPassphraseLen)
	}
}

func buildResolveOpts(rootFlags *cli.RootFlags, logLabel string, prompt func() ([]byte, error)) config.ResolveOptions {
	cfg := loadConfigBestEffort("sentra.yaml", logLabel)
	return buildResolveOptsFromConfig(rootFlags, cfg, prompt)
}

func buildResolveOptsFromConfig(rootFlags *cli.RootFlags, cfg *config.Config, prompt func() ([]byte, error)) config.ResolveOptions {
	opts := config.ResolveOptions{
		PassphraseFile: rootFlags.PassphraseFile,
		Prompt:         prompt,
	}
	if cfg != nil {
		opts.UseKeyring = cfg.Passphrase.UseKeyring
		opts.KeyringService = config.KeyringService
		opts.KeyringUser = config.KeyringUserForConfig(cfg)
		opts.KeyringFallbackUsers = config.LegacyKeyringUsersForConfig(cfg)
	}
	if opts.KeyringUser == "" {
		opts.KeyringUser = config.KeyringDefaultUser
	}
	return opts
}

func promptInitPassphrase(rootFlags *cli.RootFlags) func() ([]byte, error) {
	return func() ([]byte, error) {
		return config.Resolve(buildResolveOpts(rootFlags, "init passphrase prompt", func() ([]byte, error) {
			return ui.PromptPassphraseWithConfirm("Set repository passphrase", minPassphraseLen)
		}))
	}
}

func promptSetupPassphrase(rootFlags *cli.RootFlags) func() ([]byte, error) {
	return func() ([]byte, error) {
		return config.Resolve(config.ResolveOptions{
			PassphraseFile: rootFlags.PassphraseFile,
			Prompt: func() ([]byte, error) {
				return ui.PromptPassphraseWithConfirm("Set repository passphrase", minPassphraseLen)
			},
		})
	}
}

func promptOpenPassphraseWithConfig(rootFlags *cli.RootFlags) func(*config.Config) ([]byte, error) {
	return func(cfg *config.Config) ([]byte, error) {
		return config.Resolve(buildResolveOptsFromConfig(rootFlags, cfg, func() ([]byte, error) {
			return ui.PromptPassphrase("Repository passphrase", 0)
		}))
	}
}

func saveRepoPassphraseToKeyring(cfg *config.Config, passphrase []byte) error {
	return config.StoreKeyringPassphrase(config.KeyringOptionsForConfig(cfg), passphrase)
}

func deleteRepoPassphraseFromKeyring(cfg *config.Config) (bool, error) {
	deleted, err := config.DeleteKeyringPassphrase(config.KeyringOptionsForConfig(cfg))
	if err != nil {
		return false, err
	}
	for _, user := range config.LegacyKeyringUsersForConfig(cfg) {
		legacyDeleted, err := config.DeleteKeyringPassphrase(config.StoreKeyringOptions{
			KeyringService: config.KeyringService,
			KeyringUser:    user,
		})
		if err != nil {
			return deleted, err
		}
		deleted = deleted || legacyDeleted
	}
	return deleted, nil
}
