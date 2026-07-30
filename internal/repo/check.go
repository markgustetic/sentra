package repo

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

// CheckOptions tunes a repository integrity check.
type CheckOptions struct {
	// Now is used for age calculations. Zero means time.Now().UTC().
	Now time.Time

	// StaleLockAfter is the age at which an advisory lock becomes a
	// health issue. Zero uses a conservative 24-hour default.
	StaleLockAfter time.Duration

	// ReadData additionally downloads, decrypts, decompresses, and
	// re-hashes referenced chunks instead of only Stat-ing presence —
	// the only check that proves the repo is actually restorable.
	// Costs one GET per verified chunk (S3 egress); bound it with
	// ReadDataSubset on large repos.
	ReadData bool

	// ReadDataSubset caps the deep verify at a fraction (0, 1) of the
	// referenced chunks, chosen deterministically (sorted-hash
	// stride) so repeated runs verify the same spread rather than a
	// random sample that can miss the same corrupt blob forever or
	// double-bill overlapping ones. Zero (or >= 1) verifies
	// everything. Ignored unless ReadData is set.
	ReadDataSubset float64
}

// BlobIssue describes a blobstore object that check classified as
// waste or otherwise worth surfacing.
type BlobIssue struct {
	Key  string `json:"key"`
	Size int64  `json:"size"`
}

// MissingBlob describes a data blob referenced by a snapshot manifest
// that is absent from the backing store.
type MissingBlob struct {
	Key        string `json:"key"`
	Hash       string `json:"hash"`
	SnapshotID string `json:"snapshot_id"`
	Path       string `json:"path"`
}

// ManifestIssue describes a snapshot manifest that could not be
// loaded, decoded, or trusted enough for chunk verification.
type ManifestIssue struct {
	Key        string `json:"key"`
	SnapshotID string `json:"snapshot_id,omitempty"`
	Error      string `json:"error"`
}

// LockReport describes the repo-wide advisory lock when one is
// present. A stale or unreadable lock blocks mutating operations and
// therefore makes CheckReport.Healthy return false.
type LockReport struct {
	Present    bool          `json:"present"`
	Stale      bool          `json:"stale"`
	Unreadable bool          `json:"unreadable"`
	Error      string        `json:"error,omitempty"`
	UUID       string        `json:"uuid,omitempty"`
	Operation  string        `json:"operation,omitempty"`
	Host       string        `json:"host,omitempty"`
	PID        int           `json:"pid,omitempty"`
	StartedAt  time.Time     `json:"started_at,omitempty"`
	Age        time.Duration `json:"age"`
}

// CheckReport is the structured result of a repository integrity
// audit. Missing blobs, unreadable manifests, and stale/unreadable
// locks are health failures. Orphan blobs are warnings: they waste
// storage but do not prevent a restore from succeeding.
type CheckReport struct {
	CheckedAt       time.Time       `json:"checked_at"`
	Snapshots       int             `json:"snapshots"`
	Files           int             `json:"files"`
	Bytes           int64           `json:"bytes"`
	ReferencedBlobs int             `json:"referenced_blobs"`
	DataBlobs       int             `json:"data_blobs"`
	DataBytes       int64           `json:"data_bytes"`
	OrphanBytes     int64           `json:"orphan_bytes"`
	MissingBlobs    []MissingBlob   `json:"missing_blobs"`
	OrphanBlobs     []BlobIssue     `json:"orphan_blobs"`
	ManifestIssues  []ManifestIssue `json:"manifest_issues"`
	Lock            *LockReport     `json:"lock,omitempty"`

	// ReadDataBlobs is how many referenced chunks the deep verify
	// downloaded and re-hashed (0 when ReadData was off).
	ReadDataBlobs int `json:"read_data_blobs,omitempty"`
	// CorruptBlobs are referenced chunks that exist but failed the
	// deep verify — undecryptable, undecompressable, or hashing to a
	// different address than they're stored under. Restore of any
	// snapshot referencing one WILL fail; a health failure.
	CorruptBlobs []CorruptBlob `json:"corrupt_blobs,omitempty"`
}

