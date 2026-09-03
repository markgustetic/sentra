package policy

import (
	"path/filepath"
	"strings"

	"github.com/markgustetic/sentra/internal/repo"
)

// NormalizePath resolves a policy path the way the walker records
// snapshot roots (filepath.Abs + Clean), expanding a leading ~ against
// home, so policy paths and SnapshotInfo.Root compare equal.
func NormalizePath(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// HasPolicyTag reports whether the space-joined tag string carries the
// exact token "policy:<name>" — token equality, not substring, so
// "policy:home" never matches policy "hom" or tag "my-policy:home".
func HasPolicyTag(tag, name string) bool {
	want := "policy:" + name
	for _, f := range strings.Fields(tag) {
		if f == want {
			return true
		}
	}
	return false
}

// LastRun is the policy's most recent run: the newest snapshot tagged
// policy:<name> anywhere (a run is a run, even for a path since edited
// out), falling back to the newest snapshot rooted at any of pathsAbs
// (already NormalizePath'd) when the policy has never run under its own
// tag — the backup wizard's first run goes out under the user's tag.
// Both the TUI's Last-run column and `policy run --if-due` answer from
// this one definition, so what the operator sees as the last run is
// exactly what the catch-up logic measures against.
func LastRun(name string, pathsAbs []string, snaps []repo.SnapshotInfo) (repo.SnapshotInfo, bool) {
	var best repo.SnapshotInfo
	found := false
	for _, s := range snaps {
		if HasPolicyTag(s.Tag, name) && (!found || s.CreatedAt.After(best.CreatedAt)) {
			best, found = s, true
		}
	}
	if found {
		return best, true
	}
	roots := make(map[string]bool, len(pathsAbs))
	for _, p := range pathsAbs {
		roots[p] = true
	}
	for _, s := range snaps {
		if roots[s.Root] && (!found || s.CreatedAt.After(best.CreatedAt)) {
			best, found = s, true
		}
	}
	return best, found
}
