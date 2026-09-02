package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// snapSort is the snapshot list ordering. Date (newest first) is the default —
// the order ListSnapshots already returns.
type snapSort int

const (
	sortDate snapSort = iota
	sortSize
	sortFiles
	sortTag
	sortName
)

func (s snapSort) label() string {
	switch s {
	case sortSize:
		return "size"
	case sortFiles:
		return "files"
	case sortTag:
		return "tag"
	case sortName:
		return "id"
	default:
		return "date"
	}
}

// detailLoader is the hook the snapshots view uses to fetch a
// manifest by ID. Production wires this to repo.LoadSnapshot;
// tests inject an in-memory closure that returns canned data so
// they don't need a live blobstore.
type detailLoader func(id string) (repo.Manifest, error)

// Snapshots is the snapshot list + detail drill-in. Two visual
// states: tableView (a sortable bubbles/table) and detailView (a
// rendered file-tree from the loaded manifest). Esc collapses
// detail back to table.
type Snapshots struct {
	deps   Deps
	tbl    table.Model
	snaps  []repo.SnapshotInfo
	loader detailLoader
	width  int
	height int

	// detailOpen flips on enter, off on esc. Two visual modes is
	// simpler than two sub-models; the manifest is small (a few KB
	// of file entries) so re-rendering on each frame is fine.
	// The manifest load can hit S3, so enter opens the page in a
	// loading state and the load runs in a tea.Cmd — never inline in
	// Update, where it would freeze rendering and input for the whole
	// TUI. detailID keys the in-flight load so a result that resolves
	// after esc (or after another row was opened) is dropped.
	detailOpen    bool
	detailLoading bool
	detailID      string
	detailMan     repo.Manifest
	detailErr     error

	// sortMode orders the list; filter is a live substring on id+tag. Both are
	// applied by rebuild() to derive the table rows from the full snaps slice.
	sortMode  snapSort
	filter    textinput.Model
	filtering bool
	notice    string // transient banner (e.g. "copied <id>")

	// copyFn writes text to the system clipboard. Overridable so tests don't
	// touch the real clipboard (and so CI without xclip stays green).
	copyFn func(string) error

	// pins is the repo's pin set, loaded with the snapshot list and
	// refreshed after every completed op. Pinned rows carry a marker
	// glyph — a glyph, not a color, per the NO_COLOR/testability rule.
	pins map[string]struct{}

	// shownIDs mirrors the visible table rows: shownIDs[i] is the
	// snapshot ID behind row i. The ID cell itself may carry the pin
	// marker, so code that needs the real ID (copy, detail open, pin
	// toggle) reads it from here, never by parsing the cell text.
	shownIDs []string
}

// pinDoneMsg reports a pin/unpin op back through the one-op guard.
type pinDoneMsg struct{ err error }

func (pinDoneMsg) opResult() {}

// loadPinsBestEffort mirrors loadSnapshotsBestEffort: failures render
// as "no pins" rather than blocking the view — DeleteSnapshot still
// enforces pins at the repo layer regardless of what the table shows.
func loadPinsBestEffort(deps Deps) map[string]struct{} {
	if deps.Repo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 10*time.Second)
	defer cancel()
	pins, err := deps.Repo.Pins(ctx)
	if err != nil {
		return nil
	}
	return pins
}

// NewSnapshots constructs the view with the production loader
// (repo.LoadSnapshot wrapped to satisfy the detailLoader signature).
// If deps.Repo is nil, the loader is a no-op stub that returns an
// empty manifest — keeps the view safe to construct in tests that
// only exercise navigation.
func NewSnapshots(deps Deps) Snapshots {
	loader := func(_ string) (repo.Manifest, error) {
		return repo.Manifest{}, fmt.Errorf("snapshots: no repo configured")
	}
	if deps.Repo != nil {
		loader = func(id string) (repo.Manifest, error) {
			// Derive from deps.Ctx (App-scoped) so a 'q' quit
			// cancels the loader mid-flight rather than leaking the
			// S3 call to its own per-call timeout.
			ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 10*time.Second)
			defer cancel()
			return deps.Repo.LoadSnapshot(ctx, id)
		}
	}
	return NewSnapshotsWithLoader(deps, loader)
}

