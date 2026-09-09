package policy

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/markgustetic/sentra/internal/repo"
)

// Policy paths are stored absolute. A policy is run by an OS timer whose
// working directory is nothing the operator chose — launchd starts jobs
// in `/`, systemd in the user's home — so a relative path written at
// `policy add` time would back up a different tree at every fire, and
// `--path .` under launchd would snapshot the root filesystem. The three
// resolvers below share one rule so the path `policy add` persists, the
// path `policy run` snapshots, and the root the Last-run lookup compares
// against are the same string.
//
// That rule is repo.ResolveRoot's: absolute, cleaned, symlinks resolved.
// CreateSnapshot records the RESOLVED path as Manifest.Root (a linked
// and a real spelling of one directory must share a retention group),
// so a policy path resolved only as far as Abs+Clean never equals the
// root of the snapshot it produced wherever a link sits above it — on
// macOS that is every path under /var, /tmp and /etc — and LastRun's
// root fallback, `--if-due`, and the Schedules view all miss the run
// that just happened. The one allowance policy makes beyond ResolveRoot
// is a path that does not exist yet: an operator may add a policy for a
// directory before creating it, so the resolver settles the longest
// existing prefix and keeps the rest as spelled, which is exactly what
// ResolveRoot will answer once the directory is there.

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
// hold the home directory and only compare (the TUI's Last-run lookup).
// Whatever stops the resolver on disk — a file where a directory should
// be, a dangling link, a permission failure — the answer is still the
// absolute, cleaned spelling (resolveError carries it): a comparison
// against snapshot roots then simply finds nothing, since no snapshot
// has such a root, whereas a relative spelling could never match at all.
// Only a path that has no absolute form (empty, or a tilde with no
// home) comes back merely cleaned.
func NormalizePath(p, home string) string {
	abs, err := resolvePath(p, "", func() (string, error) { return home, nil })
	if err != nil {
		var re *resolveError
		if errors.As(err, &re) {
			return re.path
		}
		return filepath.Clean(p)
	}
	return abs
}

// ErrDanglingSymlink is returned for a symlink with no target at or
// above the policy path. It is refused rather than stored as the link's
// own spelling because that spelling would drift: once the target
// appears the resolver follows the link, every snapshot root differs
// from the persisted path, and the Last-run lookup misses forever.
var ErrDanglingSymlink = errors.New("dangling symlink")

// resolveError carries the absolute, cleaned spelling of the path
// alongside whatever stopped resolveExisting, so NormalizePath can hand
// back an absolute form while ResolvePath (and through it Validate)
// still refuses the path.
type resolveError struct {
	path string
	err  error
}

func (e *resolveError) Error() string { return e.err.Error() }
func (e *resolveError) Unwrap() error { return e.err }

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
	return resolveExisting(filepath.Clean(abs))
}

// resolveExisting applies repo.ResolveRoot's canonical form to a clean
// absolute path, tolerating a path that does not exist yet. ResolveRoot
// itself is the first attempt, so an existing directory resolves by
// exactly the code CreateSnapshot runs; an existing non-directory is
// refused with repo.ErrRootNotDir (a snapshot of it can only fail). When
// the path is missing, the longest existing prefix is resolved and the
// missing tail re-joined as spelled, so a link above the future
// directory is still followed and the stored string matches the root a
// later snapshot records. Only fs.ErrNotExist earns the fallback: a
// permission failure or a link loop is a real error the operator should
// see at `policy add`, not at the timer's next fire — and a dangling
// link is refused too (ErrDanglingSymlink), since the walk-up would
// otherwise store the link's spelling, which stops matching the moment
// its target appears. Every failure leaves through resolveError so the
// caller that only compares (NormalizePath) still has an absolute form.
func resolveExisting(abs string) (string, error) {
	resolved, err := repo.ResolveRoot(abs)
	if err == nil {
		return resolved, nil
	}
	fail := func(err error) error {
		return &resolveError{path: abs, err: fmt.Errorf("resolve policy path %q: %w", abs, err)}
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", fail(err)
	}
	if isDangling(abs) {
		return "", fail(ErrDanglingSymlink)
	}
	// Walk up until a prefix resolves; the filesystem root always does,
	// so the loop terminates. The tail is re-joined as spelled: nothing
	// exists there to resolve, and Clean has already folded its `..`.
	prefix, tail := abs, ""
	for {
		parent := filepath.Dir(prefix)
		if parent == prefix {
			return abs, nil
		}
		tail = filepath.Join(filepath.Base(prefix), tail)
		prefix = parent
		real, evalErr := filepath.EvalSymlinks(prefix)
		if evalErr == nil {
			return filepath.Join(real, tail), nil
		}
		if !errors.Is(evalErr, fs.ErrNotExist) {
			return "", fail(evalErr)
		}
		if isDangling(prefix) {
			return "", fail(fmt.Errorf("%w at %s", ErrDanglingSymlink, prefix))
		}
	}
}

// isDangling reports whether p is itself a symlink that Stat could not
// follow — the one case where "does not exist" is not "not created yet".
// Only called for a path already known not to resolve, so a link here is
// dangling by construction.
func isDangling(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode()&fs.ModeSymlink != 0
}
