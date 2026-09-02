package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

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
	c.tag.Width = max(interior-6-1-ui.FieldBoxOverhead, 10)
}

func (c confirmControls) view() string {
	var b strings.Builder
	b.WriteString(boxedField(c.tag))
	box := "[ ]"
	if c.rescan {
		box = "[x]"
	}
	fmt.Fprintf(&b, "\n%s", ui.SelectRow(c.focus == confirmRescan,
		fmt.Sprintf("  %s force a full rescan (every file re-read; space toggles)", box)))
	return b.String()
}
