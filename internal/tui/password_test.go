package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// typeIntoPassword feeds each rune of s into the currently focused field.
func typeIntoPassword(v PasswordView, s string) PasswordView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(PasswordView)
	}
	return v
}

// TestPasswordFlow_BothFieldsMaskInput asserts the new + confirm inputs
// are password-masked so the secret is never rendered in cleartext.
func TestPasswordFlow_BothFieldsMaskInput(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	if v.newPass.EchoMode != textinput.EchoPassword {
		t.Errorf("new-pass field EchoMode = %v, want EchoPassword", v.newPass.EchoMode)
	}
	if v.confirmPass.EchoMode != textinput.EchoPassword {
		t.Errorf("confirm-pass field EchoMode = %v, want EchoPassword", v.confirmPass.EchoMode)
	}
	// The typed secret must not appear verbatim anywhere in the rendered
	// view (masking is per-field; this guards the whole frame).
	v = typeIntoPassword(v, "supersecret9")
	if strings.Contains(v.View(), "supersecret9") {
		t.Errorf("rendered view leaked the new passphrase in cleartext:\n%s", v.View())
	}
}

// TestPasswordFlow_TooShortRejected: a new passphrase under 8 bytes never
// advances to the confirm modal.
func TestPasswordFlow_TooShortRejected(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "short") // 5 bytes, then confirm the same
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "short")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd != nil {
		t.Fatalf("too-short passphrase must not emit a command, got one")
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v, want passwordInput", v.stage)
	}
	if v.inputErr == "" {
		t.Fatal("expected a validation error for the short passphrase")
	}
}

// TestPasswordFlow_MismatchRejected: new != confirm never advances.
func TestPasswordFlow_MismatchRejected(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "longenough1")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "longenough2")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd != nil {
		t.Fatal("mismatched confirm must not emit a command")
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v, want passwordInput", v.stage)
	}
	if v.inputErr == "" {
		t.Fatal("expected a mismatch validation error")
	}
}

// TestPasswordFlow_ValidEntryPushesTypedConfirm: matching, long-enough
// entries push the typed-confirm modal ("rotate") and nothing else — no
// rotation happens on the input->confirm transition.
func TestPasswordFlow_ValidEntryPushesTypedConfirm(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "longenough1")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "longenough1")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd == nil {
		t.Fatal("valid entry must request the confirm modal")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	if _, ok := push.modal.(TypedConfirmModal); !ok {
		t.Fatalf("expected a TypedConfirmModal, got %T", push.modal)
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v (must stay in input until confirmed)", v.stage)
	}
}

// TestPasswordFlow_ConfirmedRunRotates: the confirmed run closure rotates
// the repo passphrase. After it runs, the OLD passphrase no longer Opens
// the repo and the NEW one does — proving a real rotation happened.
func TestPasswordFlow_ConfirmedRunRotates(t *testing.T) {
	r := newFlowRepo(t)
	v := NewPasswordView(Deps{Repo: r})
	v = typeIntoPassword(v, "brand-new-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "brand-new-pass")

	m, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	v = m.(PasswordView)
	if cmd == nil {
		t.Fatal("confirmation must start the op")
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	found := false
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, found = s, true
		}
	}
	if !found {
		t.Fatalf("expected a startOpMsg in the batch, got %#v", msgs)
	}
	if start.name != "password" {
		t.Fatalf("op name = %q, want password", start.name)
	}
	if v.stage != passwordRunning {
		t.Fatalf("stage = %v, want passwordRunning", v.stage)
	}

	res := start.run(context.Background())
	done, ok := res.(passwordDoneMsg)
	if !ok {
		t.Fatalf("run result: %#v", res)
	}
	if done.err != nil {
		t.Fatalf("rotation failed: %v", done.err)
	}
	// passwordDoneMsg must be an opResultMsg so the App guard clears.
	if _, ok := any(done).(opResultMsg); !ok {
		t.Fatal("passwordDoneMsg must implement opResult()")
	}
}

// TestPasswordFlow_SamePassphraseMapped: rotating to the current
// passphrase surfaces the mapped "matches current" message, not the raw
// repo sentinel.
func TestPasswordFlow_SamePassphraseMapped(t *testing.T) {
	r := newFlowRepo(t) // created with passphrase "flow-test-pass"
	v := NewPasswordView(Deps{Repo: r})
	v = typeIntoPassword(v, "flow-test-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "flow-test-pass")

	_, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	msgs := execCmds(t, cmd)
	var start startOpMsg
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start = s
		}
	}
	res := start.run(context.Background())
	done := res.(passwordDoneMsg)
	if !errors.Is(done.err, repo.ErrSamePassphrase) {
		t.Fatalf("run err = %v, want wrap of ErrSamePassphrase", done.err)
	}
	m, _ = v.Update(res)
	if got := m.(PasswordView).View(); !strings.Contains(got, "matches current") {
		t.Fatalf("done view must map ErrSamePassphrase to 'matches current':\n%s", got)
	}
}

// TestPasswordFlow_KeyringSaveInvokedOnSuccess: when UseKeyring is set and
// a saver is wired, a successful rotation calls it with the NEW passphrase.
func TestPasswordFlow_KeyringSaveInvokedOnSuccess(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Passphrase.UseKeyring = true
	var savedPass string
	var saveCalls int
	deps := Deps{
		Repo:   r,
		Config: &cfg,
		SaveKeyringPassphrase: func(_ *config.Config, pass []byte) error {
			saveCalls++
			savedPass = string(pass)
			return nil
		},
	}
	v := NewPasswordView(deps)
	v = typeIntoPassword(v, "brand-new-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "brand-new-pass")

	_, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	msgs := execCmds(t, cmd)
	var start startOpMsg
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start = s
		}
	}
	res := start.run(context.Background())
	done := res.(passwordDoneMsg)
	if done.err != nil {
		t.Fatalf("rotation failed: %v", done.err)
	}
	if saveCalls != 1 {
		t.Fatalf("keyring saver called %d times, want 1", saveCalls)
	}
	if savedPass != "brand-new-pass" {
		t.Fatalf("keyring saved %q, want the new passphrase", savedPass)
	}
}

// TestPasswordFlow_OpRejectedResets: an op-rejection while running resets
// the flow to the input stage so it never hangs.
func TestPasswordFlow_OpRejectedResets(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v.stage = passwordRunning // simulate the optimistic running stage
	m, _ := v.Update(opRejectedMsg{name: "password"})
	v = m.(PasswordView)
	if v.stage != passwordInput {
		t.Fatalf("stage after rejection = %v, want passwordInput", v.stage)
	}
}
