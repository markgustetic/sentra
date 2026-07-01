package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
)

// ErrEmptyDest is returned by SyncTo when the destination has no
// `config` blob and InitDest was not set. The contract is "explicit
// opt-in to bootstrap a fresh mirror" — a footgun mitigation that
// stops a typo'd --dst-config from silently spreading the wrapped
// repo key to a wrong bucket.
var ErrEmptyDest = errors.New("repo: destination has no config blob (pass InitDest=true to bootstrap a new mirror)")

// ErrDifferentRepo is returned by SyncTo when the destination
// already has a `config` blob whose repo ID does NOT match the
// source's. Sync is a clone tool, not a merge tool — mixing two
// distinct repositories' data into one bucket has no defined
// semantic and is refused before any writes occur.
var ErrDifferentRepo = errors.New("repo: destination contains a different repository (refusing to mix data)")

// ErrSameSrcAndDst is returned when SyncTo is called with the
// same store on both sides. That would deadlock on the dest lock
// (which the source's open Repo is using on read-only operations
// elsewhere) and is structurally a no-op anyway, so we refuse it
// up front with a clear message.
var ErrSameSrcAndDst = errors.New("repo: source and destination resolve to the same store")

// SyncOptions tunes a SyncTo call. Zero value is valid: not
// InitDest, not DryRun, default concurrency, no progress callback.
type SyncOptions struct {
	// InitDest, when true, allows SyncTo to bootstrap an empty
	// destination by copying the source's config first. Required
	// (per design Q3=B) for a fresh dest; refused otherwise to
	// catch typo'd --dst-config flags before they spread the
	// wrapped repo key to an unintended bucket.
	InitDest bool

	// DryRun, when true, lists what would be copied and returns a
	// SyncStats with realistic counts but performs no writes on
	// the destination beyond the lock acquire/release. Useful for
	// pre-flight sanity checks on first-time syncs of large repos.
	DryRun bool

	// Concurrency caps parallel transfers per phase. Zero falls
	// back to runtime.GOMAXPROCS(0) — same default as concurrent
	// restore. Negative values are clamped to 1.
	Concurrency int

	// Progress receives Add(blob_size) for each blob copied. Total
	// is called once at the start with the estimated total bytes
	// (sum of source's data/ blob sizes — manifests are small and
	// excluded from the estimate). Nil is a no-op (NopReporter).
	Progress progress.Reporter
}

// SyncStats summarizes a SyncTo run. All fields are populated even
// on a partial-failure return so the caller has accurate accounting.
type SyncStats struct {
	// Bootstrapped is true when SyncTo copied source's config to a
	// previously-empty destination (InitDest mode firing).
	Bootstrapped bool

	// CopiedBlobs is the number of distinct blobs (chunks +
	// manifests + config-bootstrap if applicable) actually written
	// to dest. In DryRun this is "would have been written."
	CopiedBlobs int

	// CopiedBytes is the wire-size of the blobs that were copied.
	// Matches sealed-blob byte counts, not plaintext size.
	CopiedBytes int64

	// SkippedBlobs is the count of source blobs already present on
	// dest (the dedup-on-resume path). Equals (source-set minus
	// dest-set's intersection with source-set) prior to this sync.
	SkippedBlobs int

	// DryRun mirrors SyncOptions.DryRun.
	DryRun bool

	// Elapsed is the wall time SyncTo spent. Useful for cron logs.
	Elapsed time.Duration
}

