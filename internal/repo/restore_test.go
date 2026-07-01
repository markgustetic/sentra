package repo

import (
	"bytes"
	"cmp"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/chunker"
	"github.com/markgustetic/sentra/internal/crypto"
)

// fileFingerprint is the content + mode + size of one file relative
// to a tree root. Used by treeFingerprint for byte-for-byte
// comparison of two trees.
type fileFingerprint struct {
	rel  string
	size int64
	mode os.FileMode
	data []byte
}

func treeFingerprint(t *testing.T, root string) []fileFingerprint {
	t.Helper()
	var out []fileFingerprint
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(path) //nolint:gosec // path is from WalkDir under our test temp root
		if err != nil {
			return err
		}
		out = append(out, fileFingerprint{
			rel:  rel,
			size: info.Size(),
			mode: info.Mode().Perm(),
			data: raw,
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	slices.SortFunc(out, func(a, b fileFingerprint) int { return cmp.Compare(a.rel, b.rel) })
	return out
}

func TestRestore_RoundTrip(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "b.bin"), strings.Repeat("\x00\x01\x02\x03", 256))
	writeFile(t, filepath.Join(src, "sub", "c.md"), "# heading\n\nbody\n")

	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{Tag: "rt"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	want := treeFingerprint(t, src)
	got := treeFingerprint(t, dst)

	if len(want) != len(got) {
		t.Fatalf("file count: src=%d dst=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel {
			t.Errorf("rel: want %q, got %q", want[i].rel, got[i].rel)
		}
		if want[i].size != got[i].size {
			t.Errorf("%q size: want %d, got %d", want[i].rel, want[i].size, got[i].size)
		}
		// Mode comparison is permission-only — that's what the
		// manifest carries through the round-trip. Skip on Windows
		// where the perm-bits model diverges; the tree is unix-only
		// for now anyway.
		if runtime.GOOS != "windows" && want[i].mode != got[i].mode {
			t.Errorf("%q mode: want %v, got %v", want[i].rel, want[i].mode, got[i].mode)
		}
		if !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("%q content mismatch (want %d bytes, got %d bytes)",
				want[i].rel, len(want[i].data), len(got[i].data))
		}
	}
}

func TestRestore_RefusesNonEmptyDest(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := t.TempDir()
	// dst exists and is non-empty.
	writeFile(t, filepath.Join(dst, "leftover.txt"), "stale")

	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err == nil {
		t.Fatal("expected error restoring into non-empty dir, got nil")
	}
}

func TestRestore_NestedDirectories(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	for _, p := range []string{
		"top.txt",
		"a/one.txt",
		"a/b/two.txt",
		"a/b/c/three.txt",
		"a/b/c/d/four.txt",
	} {
		writeFile(t, filepath.Join(src, p), "x="+p)
	}
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "fresh")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	want := treeFingerprint(t, src)
	got := treeFingerprint(t, dst)
	if len(want) != len(got) {
		t.Fatalf("count: want %d, got %d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel || !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("mismatch on %q: want %q, got %q", want[i].rel,
				string(want[i].data), string(got[i].data))
		}
	}
}

func TestRestore_AllowsEmptyExistingDest(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "x.txt"), "value")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := t.TempDir() // exists, empty
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	got := treeFingerprint(t, dst)
	if len(got) != 1 || got[0].rel != "x.txt" || string(got[0].data) != "value" {
		t.Fatalf("restore output unexpected: %+v", got)
	}
}

func TestRestorePlan_DoesNotCreateDestination(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "sub", "b.txt"), "bravo")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "planned-restore")
	plan, err := r.PlanRestore(ctx, snap.ID, dst)
	if err != nil {
		t.Fatalf("plan restore: %v", err)
	}
	if plan.Files != 2 {
		t.Errorf("Files = %d, want 2", plan.Files)
	}
	if len(plan.Paths) != 2 {
		t.Errorf("Paths = %v, want 2 entries", plan.Paths)
	}
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Fatalf("dry-run plan created destination or got unexpected stat error: %v", err)
	}
}

