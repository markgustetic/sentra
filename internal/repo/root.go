package repo

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrRootNotDir is returned when a backup root resolves to something
// other than a directory (a regular file, or a symlink to one).
var ErrRootNotDir = errors.New("repo: backup root is not a directory")

// ResolveRoot turns an operator-supplied backup root into the
// canonical path a snapshot records as Manifest.Root: absolute,
// cleaned, symlinks resolved, and confirmed to be a directory.
//
// Resolving symlinks is what makes `sentra backup ~/Dropbox` work
// when ~/Dropbox is a link: filepath.WalkDir does not descend a root
// that is itself a symlink, so the unresolved path walked to a
// single "." symlink entry — a zero-file snapshot with exit 0. The
// RESOLVED path is the one recorded because retention groups by
// Root: the linked and the real spelling of one directory must land
// in one group, or each spelling prunes the other's dailies. The
// directory check closes the sibling failure (a file root walks to
// one "." file entry) without a generic zero-entry guard, which
// would wrongly refuse the legitimate backup of an empty directory.
//
// Exported so every surface that compares a configured path against
// SnapshotInfo.Root (policy last-run, the Schedules view) can
// normalise the same way.
func ResolveRoot(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("repo: abs root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(absRoot))
	if err != nil {
		return "", fmt.Errorf("repo: resolve root %q: %w", absRoot, err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("repo: stat root %q: %w", resolved, err)
	}
	if !fi.IsDir() {
		// Name the path as given and, when a link (or an aliased
		// mount like macOS's /var) sent it elsewhere, the path that
		// was judged: the operator who typed the link cannot
		// otherwise see what it pointed at.
		if resolved != absRoot {
			return "", fmt.Errorf("%w: %q (resolves to %q)", ErrRootNotDir, absRoot, resolved)
		}
		return "", fmt.Errorf("%w: %q", ErrRootNotDir, absRoot)
	}
	return resolved, nil
}
