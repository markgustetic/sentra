package tui

import "github.com/charmbracelet/bubbles/key"

// globalKeys are the shell-level bindings that work regardless of the
// active view (subject to overlay focus: the palette and modals see
// keys first so typing isn't hijacked).
type globalKeymap struct {
	Palette key.Binding
	Focus   key.Binding
	Help    key.Binding
	Quit    key.Binding

	// Back is the shell's escape hatch. A view that means something by esc keeps
	// it (see escapeConsumer); otherwise esc returns focus to the nav rail, so a
	// text field can never trap the keyboard.
	Back key.Binding

	// ForceQuit is what the status bar advertises while a text field owns the
	// keyboard: 'q' is typed into the field there, so only the chord quits.
	ForceQuit key.Binding
}

func newGlobalKeymap() globalKeymap {
	return globalKeymap{
		Palette:   key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		Focus:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
		Back:      key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		ForceQuit: key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}
}

// shortHelp is the always-visible subset for the status bar.
func (k globalKeymap) shortHelp() []key.Binding {
	return []key.Binding{k.Palette, k.Focus, k.Help, k.Quit}
}