func TestVerifyRestore_DetectsTamperedFile(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	writeFile(t, filepath.Join(dst, "a.txt"), "tampered")

	report, err := r.VerifyRestore(ctx, snap.ID, dst)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK() {
		t.Fatalf("expected verification failure after tamper, got %+v", report)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches = %+v, want one", report.Mismatches)
	}
	if report.Mismatches[0].Path != "a.txt" {
		t.Errorf("mismatch path = %q, want a.txt", report.Mismatches[0].Path)
	}
}

func TestVerifyRestore_DetectsEqualSizeContentMismatch(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	writeFile(t, filepath.Join(dst, "a.txt"), "bravo") // same size as alpha

	report, err := r.VerifyRestore(ctx, snap.ID, dst)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK() {
		t.Fatalf("expected verification failure after equal-size tamper, got %+v", report)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches = %+v, want one", report.Mismatches)
	}
	got := report.Mismatches[0]
	if got.Path != "a.txt" || got.Reason != "content hash mismatch" {
		t.Fatalf("mismatch = %+v, want content hash mismatch for a.txt", got)
	}
	if got.WantSize != 5 || got.GotSize != 5 {
		t.Fatalf("sizes = want %d got %d, want both 5", got.WantSize, got.GotSize)
	}
}

func TestVerifyRestore_DetectsMissingAndExtraFiles(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	writeFile(t, filepath.Join(src, "b.txt"), "bravo")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if err := os.Remove(filepath.Join(dst, "b.txt")); err != nil {
		t.Fatalf("remove restored file: %v", err)
	}
	writeFile(t, filepath.Join(dst, "extra.txt"), "not in manifest")

	report, err := r.VerifyRestore(ctx, snap.ID, dst)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if report.OK() {
		t.Fatalf("expected verification failure, got %+v", report)
	}
	if report.VerifiedFiles != 1 {
		t.Fatalf("VerifiedFiles = %d, want 1", report.VerifiedFiles)
	}
	if report.ExtraFileCount != 1 {
		t.Fatalf("ExtraFileCount = %d, want 1", report.ExtraFileCount)
	}
	reasons := map[string]string{}
	for _, mismatch := range report.Mismatches {
		reasons[mismatch.Path] = mismatch.Reason
	}
	if reasons["b.txt"] != "missing file" {
		t.Fatalf("missing file reason = %q, mismatches=%+v", reasons["b.txt"], report.Mismatches)
	}
	if reasons["extra.txt"] != "extra file not present in snapshot" {
		t.Fatalf("extra file reason = %q, mismatches=%+v", reasons["extra.txt"], report.Mismatches)
	}
}

func TestVerifyRestore_DetectsNonRegularFile(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "restored")
	if err := r.Restore(ctx, snap.ID, dst, RestoreOptions{}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	restoredPath := filepath.Join(dst, "a.txt")
	if err := os.Remove(restoredPath); err != nil {
		t.Fatalf("remove restored file: %v", err)
	}
	if err := os.Mkdir(restoredPath, 0o755); err != nil {
		t.Fatalf("mkdir at file path: %v", err)
	}

	report, err := r.VerifyRestore(ctx, snap.ID, dst)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if len(report.Mismatches) != 1 {
		t.Fatalf("Mismatches = %+v, want one", report.Mismatches)
	}
	if got := report.Mismatches[0]; got.Path != "a.txt" || got.Reason != "not a regular file" {
		t.Fatalf("mismatch = %+v, want not a regular file for a.txt", got)
	}
}

