package tui

import (
	"os"

	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// absPath resolves a directory the operator typed or the chat handed
// over — "~/docs", "rel/dir" — against the process's home and cwd, the
// way a policy path is resolved at run time. Everything that persists a
// path into sentra.yaml goes through this (or policycfg.NormalizePath
// with an explicit home) BEFORE writing: a timer runs `policy run` from
// launchd/systemd, whose cwd is not the operator's shell and whose HOME
// may not expand the tilde, so a raw path that worked interactively
// fails under the timer. An unreadable home leaves "~" unexpanded rather
// than guessing.
func absPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return policycfg.NormalizePath(p, home)
}
