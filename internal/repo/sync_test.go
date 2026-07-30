package repo

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/progress"
)

// twoRepos initializes two in-memory repos under the SAME passphrase
// and returns:
//   - source: open *Repo + its underlying store
//   - dstStore: the destination's empty blobstore (no repo init yet)
//
// Each test that wants a populated source seeds snapshots after this
// call. Tests that want a populated dest can pre-populate dstStore
// directly (raw blob writes) or by calling repo.Init against it
// before invoking SyncTo. The shared passphrase models the clone
// semantic from Q1=A.
func twoRepos(t *testing.T) (src *Repo, srcStore blobstore.Store, dstStore *blobstore.Memory) {
	t.Helper()
	srcStore = blobstore.NewMemory()
	dstStore = blobstore.NewMemory()
	r, err := Init(context.Background(), srcStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init src: %v", err)
	}
	t.Cleanup(func() { r.Close() })
	return r, srcStore, dstStore
}

// seedSourceWithSnapshot creates one snapshot containing two small
// files in the source repo. Returns the snapshot ID for follow-up
// assertions.
func seedSourceWithSnapshot(t *testing.T, r *Repo, tag string) string {
	t.Helper()
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")
	snap, err := r.CreateSnapshot(context.Background(), src, SnapshotOptions{Tag: tag})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return snap.ID
}

// TestSyncTo_ProgressTotalNotResetBelowDone: SyncTo copies data/ then
// snapshots/ against the SAME reporter. It must set one combined Total up
// front rather than resetting Total per phase — otherwise the small
// manifest total in phase 2 drops below the data bytes already reported as
// done, pinning the aggregate bar at an overshoot.
func TestSyncTo_ProgressTotalNotResetBelowDone(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	// Data bytes must dwarf manifest bytes so a per-phase Total reset would
	// drop the total below the already-accumulated done.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "big.dat"), strings.Repeat("progress-payload-", 8000))
	if _, err := src.CreateSnapshot(ctx, root, SnapshotOptions{}); err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	rep := &progress.RecordingReporter{}
	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true, Progress: rep}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	total, done, _ := rep.Snapshot()
	if done > total {
		t.Errorf("progress overshoot: done=%d exceeds total=%d (Total reset below accumulated done across phases)", done, total)
	}
	if total != done {
		t.Errorf("final total=%d should equal done=%d after a full sync", total, done)
	}
}

// TestSyncTo_FreshDest_InitDestBootstraps is the headline first-sync
// contract: empty destination + InitDest=true produces a working
// clone. Verified by Open'ing the destination with the source's
// passphrase and asserting the snapshot is restorable.
func TestSyncTo_FreshDest_InitDestBootstraps(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	snapID := seedSourceWithSnapshot(t, src, "first")

	stats, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true})
	if err != nil {
		t.Fatalf("SyncTo: %v", err)
	}
	if !stats.Bootstrapped {
		t.Errorf("SyncStats.Bootstrapped: got false, want true (empty dest with --init-dest)")
	}
	if stats.CopiedBlobs == 0 {
		t.Errorf("SyncStats.CopiedBlobs: got 0, want > 0")
	}

	// Open dest with the source's passphrase — same passphrase by
	// clone semantic — and verify the snapshot is there + readable.
	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open dest after sync: %v", err)
	}
	defer dst.Close()
	if dst.Config().ID != src.Config().ID {
		t.Errorf("dest repo ID: got %q, want %q (clone semantic)",
			dst.Config().ID, src.Config().ID)
	}
	infos, err := dst.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("ListSnapshots dest: %v", err)
	}
	if len(infos) != 1 || infos[0].ID != snapID {
		t.Errorf("dest snapshots: got %+v, want one snap with id=%q", infos, snapID)
	}
}

// TestSyncTo_FreshDest_RefusesWithoutInitDest covers the safety
// rail from Q3=B: an empty destination plus default InitDest=false
// must fail without writing anything.
func TestSyncTo_FreshDest_RefusesWithoutInitDest(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "anything")

	_, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: false})
	if !errors.Is(err, ErrEmptyDest) {
		t.Fatalf("SyncTo: got %v, want ErrEmptyDest", err)
	}
	// Dest should be untouched — no config, no data, no anything.
	infos, _ := dstStore.List(ctx, "")
	if len(infos) != 0 {
		t.Errorf("dest should be untouched after refused sync, got %d objects", len(infos))
	}
}

