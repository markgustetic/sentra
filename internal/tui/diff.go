package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type diffStage int

const (
	diffPickA diffStage = iota
	diffPickB
	diffShow
)

// Diff walks a two-snapshot picker, then renders the added/removed/
// changed lists from repo.Diff. Snapshots load synchronously at
// construction (a manifest-list read, like the Snapshots view); the diff
// itself is two manifest reads, also fast, so it runs inline at the B
// selection rather than through the async op machinery.
type Diff struct {
	deps  Deps
	stage diffStage
	snaps []repo.SnapshotInfo
	tbl   table.Model
	idA   string
	res   repo.DiffResult
	err   string
	width int
}

func NewDiff(deps Deps) Diff {
	d := Diff{deps: deps}
	if deps.Repo != nil {
		ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 20*time.Second)
		defer cancel()
		if snaps, err := deps.Repo.ListSnapshots(ctx); err == nil {
			d.snaps = snaps
		}
	}
	rows := make([]table.Row, len(d.snaps))
	for i, s := range d.snaps {
		rows[i] = table.Row{s.ID, s.CreatedAt.UTC().Format("2006-01-02 15:04"), s.Tag}
	}
	// Ideal widths until the first WindowSizeMsg; Update re-sizes columns
	// to the interior the App forwards so the table fits the content panel.
	d.tbl = table.New(table.WithColumns(snapshotPickerColumns(pickerIdealWidth, false)),
		table.WithRows(rows), table.WithFocused(true))
	return d
}

func (Diff) Init() tea.Cmd { return nil }

func (d Diff) Title() string { return "Diff" }

func (d Diff) ShortHelp() []key.Binding {
	switch d.stage {
	case diffShow:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "snapshot")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "choose")),
		}
	}
}

func (d Diff) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.tbl.SetColumns(snapshotPickerColumns(pickerContentWidth(d.width), false))
		d.tbl.SetHeight(max(msg.Height-8, 3))
		return d, nil
	case tea.KeyMsg:
		return d.handleKey(msg)
	}
	return d, nil
}

func (d Diff) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch d.stage {
	case diffPickA:
		if msg.Type == tea.KeyEnter && len(d.snaps) > 0 {
			d.idA = d.snaps[d.tbl.Cursor()].ID
			d.stage = diffPickB
			return d, nil
		}
		var cmd tea.Cmd
		d.tbl, cmd = d.tbl.Update(msg)
		return d, cmd
	case diffPickB:
		switch msg.Type {
		case tea.KeyEsc:
			// Back out to the first picker, clearing any prior failure so the
			// live picker reappears — View() gates on d.err ahead of the stage
			// switch, so a stale error would otherwise mask the picker forever.
			d.stage = diffPickA
			d.err = ""
			return d, nil
		case tea.KeyEnter:
			if len(d.snaps) == 0 {
				return d, nil
			}
			idB := d.snaps[d.tbl.Cursor()].ID
			ctx, cancel := context.WithTimeout(ctxOrBackground(d.deps.Ctx), 20*time.Second)
			defer cancel()
			// Each attempt starts clean, so a prior failure never masks a
			// subsequent successful diff.
			d.err = ""
			res, err := d.deps.Repo.Diff(ctx, d.idA, idB)
			if err != nil {
				d.err = err.Error()
				return d, nil
			}
			d.res = res
			d.stage = diffShow
			return d, nil
		}
		var cmd tea.Cmd
		d.tbl, cmd = d.tbl.Update(msg)
		return d, cmd
	default: // diffShow
		if msg.Type == tea.KeyEsc {
			d.stage = diffPickA
			d.err = ""
			return d, nil
		}
		return d, nil
	}
}

func (d Diff) View() string {
	if d.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	if d.err != "" {
		return ui.Danger.Render("Diff failed") + "\n\n" + d.err
	}
	switch d.stage {
	case diffPickA:
		return ui.Primary.Render("Diff: choose the FIRST snapshot") + "\n\n" + d.tbl.View()
	case diffPickB:
		return ui.Primary.Render("Diff "+d.idA+" → choose the SECOND snapshot") + "\n\n" + d.tbl.View()
	default:
		return d.renderResult()
	}
}

func (d Diff) renderResult() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Diff result"))
	fmt.Fprintf(&b, "  %s\n\n", ui.Muted.Render(
		fmt.Sprintf("+%d  -%d  ~%d", len(d.res.Added), len(d.res.Removed), len(d.res.Changed))))
	writeCol := func(label string, style func(...string) string, paths []string) {
		if len(paths) == 0 {
			return
		}
		b.WriteString(label + "\n")
		for _, p := range paths {
			b.WriteString("  " + style(p) + "\n")
		}
		b.WriteString("\n")
	}
	writeCol(ui.Success.Render("Added"), func(s ...string) string { return ui.Success.Render(strings.Join(s, "")) }, d.res.Added)
	writeCol(ui.Danger.Render("Removed"), func(s ...string) string { return ui.Danger.Render(strings.Join(s, "")) }, d.res.Removed)
	writeCol(ui.Warn.Render("Changed"), func(s ...string) string { return ui.Warn.Render(strings.Join(s, "")) }, d.res.Changed)
	b.WriteString(ui.Muted.Render("esc back"))
	return b.String()
}
