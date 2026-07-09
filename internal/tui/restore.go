package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type restoreStage int

const (
	restorePick restoreStage = iota
	restoreDest
	restoreConfirm
	restoreRunning
	restoreDone
)

type restoreDoneMsg struct {
	verification *repo.RestoreVerification // nil when verify was off
	err          error
}

func (restoreDoneMsg) opResult() {}

// RestoreView drives pick → dest → plan/confirm → running → done.
// PlanRestore runs synchronously at the dest step (it is a metadata
// read, cheap); the actual Restore runs through the App op guard.
type RestoreView struct {
	deps  Deps
	stage restoreStage

	snaps []repo.SnapshotInfo
	tbl   table.Model

	dest    textinput.Model
	destErr string

	plan   repo.RestorePlan
	verify bool
	snapID string

	reporter *opReporter
	bar      progress.Model
	result   restoreDoneMsg
	notice   string // transient banner, e.g. after an op rejection
	width    int
	height   int
}

func NewRestoreView(deps Deps) RestoreView {
	v := RestoreView{
		deps: deps,
		bar:  progress.New(progress.WithDefaultGradient()),
	}
	ti := textinput.New()
	ti.Prompt = "dest> "
	ti.Placeholder = "empty or new directory"
	v.dest = ti

	// Synchronous hydrate, Phase 1 style (async loading arrives with a
	// later phase). Nil repo renders a placeholder.
	if deps.Repo != nil {
		ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), hydrateTimeout)
		defer cancel()
		if snaps, err := deps.Repo.ListSnapshots(ctx); err == nil {
			v.snaps = snaps
		}
	}
	rows := make([]table.Row, len(v.snaps))
	for i, s := range v.snaps {
		rows[i] = table.Row{s.ID, s.CreatedAt.UTC().Format("2006-01-02 15:04"), s.Tag,
			fmt.Sprintf("%d", s.Stats.Files)}
	}
	// Ideal widths until the first WindowSizeMsg; Update re-sizes columns
	// to the interior the App forwards so the table fits the content panel.
	v.tbl = table.New(table.WithColumns(snapshotPickerColumns(pickerIdealWidth, true)),
		table.WithRows(rows), table.WithFocused(true))
	return v
}

func (RestoreView) Init() tea.Cmd { return nil }

func (v RestoreView) Title() string { return "Restore" }

// CapturesText is true only on the destination stage, where the dest-path text
// input is focused — a path may contain digits or 'q', so those runes must
// reach the field. The pick stage drives a table (arrow keys, no free text) and
// the confirm/running/done stages take single-key commands, so none capture.
func (v RestoreView) CapturesText() bool { return v.stage == restoreDest }

func (v RestoreView) ShortHelp() []key.Binding {
	switch v.stage {
	case restorePick:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "snapshot")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "choose")),
		}
	case restoreDest:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "plan"))}
	case restoreConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "restore")),
			key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "toggle verify")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case restoreRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	}
}

