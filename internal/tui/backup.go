package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
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

// backupConfirmID tags the backup confirmation modal so the App's confirmedMsg
// broadcast routes back to this flow (mirrors pruneConfirmID). Backup is not
// destructive, so a plain yes/no ConfirmModal — not the typed gate prune uses —
// is the right weight: it exists to stop an accidental enter from kicking off a
// snapshot, not to force deliberate intent.
const backupConfirmID = "backup"

// BackupView drives configure → running → done for a new snapshot.
// The repo call runs in the App-managed op goroutine; this view only
// renders progress (polled via opTick) and the result.
// backupFocus names which control owns the keyboard on the configure stage. The
// two halves have opposite shell contracts: the picker wants the arrow keys and
// captures no text; the tag field captures text and wants no arrows.
type backupFocus int

const (
	focusPicker backupFocus = iota
	focusTagField
)

type BackupView struct {
	deps    Deps
	stage   backupStage
	picker  dirPicker
	tag     textinput.Model
	focus   backupFocus
	pathErr string

	// rescan arms ForceRescan for the next snapshot: every file is
	// re-read even when size+mtime match the parent. ctrl+r toggles it
	// — a chord, because both the picker and the tag field own plain
	// runes on this stage.
	rescan bool

	reporter *opReporter
	bar      progress.Model
	result   backupDoneMsg
	notice   string // transient banner, e.g. after an op rejection
	pending  string // the directory awaiting the confirmation gate
	width    int
	height   int
}

func NewBackupView(deps Deps) BackupView {
	tag := textinput.New()
	tag.Prompt = "tag>  "
	tag.Placeholder = "optional label"
	// Start where the operator started sentra. An unreadable cwd is not fatal —
	// the picker renders its error and still offers the parent row.
	start, err := os.Getwd()
	if err != nil {
		start = ""
	}
	return BackupView{
		deps:   deps,
		picker: newDirPicker(start),
		tag:    tag,
		bar:    progress.New(progress.WithDefaultGradient()),
	}
}

// CapturesText only while the tag field holds focus. The picker is a list, not a
// field, so the shell keeps its globals there ('q' quits, ctrl+p opens the
// palette, digits jump views).
func (v BackupView) CapturesText() bool {
	return v.stage == backupConfigure && v.focus == focusTagField
}

// ConsumesArrows only while the picker holds focus; otherwise ↑/↓ belong to the
// nav rail (see App.routeKey).
func (v BackupView) ConsumesArrows() bool {
	return v.stage == backupConfigure && v.focus == focusPicker
}

func (BackupView) Init() tea.Cmd { return nil }

func (v BackupView) Title() string { return "Backup" }

// ConsumesTab: on the configure stage tab moves between the folder picker and
// the tag field, so the shell must not steal it for its own focus toggle. esc is
// how the operator leaves the view.
func (v BackupView) ConsumesTab() bool { return v.stage == backupConfigure }

// ConsumesEscape: only while an op is running, where esc cancels it. On the
// configure stage esc belongs to the shell — that is the escape hatch out of the
// tag field.
func (v BackupView) ConsumesEscape() bool { return v.stage == backupRunning }

func (v BackupView) ShortHelp() []key.Binding {
	switch v.stage {
	case backupRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case backupDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default: // backupConfigure — the keys depend on which control has focus
		if v.focus == focusPicker {
			return []key.Binding{
				key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move")),
				key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open/start")),
				key.NewBinding(key.WithKeys("backspace"), key.WithHelp("bksp", "up a level")),
				key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "tag")),
			}
		}
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "start")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "folders")),
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

	case confirmedMsg:
		// The confirmation gate was accepted (the App pops the modal and
		// broadcasts this). Ignore a foreign id, or one that arrives when we
		// are no longer waiting to start, then launch the pending backup.
		if msg.id != backupConfirmID || v.stage != backupConfigure {
			return v, nil
		}
		return v.startBackup(v.pending)

	case cursor.BlinkMsg:
		if v.tag.Focused() {
			var cmd tea.Cmd
			v.tag, cmd = v.tag.Update(msg)
			return v, cmd
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
		v.notice = "" // any interaction dismisses the rejection banner

		if msg.Type == tea.KeyCtrlR {
			v.rescan = !v.rescan
			return v, nil
		}
		if msg.Type == tea.KeyTab {
			if v.focus == focusPicker {
				v.focus = focusTagField
				v.tag.Focus()
				return v, textinput.Blink
			}
			v.focus = focusPicker
			v.tag.Blur()
			return v, nil
		}

		if v.focus == focusPicker {
			switch msg.Type {
			case tea.KeyUp:
				v.picker = v.picker.moveUp()
			case tea.KeyDown:
				v.picker = v.picker.moveDown()
			case tea.KeyBackspace, tea.KeyLeft:
				v.picker = v.picker.up()
			case tea.KeyRight:
				// Right descends without ever committing. activate navigates a
				// folder or ".." and changes nothing on the Start button, so
				// dropping the returned path makes right a pure navigation key —
				// only enter on the Start button starts a backup.
				v.picker, _ = v.picker.activate()
			case tea.KeyEnter:
				// enter navigates the folder rows; only the Start button, which
				// activate signals by returning the current directory, asks to
				// back up — raising the confirmation gate.
				var chosen string
				v.picker, chosen = v.picker.activate()
				if chosen != "" {
					return v.requestBackup(chosen)
				}
			}
			return v, nil
		}

		// focusTagField: enter confirms the backup of whatever the picker is
		// browsing, so a tag can be set first without hunting for a submit.
		if msg.Type == tea.KeyEnter {
			return v.requestBackup(v.picker.cwd)
		}
		var cmd tea.Cmd
		v.tag, cmd = v.tag.Update(msg)
		return v, cmd
	}
}

