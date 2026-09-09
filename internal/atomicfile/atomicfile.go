// Package atomicfile replaces a file's contents so that readers only ever
// observe the old file or the complete new one. It is the one writer behind
// every file Sentra rewrites in place — sentra.yaml and the AWS shared
// credentials file — so that the crash-safety and symlink rules those two
// files earned separately cannot drift apart again.
//
// It is a leaf package on purpose: internal/config and internal/setup both
// need it and neither may import the other.
package atomicfile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Write stages body in a temp file beside path, fsyncs it, and renames it
// over path, leaving the result at perm. A crash at any point leaves either
// the previous file or the new one, never a truncated or empty file. What
// a truncated file would cost is each caller's to say (see config.Write
// and setup.WriteAWSCredentialsProfile); this package only promises the
// old-or-new outcome.
//
// The directory is deliberately not fsynced after the rename. A directory
// fsync would only shorten the window in which a power loss reverts the
// rename and leaves the PREVIOUS file in place — which is one of the two
// outcomes promised above, not a corruption. The file's own fsync is the
// one that matters: it is what keeps the new name from ever landing on
// zero-length content. Both callers re-read the file on the next launch,
// so a reverted rename shows up as "the setting did not stick", and the
// extra fsync of the directory would cost every settings toggle a disk
// flush to close that window.
//
// Every failure leg removes the temp file — a stray `.<name>-*.tmp` beside
// the target would otherwise outlive the crash it was meant to protect
// against. The temp file lives in the target's directory because rename is
// atomic only within one filesystem. The directory must already exist:
// creating it is the caller's decision (config creates its own private
// one), and a typo'd path must not gain a directory.
//
// A symlinked path is written through, not replaced. Operators keep the
// files this writes in a dotfiles repo behind a symlink (stow, chezmoi,
// `ln -s`), and renaming the temp file over the link would swap the link
// for a regular file, severing the dotfiles on every rewrite, which the
// plain os.WriteFile this replaced never did. So
// when path is a symlink it is resolved with EvalSymlinks and the resolved
// file is what gets staged beside and renamed over. A dangling link is an
// error rather than a fresh file: creating a regular file at the link's
// path is the same severing, and inventing the target's directory writes
// where nobody asked. A path that does not exist at all stays as given, so
// a fresh file lands exactly where the caller named it.
func Write(path string, body []byte, perm os.FileMode) error {
	return writeFrom(path, perm, func(w io.Writer) error {
		_, err := w.Write(body)
		return err
	})
}

// writeFrom is Write with the body streamed by a callback, so a test can
// fail midway through the write and prove the previous file survives.
// Write itself just hands over a finished body.
func writeFrom(path string, perm os.FileMode, write func(w io.Writer) error) error {
	target, err := resolveTarget(path)
	if err != nil {
		return err
	}
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+"-*.tmp")
	if err != nil {
		// Name both: the caller knows path, but through a symlink the
		// directory that refused the temp file can sit in another tree.
		return fmt.Errorf("create temp file for %s in %s: %w", path, dir, err)
	}
	tmpPath := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s %s: %w", step, path, err)
	}
	if err := write(tmp); err != nil {
		return fail("write", err)
	}
	// CreateTemp opens 0o600 and a umask can only clear bits from that, so
	// for the 0o600 callers this is belt and braces: set perm explicitly in
	// case a platform's CreateTemp decides otherwise. Every caller so far
	// writes something private, so a looser default must not leak through.
	if err := tmp.Chmod(perm); err != nil {
		return fail("chmod", err)
	}
	// Sync before rename: on a power loss, an unsynced rename can land the
	// new name on zero-length content — exactly the empty file this exists
	// to prevent.
	if err := tmp.Sync(); err != nil {
		return fail("sync", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, target); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// resolveTarget returns the file writeFrom should stage beside and rename
// over: path itself unless path is a symlink, in which case the fully
// resolved target. See Write for why a link is written through and why a
// dangling one fails instead of being overwritten.
func resolveTarget(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
		return "", fmt.Errorf("stat %s: %w", path, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return path, nil
	}
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve symlink %s: %w", path, err)
	}
	return target, nil
}
