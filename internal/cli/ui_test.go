package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/tui"
)

// uiFixture builds a UIDeps wired to an in-memory repo. The runner
// stub captures the App that would have been launched and exits
// immediately so the test doesn't need a real terminal.
func uiFixture(t *testing.T, passphrase string) (UIDeps, *tui.App) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(passphrase))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte(passphrase), nil },
		},
		Provider: nil, // agent view will show the placeholder
		Run: func(app tui.App) error {
			captured = app
			return nil
		},
	}
	return deps, &captured
}

// TestUI_LaunchesApp verifies that the cobra command opens the repo
// and hands a constructed App to the Run hook. We don't actually
// run a Bubbletea program — that requires a TTY.
func TestUI_LaunchesApp(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	view := captured.View()
	if !strings.Contains(view, "sentra") {
		t.Errorf("captured app's view did not contain brand: %s", view)
	}
}

// TestUI_PropagatesRunError ensures errors from Run() bubble out as
// the cobra command's exit code path.
func TestUI_PropagatesRunError(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _ := uiFixture(t, "hunter2")
	deps.Run = func(_ tui.App) error {
		return errors.New("tea boom")
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error from execute")
	}
	if !strings.Contains(err.Error(), "tea boom") {
		t.Errorf("expected tea boom in error, got %v", err)
	}
}

// TestUI_PassesProviderToApp verifies the Provider deps reach the
// constructed App via tui.Deps. We pass a non-nil llm.Provider and
// assert the captured App's agent view does NOT show the placeholder
// (since a provider is configured).
func TestUI_PassesProviderToApp(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, captured := uiFixture(t, "hunter2")
	deps.Provider = &llm.FakeProvider{}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	// Switch to the agent tab — when a provider is configured the
	// agent placeholder ("ANTHROPIC_API_KEY") should be absent.
	updated, _ := captured.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	view := updated.(tui.App).View()
	if strings.Contains(view, "ANTHROPIC_API_KEY") {
		t.Errorf("agent view showed configure hint despite provider being wired: %s", view)
	}
}

// TestRoot_BareSentraLaunchesUI ensures invoking root with no
// subcommand falls through to the ui command rather than printing
// the help text. We register a tiny ui stub that records its
// invocation; no other subcommands are wired so any non-flag arg
// would be unmatched too — but bare invocation specifically must
// reach our ui handler.
func TestRoot_BareSentraLaunchesUI(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	deps, _ := uiFixture(t, "hunter2")
	uiCalled := false
	deps.Run = func(_ tui.App) error {
		uiCalled = true
		return nil
	}
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewUI(deps))
	SetUIAsDefault(root, deps)
	root.SetOut(&bytes.Buffer{})
	root.SetErr(io.Discard)
	root.SetArgs([]string{})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !uiCalled {
		t.Errorf("bare sentra invocation did not launch ui")
	}
}
