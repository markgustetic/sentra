package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// backupStage is the wizard's position: three configure steps, then the run.
type backupStage int

const (
	backupLocation backupStage = iota
	backupSchedule
	backupConfirm
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

// BackupView is the three-step backup wizard: Location (folder picker) →
// Schedule (one-shot or a cadence that becomes a named policy + OS timer) →
// Confirm (summary, tag, rescan; the gate) → running → done. Each
// configure step owns its widgets through a small struct so focus follows
// the stage: entering a stage focuses its default field, leaving it blurs.
type BackupView struct {
	deps    Deps
	stage   backupStage
	picker  dirPicker
	sched   scheduleForm
	confirm confirmControls
	pending string // the directory chosen on Location
	pathErr string
	notice  string // transient banner, e.g. after an op rejection

	// installedName/Next record the schedule Confirm installed, for the
	// done screen's "next run" line.
	installedName   string
	installedNext   time.Time
	installedNextOK bool

	// now pins the clock for next-run rendering; schedGOOS/schedHome/
	// schedExe are the scheduler seams (empty = production defaults).
	now                            func() time.Time
	schedGOOS, schedHome, schedExe string

	reporter *opReporter
	bar      progress.Model
	result   backupDoneMsg
	width    int
	height   int
}

func NewBackupView(deps Deps) BackupView {
	// Start where the operator started sentra. An unreadable cwd is not fatal —
	// the picker renders its error and still offers the parent row.
	start, err := os.Getwd()
	if err != nil {
		start = ""
	}
	return BackupView{
		deps:    deps,
		picker:  newDirPicker(start),
		confirm: newConfirmControls(),
		now:     time.Now,
		bar:     progress.New(progress.WithDefaultGradient()),
	}
}

// clock reads the view's time seam. A BackupView built as a struct literal
// (tests do) has no seam, and a nil call would panic inside a render.
func (v BackupView) clock() time.Time {
	if v.now == nil {
		return time.Now()
	}
	return v.now()
}

// CapturesText on whichever configure step owns a text field right now. The
// picker is a list, not a field, so the shell keeps its globals on Location
// ('q' quits, ctrl+p opens the palette, digits jump views).
func (v BackupView) CapturesText() bool {
	switch v.stage {
	case backupSchedule:
		return v.sched.capturesText()
	case backupConfirm:
		return v.confirm.capturesText()
	}
	return false
}

// ConsumesArrows where a list owns them: the folder picker, and the
// Schedule step's cadence list. Everywhere else ↑/↓ belong to the nav rail
// (see App.routeKey).
func (v BackupView) ConsumesArrows() bool {
	switch v.stage {
	case backupLocation:
		return true
	case backupSchedule:
		return v.sched.consumesArrows()
	}
	return false
}

func (BackupView) Init() tea.Cmd { return nil }

func (v BackupView) Title() string { return "Backup" }

// ConsumesTab on the two steps that have more than one control, so the
// shell must not steal it for its own focus toggle.
func (v BackupView) ConsumesTab() bool { return v.stage == backupSchedule || v.stage == backupConfirm }

// ConsumesEscape on Schedule/Confirm (step back) and while running (cancel).
// On Location esc belongs to the shell — it is how the operator leaves.
func (v BackupView) ConsumesEscape() bool {
	return v.stage == backupSchedule || v.stage == backupConfirm || v.stage == backupRunning
}

func (v BackupView) ShortHelp() []key.Binding {
	switch v.stage {
	case backupSchedule:
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "cadence")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case backupConfirm:
		return []key.Binding{
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "tag/rescan")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "start")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		}
	case backupRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case backupDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default: // backupLocation
		return []key.Binding{
			key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "move")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open/choose")),
			key.NewBinding(key.WithKeys("backspace"), key.WithHelp("bksp", "up a level")),
		}
	}
}