// CorruptBlob is one deep-verify failure: the chunk is present but its
// content cannot be trusted.
type CorruptBlob struct {
	Key   string `json:"key"`
	Hash  string `json:"hash"`
	Error string `json:"error"`
}

// Healthy reports whether the repo can be trusted for restore and
// mutation based on the issues Check found.
func (r CheckReport) Healthy() bool {
	if len(r.MissingBlobs) > 0 || len(r.ManifestIssues) > 0 || len(r.CorruptBlobs) > 0 {
		return false
	}
	if r.Lock != nil && (r.Lock.Stale || r.Lock.Unreadable) {
		return false
	}
	return true
}

// Check verifies the readable snapshot manifests, confirms every
// referenced data blob exists, detects unreferenced data blobs, and
// reports an advisory lock that appears stale.
func (r *Repo) Check(ctx context.Context, opts CheckOptions) (CheckReport, error) {
	// The key stays alive for the whole call (deferred zeroize, not
	// immediate): the deep verify decrypts chunk bodies. Presence-only
	// runs never touch it after the manifest loads.
	repoKey, err := r.keyOrErr()
	if err != nil {
		return CheckReport{}, err
	}
	defer crypto.Zeroize(repoKey)

	now := opts.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if opts.StaleLockAfter == 0 {
		opts.StaleLockAfter = 24 * time.Hour
	}

	report := CheckReport{CheckedAt: now}

	lock, err := r.checkLock(ctx, now, opts.StaleLockAfter)
	if err != nil {
		return CheckReport{}, err
	}
	report.Lock = lock

	snapshotObjects, err := r.store.List(ctx, snapshotPrefix)
	if err != nil {
		return CheckReport{}, fmt.Errorf("repo: list snapshots: %w", err)
	}
	slices.SortFunc(snapshotObjects, func(a, b blobstore.Info) int {
		return cmp.Compare(a.Key, b.Key)
	})

	referenced := make(map[string]struct{})
	missingSeen := make(map[string]struct{})
	var hashes []string // unique referenced chunk hashes, for --read-data
	for _, obj := range snapshotObjects {
		id := strings.TrimPrefix(obj.Key, snapshotPrefix)
		if id == "" || id == obj.Key {
			report.ManifestIssues = append(report.ManifestIssues, ManifestIssue{
				Key:   obj.Key,
				Error: "invalid snapshot object key",
			})
			continue
		}
		if err := validateSnapshotID(id); err != nil {
			report.ManifestIssues = append(report.ManifestIssues, ManifestIssue{
				Key:        obj.Key,
				SnapshotID: id,
				Error:      err.Error(),
			})
			continue
		}

		m, err := r.LoadSnapshot(ctx, id)
		if err != nil {
			report.ManifestIssues = append(report.ManifestIssues, ManifestIssue{
				Key:        obj.Key,
				SnapshotID: id,
				Error:      err.Error(),
			})
			continue
		}

		report.Snapshots++
		report.Files += m.Stats.Files
		report.Bytes += m.Stats.Bytes

		for _, fe := range m.Tree {
			for _, hash := range fe.Chunks {
				key := ChunkKey(hash)
				if _, seen := referenced[key]; !seen {
					hashes = append(hashes, hash)
				}
				referenced[key] = struct{}{}
				if _, dup := missingSeen[key]; dup {
					continue
				}
				if _, err := r.store.Stat(ctx, key); err != nil {
					if errors.Is(err, blobstore.ErrNotFound) {
						missingSeen[key] = struct{}{}
						report.MissingBlobs = append(report.MissingBlobs, MissingBlob{
							Key:        key,
							Hash:       hash,
							SnapshotID: m.ID,
							Path:       fe.Path,
						})
						continue
					}
					return CheckReport{}, fmt.Errorf("repo: stat %s: %w", key, err)
				}
			}
		}
	}
	report.ReferencedBlobs = len(referenced)

	if opts.ReadData {
		if err := r.checkReadData(ctx, repoKey, hashes, missingSeen, opts.ReadDataSubset, &report); err != nil {
			return CheckReport{}, err
		}
	}

	dataObjects, err := r.store.List(ctx, DataPrefix)
	if err != nil {
		return CheckReport{}, fmt.Errorf("repo: list data: %w", err)
	}
	slices.SortFunc(dataObjects, func(a, b blobstore.Info) int {
		return cmp.Compare(a.Key, b.Key)
	})
	report.DataBlobs = len(dataObjects)
	for _, obj := range dataObjects {
		size, err := lookupSize(ctx, r.store, obj)
		if err != nil {
			return CheckReport{}, fmt.Errorf("repo: stat %s: %w", obj.Key, err)
		}
		report.DataBytes += size
		if _, ok := referenced[obj.Key]; !ok {
			report.OrphanBlobs = append(report.OrphanBlobs, BlobIssue{
				Key:  obj.Key,
				Size: size,
			})
			report.OrphanBytes += size
		}
	}

	return report, nil
}

