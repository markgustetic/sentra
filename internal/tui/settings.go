package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
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

// Op names for the two guarded mutations, distinct so an opRejectedMsg
// bounces exactly the one that was refused.
const (
	settingsSplashOpName = "settings-splash"
	settingsForgetOpName = "settings-forget"
)

// settingsSavedMsg is the guard-clearing result of a settings mutation.
// apply mirrors the persisted change into the resolved config on the UI
// goroutine — only after the file is on disk, so a failed write never
// leaves the process disagreeing with sentra.yaml. It is nil on error.
type settingsSavedMsg struct {
	op    string
	apply func(cfg *config.Config)
	err   error
}

func (settingsSavedMsg) opResult() {}

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

	// busy is set while a guarded mutation runs: enter is ignored until
	// the result (or a rejection) clears it, and spin draws beside the row.
	busy   bool
	busyOp string
	spin   spinner.Model
}

func NewSettingsView(deps Deps) SettingsView {
	spin := spinner.New()
	spin.Spinner = spinner.Dot
	return SettingsView{
		deps: deps,
		spin: spin,
		entries: []settingsEntry{
			// The management views live behind Settings rather than on the
			// rail: they are configured rarely, and each rail slot they held
			// taxed the daily backup/snapshot/restore loop. Every demoted
			// view keeps a navigate entry here — hidden from the rail must
			// never mean unreachable. Scheduled backups (jobs) is the
			// exception: it moved onto the rail directly under Backup, so it
			// no longer needs a Settings launcher.
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
			if v.busy {
				return v, nil
			}
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
		if msg.id == settingsForgetConfirmID && !v.busy {
			return v.forgetKeyring()
		}
		return v, nil

	case settingsSavedMsg:
		if !v.busy || msg.op != v.busyOp {
			return v, nil
		}
		v.busy, v.busyOp = false, ""
		if msg.err != nil {
			v.err = msg.err.Error()
			return v, nil
		}
		msg.apply(v.deps.Config)
		v.err = ""
		return v, nil

	case opRejectedMsg:
		if v.busy && msg.name == v.busyOp {
			v.busy, v.busyOp = false, ""
			v.err = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case spinner.TickMsg:
		if !v.busy {
			return v, nil
		}
		var cmd tea.Cmd
		v.spin, cmd = v.spin.Update(msg)
		return v, cmd
	}
	return v, nil
}

// startOp marks the view busy and hands run to the App's one-op guard.
// Both settings mutations rewrite sentra.yaml (and forget talks to the OS
// keyring); inline in Update they blocked the UI goroutine and ran
// unserialized against every other config writer. The spinner's tick is
// batched with the start so the first frame moves.
func (v SettingsView) startOp(op string, run func(ctx context.Context) tea.Msg) (tea.Model, tea.Cmd) {
	v.busy, v.busyOp = true, op
	v.err = ""
	start := startOpMsg{name: op, run: run}
	return v, tea.Batch(func() tea.Msg { return start }, v.spin.Tick)
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
	del, cfg, path := v.deps.DeleteKeyringPassphrase, v.deps.Config, v.deps.ConfigPath
	return v.startOp(settingsForgetOpName, func(context.Context) tea.Msg {
		if _, err := del(cfg); err != nil {
			return settingsSavedMsg{op: settingsForgetOpName, err: fmt.Errorf("keyring delete failed: %w", err)}
		}
		if err := config.Update(path, func(c *config.Config) error {
			c.Passphrase.UseKeyring = false
			return nil
		}); err != nil {
			return settingsSavedMsg{op: settingsForgetOpName, err: fmt.Errorf("could not save: %w", err)}
		}
		return settingsSavedMsg{op: settingsForgetOpName, apply: func(c *config.Config) { c.Passphrase.UseKeyring = false }}
	})
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
		row := ui.SelectRow(i == v.cursor, line)
		if v.busy && i == v.cursor {
			row += "  " + v.spin.View()
		}
		fmt.Fprintf(&b, "%s\n", row)
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
	path := v.deps.ConfigPath
	return v.startOp(settingsSplashOpName, func(context.Context) tea.Msg {
		err := config.Update(path, func(c *config.Config) error {
			c.UI.HideSplash = next
			return nil
		})
		if err != nil {
			return settingsSavedMsg{op: settingsSplashOpName, err: fmt.Errorf("could not save: %w", err)}
		}
		return settingsSavedMsg{op: settingsSplashOpName, apply: func(c *config.Config) { c.UI.HideSplash = next }}
	})
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
