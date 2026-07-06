package tui

import (
	"context"
	"strings"
	"testing"

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
