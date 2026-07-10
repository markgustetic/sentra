package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tempTree builds  root/{alpha, beta, gamma}  plus a file, which must not appear.
func tempTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"beta", "alpha", "gamma"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// Row 0 is always "use this folder" and row 1 the parent, so enter has exactly
// one meaning — activate the highlighted row — and never doubles as "choose".
func TestDirPickerRowsAreCurrentParentThenSortedDirs(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	if p.err != "" {
		t.Fatalf("unexpected error: %s", p.err)
	}
	want := []rowKind{rowUseCurrent, rowParent, rowChild, rowChild, rowChild}
	if len(p.rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(p.rows), len(want), p.rows)
	}
	for i, k := range want {
		if p.rows[i].kind != k {
			t.Errorf("row %d kind = %v, want %v", i, p.rows[i].kind, k)
		}
	}
	// Directories only, sorted. The file must not be offered as a backup root.
	for i, name := range []string{"alpha", "beta", "gamma"} {
		if got := p.rows[2+i].label; got != name {
			t.Errorf("row %d label = %q, want %q", 2+i, got, name)
		}
	}
	for _, r := range p.rows {
		if r.label == "notes.txt" {
			t.Error("a regular file must never be listed")
		}
	}
}

func TestDirPickerCursorClamps(t *testing.T) {
	p := newDirPicker(tempTree(t))
	for i := 0; i < 20; i++ {
		p = p.moveDown()
	}
	if p.cursor != len(p.rows)-1 {
		t.Errorf("cursor after down-spam = %d, want %d", p.cursor, len(p.rows)-1)
	}
	for i := 0; i < 20; i++ {
		p = p.moveUp()
	}
	if p.cursor != 0 {
		t.Errorf("cursor after up-spam = %d, want 0", p.cursor)
	}
}

// Enter on a child descends; enter on ".." ascends; enter on row 0 chooses.
func TestDirPickerActivate(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	p.cursor = 2 // alpha
	p, chosen := p.activate()
	if chosen != "" {
		t.Errorf("descending must not choose a folder, got %q", chosen)
	}
	if filepath.Base(p.cwd) != "alpha" {
		t.Fatalf("cwd = %q, want .../alpha", p.cwd)
	}
	if p.cursor != 0 {
		t.Errorf("cursor must reset on descend, got %d", p.cursor)
	}

	p.cursor = 1 // ".."
	p, chosen = p.activate()
	if chosen != "" {
		t.Errorf("ascending must not choose a folder, got %q", chosen)
	}
	if p.cwd != root {
		t.Fatalf("cwd after .. = %q, want %q", p.cwd, root)
	}

	p.cursor = 0 // "use this folder"
	_, chosen = p.activate()
	if chosen != root {
		t.Errorf("choose = %q, want %q", chosen, root)
	}
}

// An unreadable directory must surface an error, not panic or show stale rows.
func TestDirPickerUnreadableDirectory(t *testing.T) {
	p := newDirPicker(filepath.Join(t.TempDir(), "no-such-dir"))
	if p.err == "" {
		t.Error("an unreadable directory must set err")
	}
	// It still offers a way out.
	if len(p.rows) == 0 || p.rows[0].kind != rowUseCurrent {
		t.Error("even on error the picker must offer 'use this folder' / parent rows")
	}
}

// The action line reads its verb from the highlighted row. Enter means three
// different things in this picker, and a footer that promises "open" while the
// cursor rests on "use this folder" is lying — the same defect the setup
// wizard's print-IAM short-circuit fixed.
func TestDirPickerEnterVerbNamesWhatEnterActuallyDoes(t *testing.T) {
	p := newDirPicker(tempTree(t))

	p.cursor = 0
	if got := p.enterVerb(); got != "Back up this folder" {
		t.Errorf("on the use-this-folder row, verb = %q", got)
	}
	p.cursor = 1
	if got := p.enterVerb(); !strings.HasPrefix(got, "Go up to ") {
		t.Errorf("on the parent row, verb = %q", got)
	}
	p.cursor = 2
	if got := p.enterVerb(); got != "Open alpha" {
		t.Errorf("on a child row, verb = %q", got)
	}
}

// An unfocused picker must not draw the selection marker, or two controls look
// like they own the keyboard at once.
func TestDirPickerMarkerOnlyWhenFocused(t *testing.T) {
	p := newDirPicker(tempTree(t))
	if !strings.Contains(p.View(true), "▍") {
		t.Error("a focused picker must mark its highlighted row")
	}
	if strings.Contains(p.View(false), "▍") {
		t.Error("an unfocused picker must not mark any row")
	}
}
