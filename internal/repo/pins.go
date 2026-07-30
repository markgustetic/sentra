package repo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
)

// ErrSnapshotPinned is returned by DeleteSnapshot when the target is
// pinned. Callers branch with errors.Is; the fix is `sentra unpin`.
var ErrSnapshotPinned = errors.New("repo: snapshot is pinned")

// pinsKey is the blobstore key for the encrypted pin set. Lives under
// meta/ beside the snapshot index; same sealed-JSON envelope.
const pinsKey = "meta/pins"

// pinSet is the wire form of the pin set. IDs are kept sorted so the
// sealed blob is deterministic for a given set.
type pinSet struct {
	Version int      `json:"version"`
	IDs     []string `json:"ids"`
}

const pinSetVersion = 1

// Pin marks a snapshot as protected: retention planning always keeps
// it (callers pass Pins() into RetentionPolicy.Pinned) and
// DeleteSnapshot refuses it. Pinning a nonexistent snapshot is an
// error — a typo'd pin that silently "protects" nothing would give
// false confidence. Serialized on the repo lock like every other
// mutation so concurrent pins can't lose each other's writes.
func (r *Repo) Pin(ctx context.Context, id string) error {
	return r.updatePins(ctx, id, func(ids []string) []string {
		if slices.Contains(ids, id) {
			return ids
		}
		ids = append(ids, id)
		slices.Sort(ids)
		return ids
	})
}

// Unpin removes a snapshot's pin. Unpinning a snapshot that isn't
// pinned is a no-op, not an error — the desired end state holds.
func (r *Repo) Unpin(ctx context.Context, id string) error {
	return r.updatePins(ctx, id, func(ids []string) []string {
		return slices.DeleteFunc(ids, func(x string) bool { return x == id })
	})
}

// Pins returns the current pin set keyed by snapshot ID. Read-only;
// no lock (same freshness contract as ListSnapshots).
func (r *Repo) Pins(ctx context.Context) (map[string]struct{}, error) {
	repoKey, err := r.keyOrErr()
	if err != nil {
		return nil, err
	}
	defer crypto.Zeroize(repoKey)
	set, err := r.loadPins(ctx, repoKey)
	if err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(set.IDs))
	for _, id := range set.IDs {
		out[id] = struct{}{}
	}
	return out, nil
}

// updatePins is the shared read-modify-write: validate the target
// exists (Pin only cares, but a cheap manifest stat hurts Unpin
// nothing... except unpinning an already-deleted snapshot must still
// work, so existence is enforced only when the mutation ADDS the id).
func (r *Repo) updatePins(ctx context.Context, id string, mutate func([]string) []string) error {
	if err := validateSnapshotID(id); err != nil {
		return err
	}
	repoKey, err := r.keyOrErr()
	if err != nil {
		return err
	}
	defer crypto.Zeroize(repoKey)

	heldLock, err := acquireLock(ctx, r.store, "pin")
	if err != nil {
		return err
	}
	defer releaseLock(ctx, r.store, heldLock)

	set, err := r.loadPins(ctx, repoKey)
	if err != nil {
		return err
	}
	next := mutate(slices.Clone(set.IDs))
	added := len(next) > len(set.IDs)
	if added {
		if _, err := r.store.Stat(ctx, snapshotPrefix+id); err != nil {
			if errors.Is(err, blobstore.ErrNotFound) {
				return fmt.Errorf("repo: cannot pin %s: snapshot does not exist", id)
			}
			return fmt.Errorf("repo: stat snapshot %s: %w", id, err)
		}
	}
	if slices.Equal(next, set.IDs) {
		return nil // no-op mutation; skip the write
	}
	return r.putSealedJSON(ctx, repoKey, pinsKey, &pinSet{Version: pinSetVersion, IDs: next}, "pins")
}

// loadPins fetches and decodes the pin set. Absent blob → empty set.
func (r *Repo) loadPins(ctx context.Context, repoKey []byte) (pinSet, error) {
	rc, err := r.store.Get(ctx, pinsKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return pinSet{Version: pinSetVersion}, nil
		}
		return pinSet{}, fmt.Errorf("repo: get pins: %w", err)
	}
	defer rc.Close()
	sealed, err := io.ReadAll(rc)
	if err != nil {
		return pinSet{}, fmt.Errorf("repo: read pins: %w", err)
	}
	compressed, err := crypto.Open(repoKey, sealed)
	if err != nil {
		return pinSet{}, fmt.Errorf("repo: decrypt pins: %w", err)
	}
	raw, err := chunker.DecompressLimit(compressed, 1<<24)
	if err != nil {
		return pinSet{}, fmt.Errorf("repo: decompress pins: %w", err)
	}
	var set pinSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return pinSet{}, fmt.Errorf("repo: unmarshal pins: %w", err)
	}
	if set.Version > pinSetVersion {
		return pinSet{}, fmt.Errorf("repo: pins format v%d is newer than this binary supports (v%d)", set.Version, pinSetVersion)
	}
	return set, nil
}
