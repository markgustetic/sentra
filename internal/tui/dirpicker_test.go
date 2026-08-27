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
// Start button is the cursor slot BEFORE the rows (the top, default option), not
// a stored row, so enter on the rows means only "navigate".
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

// The cursor ranges over [0, len(rows)] — cursor 0 is the Start button (the
// top, default option) and 1..len(rows) are the folder rows. A fresh picker
// opens on the button; up-spam returns to it, down-spam reaches the last row.
func TestDirPickerCursorClamps(t *testing.T) {
	p := newDirPicker(tempTree(t))
	if p.cursor != 0 || !p.onStart() {
		t.Errorf("a fresh picker must open on the Start button, cursor=%d", p.cursor)
	}
	for range 20 {
		p = p.moveDown()
	}
	if p.cursor != len(p.rows) {
		t.Errorf("cursor after down-spam = %d, want %d (the last folder row)", p.cursor, len(p.rows))
	}
	if p.onStart() {
		t.Error("down-spam must leave the Start button")
	}
	for range 20 {
		p = p.moveUp()
	}
	if p.cursor != 0 || !p.onStart() {
		t.Errorf("up-spam must land back on the Start button, cursor=%d", p.cursor)
	}
}

// Enter on a child descends; enter on ".." ascends; neither chooses. Only the
// Start button (cursor 0, the top) returns the chosen directory. Descending
// resets the cursor onto the Start button so the operator can immediately back
// up the folder they just entered.
func TestDirPickerActivate(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	p.cursor = 2 // alpha (cursor 0 = Start button, cursor 1 = "..")
	p, chosen := p.activate()
	if chosen != "" {
		t.Errorf("descending must not choose a folder, got %q", chosen)
	}
	if filepath.Base(p.cwd) != "alpha" {
		t.Fatalf("cwd = %q, want .../alpha", p.cwd)
	}
	if !p.onStart() {
		t.Errorf("cursor must reset onto the Start button on descend, got %d", p.cursor)
	}

	p.cursor = 1 // ".." (alpha has no subdirs, so rows are just the parent)
	p, chosen = p.activate()
	if chosen != "" {
		t.Errorf("ascending must not choose a folder, got %q", chosen)
	}
	if p.cwd != root {
		t.Fatalf("cwd after .. = %q, want %q", p.cwd, root)
	}

	p.cursor = 0 // the Start button (the top)
	if !p.onStart() {
		t.Fatal("cursor 0 must be the Start button")
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
	// And a Start button to commit anyway (cursor 0, the top).
	p.cursor = 0
	if !p.onStart() {
		t.Error("cursor 0 must be the Start button even on an unreadable directory")
	}
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

	p.cursor = 0 // the Start button (the top)
	if got, want := p.enterVerb(), "start the backup of "+filepath.Base(root); got != want {
		t.Errorf("on the Start button, verb = %q, want %q", got, want)
	}
	p.cursor = 1 // ".."
	if got := p.enterVerb(); !strings.HasPrefix(got, "go up to ") {
		t.Errorf("on the parent row, verb = %q", got)
	}
	p.cursor = 2 // alpha
	if got := p.enterVerb(); got != "open alpha" {
		t.Errorf("on a child row, verb = %q", got)
	}
}

// The Start button is drawn ABOVE the folder list (the top, default option) and
// stays highlighted-able no matter how far the list scrolls: with more folders
// than fit, the "…" overflow indicator shows AND the pinned button still
// renders above the first folder, and when the cursor is on it no folder row is
// marked.
func TestDirPickerStartButtonPinnedAboveScrollingList(t *testing.T) {
	root := t.TempDir()
	for i := range 40 { // far more than dirPickerHeight
		if err := os.Mkdir(filepath.Join(root, fmt.Sprintf("d%02d", i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := newDirPicker(root) // opens on the Start button (cursor 0)

	out := p.View(true)
	if !strings.Contains(out, "…") {
		t.Errorf("a list longer than the window must show the overflow indicator:\n%s", out)
	}
	const btn = "▸ backup the current directory"
	if !strings.Contains(out, btn) {
		t.Errorf("the Start button must render above the folder list:\n%s", out)
	}
	// The button leads the folder rows: its line comes before the first folder.
	lines := strings.Split(out, "\n")
	btnLine, firstFolderLine := -1, -1
	for i, line := range lines {
		if btnLine == -1 && strings.Contains(line, btn) {
			btnLine = i
		}
		if firstFolderLine == -1 && strings.Contains(line, "d00") {
			firstFolderLine = i
		}
	}
	if btnLine == -1 || firstFolderLine == -1 || btnLine >= firstFolderLine {
		t.Errorf("the Start button must be the top option, above the folders (btn=%d, folder=%d):\n%s",
			btnLine, firstFolderLine, out)
	}
	// The marker sits on the Start button line, not on any folder row.
	for _, line := range lines {
		if strings.Contains(line, "▍") && !strings.Contains(line, btn) {
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

// fakeHome points the picker's home lookup at a temp dir with the given
// well-known subdirectories, restoring the neutralized test default after.
func fakeHome(t *testing.T, wellKnown ...string) string {
	t.Helper()
	home := t.TempDir()
	for _, d := range wellKnown {
		if err := os.Mkdir(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	prev := dirPickerHome
	dirPickerHome = func() (string, error) { return home, nil }
	t.Cleanup(func() { dirPickerHome = prev })
	return home
}

// placeLabels collects the place-row labels in order.
func placeLabels(p dirPicker) []string {
	var out []string
	for _, r := range p.rows {
		if r.kind == rowPlace {
			out = append(out, r.label)
		}
	}
	return out
}

// The picker lists jump-to places for the home directory and its
// well-known folders — but only the ones that actually exist on disk: a
// row that navigates to a missing directory would just render an error.
func TestDirPicker_PlacesListExistingWellKnownDirs(t *testing.T) {
	fakeHome(t, "Documents", "Downloads")
	p := newDirPicker(tempTree(t))
	got := placeLabels(p)
	want := []string{"~", "~/Documents", "~/Downloads"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("places = %v, want %v", got, want)
	}
}

// Activating a place jumps the browse root there and parks the cursor on
// the Start button — one enter later the jumped-to folder is backed up,
// same contract as descending into a folder row.
func TestDirPicker_PlaceJumpsAndResetsCursor(t *testing.T) {
	home := fakeHome(t, "Documents")
	p := newDirPicker(tempTree(t))
	for i := 1; i <= len(p.rows); i++ {
		if p.rows[i-1].kind == rowPlace && p.rows[i-1].label == "~/Documents" {
			p.cursor = i
			break
		}
	}
	p2, committed := p.activate()
	if committed != "" {
		t.Fatalf("place activation committed %q, want navigation", committed)
	}
	if want := filepath.Join(home, "Documents"); p2.cwd != want {
		t.Fatalf("cwd = %q, want %q", p2.cwd, want)
	}
	if !p2.onStart() {
		t.Fatal("jump must park the cursor on the Start button")
	}
}

// The place for the directory being browsed is dropped — jumping to where
// you already stand is noise.
func TestDirPicker_PlaceForCurrentDirHidden(t *testing.T) {
	home := fakeHome(t, "Documents", "Downloads")
	p := newDirPicker(filepath.Join(home, "Documents"))
	for _, l := range placeLabels(p) {
		if l == "~/Documents" {
			t.Fatalf("place for the current directory must be hidden: %v", placeLabels(p))
		}
	}
}

// No resolvable home (the hermetic test default) means no place rows at
// all — the picker degrades to plain browsing.
func TestDirPicker_NoHomeNoPlaces(t *testing.T) {
	p := newDirPicker(tempTree(t))
	if got := placeLabels(p); len(got) != 0 {
		t.Fatalf("places with no home = %v, want none", got)
	}
}

// The action line must say where enter will jump.
func TestDirPicker_PlaceEnterVerb(t *testing.T) {
	fakeHome(t, "Downloads")
	p := newDirPicker(tempTree(t))
	for i := 1; i <= len(p.rows); i++ {
		if p.rows[i-1].kind == rowPlace && p.rows[i-1].label == "~/Downloads" {
			p.cursor = i
			break
		}
	}
	if got := p.enterVerb(); !strings.Contains(got, "~/Downloads") {
		t.Fatalf("enterVerb = %q, want it to name ~/Downloads", got)
	}
}