// SyncTo copies every snapshot, chunk, and (on InitDest) the config
// from r to dest. Additive: never deletes anything on dest.
//
// Lock contract: dest acquires meta/lock for the duration of the
// sync; source is untouched at the lock level. This matches
// restic copy / standard replication semantics — locking source
// for hours during a large first-sync would block production
// backup activity for no defensible benefit.
//
// Phase order: data/ first, then snapshots/. This guarantees that
// at every observable moment, every dest manifest's chunks already
// exist on dest. Restoring from dest mid-sync is therefore safe
// for any snapshot dest already has — partial sync states never
// produce dangling-manifest restore failures.
//
// Errors:
//   - ErrEmptyDest: dest has no config + InitDest=false
//   - ErrDifferentRepo: dest's config has a different repo ID
//   - ErrSameSrcAndDst: r.Store() and dest are the same instance
//   - ErrRepoLocked: dest's meta/lock is held by another op
//   - ErrConfigTampered: source's config MAC fails (refuse to replicate tampered data)
//   - ErrClosed: r has been Close'd
//   - any blobstore transport error from either endpoint
func (r *Repo) SyncTo(ctx context.Context, dest blobstore.Store, opts SyncOptions) (SyncStats, error) {
	start := time.Now()
	stats := SyncStats{DryRun: opts.DryRun}

	// keyOrErr enforces the post-Close contract every other repo
	// method uses. The defensive copy is zeroized immediately —
	// SyncTo doesn't touch the repo key (everything is opaque
	// byte transfer) but we still want a clean post-Close failure
	// rather than a misleading EmptyDest or DifferentRepo.
	k, err := r.keyOrErr()
	if err != nil {
		return stats, err
	}
	crypto.Zeroize(k)

	// Refuse self-sync up front. Without this guard, the dest-lock
	// acquire would succeed (dest store IS the open Repo's source),
	// then any read of dest's config would see source's config
	// (same store), then a write to dest's data/ would land in
	// source's data/ — no-op at best, surprising at worst.
	if dest == r.store {
		return stats, ErrSameSrcAndDst
	}

	// Determine bootstrap vs incremental by looking for dest's
	// config blob. We do this BEFORE acquiring the dest lock so a
	// fail-fast refusal (ErrEmptyDest, ErrDifferentRepo) doesn't
	// leave a temporary lock blob behind.
	bootstrap, err := r.classifyDest(ctx, dest, opts.InitDest)
	if err != nil {
		return stats, err
	}
	stats.Bootstrapped = bootstrap

	// Acquire dest's lock. Source is intentionally NOT locked.
	// Even DryRun acquires the lock so a concurrent destructive
	// op (GC, passwd) on dest can't race the listings we're
	// about to issue; the lock is released on return either way.
	// Local var is `heldLock` (not `lockInfo`) because `lockInfo`
	// is the unexported type name in this package; reusing it as
	// a local would shadow the type.
	heldLock, err := acquireLock(ctx, dest, "sync")
	if err != nil {
		return stats, err
	}
	defer releaseLock(ctx, dest, heldLock)

	// On InitDest bootstrap, write source's config to dest first.
	// This MUST happen before any chunks/manifests so an interrupted
	// sync's dest is either fully unconfigured (no config, no data)
	// or in a self-consistent partial state (config + some data, no
	// manifests yet). Either is recoverable on resume.
	if bootstrap && !opts.DryRun {
		if err := r.copyConfig(ctx, dest); err != nil {
			return stats, fmt.Errorf("repo: bootstrap dest config: %w", err)
		}
		stats.CopiedBlobs++
		// We don't know the config's exact size cheaply (would
		// require a re-stat), but it's tiny — under a KiB. Don't
		// bother adding to CopiedBytes; it's noise relative to the
		// chunk-byte totals operators actually care about.
	}

	concurrency := resolveConcurrency(opts.Concurrency)

	reporter := opts.Progress
	if reporter == nil {
		reporter = progress.NopReporter{}
	}

	// Plan both phases up front (list only, no writes) so we can report a
	// single combined progress Total that spans data/ + snapshots/. Setting
	// Total once — rather than per phase — keeps the aggregate bar monotonic
	// instead of resetting to the small manifest total after the data phase.
	dataPlan, err := planSyncPhase(ctx, r.store, dest, DataPrefix)
	if err != nil {
		stats.Elapsed = time.Since(start)
		return stats, fmt.Errorf("repo: sync data/: %w", err)
	}
	manPlan, err := planSyncPhase(ctx, r.store, dest, snapshotPrefix)
	if err != nil {
		stats.Elapsed = time.Since(start)
		return stats, fmt.Errorf("repo: sync snapshots/: %w", err)
	}
	reporter.Total(dataPlan.totalBytes + manPlan.totalBytes)

	// Phase 1: data/ blobs (chunks). Copy every source key under
	// data/ that isn't already on dest.
	dataCopied, dataBytes, dataSkipped, err := runSyncPhase(ctx, r.store, dest,
		dataPlan, concurrency, opts.DryRun, reporter)
	stats.CopiedBlobs += dataCopied
	stats.CopiedBytes += dataBytes
	stats.SkippedBlobs += dataSkipped
	if err != nil {
		stats.Elapsed = time.Since(start)
		return stats, fmt.Errorf("repo: sync data/: %w", err)
	}

	// Phase 2: snapshots/ blobs (manifests). At this point every
	// chunk a Phase-2 manifest references already exists on dest.
	manCopied, manBytes, manSkipped, err := runSyncPhase(ctx, r.store, dest,
		manPlan, concurrency, opts.DryRun, reporter)
	stats.CopiedBlobs += manCopied
	stats.CopiedBytes += manBytes
	stats.SkippedBlobs += manSkipped
	if err != nil {
		stats.Elapsed = time.Since(start)
		return stats, fmt.Errorf("repo: sync snapshots/: %w", err)
	}

	stats.Elapsed = time.Since(start)
	return stats, nil
}