// checkReadData deep-verifies referenced chunks: download, decrypt,
// decompress, re-hash (fetchChunk — the exact read path restore
// uses). A failing chunk is recorded as a finding, never an abort, so
// one bad blob doesn't hide the rest of the report. Already-missing
// chunks are skipped; they're reported separately.
//
// Sampling is a stride over the SORTED hash list: deterministic, so
// repeated subset runs verify the same spread — a random sample could
// miss the same corrupt blob forever while re-billing overlapping
// reads.
func (r *Repo) checkReadData(
	ctx context.Context,
	repoKey []byte,
	hashes []string,
	missing map[string]struct{},
	subset float64,
	report *CheckReport,
) error {
	slices.Sort(hashes)
	selected := hashes
	if subset > 0 && subset < 1 && len(hashes) > 0 {
		n := int(math.Ceil(float64(len(hashes)) * subset))
		if n < 1 {
			n = 1
		}
		stride := float64(len(hashes)) / float64(n)
		sel := make([]string, 0, n)
		for i := 0; i < n; i++ {
			sel = append(sel, hashes[int(float64(i)*stride)])
		}
		selected = sel
	}

	var mu sync.Mutex
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(resolveConcurrency(0))
	for _, hash := range selected {
		if _, gone := missing[ChunkKey(hash)]; gone {
			continue
		}
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			_, err := r.fetchChunk(gctx, repoKey, hash)
			mu.Lock()
			defer mu.Unlock()
			report.ReadDataBlobs++
			if err != nil {
				report.CorruptBlobs = append(report.CorruptBlobs, CorruptBlob{
					Key:   ChunkKey(hash),
					Hash:  hash,
					Error: err.Error(),
				})
			}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	slices.SortFunc(report.CorruptBlobs, func(a, b CorruptBlob) int {
		return cmp.Compare(a.Key, b.Key)
	})
	return nil
}

func (r *Repo) checkLock(ctx context.Context, now time.Time, staleAfter time.Duration) (*LockReport, error) {
	rc, err := r.store.Get(ctx, lockKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("repo: get lock: %w", err)
	}
	defer rc.Close()

	body, err := io.ReadAll(rc)
	if err != nil {
		return nil, fmt.Errorf("repo: read lock: %w", err)
	}
	var info lockInfo
	if err := json.Unmarshal(body, &info); err != nil {
		return &LockReport{
			Present:    true,
			Unreadable: true,
			Error:      err.Error(),
		}, nil
	}
	age := now.Sub(info.StartedAt)
	if age < 0 {
		age = 0
	}
	return &LockReport{
		Present:   true,
		Stale:     age >= staleAfter,
		UUID:      info.UUID,
		Operation: info.Operation,
		Host:      info.Host,
		PID:       info.PID,
		StartedAt: info.StartedAt,
		Age:       age,
	}, nil
}
