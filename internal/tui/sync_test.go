package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// writeDestConfig writes a minimal sentra.yaml at a temp path whose S3
// bucket differs from the source's, so the sameS3Location guard passes.
// SyncView only reads the file to (a) confirm it exists and (b) hand it
// to config.Load; the dest store is built by the stub NewStore below,
// which ignores the config contents entirely.
func writeDestConfig(t *testing.T, bucket string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	body := "repo:\n  s3:\n    bucket: " + bucket + "\n    prefix: \"\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write dest config: %v", err)
	}
	return path
}

// stubNewStore returns a Deps.NewStore that always yields the same
// in-memory store, letting a sync run end-to-end without S3. The
// returned store is the sync destination.
func stubNewStore(dst blobstore.Store) func(context.Context, *config.Config) (blobstore.Store, error) {
	return func(context.Context, *config.Config) (blobstore.Store, error) {
		return dst, nil
	}
}

func typeIntoSync(v SyncView, s string) SyncView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SyncView)
	}
	return v
}

// TestSyncFlow_EnterValidatesAndPushesConfirm: with a real dest config
// path typed and a valid dest store, enter must NOT start the op
// directly — it must push a ConfirmModal keyed to syncConfirmID.
func TestSyncFlow_EnterValidatesAndPushesConfirm(t *testing.T) {
	r := newFlowRepo(t)
	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd == nil {
		t.Fatal("enter on a valid dest path must emit a command")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	if !strings.Contains(push.modal.View(), "Confirm sync") {
		t.Errorf("modal should be the sync confirmation:\n%s", push.modal.View())
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage before confirm = %v, want syncConfigure", v.stage)
	}
}

// TestSyncFlow_ConfirmStartsOpAndSyncsBlobs: after confirmation the flow
// emits startOpMsg{name:"sync"} batched with a seeded opTickMsg; running
// the op copies the source's blobs to the dest store and returns a
// syncDoneMsg with accurate stats.
func TestSyncFlow_ConfirmStartsOpAndSyncsBlobs(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r) // one snapshot => data/ + snapshots/ blobs on src

	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)
	// Enable --init-dest so the empty dest is bootstrapped rather than
	// refused with ErrEmptyDest.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → snapshots field
	v = m.(SyncView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → init-dest toggle
	v = m.(SyncView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // toggle on
	v = m.(SyncView)

	// Enter -> validate -> push confirm.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected confirm modal, got %#v", cmd())
	}

	// The App broadcasts confirmedMsg back to the flow.
	m, cmd = v.Update(confirmedMsg{id: syncConfirmID})
	v = m.(SyncView)
	if v.stage != syncRunning {
		t.Fatalf("stage after confirm = %v, want syncRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart, foundTick bool
	for _, msg := range msgs {
		switch mm := msg.(type) {
		case startOpMsg:
			start, foundStart = mm, true
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart {
		t.Fatalf("expected startOpMsg in batch, got %#v", msgs)
	}
	if !foundTick {
		t.Fatalf("expected seeded opTickMsg in batch, got %#v", msgs)
	}
	if start.name != "sync" {
		t.Fatalf("op name = %q, want sync", start.name)
	}

	res := start.run(context.Background())
	done, ok := res.(syncDoneMsg)
	if !ok {
		t.Fatalf("expected syncDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("sync failed: %v", done.err)
	}
	if !done.stats.Bootstrapped {
		t.Errorf("init-dest run should report Bootstrapped")
	}
	if done.stats.CopiedBlobs == 0 {
		t.Errorf("expected copied blobs > 0, got %d", done.stats.CopiedBlobs)
	}

	// syncDoneMsg must implement opResult so the App guard clears.
	var _ opResultMsg = syncDoneMsg{}

	// Delivering the result advances to done and renders the summary.
	m, _ = v.Update(res)
	v = m.(SyncView)
	if v.stage != syncDone {
		t.Fatalf("stage after result = %v, want syncDone", v.stage)
	}
	if out := v.View(); !strings.Contains(out, "Sync complete") {
		t.Errorf("done view should confirm completion:\n%s", out)
	}

	// The dest store really has the source's data/ blobs now.
	got, err := dst.List(context.Background(), repo.DataPrefix)
	if err != nil {
		t.Fatalf("dst.List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("dest should have received data/ blobs")
	}
}

// TestSyncFlow_DryRunSkipsConfirm: a dry-run needs no confirmation gate —
// it writes nothing. Enter with dry-run on must start the op directly.
func TestSyncFlow_DryRunSkipsConfirm(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)
	v.initDest = true // bootstrap allowed in the plan (dry-run writes nothing)
	v.dryRun = true

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if v.stage != syncRunning {
		t.Fatalf("dry-run enter should go straight to running, stage = %v", v.stage)
	}
	msgs := execCmds(t, cmd)
	var foundStart bool
	for _, msg := range msgs {
		if _, ok := msg.(startOpMsg); ok {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("dry-run enter must emit startOpMsg, got %#v", msgs)
	}
	// Dry-run performs no writes on dest.
	got, _ := dst.List(context.Background(), repo.DataPrefix)
	if len(got) != 0 {
		t.Fatalf("dry-run must not write to dest, found %d blobs", len(got))
	}
	_ = v
}

// TestSyncFlow_SameLocationRefused: a dest config whose bucket+prefix
// equals the source's must be refused BEFORE any store is built.
func TestSyncFlow_SameLocationRefused(t *testing.T) {
	r := newFlowRepo(t)
	// Source config carries the same bucket the dest file will use.
	srcCfg := &config.Config{}
	srcCfg.Repo.S3.Bucket = "same-bucket"
	dstPath := writeDestConfig(t, "same-bucket")

	built := false
	v := NewSyncView(Deps{
		Repo:   r,
		Config: srcCfg,
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			built = true
			return blobstore.NewMemory(), nil
		},
	})
	v = typeIntoSync(v, dstPath)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd != nil {
		t.Fatalf("same-location sync must not emit a command, got %#v", cmd())
	}
	if built {
		t.Fatal("same-location refusal must short-circuit before building a store")
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "same S3 location") {
		t.Errorf("view should surface the same-location error:\n%s", v.View())
	}
}

// TestSyncFlow_MissingPathRefuses: a dest path that does not exist keeps
// the flow in configure with a validation error.
func TestSyncFlow_MissingPathRefuses(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	v = typeIntoSync(v, "/definitely/not/a/real/sentra.yaml")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd != nil {
		t.Fatal("nonexistent dest config must not start a sync")
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the path error:\n%s", v.View())
	}
}

// TestSyncFlow_OpRejectedResetsStage: an opRejectedMsg{name:"sync"} while
// running resets the flow to configure with a notice.
func TestSyncFlow_OpRejectedResetsStage(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	v.stage = syncRunning
	m, _ := v.Update(opRejectedMsg{name: "sync"})
	v = m.(SyncView)
	if v.stage != syncConfigure {
		t.Fatalf("stage after rejection = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "in progress") {
		t.Errorf("rejection notice should be shown:\n%s", v.View())
	}
}

// TestSyncFlow_SelectedSnapshotOnly: the configure stage's snapshot
// field (tab-reachable) narrows the copy to the named refs — the TUI
// face of `sync --snapshot`.
func TestSyncFlow_SelectedSnapshotOnly(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "one.txt"), []byte(strings.Repeat("one-", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	s1, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "two.txt"), []byte(strings.Repeat("two-", 100)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}

	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)

	// tab to the snapshot field and type a suffix ref for s1.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → snapshots field
	v = m.(SyncView)
	suffix := s1.ID[len(s1.ID)-8:]
	for _, ch := range suffix {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(SyncView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → init-dest toggle
	v = m.(SyncView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	v = m.(SyncView)

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected confirm modal, got %#v", cmd())
	}
	_, cmd = v.Update(confirmedMsg{id: syncConfirmID})
	var start startOpMsg
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok {
			start = s
		}
	}
	res := start.run(context.Background())
	if done, ok := res.(syncDoneMsg); !ok || done.err != nil {
		t.Fatalf("sync op: %#v", res)
	}

	dstRepo, err := repo.Open(context.Background(), dst, []byte("flow-test-pass"))
	if err != nil {
		t.Fatal(err)
	}
	defer dstRepo.Close()
	infos, err := dstRepo.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(infos) != 1 || infos[0].ID != s1.ID {
		t.Fatalf("dest snapshots: got %+v, want only %s", infos, s1.ID)
	}
}

// TestSync_ExactlyOneBoxAndItFollowsFocus mirrors the brief's canonical
// shape for the dst/snapshots pair. Construction focuses dstPath
// (sync.go:94) — the landing state for this flow — so Init must schedule
// the blink, the same contract unlock's/password's Init establishes. Init's
// cmd is the REAL one Focus() produced at construction (see
// SyncView.initBlink); executing it would block for the field's
// Cursor.BlinkSpeed (~530ms), and there is no field handle to preset that
// before NewSyncView's internal Focus() call runs, so this only checks the
// cmd exists — TestBlinkChain_ClosesEndToEnd (snapshots_test.go) proves the
// real round-trip once, on a key-triggered site where BlinkSpeed can be
// dropped first.
func TestSync_ExactlyOneBoxAndItFollowsFocus(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	if v.Init() == nil {
		t.Fatal("expected a blink command, got nil")
	}

	base := v
	base.dstPath.Blur()
	base.snapRefs.Blur()
	n := boxCount(base.View())

	if got := boxCount(v.View()); got != n+1 {
		t.Fatalf("dstPath focused: boxCount = %d, want %d (+1 over blurred)", got, n+1)
	}

	// snapRefs already exists on v, so its BlinkSpeed can be dropped before
	// tab fires — the tab handler's cmd is the REAL one Focus() produces,
	// and executing it (assertBlinkCmd does) would otherwise block for the
	// default ~530ms.
	v.snapRefs.Cursor.BlinkSpeed = time.Millisecond
	tabbed, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // path -> snapshots
	tv := tabbed.(SyncView)
	if got := boxCount(tv.View()); got != n+1 {
		t.Fatalf("box count changed on tab (got %d, want %d) — box must follow focus, one at a time", got, n+1)
	}
	assertBlinkCmd(t, cmd)

	tv.snapRefs.Cursor.BlinkSpeed = time.Millisecond
	tick := tv.snapRefs.Cursor.BlinkCmd()
	if _, tickCmd := tv.Update(tick()); tickCmd == nil {
		t.Fatal("blink tick not routed to the newly focused snapRefs field")
	}
}

// TestSync_RoutesBlinkTicksToDstPathField exercises the switch's other arm:
// a tick reaches dstPath while it holds focus (the state right after
// construction).
func TestSync_RoutesBlinkTicksToDstPathField(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	v.dstPath.Cursor.BlinkSpeed = time.Millisecond
	tick := v.dstPath.Cursor.BlinkCmd()
	if _, cmd := v.Update(tick()); cmd == nil {
		t.Fatal("blink tick not routed to the focused dstPath field")
	}
}

// TestSync_NoBoxWhenToggleFocused: tabbing past both text fields onto the
// init-dest/dry-run toggles must drop the box entirely — neither toggle is a
// text field, so the box must never mark a fixed position.
func TestSync_NoBoxWhenToggleFocused(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	base := v
	base.dstPath.Blur()
	base.snapRefs.Blur()
	n := boxCount(base.View())

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // path -> snapshots
	v = m.(SyncView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // snapshots -> init-dest
	v = m.(SyncView)
	if got := boxCount(v.View()); got != n {
		t.Fatalf("toggle focused: boxCount = %d, want %d (no box on a non-text field)", got, n)
	}
}
