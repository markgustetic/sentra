package tui

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// minPasswordLen mirrors the operational floor the CLI enforces on the new
// passphrase. Below this we refuse to advance to the confirm modal.
const minPasswordLen = 8

// passwordConfirmID ties the typed-confirm modal result back to this flow.
const passwordConfirmID = "password-rotate"

type passwordStage int

const (
	passwordInput passwordStage = iota
	passwordRunning
	passwordDone
)

// passwordDoneMsg is the flow's terminal message. Rotation is a mutating
// operation that takes the repo advisory lock, so it implements
// opResultMsg — the App guard clears on it just like backup/prune.
type passwordDoneMsg struct {
	// keyringSaved reports the keyring entry was re-saved with the new
	// secret (only when UseKeyring + a saver were wired).
	keyringSaved bool
	err          error
}

func (passwordDoneMsg) opResult() {}

// PasswordView rotates the repository passphrase. Flow:
//
//	input   → two masked fields (new + confirm). Enter validates length
//	          and equality, then pushes the typed-confirm modal.
//	confirm → TypedConfirmModal (type "rotate"); its confirmedMsg starts
//	          the op. This is destructive/irreversible — the old
//	          passphrase stops working and there is no recovery path — so
//	          it uses the TYPED confirm, matching prune's gate.
//	running → the App-managed op goroutine calls repo.Passwd under the
//	          meta/lock; on success it re-saves the keyring entry.
//	done    → result / mapped error.
//
// Security: the typed secret lives only in the textinput buffers and the
// derived run-closure copy, is masked in every rendered frame, is never
// logged, and the run closure zeroizes its copy on return.
type PasswordView struct {
	deps  Deps
	stage passwordStage

	newPass      textinput.Model
	confirmPass  textinput.Model
	focusConfirm bool

	inputErr string
	notice   string // transient banner, e.g. after an op rejection

	// initBlink is the cmd newField.Focus() returned at construction,
	// captured so Init can return it. Focus() (not textinput.Blink) is the
	// only source of a REAL, tag-matched blink cmd — see unlock.go's
	// initBlink doc comment for why the bootstrap sentinel is a dead end
	// and why this can't be recomputed inside a value-receiver Init.
	initBlink tea.Cmd

	result passwordDoneMsg
	width  int
}

func NewPasswordView(deps Deps) PasswordView {
	newField := textinput.New()
	newField.Prompt = "new>     "
	newField.Placeholder = "new passphrase"
	newField.EchoMode = textinput.EchoPassword
	newField.EchoCharacter = '•'
	cmd := newField.Focus()

	confirmField := textinput.New()
	confirmField.Prompt = "confirm> "
	confirmField.Placeholder = "retype new passphrase"
	confirmField.EchoMode = textinput.EchoPassword
	confirmField.EchoCharacter = '•'

	return PasswordView{deps: deps, newPass: newField, confirmPass: confirmField, initBlink: cmd}
}

// Init starts the cursor blinking. newPass is constructed already focused
// (NewPasswordView) and this is the flow's landing state — there is no
// later Focus() transition to hang the blink on, so Init carries it, same
// as unlock's.
func (v PasswordView) Init() tea.Cmd { return v.initBlink }

func (v PasswordView) Title() string { return "Password" }

// focusField focuses the one input focusConfirm names, blurs the other, and
// returns Focus()'s blink cmd. Every path that puts the keyboard on the
// input stage goes through here — tab, and the one-op guard bouncing a
// start back — so the flag and the fields' Focused() cannot disagree.
func (v *PasswordView) focusField() tea.Cmd {
	v.newPass.Blur()
	v.confirmPass.Blur()
	if v.focusConfirm {
		return v.confirmPass.Focus()
	}
	return v.newPass.Focus()
}

// blurFields blurs both inputs. The running and done stages render neither,
// and a focused field nobody renders keeps its blink chain alive while
// Focused() lies to every guard that reads it.
func (v *PasswordView) blurFields() {
	v.newPass.Blur()
	v.confirmPass.Blur()
}

// CapturesText is true only on the input stage, where the masked new/confirm
// passphrase fields are focused — a rotated passphrase may contain 'q', digits,
// or 'A', so those runes must reach the field, not the shell's globals. The
// running/done stages have no input.
func (v PasswordView) CapturesText() bool { return v.stage == passwordInput }

func (v PasswordView) ShortHelp() []key.Binding {
	switch v.stage {
	case passwordRunning:
		return nil
	case passwordDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "rotate…")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		}
	}
}

func (v PasswordView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case passwordDoneMsg:
		v.stage = passwordDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our start was refused (another op holds the guard). Leave the
		// optimistic running stage so the flow doesn't hang forever.
		if v.stage == passwordRunning && msg.name == "password" {
			v.stage = passwordInput
			v.notice = "another operation is in progress — try again when it finishes"
			// startRotate blurred both fields on the way out; landing back
			// on input re-focuses the one tab had left focus on.
			cmd := v.focusField()
			return v, cmd
		}
		return v, nil

	case confirmedMsg:
		if msg.id != passwordConfirmID || v.stage != passwordInput {
			return v, nil
		}
		v.notice = ""
		return v.startRotate()

	case cursor.BlinkMsg:
		// Exactly one of newPass/confirmPass is focused at a time (tab
		// swaps which); route the tick to whichever that is.
		var cmd tea.Cmd
		switch {
		case v.newPass.Focused():
			v.newPass, cmd = v.newPass.Update(msg)
		case v.confirmPass.Focused():
			v.confirmPass, cmd = v.confirmPass.Update(msg)
		}
		return v, cmd

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v PasswordView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case passwordRunning:
		return v, nil

	case passwordDone:
		if msg.Type == tea.KeyEnter {
			fresh := NewPasswordView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil

	default: // passwordInput
		switch msg.Type {
		case tea.KeyTab:
			v.focusConfirm = !v.focusConfirm
			cmd := v.focusField()
			return v, cmd
		case tea.KeyEnter:
			return v.requestConfirm()
		}
		var cmd tea.Cmd
		if v.focusConfirm {
			v.confirmPass, cmd = v.confirmPass.Update(msg)
		} else {
			v.newPass, cmd = v.newPass.Update(msg)
		}
		v.inputErr = "" // typing clears the last validation error
		v.notice = ""
		return v, cmd
	}
}

