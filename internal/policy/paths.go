package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Policy paths are stored absolute. A policy is run by an OS timer whose
// working directory is nothing the operator chose — launchd starts jobs
// in `/`, systemd in the user's home — so a relative path written at
// `policy add` time would back up a different tree at every fire, and
// `--path .` under launchd would snapshot the root filesystem. The three
// resolvers below share one rule so the path `policy add` persists, the
// path `policy run` snapshots, and the root the Last-run lookup compares
// against are the same string.

// ResolvePath expands a leading `~` against the home directory and
// returns the absolute, cleaned form of p, with a relative p anchored to
// the process cwd. That anchor is right exactly once — at `policy add`,
// where the cwd is the directory the operator typed the command in — so
// this is the resolver for persisting a path; a run should use
// ResolvePathFrom. It fails on an empty path or a tilde with no home to
// expand against, so Validate can refuse the policy up front.
func ResolvePath(p string) (string, error) {
	return resolvePath(p, "", os.UserHomeDir)
}

// ResolvePathFrom resolves p like ResolvePath but anchors a relative path
// to base, which must be absolute — the directory of the sentra.yaml the
// policy was read from. This is the run-time resolver: a hand-written
// config that says `paths: [src]` keeps meaning the src next to the file
// no matter which cwd the timer fires from.
func ResolvePathFrom(p, base string) (string, error) {
	if !filepath.IsAbs(base) {
		return "", fmt.Errorf("resolve policy path %q: base %q is not absolute", p, base)
	}
	return resolvePath(p, base, os.UserHomeDir)
}

// NormalizePath is the error-free ResolvePath for callers that already
// hold the home directory and only compare (the TUI's Last-run lookup):
// a path it cannot resolve comes back cleaned, which is what the walker
// would have recorded as the snapshot root anyway.
func NormalizePath(p, home string) string {
	abs, err := resolvePath(p, "", func() (string, error) { return home, nil })
	if err != nil {
		return filepath.Clean(p)
	}
	return abs
}

// resolvePath is the one implementation. base "" means the process cwd
// (filepath.Abs); homeDir is consulted only for a tilde form, so a
// missing HOME cannot fail an absolute path.
func resolvePath(p, base string, homeDir func() (string, error)) (string, error) {
	if strings.TrimSpace(p) == "" {
		return "", errors.New("resolve policy path: empty path")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := homeDir()
		if err != nil {
			return "", fmt.Errorf("resolve policy path %q: %w", p, err)
		}
		if home == "" {
			return "", fmt.Errorf("resolve policy path %q: home directory is unknown", p)
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}
	if !filepath.IsAbs(p) && base != "" {
		p = filepath.Join(base, p)
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("resolve policy path %q: %w", p, err)
	}
	return filepath.Clean(abs), nil
}