// Title names the view in the sidebar, palette, and title bar.
func (Snapshots) Title() string { return "Snapshots" }

// ShortHelp lists the view-specific keys for the status bar.
// ConsumesEscape: esc closes the detail page or the filter field. With the plain
// list showing, a second esc leaves the view — which is what an operator expects.
func (s Snapshots) ConsumesEscape() bool { return s.detailOpen || s.filtering }

// launchRestoreMsg and launchDiffMsg are the snapshot-first launch
// hooks: Snapshots emits them with the highlighted snapshot, the shell
// seeds the hidden target view and activates it. They exist because
// restore/diff left the rail — the snapshot row is now their only door.
type launchRestoreMsg struct{ snapID string }

type launchDiffMsg struct{ snapID string }

func (s Snapshots) ShortHelp() []key.Binding {
	if s.filtering {
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "apply filter")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		}
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "row")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sort")),
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "copy id")),
		key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "pin/unpin")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "diff")),
	}
}

// NewSnapshotsWithLoader is the tests' construction path. It exposes
// the loader so test code can return canned manifest data without
// reaching into the repo package.
func NewSnapshotsWithLoader(deps Deps, loader detailLoader) Snapshots {
	fi := textinput.New()
	fi.Prompt = "filter> "
	fi.Placeholder = "id or tag"
	s := Snapshots{
		deps:   deps,
		tbl:    newSnapshotsTable(nil),
		loader: loader,
		filter: fi,
		copyFn: clipboard.WriteAll,
		pins:   loadPinsBestEffort(deps),
	}
	if deps.Repo != nil {
		snaps, _ := initialSnapshots(deps) // shared load; empty on error
		s = s.SetSnapshots(snaps)
	}
	return s
}

// snapshotPreload is the App's ONE shared snapshot-list load, handed to every
// view that needs the list at construction (dashboard, snapshots, diff, restore,
// prune) so they don't each hit the store — five ListSnapshots at launch became
// one. Both the slice and the error are carried so callers keep their own error
// handling (prune surfaces it; diff/restore/snapshots fall back to empty).
type snapshotPreload struct {
	snaps []repo.SnapshotInfo
	err   error
}

// initialSnapshots returns the App's shared preload if one was set (the common
// path — the shell always sets it), else a fresh bounded load. It mirrors
// ListSnapshots' (snaps, err) contract. Used only at view construction; the
// refresh-after-op paths reload fresh so they never serve a stale preload.
func initialSnapshots(deps Deps) ([]repo.SnapshotInfo, error) {
	if deps.preload != nil {
		return deps.preload.snaps, deps.preload.err
	}
	if deps.Repo == nil {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 20*time.Second)
	defer cancel()
	return deps.Repo.ListSnapshots(ctx)
}

// loadSnapshotsBestEffort wraps repo.ListSnapshots with a timeout
// and an error-swallow so a slow blobstore can't block construction.
// Failures yield a nil slice; the view then renders the empty-state.
//
// A nil Repo (Deps{} in tests, or a shell built before unlock) yields
// nil rather than panicking, so callers — the constructor and the
// op-completion reload in Update — need no guard of their own.
//
// The 10s timeout is per-call; the parent context comes from
// deps.Ctx (App-scoped) so a quick quit cancels the load.
func loadSnapshotsBestEffort(deps Deps) []repo.SnapshotInfo {
	if deps.Repo == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 10*time.Second)
	defer cancel()
	snaps, err := deps.Repo.ListSnapshots(ctx)
	if err != nil {
		return nil
	}
	return snaps
}