func TestInspectDestDir(t *testing.T) {
	parent := t.TempDir()

	exists, empty, err := inspectDestDir(filepath.Join(parent, "missing"))
	if err != nil {
		t.Fatalf("missing dest: %v", err)
	}
	if exists || !empty {
		t.Fatalf("missing dest: exists=%t empty=%t, want false true", exists, empty)
	}

	emptyDir := filepath.Join(parent, "empty")
	if err := os.Mkdir(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir empty: %v", err)
	}
	exists, empty, err = inspectDestDir(emptyDir)
	if err != nil {
		t.Fatalf("empty dir: %v", err)
	}
	if !exists || !empty {
		t.Fatalf("empty dir: exists=%t empty=%t, want true true", exists, empty)
	}

	nonEmptyDir := filepath.Join(parent, "non-empty")
	writeFile(t, filepath.Join(nonEmptyDir, "leftover.txt"), "leftover")
	exists, empty, err = inspectDestDir(nonEmptyDir)
	if err == nil {
		t.Fatalf("non-empty dir: expected error")
	}
	if !exists || empty {
		t.Fatalf("non-empty dir: exists=%t empty=%t, want true false", exists, empty)
	}

	fileDest := filepath.Join(parent, "file")
	writeFile(t, fileDest, "not a dir")
	exists, empty, err = inspectDestDir(fileDest)
	if err == nil {
		t.Fatalf("file dest: expected error")
	}
	if !exists || empty {
		t.Fatalf("file dest: exists=%t empty=%t, want true false", exists, empty)
	}
}

func TestHashFileChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.bin")
	body := strings.Repeat("hash-me-", 2048)
	writeFile(t, path, body)

	got, err := hashFileChunks(path)
	if err != nil {
		t.Fatalf("hashFileChunks: %v", err)
	}
	var want []string
	if err := chunker.ChunkStream(strings.NewReader(body), func(c chunker.Chunk) error {
		want = append(want, hex.EncodeToString(c.Hash))
		return nil
	}); err != nil {
		t.Fatalf("chunk expected body: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("hashes = %v, want %v", got, want)
	}
}

// TestRestore_ConcurrentMatchesSequential confirms that restoring a
// non-trivial tree with Concurrency=1 and Concurrency=8 produces
// byte-identical output. The fan-out path must preserve the exact
// dedup + per-file write semantics the sequential path has.
func TestRestore_ConcurrentMatchesSequential(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)

	src := t.TempDir()
	// Mix of file sizes and content patterns so multiple files have
	// multiple chunks, exercising the inner per-file fetch loop in
	// parallel across workers.
	for i := 0; i < 30; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("a/%d.bin", i)),
			strings.Repeat(fmt.Sprintf("seed-%d-", i), 1024))
	}
	for i := 0; i < 20; i++ {
		writeFile(t, filepath.Join(src, fmt.Sprintf("b/%d.txt", i)),
			fmt.Sprintf("text content %d\n", i))
	}

	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	dstSequential := filepath.Join(t.TempDir(), "seq")
	if err := r.Restore(ctx, snap.ID, dstSequential, RestoreOptions{Concurrency: 1}); err != nil {
		t.Fatalf("sequential restore: %v", err)
	}
	dstParallel := filepath.Join(t.TempDir(), "par")
	if err := r.Restore(ctx, snap.ID, dstParallel, RestoreOptions{Concurrency: 8}); err != nil {
		t.Fatalf("parallel restore: %v", err)
	}

	want := treeFingerprint(t, dstSequential)
	got := treeFingerprint(t, dstParallel)
	if len(want) != len(got) {
		t.Fatalf("file count: sequential=%d parallel=%d", len(want), len(got))
	}
	for i := range want {
		if want[i].rel != got[i].rel {
			t.Errorf("rel: sequential=%q parallel=%q", want[i].rel, got[i].rel)
		}
		if want[i].size != got[i].size {
			t.Errorf("%q size: sequential=%d parallel=%d",
				want[i].rel, want[i].size, got[i].size)
		}
		if !bytes.Equal(want[i].data, got[i].data) {
			t.Errorf("%q content mismatch between sequential and parallel restore",
				want[i].rel)
		}
	}
}

