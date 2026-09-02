package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/recoverykit"
	"github.com/markgustetic/sentra/internal/ui"
)

type rkStage int

const (
	rkIdle rkStage = iota
	rkRunning
	rkDone
	rkSaving
)

// recoveryKitDoneMsg carries the built kit (and its rendered markdown)
// back to the flow. Build is a READ-ONLY snapshot-list read, so this is
// deliberately NOT an opResultMsg — it must never take the mutating-op
// guard and can run alongside a backup.
type recoveryKitDoneMsg struct {
	markdown string
	err      error
}

// RecoveryKitView renders a non-secret recovery kit. Building it lists
// snapshots (a repo with many manifests can take a moment), so the build
// runs asynchronously behind a spinner like the Check and Diff views.
// The kit contains only non-secret identity/storage/snapshot data — the
// renderer in internal/recoverykit guarantees no passphrase, wrapped key,
// salt, or MAC ever appears.
type RecoveryKitView struct {
	deps  Deps
	stage rkStage
	spin  spinner.Model
	vp    viewport.Model
	err   string

	// markdown is the full rendered kit, retained verbatim. The viewport
	// gets the same string via SetContent, but vp.View() returns only the
	// visible, clipped, space-padded window — so the save path writes this
	// raw copy, never vp.View(), to avoid persisting a truncated artifact.
	markdown string

	// save sub-action state.
	savePath textinput.Model
	saveErr  string
	saved    string // last successfully written path (shown on done)

	width  int
	height int
}

func NewRecoveryKitView(deps Deps) RecoveryKitView {
	s := spinner.New()
	s.Spinner = spinner.Dot

	// Height is generous (not 12, unlike agent.go's streaming pane) because
	// a rendered kit is a fixed ~30-line document, not an open-ended log —
	// the goal is "whole kit visible by default", with WindowSizeMsg still
	// free to grow it further on resize. A too-short default would clip the
	// Recovery Commands section off the bottom before any resize event.
	vp := viewport.New(80, 40)
	vp.SetContent("")

	ti := textinput.New()
	ti.Prompt = "save to> "
	ti.Placeholder = "path/to/recovery-kit.md"

	return RecoveryKitView{deps: deps, spin: s, vp: vp, savePath: ti}
}

func (RecoveryKitView) Init() tea.Cmd { return nil }

// ConsumesArrows: only once the kit is rendered, where up/down scroll the
// preview viewport.
func (v RecoveryKitView) ConsumesArrows() bool { return v.stage == rkDone }

func (v RecoveryKitView) Title() string { return "Recovery Kit" }

// CapturesText is true only on the saving stage, where the save-path text input
// is focused — a file path may contain digits or 'q'. The idle/running/done
// stages take single-key commands (enter to build, s to save, scroll keys), so
// they keep the shell globals.
func (v RecoveryKitView) CapturesText() bool { return v.stage == rkSaving }

// ConsumesEscape: esc abandons the save-path prompt.
func (v RecoveryKitView) ConsumesEscape() bool { return v.stage == rkSaving }

func (v RecoveryKitView) ShortHelp() []key.Binding {
	switch v.stage {
	case rkRunning:
		return nil
	case rkDone:
		return []key.Binding{
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "rebuild")),
		}
	case rkSaving:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "write")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "build kit"))}
	}
}

func (v RecoveryKitView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.vp.Width = max(40, msg.Width-2)
		v.vp.Height = max(6, msg.Height-6)
		return v, nil

	case recoveryKitDoneMsg:
		v.stage = rkDone
		if msg.err != nil {
			v.err = msg.err.Error()
			return v, nil
		}
		v.err = ""
		v.markdown = msg.markdown
		v.vp.SetContent(msg.markdown)
		v.vp.GotoTop()
		return v, nil

	case spinner.TickMsg:
		if v.stage == rkRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case cursor.BlinkMsg:
		if v.savePath.Focused() {
			var cmd tea.Cmd
			v.savePath, cmd = v.savePath.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v RecoveryKitView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case rkSaving:
		switch msg.Type {
		case tea.KeyEsc:
			v.stage = rkDone
			v.saveErr = ""
			// Leaving the stage must blur the field. A focused field that
			// is no longer rendered keeps rescheduling its blink forever,
			// and — since the box is drawn from Focused() — it would come
			// back framed but unreachable if the stage were re-entered.
			v.savePath.Blur()
			return v, nil
		case tea.KeyEnter:
			return v.writeKit()
		}
		var cmd tea.Cmd
		v.savePath, cmd = v.savePath.Update(msg)
		v.saveErr = ""
		return v, cmd

	case rkDone:
		switch {
		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's':
			v.stage = rkSaving
			v.saveErr = ""
			v.savePath.SetValue("")
			// Focus()'s own return is the real, tag-matched blink cmd; the
			// dead-end textinput.Blink sentinel is never used. Sequenced
			// rather than inlined into the return: Focus() mutates
			// v.savePath, and the order in which a return copies v versus
			// evaluates the call is unspecified.
			cmd := v.savePath.Focus()
			return v, cmd
		case msg.Type == tea.KeyEnter:
			return v.startBuild()
		}
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(msg) // scroll keys
		return v, cmd

	default: // rkIdle
		if msg.Type == tea.KeyEnter && v.deps.Repo != nil {
			return v.startBuild()
		}
		return v, nil
	}
}

