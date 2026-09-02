package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
)

// resolveConfigPath applies config discovery to a --config value at run
// time and requires an explicitly named file to exist. An explicitly passed
// flag always wins; so does any programmatic non-default value (`sentra
// local` hands runUI .sentra-local.yaml without registering a --config
// flag). Only the untouched default falls through to config.DiscoverPath:
// ./sentra.yaml when present, else the user-level ~/.config/sentra/sentra.yaml.
//
// The existence check belongs here, not in config.Load. Load tolerates a
// missing file by design — discovery, env-only loads, and the first-run
// wizard all depend on "no file" meaning Defaults() — so a typo'd --config
// used to load an empty config and act on it: `policy list` printed "No
// policies configured" and exited 0, repo commands blamed a sentra.yaml
// that was never there, and every config.Update path (policy add/remove,
// passwd forget) authored a brand-new file at the typo. An explicit path
// is the one case where "missing" can only be a mistake, so it fails
// before any load. The discovery path keeps Load's tolerance.
//
// It must run at RunE time, not wiring time: Flags().Changed is only
// meaningful after cobra parses argv, and tests chDir after building
// commands. Root's PersistentPreRunE is spoken for (slog setup in
// cmd/sentra), and a per-command PersistentPreRunE would shadow it — so
// this is a plain call at the top of each run body, not a cobra hook.
func resolveConfigPath(cmd *cobra.Command, cfgPath string) (string, error) {
	if !configFlagChanged(cmd) {
		return discoverConfigPath(cfgPath), nil
	}
	if configFileMissing(cfgPath) {
		return cfgPath, fmt.Errorf("--config %s: %w (run `sentra setup --config %s` to create it)",
			cfgPath, os.ErrNotExist, cfgPath)
	}
	return cfgPath, nil
}

// resolveConfigPathForLaunch is resolveConfigPath without the existence
// check, for the TUI launcher (`ui`, `setup`, and `local` through runUI)
// only. The launcher hosts the first-run wizard — the one flow that
// creates the file — so an explicit --config there may legitimately name a
// path that does not exist yet; probeLaunchState turns "absent" into the
// wizard route rather than an error. Every other command reads or edits an
// existing file and goes through resolveConfigPath.
func resolveConfigPathForLaunch(cmd *cobra.Command, cfgPath string) string {
	if configFlagChanged(cmd) {
		return cfgPath
	}
	return discoverConfigPath(cfgPath)
}

// configFlagChanged reports whether the operator passed --config on the
// command line (even at its default value, which still bypasses discovery).
func configFlagChanged(cmd *cobra.Command) bool {
	f := cmd.Flags().Lookup("config")
	return f != nil && f.Changed
}

// discoverConfigPath is the no-flag branch: a programmatic non-default
// value is used as is; the untouched default falls through to discovery.
func discoverConfigPath(cfgPath string) string {
	if cfgPath != configFileName {
		return cfgPath
	}
	return config.DiscoverPath()
}

// configFileMissing reports whether path names nothing at all. Any other
// stat outcome — a directory, a permission error — is deliberately left for
// config.Load, which already reports those with its own wording.
func configFileMissing(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, os.ErrNotExist)
}
