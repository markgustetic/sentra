package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
)

// newTestApp constructs an App with no repo or provider — every view
// must render and react to keys without those external deps. View
// constructors that need data fall back to "empty repo" / "configure
// API key" placeholders so headless tests work.
func newTestApp(t *testing.T) App {
	t.Helper()
	return NewApp(Deps{})
}

// TestApp_StartsOnDashboard locks in the contract that the parent
// model boots into the dashboard view. A user typing `sentra ui` and
// staring at the snapshots table or agent log first would be confusing
// — the dashboard is the home screen.
func TestApp_StartsOnDashboard(t *testing.T) {
	app := newTestApp(t)
	if app.active != ViewDashboard {
		t.Fatalf("active view: got %v, want %v", app.active, ViewDashboard)
	}
	view := app.View()
	// The top bar always shows the brand "sentra" — a cheap smoke test
	// that View() renders something (and not a blank string).
	if !strings.Contains(view, "sentra") {
		t.Errorf("view did not contain brand %q: %s", "sentra", view)
	}
}

// TestApp_SwitchesViewWithKey covers the four view-switch keys plus
// the parent's ability to route them to the underlying view enum.
// Each case sends one synthetic key and asserts on the resulting
// active view; we don't assert on rendered substrings here because
// each individual view has its own tests for content.
func TestApp_SwitchesViewWithKey(t *testing.T) {
	cases := []struct {
		key  string
		want View
	}{
		{"s", ViewSnapshots},
		{"D", ViewDiff},
		{"a", ViewAgent},
		{"d", ViewDashboard},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			app := newTestApp(t)
			// First switch to a non-target view so the test exercises
			// real transitions even when the target is the default.
			updated, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
			a := updated.(App)
			updated, _ = a.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.key)})
			got := updated.(App)
			if got.active != tc.want {
				t.Errorf("after key %q: active = %v, want %v", tc.key, got.active, tc.want)
			}
		})
	}
}

// TestApp_QuitOnQ asserts that pressing `q` returns a tea.QuitMsg via
// the returned tea.Cmd. We invoke the cmd inline to verify the
// resulting message type rather than running a real tea.Program.
func TestApp_QuitOnQ(t *testing.T) {
	app := newTestApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("expected non-nil cmd from `q` keypress")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}

// TestApp_QuitOnCtrlC mirrors TestApp_QuitOnQ for Ctrl+C; both must
// produce a quit so users with either muscle memory can leave cleanly.
func TestApp_QuitOnCtrlC(t *testing.T) {
	app := newTestApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("expected non-nil cmd from Ctrl+C")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Errorf("expected tea.QuitMsg, got %T", cmd())
	}
}

// TestApp_WindowSizeMsg verifies the App stores width/height when a
// WindowSizeMsg comes through. Sub-views need this to size themselves;
// without it, the bubbles/table+viewport components render as 0x0.
func TestApp_WindowSizeMsg(t *testing.T) {
	app := newTestApp(t)
	updated, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	got := updated.(App)
	if got.width != 100 || got.height != 40 {
		t.Errorf("size: got (%d,%d), want (100,40)", got.width, got.height)
	}
}

// TestApp_QuitCancelsAgentScan asserts that the App's quit handler
// invokes AgentView.Cleanup, which cancels any in-flight scan's
// context. Without this, pressing q during an LLM streaming call
// leaks the network round-trip past process exit.
//
// We don't construct a real LLM-backed AgentView; we install a
// runner that blocks on its ctx and observes the cancellation.
func TestApp_QuitCancelsAgentScan(t *testing.T) {
	app := newTestApp(t)

	// Build an AgentView whose runner blocks until its ctx is cancelled,
	// then drive it through Update directly (the 's' start-scan key is
	// intercepted by the App and routed to the snapshots view, so we
	// have to start the scan at the sub-view level).
	cancelled := make(chan struct{}, 1)
	agentView := NewAgentViewWithRunner(Deps{}, func(ctx context.Context, _ chan<- string) ([]agent.Recommendation, error) {
		<-ctx.Done()
		cancelled <- struct{}{}
		return nil, ctx.Err()
	})
	updated, _ := agentView.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	// The goroutine started inside spawnScan and is now blocking on
	// our runner's <-ctx.Done(). We do NOT invoke the returned cmd
	// here — that's the waitForAgentEvent select, which would block
	// the test goroutine waiting for a token that never comes.

	// Install the post-scan-started agent view back into the App and
	// then send the App `q` to trigger cleanup().
	app.agent = updated
	_, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	select {
	case <-cancelled:
		// Runner observed ctx cancellation — good.
	case <-time.After(2 * time.Second):
		t.Fatal("agent runner did not see ctx cancel after q; cleanup() failed")
	}
}

// TestApp_HelpToggle asserts pressing `?` flips the help-shown flag,
// and the bottom bar shows expanded hints when toggled on.
func TestApp_HelpToggle(t *testing.T) {
	app := newTestApp(t)
	if app.help {
		t.Fatal("help should be off initially")
	}
	updated, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if !updated.(App).help {
		t.Fatal("help did not toggle on")
	}
}
