package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/ui"
)

// confirmControl names which control of the Confirm step owns the keyboard.
type confirmControl int

const (
	confirmTag confirmControl = iota
	confirmRescan
)

// confirmControls are the two per-run knobs the Confirm step offers beneath
// its summary: an optional tag and the force-rescan toggle. They replaced
// the old configure-screen tag field and the ctrl+r chord — the run's
// adjustments belong where the operator reads what the run will do.
type confirmControls struct {
	tag    textinput.Model
	rescan bool
	focus  confirmControl
	// interior is the pane's text region, kept so the toggle row can clip
	// itself: the panel must never wrap, and the row is the widest line
	// the step renders at an 80-column terminal.
	interior int
}

// newConfirmControls focuses nothing; the stage entry calls refocus.
func newConfirmControls() confirmControls {
	tag := textinput.New()
	tag.Prompt = "tag>  "
	tag.Placeholder = "optional label"
	return confirmControls{tag: tag}
}

// refocus blurs the tag, re-focuses it if it owns the keyboard, and returns
// Focus()'s cmd (nil on the rescan row, which has no cursor).
func (c *confirmControls) refocus() tea.Cmd {
	c.tag.Blur()
	if c.focus == confirmTag {
		return c.tag.Focus()
	}
	return nil
}

func (c *confirmControls) blur()             { c.tag.Blur() }
func (c confirmControls) capturesText() bool { return c.focus == confirmTag && c.tag.Focused() }

// consumesArrows on the rescan row, where ↑/↓ are swallowed rather than
// handed to the nav rail — the rail's live preview would swap the view out
// from under the confirm gate (see scheduleForm.consumesArrows).
func (c confirmControls) consumesArrows() bool { return c.focus == confirmRescan }

// update handles tab (cycle), space on the rescan row (toggle), and typing
// into the tag. Enter and esc belong to the wizard.
func (c confirmControls) update(msg tea.KeyMsg) (confirmControls, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyTab:
		if c.focus == confirmTag {
			c.focus = confirmRescan
		} else {
			c.focus = confirmTag
		}
		cmd := c.refocus()
		return c, cmd
	case c.focus == confirmRescan:
		if msg.Type == tea.KeySpace {
			c.rescan = !c.rescan
		}
		return c, nil
	default:
		var cmd tea.Cmd
		c.tag, cmd = c.tag.Update(msg)
		return c, cmd
	}
}

// setWidth sizes the tag from the pane interior: 6-cell prompt, 1 cursor,
// and the box's cells reserved unconditionally so focusing never resizes.
func (c *confirmControls) setWidth(interior int) {
	c.interior = interior
	c.tag.Width = max(interior-6-1-ui.FieldBoxOverhead, 10)
}

func (c confirmControls) view() string {
	var b strings.Builder
	b.WriteString(boxedField(c.tag))
	box := "[ ]"
	if c.rescan {
		box = "[x]"
	}
	// ui.SelectRow prepends its own 2-cell gutter, so the row's budget is
	// the interior less those cells. The parenthetical is dropped before
	// the keybind is: an operator who cannot see "space toggles" cannot
	// work the row at all.
	row := fmt.Sprintf("  %s force a full rescan (every file re-read; space toggles)", box)
	if budget := c.interior - 2; c.interior > 0 && lipgloss.Width(row) > budget {
		row = fmt.Sprintf("  %s force a full rescan (space toggles)", box)
		row = truncateToWidth(row, max(budget, 1))
	}
	fmt.Fprintf(&b, "\n%s", ui.SelectRow(c.focus == confirmRescan, row))
	return b.String()
}

// confirmRun is the wizard's gate. Re-check the directory (it can vanish
// between steps), install the schedule if one was chosen — FIRST, so a
// failed install blocks the run rather than degrading a confirmed
// repeating backup to a one-shot — then start the op.
func (v BackupView) confirmRun() (tea.Model, tea.Cmd) {
	if !v.checkDir(v.pending) {
		return v, nil
	}
	v.installedName, v.installedNextOK = "", false
	if !v.sched.oneShot() {
		name, sched, _, err := v.sched.build()
		if err != nil {
			v.pathErr = err.Error()
			return v, nil
		}
		if err := v.installRepeat(v.pending, name, sched, v.confirm.tag.Value()); err != nil {
			v.pathErr = "could not install the schedule: " + err.Error()
			return v, nil
		}
		v.installedName = name
		v.installedNext, v.installedNextOK = policycfg.NextRun(sched, v.clock())
	}
	return v.startBackup(v.pending)
}

// summaryLabelWidth is the label column every summary row shares
// ("directory   "), reserved when clipping a row to the panel interior.
const summaryLabelWidth = 12

// confirmSummary renders the read-only block above the controls. Each row
// is clipped to the interior minus its label, so the panel never wraps:
// the path keeps its TAIL (a clipped basename tells the operator nothing),
// the prose rows keep their head.
func (v BackupView) confirmSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "directory   %s\n", v.fitTail(v.pending))
	if v.sched.oneShot() {
		b.WriteString("schedule    one-shot\n")
		return b.String()
	}
	name, sched, reuses, err := v.sched.build()
	if err != nil { // cannot happen: Schedule's enter validated; render honestly anyway
		fmt.Fprintf(&b, "schedule    %s\n", ui.Danger.Render(v.fitValue(err.Error())))
		return b.String()
	}
	verb := "installs an OS timer"
	if reuses {
		verb = "updates the existing policy"
	}
	fmt.Fprintf(&b, "schedule    %s\n",
		v.fitValue(fmt.Sprintf("%s as policy %q — %s", v.sched.describe(), name, verb)))
	if next, ok := policycfg.NextRun(sched, v.clock()); ok {
		fmt.Fprintf(&b, "next run    %s\n", next.Format("Mon 2006-01-02 15:04"))
	}
	return b.String()
}

// fitValue bounds a summary value to what is left of the interior beside
// its label; fitTail does the same keeping the end of the string.
func (v BackupView) fitValue(s string) string {
	if v.width <= 0 {
		return s
	}
	return truncateToWidth(s, max(pickerContentWidth(v.width)-summaryLabelWidth, 1))
}

func (v BackupView) fitTail(s string) string {
	if v.width <= 0 {
		return s
	}
	return truncateToWidthLeft(s, max(pickerContentWidth(v.width)-summaryLabelWidth, 1))
}