// ctxOrBackground returns ctx, or context.Background() when ctx is
// nil. Sub-views derive per-call timeouts via this helper so a
// Deps{} (zero value used in tests) doesn't crash on a nil parent.
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// newSnapshotsTable builds the bubbles/table with our column layout.
// Pulled out so SetSnapshots and constructors share the same
// headers and column widths.
func newSnapshotsTable(rows []table.Row) table.Model {
	cols := []table.Column{
		{Title: "ID", Width: 18},
		{Title: "Created", Width: 22},
		{Title: "Tag", Width: 12},
		{Title: "Files", Width: 8},
		{Title: "Bytes", Width: 10},
	}
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	st := table.DefaultStyles()
	st.Header = st.Header.Foreground(lipgloss.Color("#7C3AED")).Bold(true)
	st.Selected = st.Selected.Foreground(lipgloss.Color("#FFFFFF")).Background(lipgloss.Color("#7C3AED"))
	t.SetStyles(st)
	return t
}

// SetSnapshots replaces the model's full snapshot list and rebuilds the visible
// table (applying the current sort and filter). Returns the updated model so
// callers can chain.
func (s Snapshots) SetSnapshots(snaps []repo.SnapshotInfo) Snapshots {
	s.snaps = snaps
	return s.rebuild()
}

// rebuild derives the table rows from the full snaps slice by applying the
// active filter then the active sort. Kept separate from SetSnapshots so a sort
// or filter change re-renders without re-fetching.
func (s Snapshots) rebuild() Snapshots {
	shown := filterSnaps(s.snaps, strings.TrimSpace(s.filter.Value()))
	sortSnaps(shown, s.sortMode)

	rows := make([]table.Row, 0, len(shown))
	s.shownIDs = make([]string, 0, len(shown))
	for _, sn := range shown {
		tag := sn.Tag
		if tag == "" {
			tag = "-"
		}
		// The marker leads the cell — the ID column truncates on the
		// right, so a trailing marker would be the first thing cut.
		id := sn.ID
		if _, pinned := s.pins[sn.ID]; pinned {
			id = "* " + id
		}
		s.shownIDs = append(s.shownIDs, sn.ID)
		rows = append(rows, table.Row{
			id,
			sn.CreatedAt.UTC().Format(time.RFC3339),
			tag,
			fmt.Sprintf("%d", sn.Stats.Files),
			ui.FormatBytes(sn.Stats.Bytes),
		})
	}
	s.tbl.SetRows(rows)
	return s
}

// filterSnaps keeps snapshots whose id or tag contains q (case-insensitive). An
// empty query returns the slice unfiltered. It copies rather than mutating the
// caller's slice so the full list is preserved for a later filter change.
func filterSnaps(snaps []repo.SnapshotInfo, q string) []repo.SnapshotInfo {
	if q == "" {
		return append([]repo.SnapshotInfo(nil), snaps...)
	}
	q = strings.ToLower(q)
	out := make([]repo.SnapshotInfo, 0, len(snaps))
	for _, sn := range snaps {
		if strings.Contains(strings.ToLower(sn.ID), q) || strings.Contains(strings.ToLower(sn.Tag), q) {
			out = append(out, sn)
		}
	}
	return out
}

// sortSnaps orders in place by the given mode. Date/size/files sort descending
// (newest, biggest, most first — the interesting end leads); tag/id ascending.
// Every mode breaks ties by CreatedAt descending so the order is deterministic.
func sortSnaps(snaps []repo.SnapshotInfo, mode snapSort) {
	newerFirst := func(i, j int) bool { return snaps[i].CreatedAt.After(snaps[j].CreatedAt) }
	sort.SliceStable(snaps, func(i, j int) bool {
		switch mode {
		case sortSize:
			if snaps[i].Stats.Bytes != snaps[j].Stats.Bytes {
				return snaps[i].Stats.Bytes > snaps[j].Stats.Bytes
			}
		case sortFiles:
			if snaps[i].Stats.Files != snaps[j].Stats.Files {
				return snaps[i].Stats.Files > snaps[j].Stats.Files
			}
		case sortTag:
			if snaps[i].Tag != snaps[j].Tag {
				return snaps[i].Tag < snaps[j].Tag
			}
		case sortName:
			if snaps[i].ID != snaps[j].ID {
				return snaps[i].ID < snaps[j].ID
			}
		}
		return newerFirst(i, j)
	})
}

