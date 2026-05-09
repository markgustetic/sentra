package repo

import (
	"fmt"
	"path/filepath"
	"strings"
)

// safeJoinPath joins relPath under root and verifies that the result
// is lexically contained inside root. It rejects:
//
//   - absolute relPath (manifests and plans must always store
//     relative paths; an absolute input is an attempted override
//     of the root)
//   - any joined path whose root-relative form starts with ".." or
//     equals "." or "..", which would walk above (or land exactly
//     on) root
//   - empty relPath, which collapses to root itself and is caught
//     by the rel == "." check below — no separate early-exit case
//     because uniform error messages are easier to grep / log /
//     correlate across audit traces than a one-off "empty path"
//     special case
//
// root must already be absolute and clean (the caller is responsible
// for that — Restore Abs+Cleans destDir once at the top, and
// validateBackupPlanShape did so for the plan).
//
// We compare on lexical paths only and do NOT call EvalSymlinks: the
// destination tree is freshly created (or empty) before restore, so
// a symlink-based escape would require us to follow our own writes.
// Plan validation runs before any write, so the on-disk state is
// either a path the caller already trusts (their backup root) or a
// path whose symlinks we'd be auditing AGAINST our own about-to-write
// behavior — neither helps a real attacker.
//
// errLabel is folded into the error message so an operator triaging a
// failed restore can tell whether the rejection came from the restore
// path ("restore destination") or the backup-plan validator ("backup
// root") without a stack trace. The label is a free-form short
// noun-phrase chosen by the caller; %q-formatting around the relPath
// keeps quoting consistent across both error paths.
//
// History: this function consolidates two near-identical predecessors
// — safeRestorePath in restore.go and safePlanPath in backup_plan.go
// — that drifted in subtle ways (one had an early "empty path in
// manifest" message, the other rolled empty into the general escape
// path). Dual implementations of security-critical path-traversal
// logic are how CVEs happen; the single source of truth here closes
// that drift.
func safeJoinPath(root, relPath, errLabel string) (string, error) {
	if filepath.IsAbs(relPath) || strings.HasPrefix(relPath, "/") {
		return "", fmt.Errorf("repo: path %q escapes %s", relPath, errLabel)
	}
	joined := filepath.Join(root, filepath.FromSlash(relPath))
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("repo: path %q escapes %s", relPath, errLabel)
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("repo: path %q escapes %s", relPath, errLabel)
	}
	return joined, nil
}
