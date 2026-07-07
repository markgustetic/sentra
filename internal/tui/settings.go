package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/ui"
)

// settingsEntry is one actionable row in the Settings view. Activating it
// emits an activateMsg for targetID, which App routes to view navigation
// (app.go's activateMsg case). Settings itself performs no I/O and holds
// no secrets — it is a read-only launcher over the config summary.
type settingsEntry struct {
	label    string
	desc     string
	targetID string
}

// SettingsView is the Settings hub: a non-secret summary of the resolved
// configuration (bucket, prefix, keyring flag, config path) plus a short
// list of entries that re-enter other views — "Re-run setup" jumps to the
// setup wizard, "Change passphrase" jumps to the password view. It owns no
// goroutines and takes no op guard; Enter merely emits an activateMsg the
// shell already knows how to route.
//
// Security: it renders only non-secret configuration fields. The passphrase
// itself, AWS credentials, wrapped keys, salts, and MAC material are never
// read here — the summary is limited to bucket/prefix/path/keyring-flag,
// which are plain YAML data.
type SettingsView struct {
	deps    Deps
	entries []settingsEntry
	cursor  int
	width   int
}

func NewSettingsView(deps Deps) SettingsView {
	return SettingsView{
		deps: deps,
		entries: []settingsEntry{
			{label: "Re-run setup", desc: "reconfigure the backend and repository", targetID: "setup"},
			{label: "Change passphrase", desc: "rotate the repository passphrase", targetID: "password"},
		},
	}
}

func (SettingsView) Init() tea.Cmd { return nil }

func (v SettingsView) Title() string { return "Settings" }

func (v SettingsView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "entry")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
}

func (v SettingsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if v.cursor > 0 {
				v.cursor--
			}
			return v, nil
		case tea.KeyDown:
			if v.cursor < len(v.entries)-1 {
				v.cursor++
			}
			return v, nil
		case tea.KeyEnter:
			id := v.entries[v.cursor].targetID
			return v, func() tea.Msg { return activateMsg{id: id} }
		}
		return v, nil
	}
	return v, nil
}

func (v SettingsView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Settings") + "\n\n")
	b.WriteString(v.renderSummary() + "\n")
	for i, e := range v.entries {
		line := e.label
		if i == v.cursor {
			b.WriteString(ui.SidebarActive.Render(line) + "\n")
		} else {
			b.WriteString(ui.SidebarItem.Render(line) + "\n")
		}
		b.WriteString("    " + ui.Muted.Render(e.desc) + "\n")
	}
	b.WriteString("\n" + ui.Muted.Render("↑↓ move   ⏎ open"))
	return b.String()
}

// renderSummary shows the non-secret configuration identity. With a nil
// config it renders a single placeholder line so the view still draws
// (Deps{} in tests, unconfigured installs).
func (v SettingsView) renderSummary() string {
	cfg := v.deps.Config
	if cfg == nil {
		return ui.Muted.Render("no configuration loaded") + "\n"
	}
	var b strings.Builder
	field := func(label, val string) {
		if val == "" {
			val = ui.Subtle.Render("(unset)")
		}
		b.WriteString("  " + ui.Muted.Render(label) + "  " + val + "\n")
	}
	field("bucket ", cfg.Repo.S3.Bucket)
	field("prefix ", cfg.Repo.S3.Prefix)
	field("region ", cfg.Repo.S3.Region)
	keyring := "off"
	if cfg.Passphrase.UseKeyring {
		keyring = "on"
	}
	field("keyring", keyring)
	if v.deps.ConfigPath != "" {
		field("config ", v.deps.ConfigPath)
	}
	return b.String()
}