// cursor returns the table's current cursor index. Exposed for
// tests so they can assert on navigation without poking the
// embedded table directly.
// ConsumesArrows: only when the table has rows, the detail page is closed, and
// the filter field is not capturing (its single line ignores arrows anyway). In
// the other states the arrows belong to the nav rail.
func (s Snapshots) ConsumesArrows() bool {
	return !s.detailOpen && !s.filtering && len(s.snaps) > 0
}

// CapturesText: while the filter field is focused it owns printable keys, so the
// shell must not treat them as globals ('q' quit, digit view-jumps).
func (s Snapshots) CapturesText() bool { return s.filtering }

// handleFilterKey routes keys while the filter field is focused: esc clears and
// closes it, enter keeps the filter and closes it, everything else edits the
// query and re-renders the list live.
func (s Snapshots) handleFilterKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		s.filtering = false
		s.filter.Blur()
		s.filter.SetValue("")
		return s.rebuild(), nil
	case tea.KeyEnter:
		s.filtering = false
		s.filter.Blur()
		return s, nil
	}
	var cmd tea.Cmd
	s.filter, cmd = s.filter.Update(msg)
	return s.rebuild(), cmd
}

// copySelectedID copies the highlighted snapshot's ID to the clipboard, setting
// a transient notice with the outcome. Uses OSC-52-free clipboard tools, so it
// works locally; over SSH the copy may not reach the client (a known limit).
func (s Snapshots) copySelectedID() Snapshots {
	id, ok := s.selectedID()
	if !ok {
		return s
	}
	if err := s.copyFn(id); err != nil {
		s.notice = "copy failed: " + err.Error()
		return s
	}
	s.notice = "copied " + id
	return s
}

// selectedID returns the snapshot ID behind the highlighted row. The
// table cell may carry a pin marker, so this reads the parallel
// shownIDs slice instead of the rendered text.
func (s Snapshots) selectedID() (string, bool) {
	i := s.tbl.Cursor()
	if i < 0 || i >= len(s.shownIDs) {
		return "", false
	}
	return s.shownIDs[i], true
}

func (s Snapshots) cursor() int { return s.tbl.Cursor() }

// togglePinSelected pins (or unpins) the highlighted snapshot. Pinning
// mutates the repo — it takes the advisory lock and rewrites meta/pins
// — so it goes through the App's one-op guard like every other
// mutation, not a bare tea.Cmd.
func (s Snapshots) togglePinSelected() (tea.Model, tea.Cmd) {
	id, ok := s.selectedID()
	if !ok || s.deps.Repo == nil {
		return s, nil
	}
	r := s.deps.Repo
	_, pinned := s.pins[id]
	name := "pin"
	if pinned {
		name = "unpin"
	}
	start := startOpMsg{
		name: name,
		run: func(ctx context.Context) tea.Msg {
			if pinned {
				return pinDoneMsg{err: r.Unpin(ctx, id)}
			}
			return pinDoneMsg{err: r.Pin(ctx, id)}
		},
	}
	return s, func() tea.Msg { return start }
}

// snapshotsReloadedMsg carries the post-op refresh (snapshot list +
// pin set) back to the view, keeping the blobstore reads off the UI
// goroutine.
type snapshotsReloadedMsg struct {
	snaps []repo.SnapshotInfo
	pins  map[string]struct{}
}

// snapDetailLoadedMsg carries a finished manifest load back to the view.
// id echoes the request so Update can drop results the operator has
// since navigated away from.
type snapDetailLoadedMsg struct {
	id  string
	man repo.Manifest
	err error
}

// Init is a no-op.
func (Snapshots) Init() tea.Cmd { return nil }