func (v BackupView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		// The picker's column width depends on whether the pane fits:
		// beside it the picker is pinned to pickerColWidth so the join
		// stays aligned; alone it may use the whole interior (which also
		// stops a deep path from wrapping inside the panel).
		if interior := pickerContentWidth(msg.Width); previewPaneWidth(interior) > 0 {
			v.picker.width = pickerColWidth
		} else {
			v.picker.width = interior
		}
		// The steps' fields size themselves from the interior, reserving
		// the box's cells so focusing one never resizes it.
		v.sched.setWidth(pickerContentWidth(msg.Width))
		v.confirm.setWidth(pickerContentWidth(msg.Width))
		return v, nil

	case backupDoneMsg:
		v.stage = backupDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our start was refused; return to Confirm (not Location — the
		// operator's choices stand) with the tag re-focused for the retry.
		if v.stage == backupRunning && msg.name == "backup" {
			v.stage = backupConfirm
			v.notice = "another operation is in progress — try again when it finishes"
			cmd := v.confirm.refocus()
			return v, cmd
		}
		return v, nil

	case opTickMsg:
		if v.stage == backupRunning {
			return v, opTick() // keep ticking while running
		}
		return v, nil

	case chatBackupMsg:
		// The chat's start_backup intent lands on Confirm — the same human
		// gate a hand-driven backup reaches — with the directory and tag
		// seeded and one-shot chosen. Ignored mid-flow.
		if v.stage == backupRunning || v.stage == backupDone {
			return v, nil
		}
		dir := strings.TrimSpace(msg.dir)
		if !v.checkDir(dir) {
			// Drop back to Location with the error, blurring whatever the
			// step we were on owned: a field left focused on a stage that
			// never renders it keeps its blink chain rescheduling and makes
			// Focused() lie to every guard that reads it. checkDir's pathErr
			// stands, so backTo (which clears it) is not the path here.
			v.sched.blur()
			v.confirm.blur()
			v.stage = backupLocation
			return v, nil
		}
		v.picker = newDirPicker(dir)
		m, _ := v.enterSchedule(dir)
		v = m.(BackupView)
		v.confirm = newConfirmControls()
		v.confirm.setWidth(pickerContentWidth(v.width))
		if msg.tag != "" {
			v.confirm.tag.SetValue(msg.tag)
		}
		return v.enterConfirm()

	case viewShownMsg:
		// On screen: re-focus whatever field the current stage owns. The
		// picker and the cadence list own none, so they schedule nothing.
		switch v.stage {
		case backupSchedule:
			cmd := v.sched.refocus()
			return v, cmd
		case backupConfirm:
			cmd := v.confirm.refocus()
			return v, cmd
		}
		return v, nil

	case viewHiddenMsg:
		v.sched.blur()
		v.confirm.blur()
		return v, nil

	case cursor.BlinkMsg:
		// Route the tick to the one field that can still be focused, so the
		// chain keeps rescheduling itself.
		var cmd tea.Cmd
		switch {
		case v.sched.name.Focused():
			v.sched.name, cmd = v.sched.name.Update(msg)
		case v.sched.at.Focused():
			v.sched.at, cmd = v.sched.at.Update(msg)
		case v.confirm.tag.Focused():
			v.confirm.tag, cmd = v.confirm.tag.Update(msg)
		}
		return v, cmd

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// resetTo returns a fresh view carrying the current window size so the
// progress bar keeps its width (bubbletea does not re-emit WindowSizeMsg
// after a model swap). The fresh view is on screen the moment it replaces
// this one, so it takes the same viewShownMsg the shell sends (a no-op
// today — a fresh view lands on the picker — but the seam is the same one
// every field-owning stage uses).
func (v BackupView) resetTo() (tea.Model, tea.Cmd) {
	fresh := NewBackupView(v.deps)
	fresh.schedGOOS, fresh.schedHome, fresh.schedExe = v.schedGOOS, v.schedHome, v.schedExe
	fresh.now = v.now
	m, _ := fresh.Update(tea.WindowSizeMsg{Width: v.width, Height: v.height})
	return m.Update(viewShownMsg{})
}

// enterSchedule leaves Location for Schedule: the picker's directory becomes
// pending and a fresh scheduleForm is built for it against the known
// policies. Nothing is focused — the list has the keyboard.
func (v BackupView) enterSchedule(dir string) (tea.Model, tea.Cmd) {
	v.pending = dir
	var policies map[string]config.PolicyConfig
	if v.deps.Config != nil {
		policies = v.deps.Config.Policies
	}
	v.sched = newScheduleForm(dir, policies)
	v.sched.setWidth(pickerContentWidth(v.width))
	v.stage = backupSchedule
	return v, nil
}

// enterConfirm leaves Schedule for Confirm with the tag field focused.
func (v BackupView) enterConfirm() (tea.Model, tea.Cmd) {
	v.sched.blur()
	v.stage = backupConfirm
	v.pathErr = ""
	v.confirm.focus = confirmTag
	cmd := v.confirm.refocus()
	return v, cmd
}

// backTo steps the wizard back one stage, blurring what it leaves.
func (v BackupView) backTo(stage backupStage) (tea.Model, tea.Cmd) {
	v.sched.blur()
	v.confirm.blur()
	v.pathErr = ""
	v.stage = stage
	if stage == backupSchedule {
		v.sched.err = ""
		v.sched.focus = schedCadence
	}
	return v, nil
}

// checkDir is the cheap validation both the Location step and Confirm run:
// stat says directory, and a repo is configured. The walker surfaces
// everything else.
func (v *BackupView) checkDir(root string) bool {
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		v.pathErr = fmt.Sprintf("directory not found: %s", root)
		return false
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return false
	}
	v.pathErr = ""
	return true
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

	case backupSchedule:
		v.notice = ""
		switch msg.Type {
		case tea.KeyEsc:
			return v.backTo(backupLocation)
		case tea.KeyEnter:
			// Validate here, not on Confirm: a bad time or a name owned by
			// another directory is the Schedule step's problem to show.
			if _, _, _, err := v.sched.build(); err != nil {
				v.sched.err = err.Error()
				return v, nil
			}
			return v.enterConfirm()
		}
		var cmd tea.Cmd
		v.sched, cmd = v.sched.update(msg)
		return v, cmd

	case backupConfirm:
		v.notice = ""
		switch msg.Type {
		case tea.KeyEsc:
			return v.backTo(backupSchedule)
		case tea.KeyEnter:
			return v.confirmRun()
		}
		var cmd tea.Cmd
		v.confirm, cmd = v.confirm.update(msg)
		return v, cmd

	default: // backupLocation
		v.notice = "" // any interaction dismisses the rejection banner
		switch msg.Type {
		case tea.KeyUp:
			v.picker = v.picker.moveUp()
		case tea.KeyDown:
			v.picker = v.picker.moveDown()
		case tea.KeyBackspace, tea.KeyLeft:
			v.picker = v.picker.up()
		case tea.KeyRight:
			// Right descends without ever choosing. activate navigates a
			// folder or ".." and changes nothing on the button, so dropping
			// the returned path makes right a pure navigation key.
			v.picker, _ = v.picker.activate()
		case tea.KeyEnter:
			// enter navigates the folder rows; only the button, which
			// activate signals by returning the current directory, chooses
			// it and moves the wizard on.
			var chosen string
			v.picker, chosen = v.picker.activate()
			if chosen == "" {
				return v, nil
			}
			chosen = strings.TrimSpace(chosen)
			if !v.checkDir(chosen) {
				return v, nil
			}
			return v.enterSchedule(chosen)
		}
		return v, nil
	}
}

