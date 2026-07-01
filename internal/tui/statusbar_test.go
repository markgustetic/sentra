package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
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