// requestBackup validates root and raises the confirmation gate before any
// snapshot is taken. Validation is deliberately cheap (stat only) and happens
// BEFORE the modal, so the operator never confirms a path that can't be backed
// up; the walker surfaces everything else. On confirm the App broadcasts a
// confirmedMsg{backupConfirmID} that Update turns into startBackup(v.pending).
func (v BackupView) requestBackup(root string) (tea.Model, tea.Cmd) {
	root = strings.TrimSpace(root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		v.pathErr = fmt.Sprintf("directory not found: %s", root)
		return v, nil
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return v, nil
	}
	v.pathErr = ""
	v.notice = ""
	v.pending = root

	// Plain body text — the modal frames it. (Embedding styled fragments would
	// hit the "never wrap an already-styled string" trap, since ModalBox renders
	// the whole body.)
	body := "Back up this directory?\n\n" + root
	if tag := strings.TrimSpace(v.tag.Value()); tag != "" {
		body += "\n\ntag: " + tag
	} else {
		body += "\n\nno tag"
	}
	modal := NewConfirmModal("Confirm backup", body, backupConfirmID, v.width, v.height)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startBackup validates root and emits startOpMsg. Validation is deliberately
// cheap (stat only) — the walker surfaces everything else. The stat is kept even
// though requestBackup already checked: the folder can be removed between the
// confirmation and the confirm.
func (v BackupView) startBackup(root string) (tea.Model, tea.Cmd) {
	root = strings.TrimSpace(root)
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
	rescan := v.rescan
	var wopts walker.Options
	if v.deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    v.deps.Config.Backup.IgnoreFile,
			ExcludeCaches: v.deps.Config.Backup.ExcludeCaches,
			Concurrency:   v.deps.Config.Backup.Concurrency,
		}
	}
	start := startOpMsg{
		name: "backup",
		run: func(ctx context.Context) tea.Msg {
			info, err := r.CreateSnapshot(ctx, root, repo.SnapshotOptions{
				Tag:         tag,
				Progress:    reporter,
				Walker:      wopts,
				ForceRescan: rescan,
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
		fmt.Fprintf(&b, "\n%s", ui.Muted.Render("esc cancel"))

	case backupDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Backup failed"))
			fmt.Fprintf(&b, "\n\n%s", v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Backup complete"))
			info := v.result.info
			fmt.Fprintf(&b, "\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  new       %s",
				info.ID, info.Stats.Files,
				ui.FormatBytes(info.Stats.Bytes), ui.FormatBytes(info.Stats.NewBytes))
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("run another backup", ""))

	default:
		b.WriteString(ui.Primary.Render("New backup"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.notice))
		}
		fmt.Fprintf(&b, "\n\n%s", v.picker.View(v.focus == focusPicker))
		tagField := v.tag.View()
		if v.tag.Focused() {
			// The box IS the focus affordance: only while tab has moved
			// focus onto the tag field does it carry the frame.
			tagField = ui.FieldBox.Render(tagField)
		}
		fmt.Fprintf(&b, "\n%s", tagField)
		if v.rescan {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render("  rescan armed — every file will be re-read (ctrl+r to disarm)"))
		} else {
			fmt.Fprintf(&b, "\n%s", ui.Muted.Render("  incremental scan on (ctrl+r to force a full rescan)"))
		}
		if v.pathErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.pathErr))
		}

		// The action line names what enter does to the FOCUSED control right now.
		// In the picker that is one of three things depending on the cursor
		// (open a folder / go up / start on the Start button), so it is read from
		// the picker.
		if v.focus == focusPicker {
			fmt.Fprintf(&b, "\n\n%s", ui.ActionLine(v.picker.enterVerb(), "↑↓ move · ↓ to browse folders · backspace up a level · tab to add a tag"))
		} else {
			fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("start the backup of "+filepath.Base(v.picker.cwd), "tab back to the folder picker"))
		}
	}
	return b.String()
}
