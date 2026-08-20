package config

import (
	"os"
	"path/filepath"
)

// DiscoverPath returns the config path commands use when the operator did
// not pass --config explicitly:
//
//  1. ./sentra.yaml, when it exists as a regular file — a project-local
//     config always outranks the user-level one.
//  2. $XDG_CONFIG_HOME/sentra/sentra.yaml, defaulting XDG_CONFIG_HOME to
//     ~/.config. This is the gh-CLI convention: ~/.config even on macOS,
//     deliberately not os.UserConfigDir's ~/Library/Application Support.
//
// When neither file exists the home path is still returned — it is the
// write target a first-run setup should persist to, so bare `sentra` from
// any directory lands on the wizard once and the dashboard forever after.
// If the home directory cannot be determined, fall back to the
// cwd-relative name (the pre-discovery behavior) rather than failing.
//
// DiscoverPath only names the path; it never reads or writes the file.
// The "sentra.yaml" literal mirrors internal/cli's configFileName — the
// canonical file name is part of the surface contract (AGENTS.md).
func DiscoverPath() string {
	if info, err := os.Stat("sentra.yaml"); err == nil && info.Mode().IsRegular() {
		return "sentra.yaml"
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "sentra.yaml"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sentra", "sentra.yaml")
}
