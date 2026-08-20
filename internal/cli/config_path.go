package cli

import (
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
)

// resolveConfigPath applies config discovery to a --config value at run
// time. An explicitly passed flag always wins; so does any programmatic
// non-default value (`sentra local` hands runUI .sentra-local.yaml
// without registering a --config flag). Only the untouched default falls
// through to config.DiscoverPath: ./sentra.yaml when present, else the
// user-level ~/.config/sentra/sentra.yaml.
//
// It must run at RunE time, not wiring time: Flags().Changed is only
// meaningful after cobra parses argv, and tests chDir after building
// commands. Root's PersistentPreRunE is spoken for (slog setup in
// cmd/sentra), and a per-command PersistentPreRunE would shadow it — so
// this is a plain call at the top of each run body, not a cobra hook.
func resolveConfigPath(cmd *cobra.Command, cfgPath string) string {
	if f := cmd.Flags().Lookup("config"); f != nil && f.Changed {
		return cfgPath
	}
	if cfgPath != configFileName {
		return cfgPath
	}
	return config.DiscoverPath()
}
