package tui

import (
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
// Row 0 is always "use this folder" and row 1 the parent, so enter carries
// exactly one meaning — activate the highlighted row — and never doubles as
// "choose this one" depending on what the cursor happens to be over.
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
	rowUseCurrent rowKind = iota
	rowParent
	rowChild
)

type dirRow struct {
	kind  rowKind
	label string
	path  string
}

// dirPickerHeight is the default window size; the view scrolls within it.
const dirPickerHeight = 10

// newDirPicker opens start. An unreadable directory is not fatal: the picker
// still renders its "use this folder" and parent rows so the operator can climb
// back out, with the error shown alongside.
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

// reload rebuilds rows for p.cwd and clamps the cursor.
func (p dirPicker) reload() dirPicker {
	p.err = ""
	p.rows = []dirRow{{kind: rowUseCurrent, label: "use this folder", path: p.cwd}}
	if parent := filepath.Dir(p.cwd); parent != p.cwd {
		p.rows = append(p.rows, dirRow{kind: rowParent, label: "..", path: parent})
	}

	entries, err := os.ReadDir(p.cwd)
	if err != nil {
		p.err = "cannot read " + p.cwd + ": " + errReason(err)
		p.cursor = 0
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
	if p.cursor >= len(p.rows) {
		p.cursor = len(p.rows) - 1
	}
	return p
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
	if p.cursor < len(p.rows)-1 {
		p.cursor++
	}
	return p
}

// activate applies enter to the highlighted row. It returns the chosen folder
// only for the "use this folder" row; descending and ascending choose nothing,
// which is what keeps enter unambiguous.
func (p dirPicker) activate() (dirPicker, string) {
	if len(p.rows) == 0 {
		return p, ""
	}
	row := p.rows[p.cursor]
	switch row.kind {
	case rowUseCurrent:
		return p, row.path
	case rowParent, rowChild:
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

// window returns the slice of rows to draw and the cursor's index within it,
// scrolling so the cursor stays visible.
func (p dirPicker) window() ([]dirRow, int) {
	h := p.height
	if h <= 0 || h > len(p.rows) {
		h = len(p.rows)
	}
	start := p.cursor - h/2
	if start < 0 {
		start = 0
	}
	if start+h > len(p.rows) {
		start = len(p.rows) - h
	}
	return p.rows[start : start+h], p.cursor - start
}

// enterVerb names what enter will do to the HIGHLIGHTED row. The action line
// renders this rather than a fixed string, because enter means three different
// things here and a footer that says "open" while the cursor rests on "use this
// folder" is simply lying.
func (p dirPicker) enterVerb() string {
	if len(p.rows) == 0 {
		return ""
	}
	switch r := p.rows[p.cursor]; r.kind {
	case rowUseCurrent:
		return "back up this folder"
	case rowParent:
		return "go up to " + filepath.Base(r.path)
	default:
		return "open " + r.label
	}
}

// View renders the picker. focused controls whether the highlighted row carries
// the ▍ marker: an unfocused picker must not look like it still owns the
// keyboard while the tag field does.
func (p dirPicker) View(focused bool) string {
	var b strings.Builder
	b.WriteString(ui.Muted.Render(p.cwd) + "\n")
	if p.err != "" {
		b.WriteString(ui.Danger.Render(p.err) + "\n")
	}
	rows, cur := p.window()
	for i, r := range rows {
		label := r.label
		if r.kind == rowChild {
			label += string(filepath.Separator)
		}
		b.WriteString(ui.SelectRow(focused && i == cur, label) + "\n")
	}
	if len(p.rows) > len(rows) {
		b.WriteString(ui.Subtle.Render("  …") + "\n")
	}
	return b.String()
}
