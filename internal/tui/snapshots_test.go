package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
)

func sampleSnaps() []repo.SnapshotInfo {
	return []repo.SnapshotInfo{
		{
			ID:        "snap-aaaa",
			CreatedAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
			Tag:       "weekly",
			Stats:     repo.SnapshotStats{Files: 100, Bytes: 1024},
		},
		{
			ID:        "snap-bbbb",
			CreatedAt: time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC),
			Tag:       "daily",
			Stats:     repo.SnapshotStats{Files: 200, Bytes: 2048},
		},
	}
}

func sampleManifest() repo.Manifest {
	return repo.Manifest{
		Version:   1,
		ID:        "snap-aaaa",
		CreatedAt: time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		Tag:       "weekly",
		Tree: []repo.FileEntry{
			{Path: "src/a.go", Size: 100},
			{Path: "src/b.go", Size: 200},
			{Path: "README.md", Size: 50},
		},
		Stats: repo.SnapshotStats{Files: 3, Bytes: 350},
	}
}

// TestSnapshots_RendersAllSnapshots verifies every row in the model
// renders into the table view. We hydrate via SetSnapshots so tests
// don't need a live repo.
func TestSnapshots_RendersAllSnapshots(t *testing.T) {
	s := NewSnapshots(Deps{})
	s = s.SetSnapshots(sampleSnaps())
	view := s.View()
	if !strings.Contains(view, "snap-aaaa") {
		t.Errorf("view missing snap-aaaa: %s", view)
	}
	if !strings.Contains(view, "snap-bbbb") {
		t.Errorf("view missing snap-bbbb: %s", view)
	}
}

// TestSnapshots_NavigatesWithArrows asserts the cursor moves down
// when a Down key arrives. We don't pin the absolute index because
// table internals may differ; just check movement happened from 0.
func TestSnapshots_NavigatesWithArrows(t *testing.T) {
	s := NewSnapshots(Deps{})
	s = s.SetSnapshots(sampleSnaps())
	if s.cursor() != 0 {
		t.Fatalf("initial cursor: got %d, want 0", s.cursor())
	}
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyDown})
	got := updated.(Snapshots)
	if got.cursor() == 0 {
		t.Errorf("cursor did not advance on Down key")
	}
}

