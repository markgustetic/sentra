package repo

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

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
}

// Healthy reports whether the repo can be trusted for restore and
// mutation based on the issues Check found.
func (r CheckReport) Healthy() bool {
	if len(r.MissingBlobs) > 0 || len(r.ManifestIssues) > 0 {
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
	repoKey, err := r.keyOrErr()
	if err != nil {
		return CheckReport{}, err
	}
	crypto.Zeroize(repoKey)

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