// startBackup validates root and emits startOpMsg. Validation is deliberately
// cheap (stat only) — the walker surfaces everything else. The stat is kept
// even though Confirm already checked: the folder can be removed between the
// steps.
func (v BackupView) startBackup(root string) (tea.Model, tea.Cmd) {
	root = strings.TrimSpace(root)
	if !v.checkDir(root) {
		return v, nil
	}

	v.reporter = newOpReporter()
	v.stage = backupRunning
	// Leaving Confirm blurs its field: the running screen never renders it,
	// and a focused field nobody renders keeps its blink chain rescheduling
	// while Focused() lies to every guard that reads it.
	tag := strings.TrimSpace(v.confirm.tag.Value())
	rescan := v.confirm.rescan
	v.confirm.blur()
	r := v.deps.Repo
	reporter := v.reporter
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

// fit bounds a line to the view's interior so the panel never wraps it;
// width 0 (no resize yet) leaves it alone, the picker's own rule.
func (v BackupView) fit(s string) string {
	if v.width <= 0 {
		return s
	}
	return truncateToWidth(s, pickerContentWidth(v.width))
}

// actionLine renders the footer bounded to the interior. ui.ActionLine
// adds an 18-cell "⏎  Press enter to " prefix to the primary and a 3-cell
// indent to the secondary, so each is clipped to its remaining budget
// BEFORE styling — clipping afterwards would cut styled text.
func (v BackupView) actionLine(primary, secondary string) string {
	if v.width > 0 {
		region := pickerContentWidth(v.width)
		primary = truncateToWidth(primary, max(region-18, 1))
		secondary = truncateToWidth(secondary, max(region-3, 1))
	}
	return ui.ActionLine(primary, secondary)
}

// header mirrors the setup wizard's: what this is on the left, where you
// are on the right, then the step's title.
func (v BackupView) header(step int, title string) string {
	left := ui.Muted.Render("New backup")
	right := ui.Muted.Render(fmt.Sprintf("Step %d of 3", step))
	gap := max(pickerContentWidth(v.width)-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right + "\n\n" + ui.Primary.Render(title) + "\n"
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
			fmt.Fprintf(&b, "\n\n%s", humanizeErr(v.result.err))
		} else {
			b.WriteString(ui.Success.Render("Backup complete"))
			info := v.result.info
			fmt.Fprintf(&b, "\n\n  snapshot  %s\n  files     %d\n  bytes     %s\n  new       %s",
				info.ID, info.Stats.Files,
				ui.FormatBytes(info.Stats.Bytes), ui.FormatBytes(info.Stats.NewBytes))
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine("run another backup", ""))

	case backupSchedule:
		b.WriteString(v.header(2, "Schedule"))
		fmt.Fprintf(&b, "\n%s", v.sched.view())
		fmt.Fprintf(&b, "\n\n%s", v.actionLine("continue to the summary", "tab next field · esc back"))

	case backupConfirm:
		b.WriteString(v.header(3, "Confirm"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.fit(v.notice)))
		}
		fmt.Fprintf(&b, "\n%s\n%s", v.confirmSummary(), v.confirm.view())
		if v.pathErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.fit(v.pathErr)))
		}
		verb := "start the backup of " + filepath.Base(v.pending)
		if !v.sched.oneShot() {
			verb = "install the schedule and " + verb
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine(verb, "tab tag/rescan · esc back"))

	case backupLocation:
		b.WriteString(v.header(1, "Location"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.fit(v.notice)))
		}
		pickerCol := v.picker.View(true)
		if paneW := previewPaneWidth(pickerContentWidth(v.width)); paneW > 0 {
			// A Width-only style pads the picker block to its fixed column
			// without adding color codes, so the styled rows inside survive
			// (same pattern as the App's rail at app.go View). Top-aligned:
			// the pane's header sits beside the picker's path line.
			left := lipgloss.NewStyle().Width(pickerColWidth).Render(pickerCol)
			pickerCol = lipgloss.JoinHorizontal(lipgloss.Top,
				left, strings.Repeat(" ", previewGapWidth), v.picker.previewView(paneW))
		}
		fmt.Fprintf(&b, "\n%s", pickerCol)
		if v.pathErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.fit(v.pathErr)))
		}
		fmt.Fprintf(&b, "\n\n%s", v.actionLine(v.picker.enterVerb(), "↑↓ move · → opens · bksp up · esc leaves"))
	}
	return b.String()
}
