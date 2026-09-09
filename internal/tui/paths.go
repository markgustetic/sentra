package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"

	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// errNoHome is expandPath's refusal: a "~" path with no home to expand
// it against. Callers surface it (errors.Is) instead of persisting.
var errNoHome = errors.New("cannot expand ~: home directory is unknown")

// expandPath resolves a directory the operator typed, the chat handed
// over, or a policy stored — "~/docs", "rel/dir" — against home and the
// process cwd, the way a policy path is resolved at run time. Everything
// that persists a path into sentra.yaml goes through this BEFORE writing:
// a timer runs `policy run` from launchd/systemd, whose cwd is not the
// operator's shell and whose HOME may not expand the tilde, so a raw path
// that worked interactively fails under the timer.
//
// A tilde with an EMPTY home is refused with errNoHome naming the path,
// never resolved. policycfg.NormalizePath alone would join "" and "docs"
// and make "~/docs" mean <cwd>/docs — a guess that, once persisted,
// silently backs up (or, for a schedule, keeps backing up) the wrong
// tree. The raw tilde at least failed loudly.
func expandPath(p, home string) (string, error) {
	if home == "" && (p == "~" || strings.HasPrefix(p, "~/")) {
		return "", fmt.Errorf("%w: %s", errNoHome, p)
	}
	return policycfg.NormalizePath(p, home), nil
}

// expandPathOrRaw is expandPath for read-only lookups (last-run matching,
// the drill-in's newest snapshot): a refused tilde comes back unexpanded,
// which simply matches nothing, so a missing home degrades a display
// rather than blocking the view. Never use it on a persisting path.
func expandPathOrRaw(p, home string) string {
	abs, err := expandPath(p, home)
	if err != nil {
		return p
	}
	return abs
}

// absPath is expandPath against the process's own home. An unreadable
// home (os.UserHomeDir fails — no $HOME under a timer or a stripped
// environment) is the empty home expandPath refuses a tilde for.
func absPath(p string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return expandPath(p, home)
}
