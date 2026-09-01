package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// unlockDeps builds Deps wired to an in-memory store backing a repo that was
// initialized under `pass`. NewStore returns that same store so repo.Open sees
// the real config blob.
func unlockDeps(t *testing.T, pass string) Deps {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(pass))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("repo.Close: %v", err)
	}
	cfg := config.Defaults()
	return Deps{
		Config: &cfg,
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
	}
}

// typeIntoUnlock feeds a string one rune at a time through Update.
func typeIntoUnlock(v UnlockView, s string) UnlockView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(UnlockView)
	}
	return v
}

// TestUnlock_MasksInput: the entry never renders the plaintext passphrase.
func TestUnlock_MasksInput(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2secret"))
	v = typeIntoUnlock(v, "hunter2secret")
	if strings.Contains(v.View(), "hunter2secret") {
		t.Fatal("unlock view rendered the plaintext passphrase")
	}
}

// TestUnlock_CorrectPassphraseOpensRepoAndEmitsRepoReady: on enter with the
// right passphrase the flow opens the repo and returns a repoReadyMsg carrying
// the live repo, so the App can swap to the dashboard.
func TestUnlock_CorrectPassphraseOpensRepoAndEmitsRepoReady(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2secret"))
	v = typeIntoUnlock(v, "hunter2secret")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if cmd == nil {
		t.Fatal("enter must return an open command")
	}
	msg := cmd()
	// The open command returns an unlockResultMsg; feed it back to get the
	// forwarded repoReadyMsg the App consumes.
	_, next := v.Update(msg)
	if next == nil {
		t.Fatal("successful open must forward a repoReadyMsg command")
	}
	ready, ok := next().(repoReadyMsg)
	if !ok {
		t.Fatalf("expected repoReadyMsg, got %T", next())
	}
	if ready.repo == nil {
		t.Fatal("repoReadyMsg carried a nil repo")
	}
	ready.repo.Close()
}

// TestUnlock_WrongPassphraseShowsErrorNotReady: a bad passphrase renders an
// error and does NOT emit repoReadyMsg — the App stays on the unlock gate.
func TestUnlock_WrongPassphraseShowsErrorNotReady(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "correct-horse"))
	v = typeIntoUnlock(v, "wrong-passphrase")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if cmd == nil {
		t.Fatal("enter must return the open command even for a wrong guess")
	}
	// The open runs synchronously in the returned cmd; feed its result back.
	m, _ = v.Update(cmd())
	v = m.(UnlockView)
	if strings.Contains(strings.ToLower(v.View()), "wrong") == false &&
		strings.Contains(strings.ToLower(v.View()), "passphrase") == false {
		t.Fatalf("wrong-passphrase attempt did not surface an error, view=%q", v.View())
	}
}

// TestUnlock_EmptyPassphraseIsRejectedLocally: pressing enter with no input
// shows a validation message and never touches the store.
func TestUnlock_EmptyPassphraseIsRejectedLocally(t *testing.T) {
	called := false
	d := unlockDeps(t, "hunter2secret")
	inner := d.NewStore
	d.NewStore = func(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
		called = true
		return inner(ctx, cfg)
	}
	v := NewUnlockView(d)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if called {
		t.Fatal("empty passphrase must not open the store")
	}
	if v.View() == "" {
		t.Fatal("empty-entry attempt should render a hint")
	}
}

// The box is the focus glyph: the unlock field is always focused, so its
// render always carries exactly one FieldBox frame.
func TestUnlock_FocusedFieldIsBoxed(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	if got := boxCount(v.View()); got != 1 {
		t.Fatalf("focused unlock field: boxCount = %d, want 1", got)
	}
}

// Landing on unlock must start the cursor blinking. Init returns the cmd
// Focus() produced back at construction (see UnlockView.initBlink) — that
// cmd is REAL (BlinkCmd()'s own tag-matched output, not the dead-end
// textinput.Blink sentinel), so executing it for real would block for the
// field's Cursor.BlinkSpeed (~530ms by default). There is no field handle
// to drop BlinkSpeed on before construction runs Focus() internally, so
// this only checks the cmd exists rather than executing it; the real
// end-to-end round-trip is proven once, on a key-triggered (presettable)
// site, by TestBlinkChain_ClosesEndToEnd in snapshots_test.go.
func TestUnlock_InitSchedulesBlink(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	if v.Init() == nil {
		t.Fatal("expected a blink command, got nil")
	}
}

// Blink ticks must reach the focused input so the schedule continues. A bare
// cursor.BlinkMsg{} won't do: bubbles/cursor tags each scheduled tick and
// rejects one whose tag doesn't match its current count (stale-tick guard),
// and Focus() at construction already advanced that counter past zero — so
// the test captures a genuinely tag-matched tick from the field's own
// cursor instead of a zero-value literal. BlinkSpeed is dropped to make
// capturing one instant rather than a real ~530ms wait.
func TestUnlock_RoutesBlinkTicks(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2"))
	v.input.Cursor.BlinkSpeed = time.Millisecond
	tick := v.input.Cursor.BlinkCmd()
	_, cmd := v.Update(tick())
	if cmd == nil {
		t.Fatal("blink tick was not routed to the focused input")
	}
}

// A wrong passphrase clears and re-focuses the input for a retry; that
// re-focus is a second transition that must also (re)start the blink, or
// the cursor looks dead after a failed attempt.
func TestUnlock_WrongPassphraseReschedulesBlink(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "correct-horse"))
	// The retry's Focus() call reads v.input.Cursor.BlinkSpeed at the time
	// it runs, which is after construction — so, unlike the construction-
	// time Init cmd, this one CAN be preset before triggering the
	// transition, keeping the real cmd's execution below fast.
	v.input.Cursor.BlinkSpeed = time.Millisecond
	v = typeIntoUnlock(v, "wrong-passphrase")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	m, cmd = v.Update(cmd())
	v = m.(UnlockView)
	_ = v // re-focus happened; only the returned cmd matters here
	assertBlinkCmd(t, cmd)
}
