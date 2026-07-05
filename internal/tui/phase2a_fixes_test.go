package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// TestApp_SecondOpRejectedResetsFlow is the CRITICAL regression from the
// Phase 2a review: a flow that entered its running stage but had its
// startOpMsg REJECTED (because another op holds the guard) must not be
// left stuck in "running" forever. The App emits opRejectedMsg back to
// the flow, which resets to its pre-run stage.
func TestApp_SecondOpRejectedResetsFlow(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	app := NewApp(pruneDeps(r))
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)

	// Occupy the guard with a backup op. We never execute the returned
	// run cmd, so opRunning stays "backup" (the op is "in flight").
	m, _ := app.Update(startOpMsg{name: "backup", run: func(ctx context.Context) tea.Msg {
		return backupDoneMsg{}
	}})
	app = m.(App)
	if app.opRunning != "backup" {
		t.Fatalf("guard not set: opRunning = %q", app.opRunning)
	}

	// Navigate to prune and drive it into pruneRunning via its confirm
	// (the same message the typed-confirm modal emits).
	m, _ = app.Update(activateMsg{id: "prune"})
	app = m.(App)
	m, cmd := app.Update(confirmedMsg{id: pruneConfirmID})
	app = m.(App)

	// The confirm broadcast reached the prune view, which started its op
	// and emitted a startOpMsg. Feed every produced message back through
	// the App (as bubbletea would), including the rejection it emits.
	pump := func(c tea.Cmd) {
		for _, msg := range execCmds(t, c) {
			var next tea.Cmd
			m, next = app.Update(msg)
			app = m.(App)
			for _, msg2 := range execCmds(t, next) {
				m, _ = app.Update(msg2)
				app = m.(App)
			}
		}
	}
	pump(cmd)

	// An error modal explains the rejection.
	if len(app.modals) == 0 {
		t.Error("rejected op should push an error modal")
	}
	// The prune flow must have reset out of running.
	pv := findPruneView(t, app)
	if pv.stage == pruneRunning {
		t.Fatal("prune flow stuck in running after rejection; must reset to preview")
	}
	// The real backup op still resolves cleanly and clears the guard.
	m, _ = app.Update(backupDoneMsg{})
	app = m.(App)
	if app.opRunning != "" {
		t.Errorf("guard should clear when the real op resolves; opRunning = %q", app.opRunning)
	}
}

func findPruneView(t *testing.T, app App) PruneView {
	t.Helper()
	for _, v := range app.views {
		if v.id == "prune" {
			return v.model.(PruneView)
		}
	}
	t.Fatal("prune view not registered")
	return PruneView{}
}
