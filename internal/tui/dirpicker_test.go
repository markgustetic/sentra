package tui

import (
	"fmt"
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

// rows hold only real filesystem entries — the parent, then sorted folders. The
// Start button is the cursor slot past the last row, not a stored row, so enter
// on the rows means only "navigate".
func TestDirPickerRowsAreParentThenSortedDirs(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	if p.err != "" {
		t.Fatalf("unexpected error: %s", p.err)
	}
	want := []rowKind{rowParent, rowChild, rowChild, rowChild}
	if len(p.rows) != len(want) {
		t.Fatalf("rows = %d, want %d: %+v", len(p.rows), len(want), p.rows)
	}
	for i, k := range want {
		if p.rows[i].kind != k {
			t.Errorf("row %d kind = %v, want %v", i, p.rows[i].kind, k)
		}
	}
	// Directories only, sorted, starting after the parent row. The file must not
	// be offered as a backup root.
	for i, name := range []string{"alpha", "beta", "gamma"} {
		if got := p.rows[1+i].label; got != name {
			t.Errorf("row %d label = %q, want %q", 1+i, got, name)
		}
	}
	for _, r := range p.rows {
		if r.label == "notes.txt" {
			t.Error("a regular file must never be listed")
		}
	}
}

// The cursor ranges over [0, len(rows)] — the extra slot past the last row is
// the Start button, which down-spam must be able to reach.
func TestDirPickerCursorClamps(t *testing.T) {
	p := newDirPicker(tempTree(t))
	for i := 0; i < 20; i++ {
		p = p.moveDown()
	}
	if p.cursor != len(p.rows) {
		t.Errorf("cursor after down-spam = %d, want %d (the Start button)", p.cursor, len(p.rows))
	}
	if !p.onStart() {
		t.Error("down-spam must land on the Start button")
	}
	for i := 0; i < 20; i++ {
		p = p.moveUp()
	}
	if p.cursor != 0 {
		t.Errorf("cursor after up-spam = %d, want 0", p.cursor)
	}
}

// Enter on a child descends; enter on ".." ascends; neither chooses. Only the
// Start button (the slot past the last row) returns the chosen directory.
func TestDirPickerActivate(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	p.cursor = 1 // alpha (row 0 is "..")
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

	p.cursor = 0 // ".." (alpha has no subdirs, so the parent is the only row)
	p, chosen = p.activate()
	if chosen != "" {
		t.Errorf("ascending must not choose a folder, got %q", chosen)
	}
	if p.cwd != root {
		t.Fatalf("cwd after .. = %q, want %q", p.cwd, root)
	}

	p.cursor = len(p.rows) // the Start button
	if !p.onStart() {
		t.Fatal("cursor past the last row must be the Start button")
	}
	_, chosen = p.activate()
	if chosen != root {
		t.Errorf("the Start button must choose the current directory: %q, want %q", chosen, root)
	}
}

// An unreadable directory must surface an error, not panic or show stale rows —
// and it must still let the operator climb out or start.
func TestDirPickerUnreadableDirectory(t *testing.T) {
	p := newDirPicker(filepath.Join(t.TempDir(), "no-such-dir"))
	if p.err == "" {
		t.Error("an unreadable directory must set err")
	}
	// A parent row to climb out through.
	if len(p.rows) == 0 || p.rows[0].kind != rowParent {
		t.Error("even on error the picker must offer the parent row")
	}
	// And a Start button to commit anyway.
	p.cursor = len(p.rows)
	if _, chosen := p.activate(); chosen == "" {
		t.Error("the Start button must remain reachable even on an unreadable directory")
	}
}

// The action line reads its verb from the cursor. Enter means three different
// things in this picker, and a footer that promises "open" while the cursor
// rests on the Start button is lying — the same defect the setup wizard's
// print-IAM short-circuit fixed.
func TestDirPickerEnterVerbNamesWhatEnterActuallyDoes(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	p.cursor = 0 // ".."
	if got := p.enterVerb(); !strings.HasPrefix(got, "go up to ") {
		t.Errorf("on the parent row, verb = %q", got)
	}
	p.cursor = 1 // alpha
	if got := p.enterVerb(); got != "open alpha" {
		t.Errorf("on a child row, verb = %q", got)
	}
	p.cursor = len(p.rows) // the Start button
	if got, want := p.enterVerb(), "start the backup of "+filepath.Base(root); got != want {
		t.Errorf("on the Start button, verb = %q, want %q", got, want)
	}
}

// The Start button is drawn below the folder list and stays highlighted-able no
// matter how far the list scrolls: with more folders than fit, the "…" overflow
// indicator shows AND the pinned button still renders, and when the cursor is on
// it no folder row is marked.
func TestDirPickerStartButtonPinnedBelowScrollingList(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 40; i++ { // far more than dirPickerHeight
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := newDirPicker(root)
	p.cursor = len(p.rows) // the Start button

	out := p.View(true)
	if !strings.Contains(out, "…") {
		t.Errorf("a list longer than the window must show the overflow indicator:\n%s", out)
	}
	if !strings.Contains(out, "▸ start backup of "+filepath.Base(root)) {
		t.Errorf("the Start button must render below the folder list:\n%s", out)
	}
	// The marker sits on the Start button line, not on any folder row.
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "▍") && !strings.Contains(line, "▸ start backup") {
			t.Errorf("only the Start button may be marked when the cursor is on it, got: %q", line)
		}
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
