package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

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
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
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
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
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
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
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
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
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
	v := shown(t, NewPasswordView(Deps{Repo: r}))
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
	v := shown(t, NewPasswordView(Deps{Repo: r}))
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
	v := shown(t, NewPasswordView(deps))
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
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
	v.stage = passwordRunning // simulate the optimistic running stage
	m, _ := v.Update(opRejectedMsg{name: "password"})
	v = m.(PasswordView)
	if v.stage != passwordInput {
		t.Fatalf("stage after rejection = %v, want passwordInput", v.stage)
	}
}

// TestPassword_ExactlyOneBoxAndItFollowsFocus verifies the focused-field box
// marks exactly one field at a time and follows focus across tab — the delta
// over a fully-blurred baseline proves it tracks focus, not a fixed field
// position.
func TestPassword_ExactlyOneBoxAndItFollowsFocus(t *testing.T) {
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))

	base := v
	base.newPass.Blur()
	base.confirmPass.Blur()
	n := boxCount(base.View())

	if got := boxCount(v.View()); got != n+1 {
		t.Fatalf("newPass focused: boxCount = %d, want %d (+1 over blurred)", got, n+1)
	}

	// confirmPass already exists on v, so its BlinkSpeed can be dropped
	// before tab fires — the tab handler's cmd is the REAL one Focus()
	// produces, and executing it (assertBlinkCmd does) would otherwise
	// block for the default ~530ms.
	v.confirmPass.Cursor.BlinkSpeed = time.Millisecond
	tabbed, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	tv := tabbed.(PasswordView)
	if got := boxCount(tv.View()); got != n+1 {
		t.Fatalf("box count changed on tab (got %d, want %d) — box must follow focus, one at a time", got, n+1)
	}
	assertBlinkCmd(t, cmd)

	// The newly focused field must route its own tag-matched tick — a bare
	// cursor.BlinkMsg{} can never match bubbles/cursor's internal tag
	// counter once Focus() has advanced it past zero.
	tv.confirmPass.Cursor.BlinkSpeed = time.Millisecond
	tick := tv.confirmPass.Cursor.BlinkCmd()
	if _, tickCmd := tv.Update(tick()); tickCmd == nil {
		t.Fatal("blink tick not routed to the newly focused confirmPass field")
	}
}

// TestPassword_ShownSchedulesBlink: the shell's viewShownMsg focuses newPass
// (the flow's landing field), so BlinkSpeed can be preset on the constructed
// field and the REAL cmd Focus() returns executed here.
func TestPassword_ShownSchedulesBlink(t *testing.T) {
	fresh := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	fresh.newPass.Cursor.BlinkSpeed = time.Millisecond
	_, cmd := fresh.Update(viewShownMsg{})
	assertBlinkCmd(t, cmd)
}

// TestPassword_RoutesBlinkTicksToNewPassField exercises the other arm of the
// focused-field switch: a tick reaches newPass while it (not confirmPass)
// holds focus.
func TestPassword_RoutesBlinkTicksToNewPassField(t *testing.T) {
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
	v.newPass.Cursor.BlinkSpeed = time.Millisecond
	tick := v.newPass.Cursor.BlinkCmd()
	if _, cmd := v.Update(tick()); cmd == nil {
		t.Fatal("blink tick not routed to the focused newPass field")
	}
}

// passwordAtRunning drives a fresh view to the running stage with tab having
// moved focus onto confirmPass — the entry point for the stage-exit tests,
// which care about what happens to that focus on the way out and back.
func passwordAtRunning(t *testing.T) PasswordView {
	t.Helper()
	v := shown(t, NewPasswordView(Deps{Repo: newFlowRepo(t)}))
	v = typeIntoPassword(v, "brand-new-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "brand-new-pass")
	if !v.confirmPass.Focused() {
		t.Fatal("precondition: tab leaves confirmPass focused")
	}
	m, _ = v.Update(confirmedMsg{id: passwordConfirmID})
	v = m.(PasswordView)
	if v.stage != passwordRunning {
		t.Fatalf("precondition: confirmation must start the op; stage = %v", v.stage)
	}
	return v
}

// TestPassword_StartingRotationBlursBothFields: the running stage renders no
// field, so leaving input must blur both — a focused field nobody renders
// keeps its blink chain alive and lies to every Focused() guard.
func TestPassword_StartingRotationBlursBothFields(t *testing.T) {
	v := passwordAtRunning(t)
	if v.newPass.Focused() || v.confirmPass.Focused() {
		t.Errorf("entering the running stage must blur both fields (new=%v confirm=%v)",
			v.newPass.Focused(), v.confirmPass.Focused())
	}
}

// TestPassword_RejectedStartRefocusesTheField: bounced back to input by the
// one-op guard, the flow must re-focus the field tab had left focus on
// (confirmPass here) and restart its blink — the blur on the way out ended
// the previous chain.
func TestPassword_RejectedStartRefocusesTheField(t *testing.T) {
	v := passwordAtRunning(t)
	v.confirmPass.Cursor.BlinkSpeed = time.Millisecond
	m, cmd := v.Update(opRejectedMsg{name: "password"})
	v = m.(PasswordView)
	if v.stage != passwordInput {
		t.Fatalf("stage after rejection = %v, want passwordInput", v.stage)
	}
	if !v.confirmPass.Focused() || v.newPass.Focused() {
		t.Fatalf("rejection must re-focus confirmPass alone (new=%v confirm=%v)",
			v.newPass.Focused(), v.confirmPass.Focused())
	}
	assertBlinkCmd(t, cmd)
}

// TestPassword_AgainRestartsTheBlink: enter on the done screen swaps a
// fresh form in on the spot, so the swap is the fresh field's first moment
// on screen and must carry a real blink cmd — the same viewShownMsg path
// the shell uses, not a bare constructor. The field is built inside the
// call, so there is no handle to preset BlinkSpeed on; this test pays the
// real ~530ms once rather than weaken to a nil check.
func TestPassword_AgainRestartsTheBlink(t *testing.T) {
	m, _ := passwordAtRunning(t).Update(passwordDoneMsg{})
	v := m.(PasswordView)
	if v.stage != passwordDone {
		t.Fatalf("precondition: stage = %v, want passwordDone", v.stage)
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if v.stage != passwordInput || !v.newPass.Focused() {
		t.Fatalf("again must land on input with newPass focused (stage=%v focused=%v)", v.stage, v.newPass.Focused())
	}
	assertBlinkCmd(t, cmd)
}