// TestSnapshots_EnterOpensDetail asserts that Enter on a row sets
// the selected snapshot ID and switches to the detail sub-view. The
// detail view's contents are loaded via the detailLoader hook, which
// runs inside the returned tea.Cmd — the test executes it and feeds
// the resulting message back, standing in for the Bubbletea runtime.
func TestSnapshots_EnterOpensDetail(t *testing.T) {
	loaderCalled := ""
	manifest := sampleManifest()
	s := NewSnapshotsWithLoader(Deps{}, func(id string) (repo.Manifest, error) {
		loaderCalled = id
		return manifest, nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Snapshots)
	if !got.detailOpen {
		t.Fatalf("detail not opened after enter")
	}
	if cmd == nil {
		t.Fatal("enter must return a load command")
	}
	updated, _ = got.Update(cmd())
	got = updated.(Snapshots)
	if loaderCalled == "" {
		t.Errorf("detail loader was not invoked")
	}
	// The detail is a directory summary: the "src" directory (holding a.go,
	// b.go) appears with its file count, and the root-level README.md is
	// counted as a file at the root — individual paths are folded into counts.
	view := got.View()
	if !strings.Contains(view, "src/") {
		t.Errorf("detail view missing the src/ directory:\n%s", view)
	}
	if !strings.Contains(view, "2 files") {
		t.Errorf("detail view should count src/'s 2 files:\n%s", view)
	}
	if !strings.Contains(view, "files here") {
		t.Errorf("detail view should count the root-level README.md:\n%s", view)
	}
}

// TestSnapshots_EnterLoadsDetailAsync pins the rule that the manifest
// load — which can hit S3 — never runs inline in Update. Bubbletea
// calls Update on its single event goroutine, so a blocking call there
// freezes rendering AND input for the whole TUI (up to the loader's
// 10s timeout). The load must happen inside the returned tea.Cmd, with
// the detail page opening immediately in its loading state.
func TestSnapshots_EnterLoadsDetailAsync(t *testing.T) {
	loaderCalls := 0
	s := NewSnapshotsWithLoader(Deps{}, func(string) (repo.Manifest, error) {
		loaderCalls++
		return sampleManifest(), nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Snapshots)
	if loaderCalls != 0 {
		t.Fatalf("loader ran %d time(s) synchronously inside Update; it must run in the returned tea.Cmd", loaderCalls)
	}
	if !got.detailOpen {
		t.Fatal("detail page should open immediately (loading state)")
	}
	if cmd == nil {
		t.Fatal("enter must return a tea.Cmd that performs the load")
	}
	if msg := cmd(); loaderCalls != 1 {
		t.Fatalf("executing the command should invoke the loader once, got %d", loaderCalls)
	} else if updated, _ = got.Update(msg); !strings.Contains(updated.(Snapshots).View(), "src/") {
		t.Errorf("detail content missing after load message applied:\n%s", updated.(Snapshots).View())
	}
}

// TestSnapshots_StaleDetailResultDropped: a load that resolves after
// the operator already esc'd out must not reopen the detail page.
func TestSnapshots_StaleDetailResultDropped(t *testing.T) {
	s := NewSnapshotsWithLoader(Deps{}, func(string) (repo.Manifest, error) {
		return sampleManifest(), nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := updated.(Snapshots)
	if cmd == nil {
		t.Fatal("enter must return a load command")
	}
	updated, _ = got.Update(tea.KeyMsg{Type: tea.KeyEsc}) // esc before the load resolves
	got = updated.(Snapshots)
	updated, _ = got.Update(cmd()) // stale result arrives late
	if updated.(Snapshots).detailOpen {
		t.Errorf("stale detail result must not reopen the closed detail page")
	}
}

// TestSnapshots_EscClosesDetail rounds out the navigation cycle:
// from inside detail, esc returns to the table.
func TestSnapshots_EscClosesDetail(t *testing.T) {
	s := NewSnapshotsWithLoader(Deps{}, func(_ string) (repo.Manifest, error) {
		return sampleManifest(), nil
	})
	s = s.SetSnapshots(sampleSnaps())
	updated, _ := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(Snapshots).detailOpen {
		t.Fatal("detail did not open")
	}
	updated, _ = updated.(Snapshots).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if updated.(Snapshots).detailOpen {
		t.Errorf("detail did not close on esc")
	}
}

// TestSnapshots_EmptyRepo asserts the view renders a placeholder
// rather than crashing on a zero-length snapshot list.
func TestSnapshots_EmptyRepo(t *testing.T) {
	s := NewSnapshots(Deps{})
	view := s.View()
	if !strings.Contains(strings.ToLower(view), "no snapshots") {
		t.Errorf("empty snapshots did not render placeholder: %s", view)
	}
}

// TestSnapshots_ReloadsAfterOpCompletes: the list is hydrated once at launch,
// so a backup taken in this session must reload it — before, the list stayed
// frozen at launch-time contents and a fresh snapshot never appeared until the
// operator restarted sentra. Any completed op (marked by opResultMsg) triggers
// the reload.
func TestSnapshots_ReloadsAfterOpCompletes(t *testing.T) {
	r := newFlowRepo(t)
	s := NewSnapshots(Deps{Repo: r}) // empty repo → no rows
	if len(s.snaps) != 0 {
		t.Fatalf("precondition: want 0 snapshots, got %d", len(s.snaps))
	}

	seedTaggedSnaps(t, r, "nightly") // a backup lands in the repo

	m, cmd := s.Update(backupDoneMsg{})
	s = m.(Snapshots)
	if cmd == nil {
		t.Fatal("op completion must return the reload command")
	}
	m, _ = s.Update(cmd()) // run the reload, as the runtime would
	s = m.(Snapshots)
	if len(s.snaps) != 1 {
		t.Fatalf("snapshots must reload after an op completes: want 1, got %d", len(s.snaps))
	}
}

// TestSnapshots_ReloadWithNoRepoDoesNotPanic pins the nil-safety of the
// op-completion reload: a shell built before unlock (or any Deps{} view) still
// receives the broadcast opResultMsg, and reloading must yield an empty list
// rather than dereferencing a nil repo. This panicked before loadSnapshotsBestEffort
// grew its nil guard.
func TestSnapshots_ReloadWithNoRepoDoesNotPanic(t *testing.T) {
	s := NewSnapshots(Deps{}) // no repo
	m, _ := s.Update(backupDoneMsg{})
	if got := len(m.(Snapshots).snaps); got != 0 {
		t.Errorf("no-repo reload must be empty, got %d", got)
	}
}

// TestSortSnaps locks each ordering: date/size/files descending (interesting end
// first), tag/id ascending.
func TestSortSnaps(t *testing.T) {
	top := func(mode snapSort) string {
		s := append([]repo.SnapshotInfo(nil), sampleSnaps()...)
		sortSnaps(s, mode)
		return s[0].ID
	}
	// bbbb is newer (May 2), bigger (2048), more files (200); aaaa sorts first
	// by ascending tag ("daily" < ... no: daily is bbbb) and ascending id.
	if got := top(sortDate); got != "snap-bbbb" {
		t.Errorf("sortDate top = %s, want snap-bbbb", got)
	}
	if got := top(sortSize); got != "snap-bbbb" {
		t.Errorf("sortSize top = %s, want snap-bbbb", got)
	}
	if got := top(sortFiles); got != "snap-bbbb" {
		t.Errorf("sortFiles top = %s, want snap-bbbb", got)
	}
	if got := top(sortTag); got != "snap-bbbb" { // "daily" < "weekly"
		t.Errorf("sortTag top = %s, want snap-bbbb (daily)", got)
	}
	if got := top(sortName); got != "snap-aaaa" { // "snap-aaaa" < "snap-bbbb"
		t.Errorf("sortName top = %s, want snap-aaaa", got)
	}
}

// TestFilterSnaps: case-insensitive substring over id and tag; empty keeps all.
func TestFilterSnaps(t *testing.T) {
	s := sampleSnaps()
	if got := filterSnaps(s, ""); len(got) != 2 {
		t.Errorf("empty filter kept %d, want 2", len(got))
	}
	if got := filterSnaps(s, "WEEKLY"); len(got) != 1 || got[0].ID != "snap-aaaa" {
		t.Errorf("tag filter = %v, want [snap-aaaa]", got)
	}
	if got := filterSnaps(s, "bbbb"); len(got) != 1 || got[0].ID != "snap-bbbb" {
		t.Errorf("id filter = %v, want [snap-bbbb]", got)
	}
	if got := filterSnaps(s, "nope"); len(got) != 0 {
		t.Errorf("no-match filter kept %d, want 0", len(got))
	}
}

// TestSnapshots_SortKeyCycles: 's' advances the sort mode and re-renders.
func TestSnapshots_SortKeyCycles(t *testing.T) {
	s := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())
	if s.sortMode != sortDate {
		t.Fatalf("default sort = %v, want date", s.sortMode)
	}
	m, _ := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if got := m.(Snapshots).sortMode; got != sortSize {
		t.Errorf("after one 's', sort = %v, want size", got)
	}
}

// TestSnapshots_FilterFlow: '/' opens the filter (capturing text), typing narrows
// the table, esc clears it.
func TestSnapshots_FilterFlow(t *testing.T) {
	s := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())

	m, _ := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("/")})
	s = m.(Snapshots)
	if !s.filtering || !s.CapturesText() {
		t.Fatal("'/' must open the filter and capture text")
	}
	m, _ = s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("weekly")})
	s = m.(Snapshots)
	if v := s.View(); !strings.Contains(v, "snap-aaaa") || strings.Contains(v, "snap-bbbb") {
		t.Errorf("filter 'weekly' must show only snap-aaaa:\n%s", v)
	}
	m, _ = s.Update(tea.KeyMsg{Type: tea.KeyEsc})
	s = m.(Snapshots)
	if s.filtering {
		t.Error("esc must close the filter")
	}
	if v := s.View(); !strings.Contains(v, "snap-bbbb") {
		t.Errorf("esc must clear the filter (both rows back):\n%s", v)
	}
}