// TestSyncTo_IncrementalCopiesOnlyMissing verifies that a second
// sync against an already-populated dest copies only the diff.
// Stats reflect copied vs skipped counts.
func TestSyncTo_IncrementalCopiesOnlyMissing(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap1")

	// First sync: bootstrap + copy everything.
	first, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if first.CopiedBlobs == 0 {
		t.Fatal("first sync copied no blobs")
	}

	// Add a second snapshot to source; everything from snap1 stays.
	srcRoot := t.TempDir()
	writeFile(t, filepath.Join(srcRoot, "c.txt"), "charlie")
	if _, err := src.CreateSnapshot(ctx, srcRoot, SnapshotOptions{Tag: "snap2"}); err != nil {
		t.Fatalf("create snap2: %v", err)
	}

	// Second sync: should copy only snap2's manifest + chunks.
	second, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: false})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if second.SkippedBlobs == 0 {
		t.Errorf("second sync should skip already-present snap1 blobs, got SkippedBlobs=0")
	}
	if second.CopiedBlobs >= first.CopiedBlobs {
		t.Errorf("second sync copied %d blobs (>= first's %d); incremental should be smaller",
			second.CopiedBlobs, first.CopiedBlobs)
	}
	if second.Bootstrapped {
		t.Errorf("second sync should not bootstrap an already-init'd dest")
	}
}

// TestSyncTo_DifferentRepoID_Refuses covers the safety rail against
// accidental sync into an unrelated existing repo. The source's
// data must NOT be written if the dest holds a different repo's
// config.
func TestSyncTo_DifferentRepoID_Refuses(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "src-snap")

	// Init the destination as a SEPARATE repo (different repo ID).
	other, err := Init(ctx, dstStore, []byte("different-passphrase"))
	if err != nil {
		t.Fatalf("init dest as different repo: %v", err)
	}
	otherID := other.Config().ID
	other.Close()

	if otherID == src.Config().ID {
		t.Fatal("test setup bug: two Inits produced the same repo ID")
	}

	_, err = src.SyncTo(ctx, dstStore, SyncOptions{InitDest: false})
	if !errors.Is(err, ErrDifferentRepo) {
		t.Fatalf("SyncTo against different repo: got %v, want ErrDifferentRepo", err)
	}
	// Dest's config must not have been overwritten with source's.
	dst2, err := Open(ctx, dstStore, []byte("different-passphrase"))
	if err != nil {
		t.Fatalf("dest still openable with its own passphrase: %v", err)
	}
	defer dst2.Close()
	if dst2.Config().ID != otherID {
		t.Errorf("dest config got rewritten: id=%q, want %q", dst2.Config().ID, otherID)
	}
}

// TestSyncTo_DataBeforeSnapshots locks the phase-ordering invariant
// from the design: at no point in time does a manifest exist on
// dest without all of its referenced chunks already existing on
// dest. We don't directly observe the in-flight ordering; instead
// we verify that AT THE END of every successful SyncTo, every
// dest manifest's chunks are present.
func TestSyncTo_DataBeforeSnapshots(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "phase-test")

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("Open dst: %v", err)
	}
	defer dst.Close()

	// For every snapshot on dest, every chunk it references must
	// also be on dest — proving Phase 1 (data) finished before any
	// Phase 2 (snapshots) blob landed.
	infos, _ := dst.ListSnapshots(ctx)
	for _, info := range infos {
		m, err := dst.LoadSnapshot(ctx, info.ID)
		if err != nil {
			t.Fatalf("load %s: %v", info.ID, err)
		}
		for _, fe := range m.Tree {
			for _, hex := range fe.Chunks {
				if _, err := dstStore.Stat(ctx, ChunkKey(hex)); err != nil {
					t.Errorf("manifest %s references chunk %s missing on dest: %v",
						info.ID, hex, err)
				}
			}
		}
	}
}

