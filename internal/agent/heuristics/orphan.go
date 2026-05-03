package heuristics

import (
	"context"
	"fmt"
)

// dataPrefix is the blobstore key prefix where chunk blobs live. Kept
// in sync with the constant of the same name in internal/repo, but
// duplicated here to avoid forcing the repo package to export it.
// The value is part of the on-disk format; if it ever changes, this
// constant must be updated too.
const dataPrefix = "data/"

// OrphanBlobs flags blobs present in the store under "data/" but
// absent from the LiveBlobs set. The intent is to surface storage
// that GC would reclaim — usually a sign of an interrupted backup or
// a crash mid-way through CreateSnapshot.
//
// LiveBlobs is computed by the caller (Phase 11 orchestrator) by
// loading every current manifest's chunk references. The caller
// passes the chunk-key form ("data/<aa>/<hex>"), matching what
// blobstore.List returns — so the membership test is a direct map
// lookup, no key-format conversion needed.
type OrphanBlobs struct{}

// NewOrphanBlobs constructs an OrphanBlobs heuristic.
func NewOrphanBlobs() *OrphanBlobs { return &OrphanBlobs{} }

// Name is the registry-visible name of this heuristic.
func (o *OrphanBlobs) Name() string { return "orphan_blobs" }

// Run lists every key under data/ in the repo's store and emits one
// warn finding per key not present in in.LiveBlobs.
//
// Behavior on missing inputs:
//   - in.Repo == nil: returns (nil, nil). The heuristic can't meaningfully
//     run without store access; this matches the "registry stitches the
//     orchestrator together" pattern where some inputs may be absent for
//     specific run modes (e.g. a "scan walk only" CLI shortcut).
//   - in.LiveBlobs == nil: treated as an empty set. Every data/ entry
//     becomes an orphan, which is correct: if the caller had no way to
//     compute the live set, treating everything as live would mask real
//     orphans.
func (o *OrphanBlobs) Run(ctx context.Context, in Input) ([]Finding, error) {
	if in.Repo == nil {
		return nil, nil
	}
	store := in.Repo.Store()
	entries, err := store.List(ctx, dataPrefix)
	if err != nil {
		return nil, fmt.Errorf("orphan_blobs: list %s: %w", dataPrefix, err)
	}

	var out []Finding
	for _, info := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, live := in.LiveBlobs[info.Key]; live {
			continue
		}
		out = append(out, Finding{
			ID:       makeFindingID("orphan_blobs", info.Key),
			Category: "orphan_blobs",
			Severity: SeverityWarn,
			Target:   info.Key,
			Details: map[string]any{
				"size": info.Size,
			},
		})
	}
	return out, nil
}