// TestSnapshots_CopyID: 'y' copies the highlighted id through the injected
// clipboard fn and shows a confirmation.
func TestSnapshots_CopyID(t *testing.T) {
	var captured string
	s := NewSnapshots(Deps{}).SetSnapshots(sampleSnaps())
	s.copyFn = func(id string) error { captured = id; return nil }

	m, _ := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	s = m.(Snapshots)
	if !strings.HasPrefix(captured, "snap-") {
		t.Errorf("y must copy a snapshot id, got %q", captured)
	}
	if !strings.Contains(s.View(), "copied") {
		t.Errorf("copy must show a notice:\n%s", s.View())
	}
}

// TestInitialSnapshots_UsesPreload: when the App has set a shared preload,
// initialSnapshots returns it WITHOUT touching the repo (proved by a nil Repo
// that would panic if dereferenced), and a preload error propagates.
func TestInitialSnapshots_UsesPreload(t *testing.T) {
	pre := &snapshotPreload{snaps: []repo.SnapshotInfo{{ID: "x"}}}
	snaps, err := initialSnapshots(Deps{preload: pre}) // Repo is nil
	if err != nil || len(snaps) != 1 || snaps[0].ID != "x" {
		t.Errorf("preload path = %v, %v; want [{x}], nil", snaps, err)
	}
	if _, err := initialSnapshots(Deps{preload: &snapshotPreload{err: errTest}}); err == nil {
		t.Error("a preload error must propagate")
	}
}

var errTest = errors.New("boom")