// Update handles navigation:
//   - Up/Down: forward to the embedded table for cursor movement.
//   - Enter: open detail for the selected row's snapshot ID.
//   - Esc: close detail (when open).
//
// The window-size message is forwarded so the table can resize.
func (s Snapshots) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width = msg.Width
		s.height = msg.Height
		// Leave room for the parent's top/bottom bars.
		s.tbl.SetHeight(maxInt(5, msg.Height-8))
		return s, nil
	case snapshotsReloadedMsg:
		s.pins = msg.pins
		return s.SetSnapshots(msg.snaps), nil

	case snapDetailLoadedMsg:
		// Apply only the load the operator is still waiting on: esc
		// clears detailID, and opening another row changes it.
		if !s.detailOpen || msg.id != s.detailID {
			return s, nil
		}
		s.detailLoading = false
		s.detailMan = msg.man
		s.detailErr = msg.err
		return s, nil

	case cursor.BlinkMsg:
		if s.filter.Focused() {
			var cmd tea.Cmd
			s.filter, cmd = s.filter.Update(msg)
			return s, cmd
		}
		return s, nil

	case tea.KeyMsg:
		if s.detailOpen {
			if msg.Type == tea.KeyEsc {
				s.detailOpen = false
				s.detailLoading = false
				s.detailID = ""
				s.detailErr = nil
			}
			return s, nil
		}
		if s.filtering {
			return s.handleFilterKey(msg)
		}
		switch msg.String() {
		case "/":
			s.notice = ""
			s.filtering = true
			// Focus()'s own return is the real, tag-matched blink cmd; the
			// dead-end textinput.Blink sentinel is never used. Sequenced
			// rather than inlined into the return: Focus() mutates
			// s.filter, and the order in which a return copies s versus
			// evaluates the call is unspecified.
			cmd := s.filter.Focus()
			return s, cmd
		case "s":
			s.notice = ""
			s.sortMode = (s.sortMode + 1) % 5
			return s.rebuild(), nil
		case "y":
			return s.copySelectedID(), nil
		case "p":
			return s.togglePinSelected()
		case "r":
			// Restore left the rail: restoring starts from WHICH snapshot,
			// so the highlighted row is the entry point. The shell seeds
			// and activates the hidden restore view.
			if id, ok := s.selectedID(); ok {
				return s, func() tea.Msg { return launchRestoreMsg{snapID: id} }
			}
			return s, nil
		case "d":
			// Same for diff: the highlighted row becomes side A.
			if id, ok := s.selectedID(); ok {
				return s, func() tea.Msg { return launchDiffMsg{snapID: id} }
			}
			return s, nil
		}
		if msg.Type == tea.KeyEnter {
			id, ok := s.selectedID()
			if !ok {
				return s, nil
			}
			s.detailOpen = true
			s.detailLoading = true
			s.detailID = id
			s.detailMan = repo.Manifest{}
			s.detailErr = nil
			loader := s.loader
			return s, func() tea.Msg {
				man, err := loader(id)
				return snapDetailLoadedMsg{id: id, man: man, err: err}
			}
		}
	}
	// A completed operation (backup, prune, sync, pin, …) is broadcast to
	// every view as an opResultMsg. The list is hydrated once at launch, so
	// without this reload a snapshot taken this session never appears until
	// restart — and a pin toggled this session wouldn't repaint its marker.
	// Keying off the marker interface refreshes for any op, present or future.
	// The reload itself is two blobstore reads (snapshot list + pin set), so
	// it runs in a tea.Cmd — inline in Update it would freeze the whole TUI
	// after every completed op, the same rule the detail loader obeys.
	if _, ok := msg.(opResultMsg); ok {
		deps := s.deps
		return s, func() tea.Msg {
			return snapshotsReloadedMsg{
				snaps: loadSnapshotsBestEffort(deps),
				pins:  loadPinsBestEffort(deps),
			}
		}
	}
	// Forward other messages (notably arrow keys) to the table.
	var cmd tea.Cmd
	s.tbl, cmd = s.tbl.Update(msg)
	return s, cmd
}

