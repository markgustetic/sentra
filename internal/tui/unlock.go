package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// unlockStage is the position in the unlock flow's small state machine.
type unlockStage int

const (
	unlockInput   unlockStage = iota // masked entry, awaiting enter
	unlockOpening                    // repo.Open running in a returned cmd
)

// unlockResultMsg carries the outcome of an open attempt back into the view.
// On success the view forwards a repoReadyMsg to the App (which rebuilds the
// shell against the live repo); on failure it shows err and returns to input.
//
// This is NOT an opResultMsg: the unlock path runs before any repo lock exists
// and before the App's one-op guard matters — repo.Open takes no advisory lock.
type unlockResultMsg struct {
	repo   *repo.Repo
	config *config.Config
	err    error
}

// UnlockView is the launch-path gate for a configured-but-locked repo: the
// sentra.yaml exists but no passphrase source (keyring / env / file) could
// supply the secret non-interactively, so we ask for it here with an inline
// masked field rather than a huh form (huh cannot run inside a live Bubbletea
// program — it fights for os.Stdin). On a correct passphrase it opens the repo
// and hands it to the App via repoReadyMsg; a wrong passphrase shows an error
// and lets the user retry.
//
// Security: the typed secret lives only in the textinput buffer and the single
// copy handed to the open closure, which zeroizes it on return. It is masked in
// every frame and never logged. The keyring is NOT written here — this view
// only reads an existing repo; the verify-before-save keyring guard lives in
// the setup engine's InitRepo path (Unit 1/4), not on the unlock path.
type UnlockView struct {
	deps  Deps
	stage unlockStage

	input textinput.Model

	inputErr string // local validation (empty entry)
	openErr  error  // mapped repo.Open failure

	// initBlink is the cmd field.Focus() returned at construction, captured
	// here so Init can return it. Focus() (not textinput.Blink) is the only
	// source of a REAL, tag-matched blink cmd: textinput.Blink resolves to
	// cursor's unexported bootstrap message, which no view's Update switch
	// can name, so it was silently dropped and the blink chain never
	// started in a live terminal. A value-receiver Init can't call Focus()
	// itself — it would mutate a throwaway copy and orphan the tick — so
	// the cmd has to be captured once, right here, against the same model
	// value that ends up live.
	initBlink tea.Cmd

	width int
}

// NewUnlockView builds the masked-entry gate. The single field echoes bullets,
// mirroring password.go's masking discipline.
func NewUnlockView(deps Deps) UnlockView {
	field := textinput.New()
	field.Prompt = "passphrase> "
	field.Placeholder = "repository passphrase"
	field.EchoMode = textinput.EchoPassword
	field.EchoCharacter = '•'
	cmd := field.Focus()
	return UnlockView{deps: deps, input: field, initBlink: cmd}
}

// Init starts the cursor blinking. The unlock field is constructed already
// focused (NewUnlockView) — it's the landing view, not one the operator tabs
// into — so there is no later Focus() transition to hang the blink cmd on;
// Init returns the cmd Focus() produced back at construction (see
// initBlink's doc comment).
func (v UnlockView) Init() tea.Cmd { return v.initBlink }

func (v UnlockView) Title() string { return "Unlock" }

// CapturesText is always true: the unlock gate is a single masked passphrase
// field, so every rune belongs to it (a passphrase may contain 'q', digits, or
// 'A'). Only ctrl+c, handled ahead of view routing, still quits.
func (v UnlockView) CapturesText() bool { return true }

func (v UnlockView) ShortHelp() []key.Binding {
	if v.stage == unlockOpening {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "unlock")),
	}
}

func (v UnlockView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case unlockResultMsg:
		if msg.err != nil {
			v.stage = unlockInput
			v.openErr = msg.err
			// Clear the buffer so the failed secret doesn't linger and the
			// user starts a fresh attempt.
			v.input.SetValue("")
			// Re-focusing for the retry is a second focus transition (the
			// first was at construction, covered by Init) — it must also
			// restart the blink, or the cursor looks dead after a failed
			// attempt. Focus()'s own return is the real, tag-matched cmd;
			// textinput.Blink would be the dead-end bootstrap sentinel.
			// Sequenced rather than inlined into the return: Focus()
			// mutates v.input, and a return's copy-vs-evaluate order is
			// unspecified.
			cmd := v.input.Focus()
			return v, cmd
		}
		// Success: forward to the App, which rebuilds the shell against the
		// live repo and switches to the dashboard.
		ready := repoReadyMsg{repo: msg.repo, config: msg.config}
		return v, func() tea.Msg { return ready }

	case cursor.BlinkMsg:
		if v.input.Focused() {
			var cmd tea.Cmd
			v.input, cmd = v.input.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v UnlockView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.stage == unlockOpening {
		return v, nil
	}
	if msg.Type == tea.KeyEnter {
		return v.startOpen()
	}
	var cmd tea.Cmd
	v.input, cmd = v.input.Update(msg)
	v.inputErr = "" // typing clears the last validation error
	v.openErr = nil
	return v, cmd
}

// startOpen validates a non-empty entry, then returns a command that opens the
// store and the repo. The command holds the ONLY copy of the secret outside the
// input buffer and zeroizes it on return.
func (v UnlockView) startOpen() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(v.input.Value()) == "" {
		v.inputErr = "enter the repository passphrase"
		return v, nil
	}
	v.stage = unlockOpening
	v.openErr = nil
	// The opening stage renders a status line, not the field: blur it so
	// its blink chain ends and Focused() stays truthful. A failed open
	// re-focuses it (unlockResultMsg); a successful one rebuilds the shell.
	v.input.Blur()

	deps := v.deps
	pass := []byte(v.input.Value())

	return v, func() tea.Msg {
		defer crypto.Zeroize(pass)
		if deps.NewStore == nil {
			return unlockResultMsg{err: errors.New("no blobstore configured")}
		}
		ctx := ctxOrBackground(deps.Ctx)
		store, err := deps.NewStore(ctx, deps.Config)
		if err != nil {
			return unlockResultMsg{err: err}
		}
		r, err := repo.Open(ctx, store, pass)
		if err != nil {
			return unlockResultMsg{err: err}
		}
		return unlockResultMsg{repo: r, config: deps.Config}
	}
}

func (v UnlockView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Unlock repository"))
	fmt.Fprintf(&b, "\n%s", ui.Muted.Render(v.deps.RepoName))

	switch v.stage {
	case unlockOpening:
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("opening the repository…"))
	default:
		fmt.Fprintf(&b, "\n\n%s", boxedField(v.input))
		if v.inputErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.inputErr))
		}
		if v.openErr != nil {
			fmt.Fprintf(&b, "\n\n%s", unlockErrMessage(v.openErr))
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("unlock the repository", ""))
	}
	return b.String()
}

// unlockErrMessage maps repo.Open failures to operator-readable, styled
// text. Wrong passphrase is matched by sentinel (not string matching) so an
// upstream reword never silently breaks the mapping; everything else goes
// through humanizeErr, which explains known credential/network causes and
// falls back to the raw chain. The result contains styled fragments — the
// caller must not wrap it in another style.
func unlockErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrWrongPassphrase):
		return ui.Danger.Render("wrong passphrase — try again")
	default:
		return humanizeErr(err)
	}
}