// TestApp_SharesOneSnapshotLoad: NewApp performs the snapshot load once and the
// snapshot-consuming views construct from that shared preload — not five
// independent ListSnapshots.
func TestApp_SharesOneSnapshotLoad(t *testing.T) {
	r := newFlowRepo(t)
	seedTaggedSnaps(t, r, "a", "b")
	app := NewApp(Deps{Repo: r, RepoName: "x"})

	if app.Deps().preload == nil {
		t.Fatal("NewApp must set a shared snapshot preload")
	}
	if n := len(app.Deps().preload.snaps); n != 2 {
		t.Fatalf("preload holds %d snapshots, want 2", n)
	}
	if got := app.views[indexOf(app, "dashboard")].model.(Dashboard).data.SnapshotCount; got != 2 {
		t.Errorf("dashboard did not use the shared load: count = %d", got)
	}
	if got := len(app.views[indexOf(app, "snapshots")].model.(Snapshots).snaps); got != 2 {
		t.Errorf("snapshots view did not use the shared load: %d", got)
	}
}

// TestSnapshots_PinToggleSubmitsOp: 'p' on a row submits a pin op
// through the one-op guard (mutating — it takes the repo lock), and
// the completed op's broadcast reload repaints the pin marker.
func TestSnapshots_PinToggleSubmitsOp(t *testing.T) {
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}

	s := NewSnapshots(Deps{Repo: r})
	s = s.SetSnapshots(loadSnapshotsBestEffort(Deps{Repo: r}))
	m, cmd := s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	s = m.(Snapshots)
	if cmd == nil {
		t.Fatal("p should emit a command carrying the pin op")
	}
	start, ok := cmd().(startOpMsg)
	if !ok {
		t.Fatal("pin must go through the one-op guard (startOpMsg)")
	}
	result := start.run(context.Background())
	if _, ok := result.(opResultMsg); !ok {
		t.Fatalf("pin op must resolve to an opResultMsg, got %T", result)
	}

	pins, err := r.Pins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pins[snap.ID]; !ok {
		t.Fatal("pin op did not pin the snapshot")
	}

	// The op broadcast reloads the table (async — drive the returned
	// command); the pinned row is marked.
	m, reload := s.Update(result)
	s = m.(Snapshots)
	m, _ = s.Update(reload())
	s = m.(Snapshots)
	if !strings.Contains(s.View(), "*") {
		t.Errorf("pinned row should carry the pin marker:\n%s", s.View())
	}

	// A second toggle unpins.
	m, cmd = s.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("p")})
	_ = m
	start = cmd().(startOpMsg)
	if res := start.run(context.Background()); res == nil {
		t.Fatal("unpin op returned nil")
	}
	pins, err = r.Pins(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pins) != 0 {
		t.Fatalf("second toggle should unpin, pins=%v", pins)
	}
}

// TestSnapshots_DetailShowsSymlinks: v2 manifests carry symlink
// entries; the detail page must show them (they're invisible in the
// dir tree, which folds only chunk-backed files).
func TestSnapshots_DetailShowsSymlinks(t *testing.T) {
	man := sampleManifest()
	man.Tree = append(man.Tree, repo.FileEntry{
		Path: "ln", Kind: repo.EntryKindSymlink, LinkTarget: "src/a.go",
	})
	s := NewSnapshotsWithLoader(Deps{}, func(string) (repo.Manifest, error) {
		return man, nil
	})
	s = s.SetSnapshots(sampleSnaps())
	m, cmd := s.Update(tea.KeyMsg{Type: tea.KeyEnter})
	s = m.(Snapshots)
	m, _ = s.Update(cmd())
	s = m.(Snapshots)
	if !strings.Contains(s.View(), "ln -> src/a.go") {
		t.Errorf("detail should list symlinks with targets:\n%s", s.View())
	}
}

// TestSnapshots_OpReloadIsAsync pins the same rule the detail loader
// obeys: the post-op reload hits the blobstore (snapshot list + pin
// set — two network reads), so it must run in the returned tea.Cmd,
// never inline in Update, which would freeze the whole TUI after
// EVERY completed operation app-wide.
func TestSnapshots_OpReloadIsAsync(t *testing.T) {
	r := newFlowRepo(t)
	s := NewSnapshots(Deps{Repo: r}) // constructed against an empty repo

	seedTaggedSnaps(t, r, "nightly") // an op lands a snapshot

	m, cmd := s.Update(backupDoneMsg{})
	s = m.(Snapshots)
	if len(s.snaps) != 0 {
		t.Fatal("reload ran synchronously inside Update; it must run in the returned tea.Cmd")
	}
	if cmd == nil {
		t.Fatal("opResultMsg must return the reload command")
	}
	m, _ = s.Update(cmd())
	s = m.(Snapshots)
	if len(s.snaps) != 1 {
		t.Fatalf("applying the reload message should refresh the list, got %d snapshots", len(s.snaps))
	}
}