// TestRestore_ConcurrencyClamping verifies the documented clamping:
// negative -> 1 (sequential), zero -> default (>=1). We don't pin to
// GOMAXPROCS because the host CPU count varies; we just confirm a
// non-zero default fires.
func TestRestore_ConcurrencyClamping(t *testing.T) {
	ctx := context.Background()
	r, _ := newTestRepo(t)
	src := t.TempDir()
	writeFile(t, filepath.Join(src, "x.txt"), "hello")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	// Concurrency=-1 must succeed (clamped to 1). The behavior is
	// observable only via a successful restore — anything that
	// errored on the negative input would imply our clamping
	// regressed.
	dstNeg := filepath.Join(t.TempDir(), "neg")
	if err := r.Restore(ctx, snap.ID, dstNeg, RestoreOptions{Concurrency: -1}); err != nil {
		t.Fatalf("Concurrency=-1: %v", err)
	}
	dstZero := filepath.Join(t.TempDir(), "zero")
	if err := r.Restore(ctx, snap.ID, dstZero, RestoreOptions{Concurrency: 0}); err != nil {
		t.Fatalf("Concurrency=0: %v", err)
	}
}

// TestRestore_RejectsPathTraversal verifies that a manifest with a
// FileEntry.Path containing ".." cannot escape the destination
// directory. We forge a tampered manifest, write it under a fresh
// snapshot ID, then call Restore and assert it errors out and that no
// file appears at the would-be escape location.
func TestRestore_RejectsPathTraversal(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "good.txt"), "harmless")

	// Take a real snapshot so we have a manifest with valid chunks to
	// borrow.
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(m.Tree) != 1 {
		t.Fatalf("expected exactly 1 entry in seed manifest, got %d", len(m.Tree))
	}

	// Tamper with the path so it tries to escape the destination.
	m.Tree[0].Path = "../escaped.txt"
	// Re-stamp with a new ID so it co-exists with the real snapshot.
	tamperedID, err := newSnapshotID(time.Now().UTC())
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	m.ID = tamperedID

	// Re-marshal, compress, encrypt, upload at snapshots/<tamperedID>.
	raw, err := json.Marshal(&m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	compressed, err := chunker.Compress(raw)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	sealed, err := crypto.Seal(r.repoKey, compressed)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if err := store.Put(ctx, snapshotPrefix+tamperedID, bytes.NewReader(sealed)); err != nil {
		t.Fatalf("put tampered manifest: %v", err)
	}

	destParent := t.TempDir()
	destDir := filepath.Join(destParent, "restored")
	err = r.Restore(ctx, tamperedID, destDir, RestoreOptions{})
	if err == nil {
		t.Fatal("expected error restoring traversal manifest, got nil")
	}
	// Error should mention escape/traversal/destination.
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "escape") && !strings.Contains(msg, "traversal") &&
		!strings.Contains(msg, "outside") && !strings.Contains(msg, "refus") {
		t.Fatalf("error did not mention traversal/escape: %v", err)
	}

	// Most important: no file leaked outside destDir.
	leakPath := filepath.Join(destParent, "escaped.txt")
	if _, err := os.Stat(leakPath); err == nil {
		t.Fatalf("traversal succeeded — file written at %s", leakPath)
	} else if !os.IsNotExist(err) {
		t.Fatalf("unexpected stat error on %s: %v", leakPath, err)
	}
}