// View renders either the table or the detail page depending on
// detailOpen. An empty repo gets a placeholder rather than an
// awkwardly-empty header row.
func (s Snapshots) View() string {
	if s.detailOpen {
		return s.viewDetail()
	}
	if len(s.snaps) == 0 {
		return ui.Panel.Render(
			ui.Subtle.Render("snapshots")+"\n"+ui.Muted.Render("no snapshots in this repo"),
		) + "\n"
	}

	// Status line: which sort is active, plus the live filter field or a notice.
	status := ui.Muted.Render("sort: " + s.sortMode.label())
	switch {
	case s.filtering:
		// filtering decides that the field is on the status line at all;
		// the field's own focus decides whether it is framed.
		status = boxedField(s.filter)
	case s.notice != "":
		status = ui.Success.Render(s.notice)
	case strings.TrimSpace(s.filter.Value()) != "":
		status = ui.Muted.Render("sort: "+s.sortMode.label()) + "  " +
			ui.Subtle.Render("filter: "+s.filter.Value())
	}
	footer := ui.ActionLine("view this snapshot", "↑↓ move · s sort · / filter · y copy id · p pin · esc back")
	return s.tbl.View() + "\n" + status + "\n" + footer + "\n"
}

// viewDetail renders the manifest file tree as a vertical list with
// the snapshot summary at the top. We render every entry rather
// than virtualizing the list — manifests with thousands of entries
// will scroll within the terminal pager; making this a bubbles/list
// is a future iteration.
func (s Snapshots) viewDetail() string {
	if s.detailLoading {
		body := ui.Subtle.Render("loading snapshot ") + s.detailID + ui.Subtle.Render(" …")
		return ui.Panel.Render(body) + "\n" + ui.Subtle.Render("esc back") + "\n"
	}
	if s.detailErr != nil {
		body := ui.Danger.Render("error loading snapshot: ") + s.detailErr.Error()
		return ui.Panel.Render(body) + "\n" + ui.Subtle.Render("esc back") + "\n"
	}
	header := fmt.Sprintf("%s  %s  %d files  %s",
		ui.Primary.Render(s.detailMan.ID),
		ui.Subtle.Render(s.detailMan.CreatedAt.UTC().Format(time.RFC3339)),
		s.detailMan.Stats.Files,
		ui.Subtle.Render(ui.FormatBytes(s.detailMan.Stats.Bytes)),
	)
	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n\n")
	// An indented directory summary (dirs + subtree counts/sizes) rather than a
	// raw file dump — readable even for a snapshot with thousands of files.
	textW := maxInt(s.width-4, 24) // panel border(2) + padding(2)
	for _, line := range renderDirTree(buildDirTree(s.detailMan.Tree), textW) {
		fmt.Fprintf(&sb, "%s\n", ui.Subtle.Render(line))
	}
	// Symlinks don't fold into the dir tree (only chunk-backed files
	// do), so list them explicitly — a v2-manifest snapshot of a home
	// dir can carry plenty, and invisible entries read as data loss.
	links := 0
	for _, fe := range s.detailMan.Tree {
		if !fe.IsSymlink() {
			continue
		}
		if links == 0 {
			sb.WriteString("\n")
		}
		links++
		if links > 20 {
			continue
		}
		fmt.Fprintf(&sb, "%s\n", ui.Subtle.Render(fmt.Sprintf("  %s -> %s", fe.Path, fe.LinkTarget)))
	}
	if links > 20 {
		fmt.Fprintf(&sb, "%s\n", ui.Muted.Render(fmt.Sprintf("  … %d more symlinks", links-20)))
	}
	return ui.Panel.Render(sb.String()) + "\n" + ui.Subtle.Render("esc back") + "\n"
}

// maxInt is the small util we'd otherwise import. Avoiding the
// math.Max float dance keeps imports lean.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