// classifyDest stat's the destination's config key and decides
// what to do:
//
//   - dest has no config + InitDest=true   → return (true, nil)  — bootstrap
//   - dest has no config + InitDest=false  → return (false, ErrEmptyDest)
//   - dest has matching config             → return (false, nil)  — incremental
//   - dest has different repo ID           → return (false, ErrDifferentRepo)
//
// The match is on `RepoConfig.ID`, not the wrapped key — same
// repo ID is what makes dest a clone of r. Different IDs mean
// these are independent repos that happen to share a bucket
// namespace, which sync isn't designed to merge.
func (r *Repo) classifyDest(ctx context.Context, dest blobstore.Store, initDest bool) (bootstrap bool, err error) {
	rc, err := dest.Get(ctx, configKey)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			if !initDest {
				return false, ErrEmptyDest
			}
			return true, nil
		}
		return false, fmt.Errorf("repo: stat dest config: %w", err)
	}
	defer rc.Close()
	raw, err := io.ReadAll(rc)
	if err != nil {
		return false, fmt.Errorf("repo: read dest config: %w", err)
	}
	var dstCfg RepoConfig
	if err := json.Unmarshal(raw, &dstCfg); err != nil {
		// Existing config blob that won't decode cleanly is a
		// foreign object at the canonical key — refuse rather
		// than overwrite. The operator can manually delete the
		// stale key if they're sure they want to sync there.
		return false, fmt.Errorf("repo: dest config exists but does not decode: %w", err)
	}
	srcID := r.Config().ID // RLocks cfgMu; safe against a concurrent Passwd
	if dstCfg.ID != srcID {
		return false, fmt.Errorf("%w: source=%q dest=%q", ErrDifferentRepo, srcID, dstCfg.ID)
	}
	return false, nil
}

// copyConfig copies the source's `config` blob to the destination
// verbatim. Used only on InitDest bootstrap.
//
// We re-marshal r.cfg rather than ferrying raw bytes from
// srcStore.Get to dest.Put because the in-memory r.cfg already
// carries the latest config (e.g., post-passwd) which is what we
// want dest to mirror. Marshal output is deterministic for a
// given struct, so dest's MAC verification works on the same
// canonicalization the source's verifyConfig uses.
func (r *Repo) copyConfig(ctx context.Context, dest blobstore.Store) error {
	cfg := r.Config() // RLocks cfgMu; safe against a concurrent Passwd
	raw, err := json.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := dest.Put(ctx, configKey, bytes.NewReader(raw)); err != nil {
		return fmt.Errorf("put config: %w", err)
	}
	return nil
}

// syncPhasePlan is the listing result for one prefix: the source entries
// to consider, the set of keys dest already has, and the total sealed
// bytes that would be copied (entries already on dest excluded). Planning
// is separated from execution so SyncTo can sum the byte totals across ALL
// phases and report a single combined progress Total up front — resetting
// Total per phase would drop it below the bytes an earlier phase already
// reported as done, pinning the aggregate bar at an overshoot.
type syncPhasePlan struct {
	prefix     string
	srcEntries []blobstore.Info
	dstSet     map[string]struct{}
	totalBytes int64
}

