package tui

import (
	"fmt"
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
	entryForgetKeyring
)

// settingsForgetConfirmID ties the forget-keyring confirm modal back to
// this view.
const settingsForgetConfirmID = "settings-forget-keyring"

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
			// The management views live behind Settings rather than on the
			// rail: they are configured rarely, and each rail slot they held
			// taxed the daily backup/snapshot/restore loop. Every demoted
			// view keeps a navigate entry here — hidden from the rail must
			// never mean unreachable.
			{kind: entryNavigate, label: "Retention policies", desc: "named retention policies and dry-runs", targetID: "policies"},
			{kind: entryNavigate, label: "Backup schedule", desc: "emit cron entries for scheduled backups", targetID: "schedule"},
			{kind: entryNavigate, label: "Recovery kit", desc: "render the printable recovery document", targetID: "recovery-kit"},
			{kind: entryNavigate, label: "Change passphrase", desc: "rotate the repository passphrase", targetID: "password"},
			{kind: entryNavigate, label: "Re-run setup", desc: "reconfigure the backend and repository", targetID: "setup"},
			{kind: entryToggleSplash, label: "Welcome splash", desc: "show the logo screen at launch (applies next launch)"},
			{kind: entryForgetKeyring, label: "Forget keyring passphrase", desc: "remove the OS keyring entry and disable keyring lookup"},
		},
	}
}

func (SettingsView) Init() tea.Cmd { return nil }

// ConsumesArrows: the entry cursor is always present.
func (v SettingsView) ConsumesArrows() bool { return true }

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
			switch e.kind {
			case entryToggleSplash:
				return v.toggleSplash()
			case entryForgetKeyring:
				modal := NewConfirmModal("Forget keyring passphrase",
					"Remove the saved passphrase from the OS keyring and disable keyring lookup?\n"+
						"The repository passphrase itself is unchanged — you will be prompted for it.",
					settingsForgetConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
			}
			return v, func() tea.Msg { return activateMsg{id: e.targetID} }
		}
		return v, nil

	case confirmedMsg:
		if msg.id == settingsForgetConfirmID {
			return v.forgetKeyring()
		}
		return v, nil
	}
	return v, nil
}

// forgetKeyring is the TUI face of `sentra password forget`: delete the
// OS keyring entry (via the production seam) and persist
// passphrase.use_keyring: false. The repo passphrase itself is never
// touched — this only changes where it is looked up from.
func (v SettingsView) forgetKeyring() (tea.Model, tea.Cmd) {
	if v.deps.Config == nil || v.deps.ConfigPath == "" {
		v.err = "available after setup"
		return v, nil
	}
	if v.deps.DeleteKeyringPassphrase == nil {
		v.err = "keyring access is not wired in this build"
		return v, nil
	}
	if _, err := v.deps.DeleteKeyringPassphrase(v.deps.Config); err != nil {
		v.err = "keyring delete failed: " + err.Error()
		return v, nil
	}
	if err := config.Update(v.deps.ConfigPath, func(c *config.Config) error {
		c.Passphrase.UseKeyring = false
		return nil
	}); err != nil {
		v.err = "could not save: " + err.Error()
		return v, nil
	}
	v.deps.Config.Passphrase.UseKeyring = false
	v.err = ""
	return v, nil
}

func (v SettingsView) View() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("Settings"))
	fmt.Fprintf(&b, "%s\n", v.renderSummary())
	for i, e := range v.entries {
		line := e.label
		if e.kind == entryToggleSplash {
			line = e.label + "   [" + v.splashState() + "]"
		}
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(i == v.cursor, line))
		fmt.Fprintf(&b, "    %s\n", ui.Muted.Render(e.desc))
	}
	if v.err != "" {
		fmt.Fprintf(&b, "\n%s", ui.Danger.Render(v.err))
	}
	fmt.Fprintf(&b, "\n%s", ui.Muted.Render("↑↓ move   ⏎ open / toggle"))
	return b.String()
}

// toggleSplash flips ui.hide_splash and persists it. It only adopts the value
// in memory once the file is on disk — a failed write must never leave the
// process disagreeing with sentra.yaml.
//
// config.Update rewrites hide_splash against the file as it exists on disk, so
// this display-only action can't persist the SENTRA_* overrides that
// deps.Config carries. Writing deps.Config wholesale used to rewrite the
// operator's bucket with whatever the environment happened to say.
//
// The value written negates the *resolved* state, which is what the row label
// shows: under SENTRA_UI__HIDE_SPLASH the file and the display disagree, and
// negating the file's value would leave the toggle visibly stuck.
func (v SettingsView) toggleSplash() (tea.Model, tea.Cmd) {
	if v.deps.Config == nil || v.deps.ConfigPath == "" {
		v.err = "available after setup"
		return v, nil
	}
	next := !v.deps.Config.UI.HideSplash
	err := config.Update(v.deps.ConfigPath, func(c *config.Config) error {
		c.UI.HideSplash = next
		return nil
	})
	if err != nil {
		v.err = "could not save: " + err.Error()
		return v, nil
	}
	v.deps.Config.UI.HideSplash = next
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
		fmt.Fprintf(&b, "  %s  %s\n", ui.Muted.Render(label), val)
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