// TestSyncTo_PreservesDestExtras locks the additive contract from
// Q4=A: snapshots on dest that aren't on source are preserved.
// Concretely: pre-populate dest with a snapshot source doesn't
// have, sync, verify the extra is still there.
func TestSyncTo_PreservesDestExtras(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)

	// Stage dest as a clone-with-an-extra: open dest under the same
	// passphrase, take a snapshot there (produces a snapshot dest
	// has but source doesn't), close.
	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "dest-only.txt"), "this snapshot lives only on dst")
	destOnly, err := dst.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "dst-extra"})
	if err != nil {
		t.Fatalf("create dst-extra: %v", err)
	}
	dst.Close()

	// Now create a new source-only snapshot and re-sync.
	srcRoot := t.TempDir()
	writeFile(t, filepath.Join(srcRoot, "src-only.txt"), "from source")
	if _, err := src.CreateSnapshot(ctx, srcRoot, SnapshotOptions{Tag: "src-only"}); err != nil {
		t.Fatalf("create src-only: %v", err)
	}
	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: false}); err != nil {
		t.Fatalf("incremental sync: %v", err)
	}

	// Dest must still have its dst-extra snapshot.
	dst2, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("re-open dst: %v", err)
	}
	defer dst2.Close()
	infos, _ := dst2.ListSnapshots(ctx)
	var found bool
	for _, info := range infos {
		if info.ID == destOnly.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("dst-extra snapshot %q removed by additive sync; got %+v",
			destOnly.ID, infos)
	}
}

// TestSyncTo_AcquiresDestLock verifies the lock contract: sync
// against a destination whose meta/lock is already held fails
// fast with ErrRepoLocked.
func TestSyncTo_AcquiresDestLock(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// Initialize dest first so a real Repo can hold its lock.
	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	dstRepo, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer dstRepo.Close()
	held, err := acquireLock(ctx, dstRepo.store, "test-holding")
	if err != nil {
		t.Fatalf("acquire dst lock: %v", err)
	}
	defer releaseLock(ctx, dstRepo.store, held)

	// Add a new source snapshot so the second sync has work; then
	// try to sync — must fail fast with ErrRepoLocked.
	srcRoot := t.TempDir()
	writeFile(t, filepath.Join(srcRoot, "x.txt"), "x")
	if _, err := src.CreateSnapshot(ctx, srcRoot, SnapshotOptions{}); err != nil {
		t.Fatalf("create src snap: %v", err)
	}
	_, err = src.SyncTo(ctx, dstStore, SyncOptions{InitDest: false})
	if !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("SyncTo while dst lock held: got %v, want ErrRepoLocked", err)
	}
}

// TestSyncTo_DoesNotLockSource verifies the eventual-consistency
// contract: holding source's lock does NOT block sync. (The lock
// matters for source-side mutating ops, but sync only reads.)
func TestSyncTo_DoesNotLockSource(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// Hold source's lock manually.
	srcLock, err := acquireLock(ctx, src.store, "external")
	if err != nil {
		t.Fatalf("acquire src lock: %v", err)
	}
	defer releaseLock(ctx, src.store, srcLock)

	// Sync must proceed despite the held source lock.
	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo with src lock held: %v (sync must not require src lock)", err)
	}
}

// listHookStore wraps a Store and, when armed, fires onList exactly once
// after the next successful List returns. It lets a test inject source
// writes between SyncTo's two listing passes — the window a concurrent
// backup on the unlocked source can land in.
type listHookStore struct {
	blobstore.Store
	armed  atomic.Bool
	onList func()
}

func (s *listHookStore) List(ctx context.Context, prefix string) ([]blobstore.Info, error) {
	infos, err := s.Store.List(ctx, prefix)
	if err == nil && s.armed.CompareAndSwap(true, false) {
		s.onList()
	}
	return infos, err
}

// TestSyncTo_ConcurrentSourceCommitDuringListing_NoDanglingManifest pins
// the rule that a successful sync never leaves dest with a manifest whose
// chunks are absent. SyncTo does not lock the source, so a backup can
// commit a new chunk+manifest between the two listing passes; whatever
// order SyncTo lists in, the frozen plans must never copy a manifest
// whose chunks the data plan cannot see. (Chunks are uploaded strictly
// before their manifest, so listing snapshots/ before data/ is safe;
// the reverse order copies the late manifest but not its chunks.)
func TestSyncTo_ConcurrentSourceCommitDuringListing_NoDanglingManifest(t *testing.T) {
	ctx := context.Background()
	hooked := &listHookStore{Store: blobstore.NewMemory()}
	dstStore := blobstore.NewMemory()
	src, err := Init(ctx, hooked, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init src: %v", err)
	}
	defer src.Close()
	seedSourceWithSnapshot(t, src, "baseline")

	// The late snapshot's content is unique, so its chunk exists in no
	// listing taken before the commit.
	lateRoot := t.TempDir()
	writeFile(t, filepath.Join(lateRoot, "late.txt"), "unique-late-content-not-in-baseline")
	hooked.onList = func() {
		if _, err := src.CreateSnapshot(ctx, lateRoot, SnapshotOptions{Tag: "late"}); err != nil {
			t.Errorf("concurrent snapshot during sync: %v", err)
		}
	}
	hooked.armed.Store(true)

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	// Every manifest the sync copied must be fully restorable from dest
	// alone: no referenced blob may be missing.
	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer dst.Close()
	report, err := dst.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatalf("check dest: %v", err)
	}
	if len(report.MissingBlobs) > 0 {
		t.Errorf("dest has dangling manifests after successful sync: missing blobs %+v", report.MissingBlobs)
	}
	if len(report.ManifestIssues) > 0 {
		t.Errorf("dest manifest issues after successful sync: %+v", report.ManifestIssues)
	}
}