// planSyncPhase lists src and dest under prefix and computes what would be
// copied. It performs no writes.
func planSyncPhase(ctx context.Context, src, dst blobstore.Store, prefix string) (syncPhasePlan, error) {
	srcEntries, err := src.List(ctx, prefix)
	if err != nil {
		return syncPhasePlan{}, fmt.Errorf("list src %s: %w", prefix, err)
	}
	// Build a set of dest keys for O(1) membership lookup. List is the
	// cheapest call available; doing it once and caching the set is
	// dramatically cheaper than Stat-per-key.
	dstEntries, err := dst.List(ctx, prefix)
	if err != nil {
		return syncPhasePlan{}, fmt.Errorf("list dst %s: %w", prefix, err)
	}
	dstSet := make(map[string]struct{}, len(dstEntries))
	for _, info := range dstEntries {
		dstSet[info.Key] = struct{}{}
	}
	// Source's List returns the sealed-blob byte size on each entry, which
	// is what dst.Put will write — so the estimate is accurate.
	var total int64
	for _, info := range srcEntries {
		if _, has := dstSet[info.Key]; has {
			continue
		}
		total += info.Size
	}
	return syncPhasePlan{prefix: prefix, srcEntries: srcEntries, dstSet: dstSet, totalBytes: total}, nil
}

// runSyncPhase copies every planned key on src to dest, skipping any key
// dest already has. It reports per-blob progress via reporter.Add but does
// NOT touch reporter.Total — SyncTo owns the combined total.
//
// Concurrency: bounded errgroup with limit. Each goroutine reads one src
// blob and writes it to dest. RetryStore composition is transparent — if
// either endpoint is wrapped, transient errors retry below this layer.
func runSyncPhase(
	ctx context.Context,
	src, dst blobstore.Store,
	plan syncPhasePlan,
	concurrency int,
	dryRun bool,
	reporter progress.Reporter,
) (copied int, copiedBytes int64, skipped int, err error) {
	if dryRun {
		// Tally and return without writes.
		for _, info := range plan.srcEntries {
			if _, has := plan.dstSet[info.Key]; has {
				skipped++
				continue
			}
			copied++
			copiedBytes += info.Size
		}
		return copied, copiedBytes, skipped, nil
	}

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(concurrency)

	var copiedAtomic atomic.Int64
	var copiedBytesAtomic atomic.Int64
	var skippedAtomic atomic.Int64

	for _, info := range plan.srcEntries {
		info := info
		if _, has := plan.dstSet[info.Key]; has {
			skippedAtomic.Add(1)
			continue
		}
		g.Go(func() error {
			if err := gctx.Err(); err != nil {
				return err
			}
			if err := copyBlob(gctx, src, dst, info.Key); err != nil {
				return fmt.Errorf("copy %s: %w", info.Key, err)
			}
			copiedAtomic.Add(1)
			copiedBytesAtomic.Add(info.Size)
			reporter.Add(info.Size)
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return int(copiedAtomic.Load()), copiedBytesAtomic.Load(), int(skippedAtomic.Load()), err
	}
	return int(copiedAtomic.Load()), copiedBytesAtomic.Load(), int(skippedAtomic.Load()), nil
}

// copyBlob streams one blob from src to dst. We use io.ReadAll
// because every blob sentra writes is bounded (chunks ≤ ~few MiB,
// manifests ≤ ~tens of MiB) — buffering in RAM is cheap and lets
// us pass a *bytes.Reader to dst.Put which the underlying store
// can re-read on RetryStore retries.
//
// A streaming variant (io.Pipe between Get and Put) would avoid
// the buffer at the cost of breaking RetryStore's retry-on-error
// contract: a streaming Put can't be replayed. The buffer is the
// right tradeoff at sentra's blob sizes.
func copyBlob(ctx context.Context, src, dst blobstore.Store, key string) error {
	rc, err := src.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("get src: %w", err)
	}
	defer rc.Close()
	body, err := io.ReadAll(rc)
	if err != nil {
		return fmt.Errorf("read src: %w", err)
	}
	if err := dst.Put(ctx, key, bytes.NewReader(body)); err != nil {
		return fmt.Errorf("put dst: %w", err)
	}
	return nil
}

// (sync.go used to define its own acquireLockOnStore / releaseLockOnStore
// helpers — a near-clone of the methods on *Repo. Both implementations
// drifted in subtle ways, notably the release path's slog handling.
// They've now been consolidated into the free functions acquireLock /
// releaseLock in lock.go; SyncTo just calls those directly.)
