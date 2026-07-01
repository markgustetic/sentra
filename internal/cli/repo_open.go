package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
)

// RepoDeps is the dependency set shared by every read-path command: the
// blobstore factory, the two passphrase resolvers, and the stdout sink.
// Commands embed it and add their own extra fields (Stderr, Confirm, ...).
// Exported so cmd/sentra can construct it when wiring each command.
type RepoDeps struct {
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	Passphrase           func() ([]byte, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// openRepoForConfig runs the shared load-config -> open-store -> resolve-
// passphrase -> open-repo sequence. On success it returns the opened repo,
// the passphrase bytes (caller owns `defer crypto.Zeroize(pass)` and
// `defer r.Close()`), and the loaded config. On any error it cleans up the
// passphrase itself and returns it nil. Error strings are identical to the
// per-command blocks this replaces.
func openRepoForConfig(cmd *cobra.Command, cfgPath string, deps RepoDeps) (*repo.Repo, []byte, *config.Config, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve passphrase: %w", err)
	}
	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		crypto.Zeroize(pass)
		return nil, nil, nil, fmt.Errorf("open repo: %w", err)
	}
	return r, pass, cfg, nil
}