// requestConfirm validates the two entries and, if they pass, pushes the
// typed-confirm modal. It never starts the rotation itself — the modal's
// confirmedMsg does, so the destructive step is always gated.
func (v PasswordView) requestConfirm() (tea.Model, tea.Cmd) {
	// These are throwaway copies made only to length-check and compare the
	// two entries; wipe them on return so the validation step doesn't widen
	// the plaintext-residency window beyond startRotate's single zeroized copy.
	newVal := []byte(v.newPass.Value())
	confVal := []byte(v.confirmPass.Value())
	defer crypto.Zeroize(newVal)
	defer crypto.Zeroize(confVal)
	if len(newVal) < minPasswordLen {
		v.inputErr = fmt.Sprintf("new passphrase must be at least %d characters", minPasswordLen)
		return v, nil
	}
	// Constant-time equality: the two values are secrets typed by the
	// operator, and comparing them in constant time avoids a length /
	// content timing side channel on the confirm step.
	if subtle.ConstantTimeCompare(newVal, confVal) != 1 {
		v.inputErr = "passphrases do not match"
		return v, nil
	}
	v.inputErr = ""
	body := "Rotating rewrites the encrypted config so a NEW passphrase wraps the repo key.\n" +
		"The OLD passphrase stops working immediately and there is no recovery if the new one is lost.\n" +
		"Existing snapshots stay readable."
	modal := NewTypedConfirmModal("Confirm passphrase rotation", body, "rotate", passwordConfirmID, 80, 24)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startRotate builds the mutating-op start. The run closure holds the
// ONLY long-lived copy of the new secret and zeroizes it on return; the
// rotation itself serializes on the repo meta/lock inside repo.Passwd.
func (v PasswordView) startRotate() (tea.Model, tea.Cmd) {
	v.stage = passwordRunning
	v.blurFields()
	r := v.deps.Repo
	cfg := v.deps.Config
	saveKeyring := v.deps.SaveKeyringPassphrase
	// Copy the secret out of the input buffer for the goroutine; the copy
	// is zeroized in the closure's defer.
	newPass := []byte(v.newPass.Value())

	start := startOpMsg{
		name: "password",
		run: func(ctx context.Context) tea.Msg {
			defer crypto.Zeroize(newPass)
			if r == nil {
				return passwordDoneMsg{err: errors.New("no repository configured")}
			}
			if err := r.Passwd(ctx, newPass); err != nil {
				return passwordDoneMsg{err: err}
			}
			// Rotation succeeded. Only now touch the keyring, so a failed
			// rotation never leaves it stale (mirrors cli/passwd.go).
			saved := false
			if cfg != nil && cfg.Passphrase.UseKeyring && saveKeyring != nil {
				if err := saveKeyring(cfg, newPass); err != nil {
					return passwordDoneMsg{err: fmt.Errorf("passphrase rotated, but keyring update failed: %w", err)}
				}
				saved = true
			}
			return passwordDoneMsg{keyringSaved: saved}
		},
	}
	return v, func() tea.Msg { return start }
}

func (v PasswordView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	var b strings.Builder
	switch v.stage {
	case passwordRunning:
		b.WriteString(ui.Primary.Render("Rotating passphrase…"))
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("rewriting the encrypted config under the repo lock"))

	case passwordDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Rotation failed"))
			fmt.Fprintf(&b, "\n\n%s", passwordErrMessage(v.result.err))
		} else {
			b.WriteString(ui.Success.Render("Passphrase rotated"))
			b.WriteString("\n\n  the old passphrase is no longer accepted")
			if v.result.keyringSaved {
				b.WriteString("\n  OS keyring updated with the new passphrase")
			}
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("rotate the passphrase again", ""))

	default: // passwordInput
		b.WriteString(ui.Primary.Render("Rotate repository passphrase"))
		if v.notice != "" {
			fmt.Fprintf(&b, "\n%s", ui.Warn.Render(v.notice))
		}
		fmt.Fprintf(&b, "\n\n%s", boxedField(v.newPass))
		fmt.Fprintf(&b, "\n%s", boxedField(v.confirmPass))
		if v.inputErr != "" {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.inputErr))
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("rotate the passphrase", "typed confirmation required · tab switch field"))
	}
	return b.String()
}

// passwordErrMessage maps the repo sentinels to operator-readable text.
// Distinct sentinels (not string matching) so a message reword upstream
// never silently breaks the mapping.
func passwordErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrSamePassphrase):
		return "new passphrase matches current — nothing to rotate"
	case errors.Is(err, repo.ErrRepoLocked):
		return "another operation is running — try again when it finishes"
	default:
		return err.Error()
	}
}
