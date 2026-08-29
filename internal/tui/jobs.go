package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
)

// normalizeJobPath resolves a policy path the way the walker records
// snapshot roots (filepath.Abs + Clean), expanding a leading ~ against
// home, so job paths and SnapshotInfo.Root compare equal.
func normalizeJobPath(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// hasPolicyTag reports whether the space-joined tag string carries the
// exact token "policy:<name>" — token equality, not substring, so
// "policy:home" never matches job "hom" or tag "my-policy:home".
func hasPolicyTag(tag, name string) bool {
	want := "policy:" + name
	for _, f := range strings.Fields(tag) {
		if f == want {
			return true
		}
	}
	return false
}

// newestJobSnapshot is the drill-in resolver: the newest snapshot rooted
// at pathAbs, preferring ones tagged policy:<name> — an ad-hoc backup of
// the same directory must not shadow the job's own snapshots, but with
// no tagged snapshot yet (the ctrl+e repeat flow's first backup runs
// under the user's tag) any snapshot of the path is the honest answer.
func newestJobSnapshot(name, pathAbs string, snaps []repo.SnapshotInfo) (repo.SnapshotInfo, bool) {
	var best repo.SnapshotInfo
	var found, tagged bool
	for _, s := range snaps {
		if s.Root != pathAbs {
			continue
		}
		st := hasPolicyTag(s.Tag, name)
		switch {
		case st && !tagged:
			best, found, tagged = s, true, true
		case st == tagged && (!found || s.CreatedAt.After(best.CreatedAt)):
			best, found = s, true
		}
	}
	return best, found
}

// lastJobRun is the Last-run column: the newest snapshot tagged
// policy:<name> anywhere (a run is a run, even for a path since edited
// out), falling back to the newest snapshot rooted at any of the job's
// paths when the job has never run under its own tag.
func lastJobRun(name string, pathsAbs []string, snaps []repo.SnapshotInfo) (repo.SnapshotInfo, bool) {
	var best repo.SnapshotInfo
	found := false
	for _, s := range snaps {
		if hasPolicyTag(s.Tag, name) && (!found || s.CreatedAt.After(best.CreatedAt)) {
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

// relAge renders a compact "how long ago" for the Last-run column.
func relAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
