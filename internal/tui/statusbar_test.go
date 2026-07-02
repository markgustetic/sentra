package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

func TestStatusBar_ShowsGlobalAndViewKeys(t *testing.T) {
	sb := NewStatusBar(newGlobalKeymap(), 100)
	viewKeys := []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
	out := sb.View("s3://bucket", viewKeys, "")
	for _, want := range []string{"ctrl+p", "palette", "⏎", "open", "s3://bucket"} {
		if !strings.Contains(out, want) {
			t.Errorf("status bar missing %q:\n%s", want, out)
		}
	}
}

func TestStatusBar_ShowsRunningIndicator(t *testing.T) {
	sb := NewStatusBar(newGlobalKeymap(), 100)
	out := sb.View("repo", nil, "backup running")
	if !strings.Contains(out, "backup running") {
		t.Errorf("running indicator missing:\n%s", out)
	}
}

// TestStatusBar_ClampsToWidth pins the overflow guard: a view that
// contributes many ShortHelp keys plus the globals produces a hint run
// longer than the 80-col bar. Without the clamp the rendered row would
// spill past 80 and, in the shell, force every JoinVertical row out to
// the overflowing width. The bar must truncate instead.
func TestStatusBar_ClampsToWidth(t *testing.T) {
	viewKeys := []key.Binding{
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "alpha action")),
		key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "bravo action")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "charlie action")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delta action")),
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "echo action")),
		key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "foxtrot action")),
	}
	sb := NewStatusBar(newGlobalKeymap(), 80)
	out := sb.View("s3://a-fairly-long-bucket-name", viewKeys, "an-operation-running")
	if w := lipgloss.Width(out); w > 80 {
		t.Errorf("status bar overflows: width %d > 80:\n%q", w, out)
	}
}