// TestSyncTo_RestoreFromDestMatchesRestoreFromSource is the end-
// to-end correctness test: after sync, restoring the same
// snapshot from src and from dst produces byte-identical output.
func TestSyncTo_RestoreFromDestMatchesRestoreFromSource(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)

	// Build a non-trivial source snapshot.
	srcRoot := t.TempDir()
	writeFile(t, filepath.Join(srcRoot, "a.txt"), "alpha")
	writeFile(t, filepath.Join(srcRoot, "b/c.txt"), "charlie inside b")
	writeFile(t, filepath.Join(srcRoot, "data.bin"),
		strings.Repeat("\x00\x01\x02\x03", 1024))
	snap, err := src.CreateSnapshot(ctx, srcRoot, SnapshotOptions{Tag: "round-trip"})
	if err != nil {
		t.Fatalf("create snap: %v", err)
	}

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	// Restore the snap from src and from dst into separate dirs.
	srcRestore := filepath.Join(t.TempDir(), "from-src")
	if err := src.Restore(ctx, snap.ID, srcRestore, RestoreOptions{}); err != nil {
		t.Fatalf("restore from src: %v", err)
	}
	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst: %v", err)
	}
	defer dst.Close()
	dstRestore := filepath.Join(t.TempDir(), "from-dst")
	if err := dst.Restore(ctx, snap.ID, dstRestore, RestoreOptions{}); err != nil {
		t.Fatalf("restore from dst: %v", err)
	}

	// Byte-identical comparison via the existing fingerprint helper.
	want := treeFingerprint(t, srcRestore)
	got := treeFingerprint(t, dstRestore)
	if len(want) != len(got) {
		t.Fatalf("file count: src=%d dst=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel {
			t.Errorf("rel: src=%q dst=%q", want[i].rel, got[i].rel)
		}
		if !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("%q content mismatch between src and dst restore", want[i].rel)
		}
	}
}

// TestSyncTo_DoesNotCopyMetaLock verifies meta/lock is excluded
// from the sync's blob set. Even if source has a stale lock blob
// (from a crashed prior process), dest must not inherit it.
func TestSyncTo_DoesNotCopyMetaLock(t *testing.T) {
	ctx := context.Background()
	src, srcStore, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// Plant a fake stale lock on source, simulating a crashed
	// prior sentra process.
	if err := srcStore.Put(ctx, lockKey, bytes.NewReader([]byte(`{"uuid":"stale"}`))); err != nil {
		t.Fatalf("put stale lock on src: %v", err)
	}

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	// Dest must NOT have a meta/lock blob.
	if _, err := dstStore.Stat(ctx, lockKey); err == nil {
		t.Error("sync copied meta/lock to dest; should be excluded")
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("unexpected stat err: %v", err)
	}
}

// TestSyncTo_DoesNotCopyMetaSnapshots verifies the index is NOT
// directly synced. After sync, dest's meta/snapshots is absent;
// the first ListSnapshots on dest rebuilds it from the actual
// manifests (Phase 3's self-healing path).
func TestSyncTo_DoesNotCopyMetaSnapshots(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("SyncTo: %v", err)
	}

	if _, err := dstStore.Stat(ctx, snapshotIndexKey); err == nil {
		t.Error("sync copied meta/snapshots to dest; design says rebuild on next ListSnapshots")
	} else if !errors.Is(err, blobstore.ErrNotFound) {
		t.Errorf("unexpected stat err: %v", err)
	}

	// Confirm self-heal: opening dest + ListSnapshots produces the
	// right answer despite no pre-existing index.
	dst, _ := Open(ctx, dstStore, []byte("hunter2"))
	defer dst.Close()
	infos, err := dst.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("dst ListSnapshots: %v", err)
	}
	if len(infos) != 1 {
		t.Errorf("dst snapshots: got %d, want 1 (self-heal)", len(infos))
	}
}

