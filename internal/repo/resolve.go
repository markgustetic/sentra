package repo

import (
	"context"
	"fmt"
	"strings"
)

// ResolveSnapshotID turns an operator-typed snapshot reference into a
// full snapshot ID. Accepted forms, in resolution order:
//
//   - "latest"        — the newest snapshot in the repo
//   - a full valid ID — returned as-is without touching the store
//   - a unique prefix — e.g. "snap-20260729"
//   - a unique suffix — e.g. the trailing "f9f4ae6f" hex a listing shows
//
// A reference matching more than one snapshot is refused with the
// candidates named (never first-match: restoring "some snapshot that
// happened to sort first" is exactly the surprise this exists to
// prevent). Matching costs one ListSnapshots — an O(1) index read.
func (r *Repo) ResolveSnapshotID(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("repo: empty snapshot reference")
	}
	if validateSnapshotID(ref) == nil {
		return ref, nil
	}

	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return "", fmt.Errorf("repo: resolve %q: %w", ref, err)
	}
	if ref == "latest" {
		if len(snaps) == 0 {
			return "", fmt.Errorf("repo: resolve %q: repository has no snapshots", ref)
		}
		return snaps[0].ID, nil // ListSnapshots is newest-first
	}

	var matches []string
	for _, s := range snaps {
		if strings.HasPrefix(s.ID, ref) || strings.HasSuffix(s.ID, ref) {
			matches = append(matches, s.ID)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return "", fmt.Errorf("repo: no snapshot matches %q (try `sentra snapshots`)", ref)
	default:
		shown := matches
		more := ""
		if len(shown) > 5 {
			shown = shown[:5]
			more = fmt.Sprintf(" and %d more", len(matches)-len(shown))
		}
		return "", fmt.Errorf("repo: snapshot reference %q is ambiguous: matches %s%s",
			ref, strings.Join(shown, ", "), more)
	}
}
