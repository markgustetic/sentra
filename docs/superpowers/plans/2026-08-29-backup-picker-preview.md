# Backup Picker Preview Pane Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A preview pane to the right of the backup view's folder picker showing the contents (dirs + files with sizes) of whatever the cursor points at.

**Architecture:** `dirPicker` (a pure, synchronous model in `internal/tui/dirpicker.go`) gains a cached `preview` recomputed inside its existing transforms; `BackupView.View` joins the picker column and the pane with `lipgloss.JoinHorizontal`, hiding the pane below a width threshold. No new keys, no focus changes, no new shell contract.

**Tech Stack:** Go 1.27, bubbletea/lipgloss, table-driven `go test`. Spec: `docs/superpowers/specs/2026-08-29-backup-picker-preview-design.md`.

## Global Constraints

- Preview is **metadata only**: names and sizes, never file contents. `Info()` is lstat — symlinks shown by name, never followed.
- Sort: directories first (trailing `/`), then files; both case-insensitive alpha (the picker's own sort).
- Cap shown entries at the picker window height (`dirPickerHeight` = 10); tail row `… +N more`; empty dir → `(empty)`; unreadable → reason as one muted line, not fatal.
- Layout: picker column fixed at 32 cols, 2-col gap, pane gets the rest; pane hides when it would get < 20 cols. Configure stage only.
- Never wrap an already-styled string; style plain text and append styled fragments (a Width-only lipgloss style is safe — `app.go:1294` is the precedent).
- Selection/testability: assertions must be glyph/text-based (lipgloss Ascii profile emits no ANSI in tests).
- All tests hermetic: `t.TempDir()` trees; the picker's home seam is neutralized by `TestMain` — use `fakeHome` if places are needed.
- Run tests with `-race` for the changed package while iterating; full `go test -race ./...` before finishing (per project memory).
- Doc comments explain **why**, matching the file's existing density.

---

### Task 1: `readPreview` — the pane's data, read from disk

**Files:**
- Modify: `internal/tui/dirpicker.go` (append near the bottom)
- Test: `internal/tui/dirpicker_test.go` (append)

**Interfaces:**
- Produces: `type dirPreview struct { target string; entries []previewEntry; total int; err string }`, `type previewEntry struct { name string; size int64; isDir bool }`, `func readPreview(dir string, maxShown int) dirPreview`, `const previewMaxEntries = dirPickerHeight`. Task 2 calls `readPreview(target, previewMaxEntries)`; Task 3 renders `dirPreview`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/dirpicker_test.go`:

```go
// previewTree builds a mixed directory: two dirs and two files whose case
// exercises the case-insensitive sort, plus known file sizes.
func previewTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, d := range []string{"zeta", "apple"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "Banana.txt"), []byte("xy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cherry.txt"), []byte("xyz"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

// The pane's data: directories first, then files, each case-insensitive —
// the picker's own sort — with file sizes from lstat.
func TestReadPreview_DirsFirstThenFilesSorted(t *testing.T) {
	pv := readPreview(previewTree(t), previewMaxEntries)
	if pv.err != "" {
		t.Fatalf("unexpected err: %s", pv.err)
	}
	want := []previewEntry{
		{name: "apple", isDir: true},
		{name: "zeta", isDir: true},
		{name: "Banana.txt", size: 2},
		{name: "cherry.txt", size: 3},
	}
	if fmt.Sprint(pv.entries) != fmt.Sprint(want) {
		t.Fatalf("entries = %+v, want %+v", pv.entries, want)
	}
	if pv.total != 4 {
		t.Fatalf("total = %d, want 4", pv.total)
	}
}

// The cap bounds what is SHOWN, never what is COUNTED — the "+N more" tail
// needs the real total.
func TestReadPreview_CapsShownKeepsTotal(t *testing.T) {
	pv := readPreview(previewTree(t), 2)
	if len(pv.entries) != 2 {
		t.Fatalf("shown = %d, want 2", len(pv.entries))
	}
	if pv.total != 4 {
		t.Fatalf("total = %d, want 4", pv.total)
	}
}

// An unreadable target is a message, not a failure — the picker's own
// unreadable-cwd rule.
func TestReadPreview_UnreadableTarget(t *testing.T) {
	pv := readPreview(filepath.Join(t.TempDir(), "no-such-dir"), previewMaxEntries)
	if pv.err == "" {
		t.Error("an unreadable target must set err")
	}
	if len(pv.entries) != 0 {
		t.Errorf("no entries on error, got %+v", pv.entries)
	}
}

func TestReadPreview_EmptyDir(t *testing.T) {
	pv := readPreview(t.TempDir(), previewMaxEntries)
	if pv.err != "" || pv.total != 0 || len(pv.entries) != 0 {
		t.Fatalf("empty dir: %+v", pv)
	}
}

// Symlinks are listed by name and never followed: a link to a directory
// must NOT read as a directory, and its size is the link's own (lstat).
func TestReadPreview_SymlinkNotFollowed(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "real")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	pv := readPreview(root, previewMaxEntries)
	for _, e := range pv.entries {
		if e.name == "link" && e.isDir {
			t.Error("a symlink to a directory must not be listed as a directory")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/tui/ -run 'TestReadPreview' -v`
Expected: FAIL — `undefined: readPreview`, `undefined: previewMaxEntries`, `undefined: previewEntry`.

- [ ] **Step 3: Implement `readPreview`**

Append to `internal/tui/dirpicker.go`:

```go
// dirPreview is the pane's data for one target directory: the first
// window of entries plus how many exist in total, so the tail row can say
// what was elided. target doubles as the cache key (see refreshPreview).
type dirPreview struct {
	target  string
	entries []previewEntry
	total   int
	err     string
}

// previewEntry is one name in the pane. Metadata only — the preview must
// never read file contents, only what ReadDir and lstat already hold.
type previewEntry struct {
	name  string
	size  int64
	isDir bool
}

// previewMaxEntries caps the pane at the picker's own window height so
// the two columns stay visually matched.
const previewMaxEntries = dirPickerHeight

// readPreview reads what is inside dir for the pane: directories first,
// then files, each case-insensitive — the picker's own sort — capped at
// maxShown but counting everything. Info is lstat on the dirent, so sizes
// come without following symlinks (a link to a directory reads as a plain
// entry, deliberately: the preview reports the directory as it is, not as
// it resolves), and only the shown rows pay for it.
func readPreview(dir string, maxShown int) dirPreview {
	pv := dirPreview{target: dir}
	entries, err := os.ReadDir(dir)
	if err != nil {
		pv.err = "cannot read: " + errReason(err)
		return pv
	}
	pv.total = len(entries)
	sort.SliceStable(entries, func(i, j int) bool {
		di, dj := entries[i].IsDir(), entries[j].IsDir()
		if di != dj {
			return di
		}
		return strings.ToLower(entries[i].Name()) < strings.ToLower(entries[j].Name())
	})
	for _, e := range entries {
		if len(pv.entries) == maxShown {
			break
		}
		pe := previewEntry{name: e.Name(), isDir: e.IsDir()}
		if !e.IsDir() {
			if info, err := e.Info(); err == nil {
				pe.size = info.Size()
			}
		}
		pv.entries = append(pv.entries, pe)
	}
	return pv
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run 'TestReadPreview' -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dirpicker.go internal/tui/dirpicker_test.go
git commit -m "feat(tui): readPreview — pane data for the backup picker preview"
```

---

### Task 2: preview follows the cursor — target derivation, cache, wiring

**Files:**
- Modify: `internal/tui/dirpicker.go` (`dirPicker` struct, `reload`, `moveUp`, `moveDown`; struct doc comment)
- Test: `internal/tui/dirpicker_test.go` (append)

**Interfaces:**
- Consumes: `readPreview`, `previewMaxEntries`, `dirPreview` (Task 1).
- Produces: `dirPicker.preview dirPreview` field, `func (p dirPicker) previewTarget() string`, `func (p dirPicker) refreshPreview() dirPicker`. Task 3 renders `p.preview`; every existing transform (`newDirPicker`, `reload`, `moveUp`, `moveDown`, `up`, `activate`) leaves `p.preview` current.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/dirpicker_test.go`:

```go
// The preview answers "what is inside the thing enter would act on": the
// highlighted row's path, or the current directory on the Start button.
func TestDirPicker_PreviewFollowsCursor(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)

	// A fresh picker opens on the Start button — preview the cwd.
	if p.previewTarget() != root || p.preview.target != root {
		t.Fatalf("fresh picker must preview cwd: target=%q preview=%q", p.previewTarget(), p.preview.target)
	}
	// cursor 1 = ".." — preview the parent.
	p = p.moveDown()
	if want := filepath.Dir(root); p.preview.target != want {
		t.Fatalf("on .., preview = %q, want %q", p.preview.target, want)
	}
	// cursor 2 = alpha — preview the hovered child.
	p = p.moveDown()
	if want := filepath.Join(root, "alpha"); p.preview.target != want {
		t.Fatalf("on alpha, preview = %q, want %q", p.preview.target, want)
	}
	// Descend: back on the Start button, preview the new cwd.
	p, _ = p.activate()
	if want := filepath.Join(root, "alpha"); p.preview.target != want {
		t.Fatalf("after descend, preview = %q, want %q", p.preview.target, want)
	}
}

// Place rows preview their bookmark target like any other row.
func TestDirPicker_PreviewOnPlaceRow(t *testing.T) {
	home := fakeHome(t, "Documents")
	p := newDirPicker(tempTree(t))
	for i := 1; i <= len(p.rows); i++ {
		if p.rows[i-1].kind == rowPlace && p.rows[i-1].label == "~/Documents" {
			p.cursor = i
			break
		}
	}
	p = p.refreshPreview()
	if want := filepath.Join(home, "Documents"); p.preview.target != want {
		t.Fatalf("place preview = %q, want %q", p.preview.target, want)
	}
}

// The cache is keyed by target: a refresh against the SAME target must not
// re-read the disk (repaints are free), and moving away and back must —
// that replacement is what keeps the pane honest after navigation.
func TestDirPicker_PreviewCachedByTarget(t *testing.T) {
	root := tempTree(t)
	p := newDirPicker(root)
	before := p.preview.total

	// Mutate the directory behind the cache's back.
	if err := os.WriteFile(filepath.Join(root, "new.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := p.refreshPreview().preview.total; got != before {
		t.Errorf("same-target refresh must serve the cache, total %d → %d", before, got)
	}
	// Away (target "..") and back (target cwd) re-reads.
	p = p.moveDown().moveUp()
	if got := p.preview.total; got != before+1 {
		t.Errorf("away-and-back must re-read, total = %d, want %d", got, before+1)
	}
}

// An unreadable cwd still yields a preview (its error line): reload's
// error path must refresh too, not leave a stale pane.
func TestDirPicker_PreviewOnUnreadableCwd(t *testing.T) {
	p := newDirPicker(filepath.Join(t.TempDir(), "no-such-dir"))
	if p.preview.err == "" {
		t.Errorf("preview of an unreadable cwd must carry the error, got %+v", p.preview)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/tui/ -run 'TestDirPicker_Preview' -v`
Expected: FAIL — `undefined: p.previewTarget`, `p.preview`, `p.refreshPreview`.

- [ ] **Step 3: Implement target + cache + wiring**

In `internal/tui/dirpicker.go`:

1. Add the field to the struct (after `err string`):

```go
	// preview is the pane's data for previewTarget(), cached by path and
	// refreshed by every transform that can move the target (see
	// refreshPreview). It lives on the model for the same reason the rows
	// do: a pure, synchronously readable picker is drivable from tests.
	preview dirPreview
```

2. Append the methods:

```go
// previewTarget is the directory whose contents the pane shows: whatever
// enter would act on — the highlighted row's path, or the current
// directory on the Start button (exactly what the backup would include).
func (p dirPicker) previewTarget() string {
	if p.onStart() {
		return p.cwd
	}
	return p.rows[p.cursor-1].path
}

// refreshPreview rebuilds the pane data when the target changed. Cached
// by path so repaints and same-row refreshes cost nothing; the cache is
// deliberately not time-based — navigation is the only signal the picker
// reacts to anywhere else, and the pane follows the same rule.
func (p dirPicker) refreshPreview() dirPicker {
	if target := p.previewTarget(); p.preview.target != target {
		p.preview = readPreview(target, previewMaxEntries)
	}
	return p
}
```

3. Wire the transforms — every return path that can change cursor or cwd ends in `refreshPreview`:

- `reload`: both `return p` sites become `return p.refreshPreview()` (the unreadable-cwd early return AND the final return).
- `moveUp`: `return p` → `return p.refreshPreview()`
- `moveDown`: `return p` → `return p.refreshPreview()`

(`newDirPicker`, `up`, and `activate`'s navigation path all flow through `reload`; `activate`'s Start path changes nothing.)

4. Extend the `dirPicker` struct doc comment — after the paragraph about the Start button, add:

```go
// The preview pane (rendered by the backup view beside the picker) shows
// what is inside previewTarget() — metadata only, never file contents.
```

- [ ] **Step 4: Run the package tests**

Run: `go test -race ./internal/tui/ -run 'TestDirPicker' -v`
Expected: PASS — the new preview tests AND every pre-existing picker test (the transforms' contracts are unchanged).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dirpicker.go internal/tui/dirpicker_test.go
git commit -m "feat(tui): backup picker preview follows the cursor, cached by target"
```

---

### Task 3: `previewView` — rendering the pane

**Files:**
- Modify: `internal/tui/dirpicker.go` (append; add `github.com/charmbracelet/lipgloss` import)
- Test: `internal/tui/dirpicker_test.go` (append; add `github.com/charmbracelet/lipgloss` import)

**Interfaces:**
- Consumes: `p.preview` (Task 2), `shortBytes` (dashboard.go), `spread` (dashboard.go), `truncateToWidth` (snaptable.go), `ui.Muted`/`ui.Subtle`.
- Produces: `func (p dirPicker) previewView(width int) string`. Task 5 calls it with the pane width.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/dirpicker_test.go`:

```go
// The pane: a header naming the target, dirs with a trailing separator,
// files with right-aligned sizes, a "+N more" tail. Assertions are text —
// the Ascii profile emits no ANSI, so glyphs and layout are the contract.
func TestDirPicker_PreviewViewContents(t *testing.T) {
	root := previewTree(t)
	p := newDirPicker(root) // Start button → previews root
	out := p.previewView(24)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")

	if want := "in " + filepath.Base(root) + string(filepath.Separator); !strings.Contains(lines[0], want) {
		t.Errorf("header = %q, want it to contain %q", lines[0], want)
	}
	for _, dir := range []string{"apple/", "zeta/"} {
		if !strings.Contains(out, dir) {
			t.Errorf("missing directory entry %q:\n%s", dir, out)
		}
	}
	// A file's name and size share one right-aligned line.
	found := false
	for _, line := range lines {
		if strings.Contains(line, "Banana.txt") {
			found = true
			if !strings.HasSuffix(line, "2B") {
				t.Errorf("file line must end with its size: %q", line)
			}
			if w := lipgloss.Width(line); w != 24 {
				t.Errorf("file line must be spread to the pane width, got %d: %q", w, line)
			}
		}
	}
	if !found {
		t.Errorf("missing file entry Banana.txt:\n%s", out)
	}
}

// The tail names how many entries the cap elided.
func TestDirPicker_PreviewViewMoreTail(t *testing.T) {
	root := t.TempDir()
	for i := range previewMaxEntries + 3 {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%02d.txt", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out := newDirPicker(root).previewView(24)
	if !strings.Contains(out, "… +3 more") {
		t.Errorf("pane must count the elided entries:\n%s", out)
	}
}

func TestDirPicker_PreviewViewEmptyAndError(t *testing.T) {
	if out := newDirPicker(t.TempDir()).previewView(24); !strings.Contains(out, "(empty)") {
		t.Errorf("empty dir must render (empty):\n%s", out)
	}
	p := newDirPicker(filepath.Join(t.TempDir(), "no-such-dir"))
	if out := p.previewView(24); !strings.Contains(out, "cannot read") {
		t.Errorf("unreadable target must render its reason:\n%s", out)
	}
}

// Every pane line is bounded to the given width — the rule, not one case:
// long names, the header, and the size column must all clip, or the
// two-column join wraps and misaligns.
func TestDirPicker_PreviewViewBoundedWidth(t *testing.T) {
	root := t.TempDir()
	long := strings.Repeat("verylongname", 8)
	if err := os.WriteFile(filepath.Join(root, long+".txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, long), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{12, 20, 24} {
		out := newDirPicker(root).previewView(width)
		for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if w := lipgloss.Width(line); w > width {
				t.Errorf("width %d: line %d overflows (%d): %q", width, i, w, line)
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/tui/ -run 'TestDirPicker_PreviewView' -v`
Expected: FAIL — `undefined: p.previewView` (compile error).

- [ ] **Step 3: Implement `previewView`**

Add `"github.com/charmbracelet/lipgloss"` to `dirpicker.go`'s imports, then append:

```go
// previewView renders the pane beside the picker: what is inside the
// directory the cursor points at. The header names the target so the pane
// reads as "inside X" even while the cursor moves; directories lead with
// a trailing separator; files carry right-aligned sizes. Every line is
// bounded to width, and styled fragments are appended to plain text,
// never wrapped around it (the ANSI-reset trap).
func (p dirPicker) previewView(width int) string {
	var b strings.Builder
	head := "in " + filepath.Base(p.preview.target) + string(filepath.Separator)
	fmt.Fprintf(&b, "%s\n", ui.Muted.Render(truncateToWidth(head, width)))
	if p.preview.err != "" {
		fmt.Fprintf(&b, "%s\n", ui.Muted.Render(truncateToWidth(p.preview.err, width)))
		return b.String()
	}
	if p.preview.total == 0 {
		fmt.Fprintf(&b, "%s\n", ui.Subtle.Render("(empty)"))
		return b.String()
	}
	for _, e := range p.preview.entries {
		if e.isDir {
			fmt.Fprintf(&b, "%s\n", truncateToWidth(e.name+string(filepath.Separator), width))
			continue
		}
		size := shortBytes(e.size)
		name := truncateToWidth(e.name, max(width-lipgloss.Width(size)-1, 1))
		fmt.Fprintf(&b, "%s\n", spread(width, name, ui.Subtle.Render(size)))
	}
	if more := p.preview.total - len(p.preview.entries); more > 0 {
		fmt.Fprintf(&b, "%s\n", ui.Subtle.Render(truncateToWidth(fmt.Sprintf("… +%d more", more), width)))
	}
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run 'TestDirPicker_PreviewView' -v`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dirpicker.go internal/tui/dirpicker_test.go
git commit -m "feat(tui): previewView renders the backup picker pane"
```

---

### Task 4: width-bounded picker column — `truncateToWidthLeft` + clip helpers

The two-column join pads the picker to a fixed column width; any line longer than that would wrap inside `lipgloss.Style.Width` and break the row model. The picker gains an optional `width`; 0 keeps today's unbounded behavior so every existing test and call site is untouched.

**Files:**
- Modify: `internal/tui/snaptable.go` (add `truncateToWidthLeft` beside `truncateToWidth`)
- Modify: `internal/tui/dirpicker.go` (`width` field, clip helpers, `View` uses them)
- Test: `internal/tui/dirpicker_test.go` (append)

**Interfaces:**
- Produces: `dirPicker.width int` field (0 = unbounded), `func truncateToWidthLeft(s string, w int) string`. Task 5 sets `picker.width` from the layout.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/dirpicker_test.go`:

```go
// truncateToWidthLeft clips the HEAD and keeps the tail: for paths, the
// leaf answers "where am I?" while the root is noise.
func TestTruncateToWidthLeft(t *testing.T) {
	cases := []struct{ in string; w int; want string }{
		{"short", 10, "short"},
		{"/a/very/long/path/leaf", 8, "…th/leaf"},
		{"abc", 1, "…"},
		{"abc", 0, ""},
	}
	for _, c := range cases {
		if got := truncateToWidthLeft(c.in, c.w); got != c.want {
			t.Errorf("truncateToWidthLeft(%q, %d) = %q, want %q", c.in, c.w, got, c.want)
		}
		if got := truncateToWidthLeft(c.in, c.w); lipgloss.Width(got) > c.w {
			t.Errorf("result %q wider than %d", got, c.w)
		}
	}
}

// With a width set, EVERY picker line fits it — the rule: the path line
// (which keeps its leaf), the Start button, and long folder labels.
func TestDirPicker_ViewBoundedWhenWidthSet(t *testing.T) {
	root := tempTree(t)
	deep := filepath.Join(root, "alpha")
	long := strings.Repeat("deepfolder", 6)
	if err := os.Mkdir(filepath.Join(deep, long), 0o755); err != nil {
		t.Fatal(err)
	}
	p := newDirPicker(deep)
	p.width = 32
	out := p.View(true)
	for i, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if w := lipgloss.Width(line); w > 32 {
			t.Errorf("line %d overflows 32 (%d): %q", i, w, line)
		}
	}
	// The path line keeps its tail: the leaf must survive the clip.
	first := strings.Split(out, "\n")[0]
	if !strings.Contains(first, "alpha") {
		t.Errorf("clipped path must keep its leaf: %q", first)
	}
}

// Width 0 is today's behavior, verbatim — no clipping anywhere.
func TestDirPicker_ViewUnboundedAtZeroWidth(t *testing.T) {
	root := tempTree(t)
	long := strings.Repeat("deepfolder", 6)
	if err := os.Mkdir(filepath.Join(root, long), 0o755); err != nil {
		t.Fatal(err)
	}
	if out := newDirPicker(root).View(true); !strings.Contains(out, long) {
		t.Errorf("zero width must not clip labels:\n%s", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/tui/ -run 'TestTruncateToWidthLeft|TestDirPicker_View(Bounded|Unbounded)' -v`
Expected: FAIL — `undefined: truncateToWidthLeft`, `p.width undefined`.

- [ ] **Step 3: Implement**

In `internal/tui/snaptable.go`, directly after `truncateToWidth`:

```go
// truncateToWidthLeft clips s to at most w display cells by dropping the
// HEAD, marking the clip with a leading ellipsis. The picker's path line
// uses it: a deep path's leaf answers "where am I?" while its root is
// noise, the opposite trade from truncateToWidth's tail clip.
func truncateToWidthLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	const ellipsis = "…" // one cell
	if w == 1 {
		return ellipsis
	}
	budget := w - 1
	runes := []rune(s)
	width := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		rw := lipgloss.Width(string(runes[i]))
		if width+rw > budget {
			break
		}
		width += rw
		start = i
	}
	return ellipsis + string(runes[start:])
}
```

In `internal/tui/dirpicker.go`:

1. Add to the struct (after `height int`):

```go
	// width bounds every rendered line when > 0; 0 leaves lines unbounded
	// (today's behavior — set only by the backup view's two-column layout,
	// where an overlong line would wrap inside the fixed picker column and
	// break the row model).
	width int
```

2. Add the clip helpers:

```go
// clip helpers bound a line to the picker's column width. clipRow reserves
// SelectRow's 2-cell prefix; clipLeft keeps the TAIL for the path line.
func (p dirPicker) clip(s string) string {
	if p.width <= 0 {
		return s
	}
	return truncateToWidth(s, p.width)
}

func (p dirPicker) clipRow(s string) string {
	if p.width <= 0 {
		return s
	}
	return truncateToWidth(s, p.width-2)
}

func (p dirPicker) clipLeft(s string) string {
	if p.width <= 0 {
		return s
	}
	return truncateToWidthLeft(s, p.width)
}
```

3. In `View`, route every rendered string through them:

- `ui.Muted.Render(p.cwd)` → `ui.Muted.Render(p.clipLeft(p.cwd))`
- `ui.Danger.Render(p.err)` → `ui.Danger.Render(p.clip(p.err))`
- `ui.SelectRow(focused && p.onStart(), "▸ backup the current directory")` → `ui.SelectRow(focused && p.onStart(), p.clipRow("▸ backup the current directory"))`
- the row loop's `ui.SelectRow(..., label)` → `ui.SelectRow(..., p.clipRow(label))`

- [ ] **Step 4: Run the full package tests**

Run: `go test -race ./internal/tui/`
Expected: PASS — new tests green, and every pre-existing picker/backup test untouched (they all run at width 0).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/snaptable.go internal/tui/dirpicker.go internal/tui/dirpicker_test.go
git commit -m "feat(tui): width-bounded picker lines; truncateToWidthLeft keeps a path's leaf"
```

---

### Task 5: BackupView two-column layout

**Files:**
- Modify: `internal/tui/dirpicker.go` (layout constants + `previewPaneWidth`)
- Modify: `internal/tui/backup.go` (`WindowSizeMsg` handling ~line 162, configure-stage `View` ~line 421; add `github.com/charmbracelet/lipgloss` import)
- Test: `internal/tui/backup_test.go` (append)

**Interfaces:**
- Consumes: `previewView` (Task 3), `picker.width` (Task 4), `pickerContentWidth` (snaptable.go).
- Produces: `const pickerColWidth = 32`, `const previewGapWidth = 2`, `const previewMinWidth = 20`, `func previewPaneWidth(interior int) int` (returns 0 when the pane must hide).

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/backup_test.go` (the new tests use `os`, `filepath`, `strings`, `tea`, and `lipgloss` — add whichever of those the file's import block is missing):

```go
// pickerAt points the view's picker at a hermetic tree so preview
// assertions don't depend on the process cwd. It keeps the width the
// resize already derived — a fresh picker starts at 0 (unbounded), and
// losing the bound would let a long temp path wrap inside the column and
// mask the very defect the width field exists to prevent.
func pickerAt(v BackupView, dir string) BackupView {
	w := v.picker.width
	v.picker = newDirPicker(dir)
	v.picker.width = w
	return v
}

// At the 80-col minimum (the App forwards 59) the pane renders beside the
// picker, previewing the current directory, and NO line exceeds the
// panel's text region — the rule the two-column join must uphold.
func TestBackupView_PreviewPaneAtMinSize(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewBackupView(Deps{})
	const forwarded = 59 // contentW the App forwards at an 80-col terminal
	m, _ := v.Update(tea.WindowSizeMsg{Width: forwarded, Height: 16})
	v = pickerAt(m.(BackupView), root)

	out := v.View()
	if want := "in " + filepath.Base(root) + string(filepath.Separator); !strings.Contains(out, want) {
		t.Errorf("pane must preview the cwd (missing %q):\n%s", want, out)
	}
	if !strings.Contains(out, "hello.txt") {
		t.Errorf("pane must list the cwd's files:\n%s", out)
	}
	region := pickerContentWidth(forwarded)
	for i, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > region {
			t.Errorf("line %d exceeds content region (%d > %d): %q", i, w, region, line)
		}
	}
}

// The pane follows the picker cursor through real key routing, not just
// the model: two ↓ from the Start button rest on the first child dir.
func TestBackupView_PreviewFollowsCursorThroughKeys(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "docs")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	v := NewBackupView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 59, Height: 16})
	v = pickerAt(m.(BackupView), root)

	for range 2 { // Start button → ".." → docs
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(BackupView)
	}
	out := v.View()
	if !strings.Contains(out, "in docs"+string(filepath.Separator)) {
		t.Errorf("pane must preview the hovered folder:\n%s", out)
	}
	if !strings.Contains(out, "inner.txt") {
		t.Errorf("pane must list the hovered folder's files:\n%s", out)
	}
}

// Below the width threshold the pane hides entirely and the picker keeps
// the full interior — the degraded layout IS today's layout.
func TestBackupView_PreviewPaneHidesWhenNarrow(t *testing.T) {
	root := t.TempDir()
	v := NewBackupView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 50, Height: 16}) // interior 48 → pane would get 14 < 20
	v = pickerAt(m.(BackupView), root)

	if out := v.View(); strings.Contains(out, "in "+filepath.Base(root)+string(filepath.Separator)) {
		t.Errorf("pane must hide below the threshold:\n%s", out)
	}
}

// The threshold rule itself: pane width is interior minus the fixed
// picker column and gap, floored to hidden below previewMinWidth.
func TestPreviewPaneWidth(t *testing.T) {
	cases := []struct{ interior, want int }{
		{57, 23},  // 80-col terminal: 57 - 32 - 2
		{54, 20},  // exactly the floor
		{53, 0},   // one below → hidden
		{0, 0},    // no size yet (fresh view before WindowSizeMsg)
		{-2, 0},   // pickerContentWidth(0)
	}
	for _, c := range cases {
		if got := previewPaneWidth(c.interior); got != c.want {
			t.Errorf("previewPaneWidth(%d) = %d, want %d", c.interior, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/tui/ -run 'TestBackupView_Preview|TestPreviewPaneWidth' -v`
Expected: FAIL — `undefined: previewPaneWidth` (compile error).

- [ ] **Step 3: Implement the layout**

In `internal/tui/dirpicker.go`, next to `dirPickerHeight`:

```go
// Two-column layout for the backup view: the picker column is fixed and
// the preview pane takes the rest of the interior — but never squeezed.
// Below previewMinWidth usable columns the pane hides and the picker
// returns to the full interior (the single-column layout). 32 fits the
// Start button's label exactly; 20 fits a real file name plus a size.
const (
	pickerColWidth  = 32
	previewGapWidth = 2
	previewMinWidth = 20
)

// previewPaneWidth converts the view's interior text width into the
// pane's width, or 0 when the pane must hide.
func previewPaneWidth(interior int) int {
	w := interior - pickerColWidth - previewGapWidth
	if w < previewMinWidth {
		return 0
	}
	return w
}
```

In `internal/tui/backup.go`:

1. Add `"github.com/charmbracelet/lipgloss"` to the imports.

2. Extend the `WindowSizeMsg` case (after `v.bar.Width = ...`):

```go
		// The picker's column width depends on whether the pane fits:
		// beside it the picker is pinned to pickerColWidth so the join
		// stays aligned; alone it may use the whole interior (which also
		// stops a deep path from wrapping inside the panel).
		if interior := pickerContentWidth(msg.Width); previewPaneWidth(interior) > 0 {
			v.picker.width = pickerColWidth
		} else {
			v.picker.width = interior
		}
```

3. In `View`'s `default:` (configure) branch, replace

```go
		fmt.Fprintf(&b, "\n\n%s", v.picker.View(v.focus == focusPicker))
```

with

```go
		pickerCol := v.picker.View(v.focus == focusPicker)
		if paneW := previewPaneWidth(pickerContentWidth(v.width)); paneW > 0 {
			// A Width-only style pads the picker block to its fixed column
			// without adding color codes, so the styled rows inside survive
			// (same pattern as the App's rail at app.go View). Top-aligned:
			// the pane's header sits beside the picker's path line.
			left := lipgloss.NewStyle().Width(pickerColWidth).Render(pickerCol)
			pickerCol = lipgloss.JoinHorizontal(lipgloss.Top,
				left, strings.Repeat(" ", previewGapWidth), v.picker.previewView(paneW))
		}
		fmt.Fprintf(&b, "\n\n%s", pickerCol)
```

- [ ] **Step 4: Run the full package tests**

Run: `go test -race ./internal/tui/`
Expected: PASS. If a pre-existing backup test now sees the pane (it resizes to Width ≥ 54 and asserts on `View()`), fix the TEST only if its assertion was incidental (e.g. counting lines); every existing assertion greps for substrings, which the two-column join preserves.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/dirpicker.go internal/tui/backup.go internal/tui/backup_test.go
git commit -m "feat(tui): backup view renders the preview pane beside the picker"
```

---

### Task 6: full gate

**Files:** none new — verification and fallout only.

- [ ] **Step 1: Full test suite with race**

Run: `go test -race ./...`
Expected: PASS everywhere. Watch `internal/tui` app-level tests (`smoke_test.go`, `app_test.go`, `golden_test.go`): the backup view's configure stage renders differently at ≥ 54 interior cols. Goldens in `testdata/` cover Banner/Dashboard/Settings/Splash/Wizard — none snapshot the backup view, so none should change; if one fails, inspect before regenerating (the golden must snapshot the dangerous condition, per project memory).

- [ ] **Step 2: The rest of the local gate**

Run: `go build ./... && go vet ./... && gofmt -l cmd internal && go mod tidy -diff && git diff --check`
Expected: clean (no output from gofmt/tidy/diff).

Run: `golangci-lint run ./internal/tui/...`
Expected: 0 issues. (If cache-related `../../../` path weirdness appears, `golangci-lint cache clean` first — known issue.)

- [ ] **Step 3: Gate the commits, not the worktree**

If the tree holds unrelated work, verify the series builds in isolation:

```bash
git worktree add -q --detach /tmp/chk HEAD && (cd /tmp/chk && go build ./... ) 
git worktree remove --force /tmp/chk
```

- [ ] **Step 4: Reinstall and push**

```bash
just install
git push
```

(Standing preferences: push to main without asking; do not watch CI. `just install` because `~/go/bin/sentra` goes stale after CLI-visible changes.)

---

## Self-Review (done at planning time)

- **Spec coverage:** header (T3), dirs-first sort + sizes (T1), cap + `+N more` (T1/T3), empty/unreadable (T1/T3), symlink lstat (T1), cursor-following incl. places + Start→cwd (T2), cache-by-path (T2), fixed 32-col picker + 2-gap + ≥20 threshold (T5), configure-stage only (T5 — the join lives in the `default:` branch), no-wrap styling rule (T3/T5), Ascii-profile-safe tests (all), width bound at min size (T5). No gaps.
- **Type consistency:** `dirPreview`/`previewEntry`/`readPreview`/`previewMaxEntries` (T1) ← used by T2/T3; `previewTarget`/`refreshPreview`/`preview` (T2) ← T3/T5; `previewView(width int) string` (T3) ← T5; `width`/`clipRow` (T4) ← T5 sets `picker.width`; `pickerColWidth`/`previewGapWidth`/`previewMinWidth`/`previewPaneWidth` defined once in T5.
- **Placeholders:** none — every step carries real code and exact commands.