// TestSyncTo_DryRunMakesNoWrites confirms DryRun=true is read-only:
// returns realistic stats but the destination is byte-identical
// before and after.
func TestSyncTo_DryRunMakesNoWrites(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// Snapshot dst's pre-state by listing every key.
	preKeys, _ := dstStore.List(ctx, "")

	stats, err := src.SyncTo(ctx, dstStore, SyncOptions{InitDest: true, DryRun: true})
	if err != nil {
		t.Fatalf("DryRun: %v", err)
	}
	if !stats.DryRun {
		t.Error("SyncStats.DryRun should be true")
	}
	if stats.CopiedBlobs == 0 {
		t.Error("DryRun should report what WOULD be copied (CopiedBlobs > 0)")
	}

	postKeys, _ := dstStore.List(ctx, "")
	if len(preKeys) != len(postKeys) {
		t.Errorf("DryRun changed dst object count: pre=%d post=%d", len(preKeys), len(postKeys))
	}
}

// flakyDestStore wraps a blobstore.Store and fails Put for the
// first N calls, then succeeds. Used to verify the sync path
// composes with retry semantics: even with transient-looking
// errors, the eventual sync completes successfully on retry.
type flakyDestStore struct {
	blobstore.Store
	failPutsRemaining atomic.Int32
}

func (f *flakyDestStore) Put(ctx context.Context, key string, r io.Reader) error {
	if f.failPutsRemaining.Add(-1) >= 0 {
		// Drain so caller doesn't observe a half-read.
		_, _ = io.Copy(io.Discard, r)
		return errors.New("flaky: synthetic put failure")
	}
	return f.Store.Put(ctx, key, r)
}

// TestSyncTo_RetryOnTransientFailure verifies: when the dest
// returns transient errors on early Puts, sync's caller-level
// retry (or just running sync twice) should produce a healthy
// dest. We use the simplest version of this: run sync, observe
// it errors, run sync again (no flake this time), observe success.
//
// This test does NOT validate RetryStore composition (that's a
// separate test against a RetryStore-wrapped dst). It validates
// that sync is naturally idempotent across re-runs after a
// partial failure.
func TestSyncTo_RetryOnTransientFailure(t *testing.T) {
	ctx := context.Background()
	src, _, dstMem := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// Wrap the dest store so the first 2 Puts fail. Sync will
	// likely error before all blobs are copied.
	flaky := &flakyDestStore{Store: dstMem}
	flaky.failPutsRemaining.Store(2)
	_, err := src.SyncTo(ctx, flaky, SyncOptions{InitDest: true})
	if err == nil {
		t.Logf("flaky sync somehow succeeded; that's fine — moving to verify resume")
	}

	// Second sync with no flake — should resume cleanly.
	if _, err := src.SyncTo(ctx, dstMem, SyncOptions{InitDest: true}); err != nil {
		t.Fatalf("resume sync: %v", err)
	}
	dst, err := Open(ctx, dstMem, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst after resume: %v", err)
	}
	defer dst.Close()
	infos, _ := dst.ListSnapshots(ctx)
	if len(infos) != 1 {
		t.Errorf("dst snapshots after resume: got %d, want 1", len(infos))
	}
}

// TestSyncTo_ResumeAfterPartialSync covers the resumability
// contract: a sync that ran partway (some chunks landed, no
// manifests yet) can be re-run and completes cleanly.
//
// We engineer a partial state by running a first sync that fails
// after Phase 1 but before Phase 2 finishes. Then a clean re-run
// must produce a fully-functional dst.
func TestSyncTo_ResumeAfterPartialSync(t *testing.T) {
	ctx := context.Background()
	src, _, dstMem := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	// First sync via a wrapper that fails the manifest write.
	// The wrapper allows config + chunk Puts but errors on
	// snapshots/* Puts. After this sync, dst has bootstrapped
	// config + chunks but no manifests.
	failManifests := &refusingPutStore{
		Store:        dstMem,
		refusePrefix: snapshotPrefix,
	}
	if _, err := src.SyncTo(ctx, failManifests, SyncOptions{InitDest: true}); err == nil {
		t.Fatal("expected first sync to fail (manifest writes refused)")
	}

	// Resume: clean dst store, no flake. Must complete.
	if _, err := src.SyncTo(ctx, dstMem, SyncOptions{InitDest: false}); err != nil {
		t.Fatalf("resume sync: %v", err)
	}
	dst, err := Open(ctx, dstMem, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dst after resume: %v", err)
	}
	defer dst.Close()
	if infos, _ := dst.ListSnapshots(ctx); len(infos) != 1 {
		t.Errorf("snapshots after resume: got %d, want 1", len(infos))
	}
}

