package config

import (
	"os"
	"path/filepath"
)

// DefaultFileName is the canonical config file name: what discovery looks
// for in the working directory, what init writes there, and the last path
// segment of the user-level fallback. internal/cli's configFileName
// aliases it so the two surfaces cannot drift apart — the name is part of
// the surface contract (AGENTS.md).
const DefaultFileName = "sentra.yaml"

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
func DiscoverPath() string {
	if info, err := os.Stat(DefaultFileName); err == nil && info.Mode().IsRegular() {
		return DefaultFileName
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return DefaultFileName
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sentra", DefaultFileName)
}