// TestRestore_DetectsChunkContentAddressMismatch: a data blob that is
// validly sealed under the repo key but stored at the WRONG content-
// address (a swap of two authentic blobs, a manifest listing a wrong-
// but-existing hash, or an object-store copy mistake) must be caught.
// The AEAD tag only proves "sealed under this key", not "content matches
// its address", so restore must re-derive sha256(plaintext) and refuse
// on mismatch rather than silently writing wrong bytes.
func TestRestore_DetectsChunkContentAddressMismatch(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.txt"), strings.Repeat("AAAAAAAA", 500))
	writeFile(t, filepath.Join(root, "b.txt"), strings.Repeat("BBBBBBBB", 500))
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var hashA, hashB string
	for _, fe := range m.Tree {
		switch filepath.Base(fe.Path) {
		case "a.txt":
			if len(fe.Chunks) != 1 {
				t.Fatalf("a.txt has %d chunks, want 1", len(fe.Chunks))
			}
			hashA = fe.Chunks[0]
		case "b.txt":
			if len(fe.Chunks) != 1 {
				t.Fatalf("b.txt has %d chunks, want 1", len(fe.Chunks))
			}
			hashB = fe.Chunks[0]
		}
	}
	if hashA == "" || hashB == "" || hashA == hashB {
		t.Fatalf("expected two distinct single-chunk files, got a=%q b=%q", hashA, hashB)
	}

	// Place b's validly-sealed blob at a's content-address key. It
	// decrypts cleanly (same repo key) but its plaintext hashes to
	// hashB, not hashA.
	rc, err := store.Get(ctx, ChunkKey(hashB))
	if err != nil {
		t.Fatalf("get B blob: %v", err)
	}
	sealedB, err := io.ReadAll(rc)
	rc.Close()
	if err != nil {
		t.Fatalf("read B blob: %v", err)
	}
	if err := store.Put(ctx, ChunkKey(hashA), bytes.NewReader(sealedB)); err != nil {
		t.Fatalf("overwrite A blob: %v", err)
	}

	dest := t.TempDir()
	err = r.Restore(ctx, snap.ID, dest, RestoreOptions{})
	if err == nil {
		t.Fatal("Restore succeeded on a mis-addressed chunk; expected an integrity error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "hash") && !strings.Contains(msg, "integrity") &&
		!strings.Contains(msg, "content address") {
		t.Fatalf("expected an integrity/hash-mismatch error, got: %v", err)
	}
}

// TestRestore_FailedFileLeavesNoPartial: when a file restore fails
// partway through (here, a later chunk is missing from the store), the
// destination must not be left with a truncated file at its final path,
// and no temp litter should remain. restoreFile stages into a sibling
// temp file and renames only after the whole file is written, so a
// failure leaves the destination exactly as it was.
func TestRestore_FailedFileLeavesNoPartial(t *testing.T) {
	ctx := context.Background()
	r, store := newTestRepo(t)

	// ~9 MiB of varied bytes -> multiple distinct chunks (maxChunkSize
	// is 4 MiB, so a single chunk cannot cover the whole file).
	data := make([]byte, 9<<20)
	for i := range data {
		data[i] = byte(i*31 + i>>7)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.dat"), data, 0o600); err != nil {
		t.Fatalf("write big.dat: %v", err)
	}
	snap, err := r.CreateSnapshot(ctx, root, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	m, err := r.LoadSnapshot(ctx, snap.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var chunks []string
	for _, fe := range m.Tree {
		if filepath.Base(fe.Path) == "big.dat" {
			chunks = fe.Chunks
		}
	}
	if len(chunks) < 2 {
		t.Fatalf("big.dat has %d chunks, need >=2 to exercise a mid-file failure", len(chunks))
	}

	// Remove the LAST chunk so the write fails only after earlier chunks
	// were already fetched and written.
	if err := store.Delete(ctx, ChunkKey(chunks[len(chunks)-1])); err != nil {
		t.Fatalf("delete last chunk: %v", err)
	}

	dest := t.TempDir()
	// Single-file restore so the failure is deterministic.
	if err := r.Restore(ctx, snap.ID, dest, RestoreOptions{Concurrency: 1}); err == nil {
		t.Fatal("expected Restore to fail on the missing chunk")
	}

	// The destination must be pristine: no truncated big.dat, no leftover
	// temp file.
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("readdir dest: %v", err)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("failed restore left %d entries in dest (partial/temp litter): %v", len(entries), names)
	}
}

// silence unused-import linters when only some tests reference these
// helpers under build flags.
var _ = io.Discard
