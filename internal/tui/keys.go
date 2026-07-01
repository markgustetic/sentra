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
}

func newGlobalKeymap() globalKeymap {
	return globalKeymap{
		Palette: key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("ctrl+p", "palette")),
		Focus:   key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "focus")),
		Help:    key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// shortHelp is the always-visible subset for the status bar.
func (k globalKeymap) shortHelp() []key.Binding {
	return []key.Binding{k.Palette, k.Focus, k.Help, k.Quit}
}