// refusingPutStore wraps a blobstore.Store and returns an error
// for every Put whose key starts with refusePrefix. Other ops
// pass through.
type refusingPutStore struct {
	blobstore.Store
	refusePrefix string
}

func (r *refusingPutStore) Put(ctx context.Context, key string, body io.Reader) error {
	if strings.HasPrefix(key, r.refusePrefix) {
		_, _ = io.Copy(io.Discard, body)
		return errors.New("refused: synthetic prefix failure")
	}
	return r.Store.Put(ctx, key, body)
}

// TestSyncTo_RefusesSameSrcAndDst guards against an operator
// running sync with --dst-config pointing at the same store
// they're currently reading from. That would be a no-op at best
// and a deadlock-on-self-lock at worst.
func TestSyncTo_RefusesSameSrcAndDst(t *testing.T) {
	ctx := context.Background()
	src, srcStore, _ := twoRepos(t)
	seedSourceWithSnapshot(t, src, "snap")

	_, err := src.SyncTo(ctx, srcStore, SyncOptions{InitDest: false})
	if err == nil {
		t.Fatal("SyncTo against own store: got nil, want refusal")
	}
	if !errors.Is(err, ErrSameSrcAndDst) {
		t.Errorf("error: got %v, want ErrSameSrcAndDst", err)
	}
}

// _ ensures time.Duration is imported even if no test uses it
// directly yet. The implementation will populate SyncStats.Elapsed
// which uses time.Duration.
var _ = time.Duration(0)

// TestSyncTo_SelectedSnapshots: SyncOptions.Snapshots copies only the
// named snapshots' manifests plus their chunk closure — dest is a
// healthy repo containing exactly that subset. An unknown ID errors
// before any writes.
func TestSyncTo_SelectedSnapshots(t *testing.T) {
	ctx := context.Background()
	src, _, dstStore := twoRepos(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "keep.txt"), strings.Repeat("keep-me-", 100))
	s1, err := src.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "skip.txt"), strings.Repeat("skip-me-", 100))
	s2, err := src.CreateSnapshot(ctx, root, SnapshotOptions{Tag: "skip"})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := src.SyncTo(ctx, dstStore, SyncOptions{
		InitDest:  true,
		Snapshots: []string{s1.ID},
	}); err != nil {
		t.Fatalf("selective sync: %v", err)
	}

	dst, err := Open(ctx, dstStore, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open dest: %v", err)
	}
	defer dst.Close()
	infos, err := dst.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != s1.ID {
		t.Fatalf("dest snapshots: got %+v, want only %s", infos, s1.ID)
	}
	// Dest must be complete for the selected snapshot.
	report, err := dst.Check(ctx, CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.MissingBlobs) > 0 {
		t.Fatalf("selected snapshot incomplete on dest: %+v", report.MissingBlobs)
	}
	// s2's unique chunks must NOT have been copied.
	m2, err := src.LoadSnapshot(ctx, s2.ID)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := src.LoadSnapshot(ctx, s1.ID)
	if err != nil {
		t.Fatal(err)
	}
	inS1 := map[string]struct{}{}
	for _, fe := range m1.Tree {
		for _, h := range fe.Chunks {
			inS1[h] = struct{}{}
		}
	}
	for _, fe := range m2.Tree {
		for _, h := range fe.Chunks {
			if _, shared := inS1[h]; shared {
				continue
			}
			if _, err := dstStore.Stat(ctx, ChunkKey(h)); err == nil {
				t.Errorf("unselected chunk %s copied to dest", h)
			}
		}
	}

	// Unknown ID refuses.
	if _, err := src.SyncTo(ctx, blobstore.NewMemory(), SyncOptions{
		InitDest:  true,
		Snapshots: []string{"snap-20990101T000000Z-deadbeef"},
	}); err == nil {
		t.Error("unknown snapshot selection must error")
	}
}
