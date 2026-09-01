package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// seedTwoSnapshots creates two snapshots with distinct content so a
// KeepLast=1 policy drops exactly one. Both snapshots come from the
// SAME source dir — retention groups by root, so two roots would each
// keep their own snapshot and the policy would drop nothing.
func seedTwoSnapshots(t *testing.T, r *repo.Repo) {
	t.Helper()
	src := t.TempDir()
	prev := ""
	for _, name := range []string{"one", "two"} {
		if prev != "" {
			if err := os.Remove(filepath.Join(src, prev+".txt")); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(src, name+".txt"),
			[]byte(strings.Repeat(name, 200)), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
		prev = name
	}
}

func pruneDeps(r *repo.Repo) Deps {
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 1
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	return Deps{Repo: r, Config: &cfg}
}

func TestPruneFlow_PreviewShowsKeepAndDropWithReasons(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))
	out := v.View()
	if !strings.Contains(out, "keep") || !strings.Contains(out, "drop") {
		t.Errorf("preview must show keep/drop decisions:\n%s", out)
	}
}

// TestPruneFlow_NoDeletionWithoutTypedConfirm is THE confirmation-gate
// test: starting the flow and pressing enter must NOT delete anything —
// only the typed-confirm path may. This is the spec's core safety rule.
func TestPruneFlow_NoDeletionWithoutTypedConfirm(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))

	// Enter requests the typed-confirm modal (a pushModalMsg), nothing else.
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should request the confirm modal")
	}
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 2 {
		t.Fatalf("snapshots deleted without confirmation: %d left", len(snaps))
	}
}

func TestPruneFlow_ConfirmedRunDeletesAndGCs(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))

	// Simulate the App delivering the typed-confirm result.
	m, cmd := v.Update(confirmedMsg{id: pruneConfirmID})
	v = m.(PruneView)
	if cmd == nil {
		t.Fatal("confirmation must start the op")
	}
	start, ok := cmd().(startOpMsg)
	if !ok || start.name != "prune" {
		t.Fatalf("got %#v, want startOpMsg{prune}", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("prune result: %#v", res)
	}
	if done.deleted != 1 {
		t.Fatalf("deleted = %d, want 1", done.deleted)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("snapshots after prune = %d, want 1", len(snaps))
	}
	m, _ = v.Update(res)
	if m.(PruneView).stage != pruneDone {
		t.Fatal("flow must land in done stage")
	}
}

