package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

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
	detailOpen bool
	detailMan  repo.Manifest
	detailErr  error
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
// ConsumesEscape: esc closes the detail page. With the list showing, a second
// esc leaves the view — which is what an operator expects.
func (s Snapshots) ConsumesEscape() bool { return s.detailOpen }

func (Snapshots) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "row")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
	}
}

// NewSnapshotsWithLoader is the tests' construction path. It exposes
// the loader so test code can return canned manifest data without
// reaching into the repo package.
func NewSnapshotsWithLoader(deps Deps, loader detailLoader) Snapshots {
	t := newSnapshotsTable(nil)
	s := Snapshots{
		deps:   deps,
		tbl:    t,
		loader: loader,
	}
	if deps.Repo != nil {
		s = s.SetSnapshots(loadSnapshotsBestEffort(deps))
	}
	return s
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

// SetSnapshots replaces the model's snapshot list and rebuilds the
// table rows. Returns the updated model so callers can chain.
func (s Snapshots) SetSnapshots(snaps []repo.SnapshotInfo) Snapshots {
	s.snaps = snaps
	rows := make([]table.Row, 0, len(snaps))
	for _, sn := range snaps {
		tag := sn.Tag
		if tag == "" {
			tag = "-"
		}
		rows = append(rows, table.Row{
			sn.ID,
			sn.CreatedAt.UTC().Format(time.RFC3339),
			tag,
			fmt.Sprintf("%d", sn.Stats.Files),
			ui.FormatBytes(sn.Stats.Bytes),
		})
	}
	s.tbl.SetRows(rows)
	return s
}

// cursor returns the table's current cursor index. Exposed for
// tests so they can assert on navigation without poking the
// embedded table directly.
// ConsumesArrows: only when the table has rows and the detail page is closed.
// The detail page handles nothing but esc, and an empty table has no cursor to
// move — in both states the arrows belong to the nav rail.
func (s Snapshots) ConsumesArrows() bool { return !s.detailOpen && len(s.snaps) > 0 }

func (s Snapshots) cursor() int { return s.tbl.Cursor() }

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
	case tea.KeyMsg:
		if s.detailOpen {
			if msg.Type == tea.KeyEsc {
				s.detailOpen = false
				s.detailErr = nil
			}
			return s, nil
		}
		if msg.Type == tea.KeyEnter {
			row := s.tbl.SelectedRow()
			if len(row) == 0 {
				return s, nil
			}
			id := row[0]
			m, err := s.loader(id)
			s.detailOpen = true
			s.detailMan = m
			s.detailErr = err
			return s, nil
		}
	}
	// A completed operation (backup, prune, sync, …) is broadcast to every
	// view as an opResultMsg. The list is hydrated once at launch, so without
	// this reload a snapshot taken this session never appears until restart.
	// Keying off the marker interface refreshes for any op, present or future.
	if _, ok := msg.(opResultMsg); ok {
		return s.SetSnapshots(loadSnapshotsBestEffort(s.deps)), nil
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
	return s.tbl.View() + "\n" + ui.ActionLine("view this snapshot", "↑↓ move · esc back") + "\n"
}

// viewDetail renders the manifest file tree as a vertical list with
// the snapshot summary at the top. We render every entry rather
// than virtualizing the list — manifests with thousands of entries
// will scroll within the terminal pager; making this a bubbles/list
// is a future iteration.
func (s Snapshots) viewDetail() string {
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
	for _, fe := range s.detailMan.Tree {
		fmt.Fprintf(&sb, "  %s  %s\n",
			ui.Subtle.Render(ui.FormatBytes(fe.Size)),
			fe.Path,
		)
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
