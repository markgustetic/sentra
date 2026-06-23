package cli

import (
	"errors"

	"github.com/markgustetic/sentra/internal/config"
)

func resolvePassphrase(
	legacy func() ([]byte, error),
	withConfig func(*config.Config) ([]byte, error),
	cfg *config.Config,
) ([]byte, error) {
	if withConfig != nil {
		return withConfig(cfg)
	}
	if legacy != nil {
		return legacy()
	}
	return nil, errors.New("passphrase resolver is not configured")
}
