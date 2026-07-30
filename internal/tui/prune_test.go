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