// startBuild moves to the running stage and launches recoverykit.Build in
// a plain goroutine (read-only, so no op guard), batched with the spinner
// tick — the same shape as CheckView.
func (v RecoveryKitView) startBuild() (tea.Model, tea.Cmd) {
	if v.deps.Repo == nil {
		return v, nil
	}
	v.stage = rkRunning
	v.saved = ""
	r := v.deps.Repo
	cfg := v.deps.Config
	if cfg == nil {
		// recoverykit.Build unconditionally reads cfg.Repo.S3.* — an
		// unconfigured/test Deps (nil Config, same as PruneView/BackupView's
		// "may be nil" contract) still must produce a kit, just with empty
		// storage fields (RenderMarkdown's dash helper prints those as "-").
		cfg = &config.Config{}
	}
	cfgPath := v.deps.ConfigPath
	if cfgPath == "" {
		cfgPath = "sentra.yaml"
	}
	ctx := ctxOrBackground(v.deps.Ctx)
	run := func() tea.Msg {
		kit, err := recoverykit.Build(ctx, r, cfg, cfgPath)
		if err != nil {
			return recoveryKitDoneMsg{err: err}
		}
		return recoveryKitDoneMsg{markdown: recoverykit.RenderMarkdown(kit)}
	}
	return v, tea.Batch(v.spin.Tick, run)
}

// writeKit persists the rendered markdown to the typed path at 0o600.
// On failure it stays in the saving stage with an error banner (mirroring
// restore.go's in-view notices) rather than pushing a modal — the save is
// a view-local action, not an App-guarded operation.
func (v RecoveryKitView) writeKit() (tea.Model, tea.Cmd) {
	path := strings.TrimSpace(v.savePath.Value())
	if path == "" {
		v.saveErr = "destination path is required"
		return v, nil
	}
	if err := os.WriteFile(path, []byte(v.markdown), 0o600); err != nil {
		v.saveErr = err.Error()
		return v, nil
	}
	v.saved = path
	v.saveErr = ""
	v.stage = rkDone
	// Same reason as the esc exit: this is the other way out of rkSaving,
	// so it owes the same blur.
	v.savePath.Blur()
	return v, nil
}

func (v RecoveryKitView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	switch v.stage {
	case rkRunning:
		return v.spin.View() + " building recovery kit…"
	case rkDone, rkSaving:
		return v.renderKit()
	default:
		return ui.Primary.Render("Recovery kit") + "\n\n" +
			ui.Muted.Render("Build a non-secret record of this repo's identity, storage, and latest snapshot.") +
			"\n\n" + ui.ActionLine("build the recovery kit", "")
	}
}

func (v RecoveryKitView) renderKit() string {
	if v.err != "" {
		return ui.Danger.Render("Recovery kit failed") + "\n\n" + v.err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("Recovery kit"))
	b.WriteString(v.vp.View())
	if v.stage == rkSaving {
		// The box IS the focus affordance: the save-path field renders it
		// only while this stage — the only stage where it's focused — holds.
		fmt.Fprintf(&b, "\n\n%s", ui.FieldBox.Render(v.savePath.View()))
		if v.saveErr != "" {
			fmt.Fprintf(&b, "\n%s", ui.Danger.Render(v.saveErr))
		}
		fmt.Fprintf(&b, "\n%s", ui.ActionLine("write the kit to disk", "esc cancel"))
		return b.String()
	}
	if v.saved != "" {
		fmt.Fprintf(&b, "\n\n%s%s", ui.Success.Render("Saved: "), v.saved)
	}
	fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("s save · ⏎ rebuild"))
	return b.String()
}
