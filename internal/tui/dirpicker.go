package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/markgustetic/sentra/internal/ui"
)

// dirPicker browses the filesystem one directory at a time so the operator can
// point a backup at a folder without typing its path.
//
// It is a pure model: every move re-reads the directory synchronously. The local
// filesystem is fast enough that a tea.Cmd round trip would buy nothing, and a
// synchronous read keeps the model drivable straight from a test without running
// commands. The same reasoning already governs loadSnapshotsBestEffort.
//
// enter means exactly one thing on the rows themselves — navigate: descend into
// a folder or climb via "..". Committing (starting the backup of the current
// directory) is a separate affordance, the Start button. Keeping navigation and
// commit on different keys is the fix for the old model, where enter on row 0
// started the backup while enter on every other row navigated, so the same key
// did two unrelated things.
//
// The Start button is the TOP, default option — backing up the current
// directory is the common case, so it leads rather than hiding past the folder
// list, and a fresh picker opens on it (one enter backs up where you are). It is
// not a row: it is the cursor position BEFORE the rows (cursor == 0), with the
// folder rows at cursor 1..len(rows). Modelling it as a sentinel rather than a
// list entry keeps it pinned above the scrolling folder window so it never
// scrolls out of reach, and keeps rows holding only real filesystem entries.
type dirPicker struct {
	cwd    string
	rows   []dirRow
	cursor int
	err    string

	// height is how many rows fit; the view scrolls a window around the cursor.
	height int
}

type rowKind int

const (
	rowParent rowKind = iota
	rowChild
	rowPlace // a jump-to bookmark (~, ~/Documents, …), not an entry of cwd
)

// dirPickerHome is the picker's home-directory lookup, a seam so tests can
// point the place rows at a hermetic temp home (TestMain neutralizes it —
// real machines differ in which well-known folders exist, and rows that
// vary by machine would make every picker test flaky).
var dirPickerHome = os.UserHomeDir

type dirRow struct {
	kind  rowKind
	label string
	path  string
}

// dirPickerHeight is the default window size; the view scrolls within it.
const dirPickerHeight = 10

// newDirPicker opens start. An unreadable directory is not fatal: the picker
// still renders its parent row and Start button so the operator can climb back
// out or commit anyway, with the error shown alongside.
func newDirPicker(start string) dirPicker {
	if strings.TrimSpace(start) == "" {
		if wd, err := os.Getwd(); err == nil {
			start = wd
		} else {
			start = string(filepath.Separator)
		}
	}
	if abs, err := filepath.Abs(start); err == nil {
		start = abs
	}
	p := dirPicker{cwd: start, height: dirPickerHeight}
	return p.reload()
}

// reload rebuilds rows for p.cwd and clamps the cursor. rows hold only real
// filesystem entries — the parent, then folders; the Start button lives before
// the first row and is not stored here (see onStart).
func (p dirPicker) reload() dirPicker {
	p.err = ""
	p.rows = nil
	if parent := filepath.Dir(p.cwd); parent != p.cwd {
		p.rows = append(p.rows, dirRow{kind: rowParent, label: "..", path: parent})
	}

	p.rows = append(p.rows, p.placeRows()...)

	entries, err := os.ReadDir(p.cwd)
	if err != nil {
		p.err = "cannot read " + p.cwd + ": " + errReason(err)
		p.clampCursor()
		return p
	}

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		// Directories only: a backup root is a directory by definition. Symlinks
		// are followed via Stat rather than trusted from the dirent, so a link to
		// a directory is still offered.
		if e.IsDir() {
			names = append(names, e.Name())
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(p.cwd, e.Name())); err == nil && info.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	for _, n := range names {
		p.rows = append(p.rows, dirRow{kind: rowChild, label: n, path: filepath.Join(p.cwd, n)})
	}
	p.clampCursor()
	return p
}

// placeRows builds the jump-to bookmarks: the home directory and its
// well-known folders, listed right after ".." so a backup of Documents or
// Downloads is one enter away from anywhere. Only directories that exist
// are offered (a bookmark to nowhere would just render an error), and the
// one matching the directory being browsed is dropped — jumping to where
// you already stand is noise.
func (p dirPicker) placeRows() []dirRow {
	home, err := dirPickerHome()
	if err != nil || strings.TrimSpace(home) == "" {
		return nil
	}
	rows := []dirRow{{kind: rowPlace, label: "~", path: home}}
	for _, name := range []string{"Documents", "Downloads", "Desktop", "Pictures"} {
		path := filepath.Join(home, name)
		if info, err := os.Stat(path); err == nil && info.IsDir() {
			rows = append(rows, dirRow{kind: rowPlace, label: "~/" + name, path: path})
		}
	}
	kept := rows[:0]
	for _, r := range rows {
		if r.path != p.cwd {
			kept = append(kept, r)
		}
	}
	return kept
}

