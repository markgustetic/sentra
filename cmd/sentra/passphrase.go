package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/markgustetic/sentra/internal/cli"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
)

const minPassphraseLen = 8

const keyringService = "sentra"

const keyringDefaultUser = "default"

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
		opts.KeyringService = keyringService
		opts.KeyringUser = keyringUserForConfig(cfg)
		opts.KeyringFallbackUsers = legacyKeyringUsersForConfig(cfg)
	}
	if opts.KeyringUser == "" {
		opts.KeyringUser = keyringDefaultUser
	}
	return opts
}

func keyringUserForConfig(cfg *config.Config) string {
	if cfg == nil || cfg.Repo.S3.Bucket == "" {
		return keyringDefaultUser
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" {
		return keyringDefaultUser
	}
	prefix := strings.TrimSpace(cfg.Repo.S3.Prefix)
	if prefix == "" {
		return bucket
	}
	return bucket + "/" + prefix
}

func legacyKeyringUsersForConfig(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" || keyringUserForConfig(cfg) == bucket {
		return nil
	}
	return []string{bucket}
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
	return config.StoreKeyringPassphrase(keyringOptionsForConfig(cfg), passphrase)
}

func deleteRepoPassphraseFromKeyring(cfg *config.Config) (bool, error) {
	deleted, err := config.DeleteKeyringPassphrase(keyringOptionsForConfig(cfg))
	if err != nil {
		return false, err
	}
	for _, user := range legacyKeyringUsersForConfig(cfg) {
		legacyDeleted, err := config.DeleteKeyringPassphrase(config.StoreKeyringOptions{
			KeyringService: keyringService,
			KeyringUser:    user,
		})
		if err != nil {
			return deleted, err
		}
		deleted = deleted || legacyDeleted
	}
	return deleted, nil
}

func keyringOptionsForConfig(cfg *config.Config) config.StoreKeyringOptions {
	return config.StoreKeyringOptions{
		KeyringService: keyringService,
		KeyringUser:    keyringUserForConfig(cfg),
	}
}
