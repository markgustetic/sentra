package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/ui"
)

// settingsEntryKind distinguishes a row that navigates elsewhere from a row
// that mutates a setting in place.
type settingsEntryKind int

const (
	entryNavigate settingsEntryKind = iota
	entryToggleSplash
)

// settingsEntry is one actionable row in the Settings view. A navigate entry
// emits an activateMsg for targetID; a toggle entry mutates the config and
// persists it. Settings holds no secrets.
type settingsEntry struct {
	kind     settingsEntryKind
	label    string
	desc     string
	targetID string // navigate entries only
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
	err     string // inline failure text, e.g. a failed config write
}

func NewSettingsView(deps Deps) SettingsView {
	return SettingsView{
		deps: deps,
		entries: []settingsEntry{
			{kind: entryNavigate, label: "Re-run setup", desc: "reconfigure the backend and repository", targetID: "setup"},
			{kind: entryNavigate, label: "Change passphrase", desc: "rotate the repository passphrase", targetID: "password"},
			{kind: entryToggleSplash, label: "Welcome splash", desc: "show the logo screen at launch (applies next launch)"},
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
			e := v.entries[v.cursor]
			if e.kind == entryToggleSplash {
				return v.toggleSplash()
			}
			return v, func() tea.Msg { return activateMsg{id: e.targetID} }
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
		if e.kind == entryToggleSplash {
			line = e.label + "   [" + v.splashState() + "]"
		}
		if i == v.cursor {
			b.WriteString(ui.SidebarActive.Render(line) + "\n")
		} else {
			b.WriteString(ui.SidebarItem.Render(line) + "\n")
		}
		b.WriteString("    " + ui.Muted.Render(e.desc) + "\n")
	}
	if v.err != "" {
		b.WriteString("\n" + ui.Danger.Render(v.err))
	}
	b.WriteString("\n" + ui.Muted.Render("↑↓ move   ⏎ open / toggle"))
	return b.String()
}

// toggleSplash flips ui.hide_splash and persists it. It mutates a COPY, writes
// that, and only adopts the value in memory once the file is on disk — a failed
// write must never leave the process disagreeing with sentra.yaml.
func (v SettingsView) toggleSplash() (tea.Model, tea.Cmd) {
	if v.deps.Config == nil || v.deps.ConfigPath == "" {
		v.err = "available after setup"
		return v, nil
	}
	next := *v.deps.Config
	next.UI.HideSplash = !next.UI.HideSplash
	if err := config.Write(v.deps.ConfigPath, &next); err != nil {
		v.err = "could not save: " + err.Error()
		return v, nil
	}
	*v.deps.Config = next
	v.err = ""
	return v, nil
}

// splashState renders the toggle's current value for the row label.
func (v SettingsView) splashState() string {
	if v.deps.Config == nil {
		return "—"
	}
	if v.deps.Config.UI.HideSplash {
		return "off"
	}
	return "on"
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