// TestPruneFlow_ContinuesPastAlreadyDeleted: matching the CLI, a
// drop-set snapshot deleted out-of-band between preview and apply must
// not abort the whole prune — the loop skips ErrNotFound and still GCs.
func TestPruneFlow_ContinuesPastAlreadyDeleted(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	v := NewPruneView(pruneDeps(r))
	if len(v.drop) != 1 {
		t.Fatalf("drop = %d, want 1", len(v.drop))
	}
	// Delete the dropped snapshot out-of-band before the op runs.
	if err := r.DeleteSnapshot(context.Background(), v.drop[0]); err != nil {
		t.Fatalf("pre-delete: %v", err)
	}
	m, cmd := v.Update(confirmedMsg{id: pruneConfirmID})
	v = m.(PruneView)
	start, ok := cmd().(startOpMsg)
	if !ok {
		t.Fatalf("expected startOpMsg, got %#v", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok {
		t.Fatalf("expected pruneDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("prune must not error on an already-deleted snapshot: %v", done.err)
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("snapshots after prune = %d, want 1 (kept)", len(snaps))
	}
}

// pruneDepsKeepAll returns Deps whose retention keeps every seeded
// snapshot, so the preview's drop set is empty.
func pruneDepsKeepAll(r *repo.Repo) Deps {
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 5
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	return Deps{Repo: r, Config: &cfg}
}

// confirmViaModal presses enter in the preview, walks through the modal
// the view pushed, and returns the confirmedMsg the modal emits — the
// exact message flow the App shell delivers in production.
func confirmViaModal(t *testing.T, v PruneView) confirmedMsg {
	t.Helper()
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should request the GC confirm modal")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	plain, ok := push.modal.(ConfirmModal)
	if !ok {
		t.Fatalf("GC-only gate must be a plain confirm (nothing is deleted), got %T", push.modal)
	}
	if !strings.Contains(plain.View(), "GC") {
		t.Errorf("GC confirm must name GC, not snapshot deletion:\n%s", plain.View())
	}
	_, mcmd := plain.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if mcmd == nil {
		t.Fatal("modal enter must emit a confirmation")
	}
	confirmed, ok := mcmd().(confirmedMsg)
	if !ok {
		t.Fatalf("expected confirmedMsg, got %#v", mcmd())
	}
	return confirmed
}

// TestPruneFlow_EmptyDropEnterOffersGCConfirm: when retention drops
// nothing, enter must still offer a GC-only run — but only offer it;
// nothing may be reclaimed before the confirm lands.
func TestPruneFlow_EmptyDropEnterOffersGCConfirm(t *testing.T) {
	r, store := gcFlowRepo(t)
	seedTwoSnapshots(t, r)
	const orphanKey = "data/de/deadbeef6666666666666666666666666666666666666666666666666666beef"
	if err := store.Put(context.Background(), orphanKey, strings.NewReader("orphan")); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}
	v := NewPruneView(pruneDepsKeepAll(r))
	if len(v.drop) != 0 {
		t.Fatalf("precondition: drop = %d, want 0", len(v.drop))
	}
	confirmViaModal(t, v) // asserts the modal shape; discard the confirmation
	if _, err := store.Stat(context.Background(), orphanKey); err != nil {
		t.Errorf("orphan reclaimed before the confirm was delivered: %v", err)
	}
}

// TestPruneFlow_GCOnlyConfirmedReclaimsOrphans: the TUI mirror of the
// CLI/jobs rule — an empty drop set must not make orphaned blobs
// (crashed backups, out-of-band deletes) uncollectable from this
// surface. Confirmed GC-only runs go through the same one-op guard.
func TestPruneFlow_GCOnlyConfirmedReclaimsOrphans(t *testing.T) {
	r, store := gcFlowRepo(t)
	seedTwoSnapshots(t, r)
	const orphanKey = "data/de/deadbeef7777777777777777777777777777777777777777777777777777beef"
	if err := store.Put(context.Background(), orphanKey, strings.NewReader("orphan")); err != nil {
		t.Fatalf("plant orphan: %v", err)
	}
	v := NewPruneView(pruneDepsKeepAll(r))

	m, cmd := v.Update(confirmViaModal(t, v))
	v = m.(PruneView)
	if cmd == nil {
		t.Fatal("confirmation must start the GC op")
	}
	start, ok := cmd().(startOpMsg)
	if !ok || start.name != "prune" {
		t.Fatalf("got %#v, want startOpMsg{prune} so the one-op guard applies", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok || done.err != nil {
		t.Fatalf("gc result: %#v", res)
	}
	if _, err := store.Stat(context.Background(), orphanKey); err == nil {
		t.Errorf("orphan blob survived a confirmed GC-only run")
	}
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 2 {
		t.Fatalf("snapshots after GC-only run = %d, want 2 untouched", len(snaps))
	}
	m, _ = v.Update(res)
	v = m.(PruneView)
	if v.stage != pruneDone {
		t.Fatal("flow must land in done stage")
	}
	out := v.View()
	if !strings.Contains(out, "reclaimed blobs") || !strings.Contains(out, "reclaimed bytes") {
		t.Errorf("done stage must report reclaimed blobs/bytes:\n%s", out)
	}
	if strings.Contains(out, "deleted snapshots") {
		t.Errorf("GC-only done stage must not claim snapshot deletions:\n%s", out)
	}
}

// TestPruneFlow_GCOnlyEmptyRepoIsCalmNoOp: zero snapshots makes every
// blob look orphaned — GC refuses (ErrEmptyRepo) and the view must
// present that as a calm no-op, not a failure, and reclaim nothing.
func TestPruneFlow_GCOnlyEmptyRepoIsCalmNoOp(t *testing.T) {
	r, store := gcFlowRepo(t)
	const blobKey = "data/de/deadbeef8888888888888888888888888888888888888888888888888888beef"
	if err := store.Put(context.Background(), blobKey, strings.NewReader("not actually garbage")); err != nil {
		t.Fatalf("plant blob: %v", err)
	}
	v := NewPruneView(pruneDepsKeepAll(r))

	m, cmd := v.Update(confirmViaModal(t, v))
	v = m.(PruneView)
	if cmd == nil {
		t.Fatal("confirmation must start the GC op")
	}
	start, ok := cmd().(startOpMsg)
	if !ok {
		t.Fatalf("expected startOpMsg, got %#v", cmd())
	}
	res := start.run(context.Background())
	done, ok := res.(pruneDoneMsg)
	if !ok {
		t.Fatalf("expected pruneDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("zero-snapshot store must be a calm no-op, got: %v", done.err)
	}
	if _, err := store.Stat(context.Background(), blobKey); err != nil {
		t.Errorf("blob deleted from a zero-snapshot store: %v", err)
	}
	m, _ = v.Update(res)
	out := m.(PruneView).View()
	if strings.Contains(out, "failed") {
		t.Errorf("empty-repo GC must not render as a failure:\n%s", out)
	}
	if !strings.Contains(out, "no snapshots") {
		t.Errorf("empty-repo GC should say why nothing was reclaimed:\n%s", out)
	}
}

// TestPruneFlow_AllDropRequiresWipeWord: a plan that would empty the
// repo must gate on the word "wipe", not the routine "prune" — the TUI
// mirror of the CLI's --all rail.
func TestPruneFlow_AllDropRequiresWipeWord(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	cfg := config.Defaults()
	cfg.Retention.KeepLast = 0
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	v := NewPruneView(Deps{Repo: r, Config: &cfg})
	if len(v.keep) != 0 || len(v.drop) == 0 {
		t.Fatalf("precondition: all-drop plan, got keep=%d drop=%d", len(v.keep), len(v.drop))
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = m
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatal("enter should push the confirm modal")
	}
	typed, ok := push.modal.(TypedConfirmModal)
	if !ok {
		t.Fatalf("all-drop must use the typed gate, got %T", push.modal)
	}
	if !strings.Contains(typed.View(), "wipe") {
		t.Errorf("all-drop gate must demand the word \"wipe\":\n%s", typed.View())
	}
}