func (v RestoreView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		v.tbl.SetColumns(snapshotPickerColumns(pickerContentWidth(v.width), true))
		v.tbl.SetHeight(max(msg.Height-8, 3))
		return v, nil
	case restoreDoneMsg:
		v.stage = restoreDone
		v.result = msg
		return v, nil
	case opRejectedMsg:
		// Our start was refused; return to the confirm stage instead of
		// hanging in running.
		if v.stage == restoreRunning && msg.name == "restore" {
			v.stage = restoreConfirm
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil
	case opTickMsg:
		if v.stage == restoreRunning {
			return v, opTick()
		}
		return v, nil
	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// resetTo returns a fresh restore view carrying the current window size,
// so the snapshot table and progress bar keep their dimensions.
func (v RestoreView) resetTo() (tea.Model, tea.Cmd) {
	return NewRestoreView(v.deps).Update(tea.WindowSizeMsg{Width: v.width, Height: v.height})
}

func (v RestoreView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case restorePick:
		if msg.Type == tea.KeyEnter && len(v.snaps) > 0 {
			v.snapID = v.snaps[v.tbl.Cursor()].ID
			v.stage = restoreDest
			v.dest.Focus()
			return v, nil
		}
		var cmd tea.Cmd
		v.tbl, cmd = v.tbl.Update(msg)
		return v, cmd

	case restoreDest:
		switch msg.Type {
		case tea.KeyEsc:
			v.stage = restorePick
			return v, nil
		case tea.KeyEnter:
			return v.planIt()
		}
		var cmd tea.Cmd
		v.dest, cmd = v.dest.Update(msg)
		v.destErr = ""
		return v, cmd

	case restoreConfirm:
		switch {
		case msg.Type == tea.KeyEsc:
			v.stage = restoreDest
			return v, nil
		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'v':
			v.verify = !v.verify
			return v, nil
		case msg.Type == tea.KeyEnter:
			return v.startRestore()
		}
		return v, nil

	case restoreRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	default: // restoreDone
		if msg.Type == tea.KeyEnter {
			return v.resetTo()
		}
		return v, nil
	}
}

// planIt validates the destination via PlanRestore (which enforces the
// empty-or-absent rule) and advances to the confirm stage on success.
func (v RestoreView) planIt() (tea.Model, tea.Cmd) {
	dest := strings.TrimSpace(v.dest.Value())
	if dest == "" {
		v.destErr = "destination is required"
		return v, nil
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(v.deps.Ctx), hydrateTimeout)
	defer cancel()
	plan, err := v.deps.Repo.PlanRestore(ctx, v.snapID, dest)
	if err != nil {
		v.destErr = err.Error()
		return v, nil
	}
	if plan.DestExists && !plan.DestEmpty {
		v.destErr = "destination is not empty — restore requires an empty or new directory"
		return v, nil
	}
	v.plan = plan
	v.stage = restoreConfirm
	return v, nil
}

func (v RestoreView) startRestore() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.notice = ""
	v.stage = restoreRunning
	r := v.deps.Repo
	reporter := v.reporter
	snapID, dest, doVerify := v.snapID, v.plan.DestDir, v.verify
	start := startOpMsg{
		name: "restore",
		run: func(ctx context.Context) tea.Msg {
			if err := r.Restore(ctx, snapID, dest, repo.RestoreOptions{Progress: reporter}); err != nil {
				return restoreDoneMsg{err: err}
			}
			if doVerify {
				rep, err := r.VerifyRestore(ctx, snapID, dest)
				if err != nil {
					return restoreDoneMsg{err: err}
				}
				return restoreDoneMsg{verification: &rep}
			}
			return restoreDoneMsg{}
		},
	}
	// Batch the startOpMsg with the FIRST opTickMsg so the progress
	// self-loop is seeded — see backup.go's startBackup for the full
	// rationale. The App's op guard unwraps the BatchMsg to find the
	// startOpMsg.
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v RestoreView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	var b strings.Builder
	switch v.stage {
	case restorePick:
		b.WriteString(ui.Primary.Render("Restore: choose a snapshot"))
		b.WriteString("\n\n" + v.tbl.View())
	case restoreDest:
		b.WriteString(ui.Primary.Render("Restore " + v.snapID))
		b.WriteString("\n\n" + v.dest.View())
		if v.destErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.destErr))
		}
	case restoreConfirm:
		b.WriteString(ui.Primary.Render("Ready to restore"))
		if v.notice != "" {
			b.WriteString("\n" + ui.Warn.Render(v.notice))
		}
		fmt.Fprintf(&b, "\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  dest      %s",
			v.plan.SnapshotID, v.plan.Files, ui.FormatBytes(v.plan.Bytes), v.plan.DestDir)
		mark := "off"
		if v.verify {
			mark = "on"
		}
		b.WriteString("\n  verify    " + mark)
		b.WriteString("\n\n" + ui.Muted.Render("⏎ restore · v toggle verify · esc back"))
	case restoreRunning:
		total, done := v.reporter.Snapshot()
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(ui.Primary.Render("Restoring…"))
		b.WriteString("\n\n" + v.bar.ViewAs(pct))
		fmt.Fprintf(&b, "\n\n%s / %s", ui.FormatBytes(done), ui.FormatBytes(total))
	default:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Restore failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Restore complete"))
			if v.result.verification != nil {
				if v.result.verification.OK() {
					b.WriteString("\n\nverification: " + ui.Success.Render("all files match"))
				} else {
					fmt.Fprintf(&b, "\n\nverification: %s (%d mismatches)",
						ui.Danger.Render("FAILED"), len(v.result.verification.Mismatches))
				}
			}
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ restore another"))
	}
	return b.String()
}