// onStart reports whether the cursor rests on the Start button — cursor 0, the
// top position before the folder rows. A fresh picker (cursor zero value) opens
// here, so the default action is "back up the current directory".
func (p dirPicker) onStart() bool { return p.cursor <= 0 }

// clampCursor keeps the cursor within [0, len(rows)] — position 0 is the Start
// button and 1..len(rows) are the folder rows.
func (p *dirPicker) clampCursor() {
	if p.cursor > len(p.rows) {
		p.cursor = len(p.rows)
	}
	if p.cursor < 0 {
		p.cursor = 0
	}
}

// errReason strips the os.PathError wrapper so the row reads as prose.
func errReason(err error) string {
	if pe, ok := err.(*os.PathError); ok {
		return pe.Err.Error()
	}
	return err.Error()
}

func (p dirPicker) moveUp() dirPicker {
	if p.cursor > 0 {
		p.cursor--
	}
	return p
}

func (p dirPicker) moveDown() dirPicker {
	// len(rows) is the Start button slot, so the cursor may step one past the
	// last row.
	if p.cursor < len(p.rows) {
		p.cursor++
	}
	return p
}

// activate applies enter to the current cursor position. On the Start button it
// returns the current directory — the caller's signal to commit. On a folder or
// ".." row it navigates and returns "", which is what keeps enter meaning only
// "change the path" on the rows themselves. Navigating resets the cursor to the
// Start button, so entering a folder leaves you positioned to back it up.
func (p dirPicker) activate() (dirPicker, string) {
	if p.onStart() {
		return p, p.cwd
	}
	switch row := p.rows[p.cursor-1]; row.kind {
	case rowParent, rowChild, rowPlace:
		p.cwd = row.path
		p.cursor = 0
		return p.reload(), ""
	}
	return p, ""
}

// up climbs one directory, for backspace / left.
func (p dirPicker) up() dirPicker {
	parent := filepath.Dir(p.cwd)
	if parent == p.cwd {
		return p
	}
	p.cwd = parent
	p.cursor = 0
	return p.reload()
}

// window returns the slice of rows to draw and the index the slice starts at,
// scrolling so the cursor stays visible. The folder rows are at cursor
// 1..len(rows); the effective row cursor is cursor-1. When the cursor is on the
// Start button (cursor 0) the window pins to the head, keeping the first folders
// in view beneath the highlighted button.
func (p dirPicker) window() (rows []dirRow, start int) {
	h := p.height
	if h <= 0 || h > len(p.rows) {
		h = len(p.rows)
	}
	if h == 0 {
		return nil, 0
	}
	ec := p.cursor - 1
	if ec < 0 {
		ec = 0 // the button's cursor pins the window to the head
	}
	if ec > len(p.rows)-1 {
		ec = len(p.rows) - 1
	}
	start = ec - h/2
	if start < 0 {
		start = 0
	}
	if start+h > len(p.rows) {
		start = len(p.rows) - h
	}
	return p.rows[start : start+h], start
}

// enterVerb names what enter will do at the current cursor position. The action
// line renders this rather than a fixed string, because enter means three
// different things here and a footer that says "open" while the cursor rests on
// the Start button is simply lying.
func (p dirPicker) enterVerb() string {
	if p.onStart() {
		// The Start button commits — for the sole caller (backup) that means
		// starting the run against the current directory.
		return "start the backup of " + filepath.Base(p.cwd)
	}
	switch r := p.rows[p.cursor-1]; r.kind {
	case rowParent:
		return "go up to " + filepath.Base(r.path)
	case rowPlace:
		return "jump to " + r.label
	default:
		return "open " + r.label
	}
}

// View renders the picker. focused controls whether the highlighted row carries
// the ▍ marker: an unfocused picker must not look like it still owns the
// keyboard while the tag field does.
//
// The Start button is drawn first, above the folder window, so it leads as the
// default option and stays pinned in view no matter how far the list scrolls.
// The folder rows are at cursor 1..len(rows), so row start+i is highlighted when
// the cursor is one past it (cursor == start+i+1).
func (p dirPicker) View(focused bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", ui.Muted.Render(p.cwd))
	if p.err != "" {
		fmt.Fprintf(&b, "%s\n", ui.Danger.Render(p.err))
	}
	fmt.Fprintf(&b, "%s\n", ui.SelectRow(focused && p.onStart(), "▸ backup the current directory"))
	rows, start := p.window()
	for i, r := range rows {
		label := r.label
		if r.kind == rowChild {
			label += string(filepath.Separator)
		}
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(focused && start+i == p.cursor-1, label))
	}
	if len(p.rows) > len(rows) {
		fmt.Fprintf(&b, "%s\n", ui.Subtle.Render("  …"))
	}
	return b.String()
}
