package repo

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// TestPin_ProtectsFromDeleteAndRetention: a pinned snapshot survives
// both deletion paths — retention planning keeps it with an explicit
// reason, and DeleteSnapshot (the choke point shared by prune, the
// TUI, and the agent's prune action) refuses outright.
func TestPin_ProtectsFromDeleteAndRetention(t *testing.T) {
	r, _ := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "one")
	s1, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(src, "a.txt"), "two-longer")
	if _, err := r.CreateSnapshot(ctx, src, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	if err := r.Pin(ctx, s1.ID); err != nil {
		t.Fatalf("pin: %v", err)
	}
	pins, err := r.Pins(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins[s1.ID]; !ok {
		t.Fatalf("pin not recorded: %v", pins)
	}

	// Retention: keep-last 1 would drop s1; the pin must keep it.
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	decisions := PlanRetentionExplain(snaps, RetentionPolicy{KeepLast: 1, Pinned: pins})
	for _, d := range decisions {
		if d.Snapshot.ID == s1.ID {
			if !d.Keep {
				t.Fatalf("pinned snapshot planned for drop: %+v", d)
			}
			found := false
			for _, reason := range d.Reasons {
				if reason == "pinned" {
					found = true
				}
			}
			if !found {
				t.Errorf("pinned keep must carry the explicit reason: %v", d.Reasons)
			}
		}
	}

	// Direct deletion refuses.
	if err := r.DeleteSnapshot(ctx, s1.ID); !errors.Is(err, ErrSnapshotPinned) {
		t.Fatalf("DeleteSnapshot(pinned) = %v, want ErrSnapshotPinned", err)
	}

	// Unpin restores deletability.
	if err := r.Unpin(ctx, s1.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteSnapshot(ctx, s1.ID); err != nil {
		t.Fatalf("delete after unpin: %v", err)
	}
}

// TestPin_UnknownSnapshotRefused: pinning an ID that doesn't exist is
// an error — a typo'd pin that silently "protects" nothing would give
// false confidence.
func TestPin_UnknownSnapshotRefused(t *testing.T) {
	r, _ := newTestRepo(t)
	id, err := newSnapshotID(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Pin(context.Background(), id); err == nil {
		t.Error("pinning a nonexistent snapshot must error")
	}
}

// TestDeleteSnapshot_SerializesOnRepoLock: the pin check is only a
// guarantee if the check and the delete are one critical section. An
// unlocked DeleteSnapshot could read "not pinned", lose the CPU to a
// concurrent Pin (which locks, validates, commits), then delete the
// snapshot the user just protected — leaving a dangling pin forever.
func TestDeleteSnapshot_SerializesOnRepoLock(t *testing.T) {
	r, store := newTestRepo(t)
	ctx := context.Background()

	src := t.TempDir()
	writeFile(t, filepath.Join(src, "a.txt"), "alpha")
	snap, err := r.CreateSnapshot(ctx, src, SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	held, err := acquireLock(ctx, store, "external")
	if err != nil {
		t.Fatal(err)
	}
	defer releaseLock(ctx, store, held)

	if err := r.DeleteSnapshot(ctx, snap.ID); !errors.Is(err, ErrRepoLocked) {
		t.Fatalf("DeleteSnapshot under a held lock = %v, want ErrRepoLocked (pin check + delete must be one critical section)", err)
	}
}
