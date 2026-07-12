package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/ui"
)

// FilesView draws the latest snapshot's directory structure as a box-and-arrows
// graph — a filesystem "topology". It loads the manifest asynchronously (a
// LoadSnapshot can hit S3), reloads on ctrl+r or after any operation completes,
// and renders the tidy graph from filegraph.go inside the content panel.
type FilesView struct {
	deps    Deps
	width   int
	height  int
	loading bool
	err     error

	root  *dirNode // nil until loaded (or when the repo has no snapshots)
	id    string
	when  time.Time
	files int
	bytes int64
}

// filesLoadedMsg carries the result of loading the latest snapshot's tree.
type filesLoadedMsg struct {
	root  *dirNode
	id    string
	when  time.Time
	files int
	bytes int64
	err   error
}

func NewFilesView(deps Deps) FilesView {
	return FilesView{deps: deps, loading: true}
}

// Title names the view in the sidebar, palette, and title bar.
func (FilesView) Title() string { return "Files" }

// Init kicks off the first load. It runs once at startup (the App batches every
// view's Init); the load is async so it never blocks the first frame.
func (v FilesView) Init() tea.Cmd { return filesLoadCmd(v.deps) }

// ShortHelp advertises the reload key in the status bar (the reference's
// "Ctrl-R: Reload").
func (FilesView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("ctrl+r"), key.WithHelp("ctrl+r", "reload")),
	}
}

func (v FilesView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		return v, nil

	case filesLoadedMsg:
		v.loading = false
		v.root, v.id, v.when = msg.root, msg.id, msg.when
		v.files, v.bytes, v.err = msg.files, msg.bytes, msg.err
		return v, nil

	case opResultMsg:
		// A backup/restore/prune changed the repo — refresh so the tree tracks
		// the newest snapshot.
		v.loading = true
		return v, filesLoadCmd(v.deps)

	case tea.KeyMsg:
		if msg.String() == "ctrl+r" {
			v.loading = true
			return v, filesLoadCmd(v.deps)
		}
	}
	return v, nil
}

// filesLoadCmd loads the newest snapshot's manifest and reconstructs its
// directory tree off the Bubbletea loop.
func filesLoadCmd(deps Deps) tea.Cmd {
	return func() tea.Msg {
		if deps.Repo == nil {
			return filesLoadedMsg{err: fmt.Errorf("no repository configured")}
		}
		ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 15*time.Second)
		defer cancel()

		snaps, err := deps.Repo.ListSnapshots(ctx)
		if err != nil {
			return filesLoadedMsg{err: err}
		}
		if len(snaps) == 0 {
			return filesLoadedMsg{} // no snapshots yet — a nil root renders the empty state
		}
		latest := snaps[0] // ListSnapshots is newest-first
		man, err := deps.Repo.LoadSnapshot(ctx, latest.ID)
		if err != nil {
			return filesLoadedMsg{err: err}
		}
		root := buildDirTree(man.Tree)
		root.name = rootLabel(man.Root)
		return filesLoadedMsg{
			root: root, id: latest.ID, when: man.CreatedAt,
			files: man.Stats.Files, bytes: man.Stats.Bytes,
		}
	}
}

// rootLabel names the root box after the backed-up directory's basename, or
// "root" when the manifest didn't record a path.
func rootLabel(root string) string {
	if b := filepath.Base(root); b != "" && b != "." && b != string(filepath.Separator) {
		return b
	}
	return "root"
}

// filesGraphStyle tints the whole graph a soft neon; it strips to plain glyphs
// under the Ascii profile the tests and goldens render in, so the box art
// carries the meaning.
var filesGraphStyle = lipgloss.NewStyle().Foreground(ui.AccentAqua)

func (v FilesView) View() string {
	switch {
	case v.loading:
		return ui.Muted.Render("loading files…")
	case v.err != nil:
		return ui.Danger.Render("error loading files: ") + v.err.Error()
	case v.root == nil || v.root.totalFiles() == 0:
		return ui.Muted.Render("no files to show yet — run a backup, then reload (ctrl+r)")
	}

	w, h := v.dims()
	header := fmt.Sprintf("%s  %s  %s files  %s",
		ui.Primary.Render(v.id),
		ui.Subtle.Render(v.when.UTC().Format("2006-01-02 15:04")),
		ui.Subtle.Render(fmt.Sprintf("%d", v.files)),
		ui.Subtle.Render(ui.FormatBytes(v.bytes)),
	)

	// The header takes two rows (label + blank); the graph gets the rest.
	graphH := max(h-2, 1)
	graph := renderFileGraph(layoutFileGraph(v.root, w, graphH), w, graphH)
	return header + "\n\n" + filesGraphStyle.Render(graph)
}

// dims resolves the drawable interior, falling back to the min-terminal content
// pane before the first WindowSizeMsg (headless tests, goldens).
func (v FilesView) dims() (w, h int) {
	fw, fh := v.width, v.height
	if fw <= 0 {
		fw = minWidth - sidebarWidth - 3
	}
	if fh <= 0 {
		fh = minHeight - 4
	}
	return fw, fh
}

// FilesView is a Bubbletea model registered like every other view.
var _ tea.Model = FilesView{}
