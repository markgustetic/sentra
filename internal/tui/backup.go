package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// backupStage is the backup flow's state machine position.
type backupStage int

const (
	backupConfigure backupStage = iota
	backupRunning
	backupDone
)

// backupDoneMsg is the flow's terminal message; implementing
// opResultMsg clears the App guard.
type backupDoneMsg struct {
	info repo.SnapshotInfo
	err  error
}

func (backupDoneMsg) opResult() {}

// BackupView drives configure → running → done for a new snapshot.
// The repo call runs in the App-managed op goroutine; this view only
// renders progress (polled via opTick) and the result.
type BackupView struct {
	deps     Deps
	stage    backupStage
	path     textinput.Model
	tag      textinput.Model
	focusTag bool
	pathErr  string

	reporter *opReporter
	bar      progress.Model
	result   backupDoneMsg
	notice   string // transient banner, e.g. after an op rejection
	width    int
	height   int
}

func NewBackupView(deps Deps) BackupView {
	path := textinput.New()
	path.Prompt = "path> "
	path.Placeholder = "directory to back up"
	path.Focus()
	tag := textinput.New()
	tag.Prompt = "tag>  "
	tag.Placeholder = "optional label"
	return BackupView{
		deps: deps,
		path: path,
		tag:  tag,
		bar:  progress.New(progress.WithDefaultGradient()),
	}
}

func (BackupView) Init() tea.Cmd { return nil }

func (v BackupView) Title() string { return "Backup" }

func (v BackupView) ShortHelp() []key.Binding {
	switch v.stage {
	case backupRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case backupDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "start")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		}
	}
}

func (v BackupView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		return v, nil

	case backupDoneMsg:
		v.stage = backupDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our start was refused; leave the running stage we optimistically
		// entered so the flow doesn't hang forever.
		if v.stage == backupRunning && msg.name == "backup" {
			v.stage = backupConfigure
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == backupRunning {
			return v, opTick() // keep ticking while running
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// resetTo returns a fresh view carrying the current window size so the
// progress bar keeps its width (bubbletea does not re-emit WindowSizeMsg
// after a model swap).
func (v BackupView) resetTo() (tea.Model, tea.Cmd) {
	return NewBackupView(v.deps).Update(tea.WindowSizeMsg{Width: v.width, Height: v.height})
}

func (v BackupView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case backupRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	case backupDone:
		if msg.Type == tea.KeyEnter {
			return v.resetTo()
		}
		return v, nil

	default: // backupConfigure
		switch msg.Type {
		case tea.KeyTab:
			v.focusTag = !v.focusTag
			if v.focusTag {
				v.path.Blur()
				v.tag.Focus()
			} else {
				v.tag.Blur()
				v.path.Focus()
			}
			return v, nil
		case tea.KeyEnter:
			return v.startBackup()
		}
		var cmd tea.Cmd
		if v.focusTag {
			v.tag, cmd = v.tag.Update(msg)
		} else {
			v.path, cmd = v.path.Update(msg)
			v.pathErr = "" // typing clears the last validation error
		}
		v.notice = "" // any interaction dismisses the rejection banner
		return v, cmd
	}
}

// startBackup validates the path and emits startOpMsg. Validation is
// deliberately cheap (stat only) — the walker surfaces everything else.
func (v BackupView) startBackup() (tea.Model, tea.Cmd) {
	root := strings.TrimSpace(v.path.Value())
	if root == "" {
		v.pathErr = "path is required"
		return v, nil
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		v.pathErr = fmt.Sprintf("directory not found: %s", root)
		return v, nil
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return v, nil
	}

	v.reporter = newOpReporter()
	v.stage = backupRunning
	r := v.deps.Repo
	reporter := v.reporter
	tag := strings.TrimSpace(v.tag.Value())
	var wopts walker.Options
	if v.deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    v.deps.Config.Backup.IgnoreFile,
			ExcludeCaches: v.deps.Config.Backup.ExcludeCaches,
		}
	}
	start := startOpMsg{
		name: "backup",
		run: func(ctx context.Context) tea.Msg {
			info, err := r.CreateSnapshot(ctx, root, repo.SnapshotOptions{
				Tag:      tag,
				Progress: reporter,
				Walker:   wopts,
			})
			return backupDoneMsg{info: info, err: err}
		},
	}
	// Batch the startOpMsg with the FIRST opTickMsg. The App's op guard
	// unwraps the BatchMsg and sees the startOpMsg to launch the op; the
	// seeded opTickMsg is what starts the progress-repaint self-loop
	// (opTickMsg → opTick() while running). Without this seed nothing
	// ever emits the first tick — bubbletea only redraws on messages, so
	// the progress bar would never repaint during a real run.
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v BackupView) View() string {
	var b strings.Builder
	switch v.stage {
	case backupRunning:
		total, done := v.reporter.Snapshot()
		b.WriteString(ui.Primary.Render("Backing up…"))
		b.WriteString("\n\n")
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(v.bar.ViewAs(pct))
		fmt.Fprintf(&b, "\n\n%s / %s uploaded",
			ui.FormatBytes(done), ui.FormatBytes(total))
		b.WriteString("\n" + ui.Muted.Render("esc cancel"))

	case backupDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Backup failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Backup complete"))
			info := v.result.info
			fmt.Fprintf(&b, "\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  new       %s",
				info.ID, info.Stats.Files,
				ui.FormatBytes(info.Stats.Bytes), ui.FormatBytes(info.Stats.NewBytes))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ run another backup"))

	default:
		b.WriteString(ui.Primary.Render("New backup"))
		if v.notice != "" {
			b.WriteString("\n" + ui.Warn.Render(v.notice))
		}
		b.WriteString("\n\n" + v.path.View())
		b.WriteString("\n" + v.tag.View())
		if v.pathErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.pathErr))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ start · tab switch field"))
	}
	return b.String()
}
