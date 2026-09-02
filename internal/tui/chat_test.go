package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/repo"
)

// chatApp builds an App with a scripted provider and a canned snapshot
// preload, sized and with the chat overlay opened.
func chatApp(t *testing.T, p llm.Provider) App {
	t.Helper()
	deps := launchDeps()
	deps.RepoName = "x"
	deps.Repo = newFlowRepo(t)
	deps.Provider = p
	app := NewApp(deps)
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ := sized.(App).Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	return m.(App)
}

// typeAndSend types a question into the open overlay and presses enter,
// returning the App and the turn's command.
func typeAndSend(t *testing.T, app App, text string) (App, tea.Cmd) {
	t.Helper()
	m := tea.Model(app)
	var cmd tea.Cmd
	for _, r := range text {
		m, _ = m.(App).Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, cmd = m.(App).Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m.(App), cmd
}

// drainTurn pumps the chat event loop to completion: each cmd yields a
// msg, each msg may re-arm the listener. Returns the final App and every
// non-chat message the turn dispatched (the intents).
func drainTurn(t *testing.T, app App, cmd tea.Cmd) (App, []tea.Msg) {
	t.Helper()
	var intents []tea.Msg
	queue := []tea.Cmd{cmd}
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := c()
		if msg == nil {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, bc := range batch {
				queue = append(queue, bc)
			}
			continue
		}
		// A cursor blink tick is self-perpetuating chrome, not part of the
		// turn: routing it back would re-arm the chain and this loop would
		// pump it (one real BlinkSpeed wait per step) until the step cap.
		// Every view that lands on screen with a text field now starts one,
		// so drop ticks here and let the turn's own messages settle.
		if _, ok := msg.(cursor.BlinkMsg); ok {
			continue
		}
		var next tea.Cmd
		m, next := app.Update(msg)
		app = m.(App)
		if next != nil {
			queue = append(queue, next)
		}
		switch msg.(type) {
		case chatEventMsg:
		default:
			intents = append(intents, msg)
		}
	}
	return app, intents
}

// ctrl+a opens the overlay anywhere outside a startup gate; esc closes it.
func TestChat_CtrlAOpensEscCloses(t *testing.T) {
	r := newFlowRepo(t)
	app := NewApp(Deps{RepoName: "x", Repo: r, Provider: &llm.FakeProvider{}})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ := sized.(App).Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !m.(App).chatOpen {
		t.Fatal("ctrl+a must open the chat overlay")
	}
	if out := m.(App).View(); !strings.Contains(out, "Assistant") {
		t.Fatalf("open overlay must render:\n%s", out)
	}
	m, _ = m.(App).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.(App).chatOpen {
		t.Fatal("esc must close the chat overlay")
	}
}

// Startup gates own the whole keyboard; no chat there.
func TestChat_BlockedInStartupGate(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "setup", Provider: &llm.FakeProvider{}})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m, _ := sized.(App).Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if m.(App).chatOpen {
		t.Fatal("chat must not open inside a startup gate")
	}
}

// No provider → the overlay opens with a setup hint and stays inert.
func TestChat_NilProviderPlaceholder(t *testing.T) {
	app := chatApp(t, nil)
	if out := app.View(); !strings.Contains(out, "ANTHROPIC_API_KEY") {
		t.Fatalf("nil provider must render the configure hint:\n%s", out)
	}
	app2, cmd := typeAndSend(t, app, "hi")
	if cmd != nil {
		t.Fatal("nil provider must not start a turn")
	}
	_ = app2
}

// A plain answer round trip: the reply lands in the transcript.
func TestChat_PlainAnswerRendersReply(t *testing.T) {
	p := &llm.FakeProvider{Steps: []llm.FakeStep{{Text: "You have 2 snapshots."}}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "how many snapshots?")
	app, _ = drainTurn(t, app, cmd)
	out := app.View()
	if !strings.Contains(out, "You have 2 snapshots.") {
		t.Fatalf("reply missing from transcript:\n%s", out)
	}
}

// The list_snapshots tool answers from repo metadata: the tool result fed
// back to the model must carry the snapshot ids, and the final text shows.
func TestChat_ListSnapshotsToolAnswers(t *testing.T) {
	p := &llm.FakeProvider{Steps: []llm.FakeStep{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "list_snapshots", Input: map[string]any{}}}},
		{Text: "Newest is snap-bbb."},
	}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "list my snapshots")
	app, _ = drainTurn(t, app, cmd)
	if len(p.Calls) != 2 {
		t.Fatalf("expected 2 provider rounds, got %d", len(p.Calls))
	}
	last := p.Calls[1].Msgs[len(p.Calls[1].Msgs)-1]
	if last.ToolResult == nil || !strings.Contains(last.ToolResult.Content, "snap-bbb") {
		t.Fatalf("tool result must carry snapshot metadata: %+v", last)
	}
	if out := app.View(); !strings.Contains(out, "Newest is snap-bbb.") {
		t.Fatalf("final text missing:\n%s", out)
	}
}

// start_backup compiles into the Backup view's EXISTING confirm gate: the
// modal is up, the tag is set, and no operation is running.
func TestChat_StartBackupRaisesConfirmGate(t *testing.T) {
	dir := t.TempDir()
	p := &llm.FakeProvider{Steps: []llm.FakeStep{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "start_backup",
			Input: map[string]any{"path": dir, "tag": "pre-move"}}}},
		{Text: "Confirm in the dialog to start."},
	}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "back up "+dir)
	app, _ = drainTurn(t, app, cmd)

	if app.chatOpen {
		t.Fatal("dispatching an intent must close the overlay so the gate is visible")
	}
	if got := app.views[app.active].id; got != "backup" {
		t.Fatalf("active = %q, want backup", got)
	}
	if len(app.modals) == 0 {
		t.Fatal("the backup confirm modal must be up — chat must not skip the gate")
	}
	if app.opRunning != "" {
		t.Fatal("nothing may run before the human confirms")
	}
	var bv BackupView
	for _, v := range app.views {
		if v.id == "backup" {
			bv = v.model.(BackupView)
		}
	}
	if bv.pending == "" || strings.TrimSpace(bv.tag.Value()) != "pre-move" {
		t.Fatalf("backup view not seeded: pending=%q tag=%q", bv.pending, bv.tag.Value())
	}
}

// open_view routes exactly like the palette.
func TestChat_OpenViewActivates(t *testing.T) {
	p := &llm.FakeProvider{Steps: []llm.FakeStep{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "open_view", Input: map[string]any{"id": "snapshots"}}}},
		{Text: "Opened."},
	}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "show snapshots")
	app, _ = drainTurn(t, app, cmd)
	if got := app.views[app.active].id; got != "snapshots" {
		t.Fatalf("active = %q, want snapshots", got)
	}
}

// restore_snapshot lands inside the restore flow with the snapshot chosen.
func TestChat_RestoreSeedsFlow(t *testing.T) {
	p := &llm.FakeProvider{Steps: []llm.FakeStep{
		{ToolCalls: []llm.ToolCall{{ID: "t1", Name: "restore_snapshot", Input: map[string]any{"id": "snap-bbb"}}}},
		{Text: "Pick the destination and confirm."},
	}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "restore snap-bbb")
	app, _ = drainTurn(t, app, cmd)
	if got := app.views[app.active].id; got != "restore" {
		t.Fatalf("active = %q, want restore", got)
	}
	var rv RestoreView
	for _, v := range app.views {
		if v.id == "restore" {
			rv = v.model.(RestoreView)
		}
	}
	if rv.snapID != "snap-bbb" || rv.stage != restoreDest {
		t.Fatalf("restore not seeded: snapID=%q stage=%v", rv.snapID, rv.stage)
	}
}

// The system prompt and tool results must never contain file contents —
// there is no tool that could produce them, and the prompt says so.
func TestChat_SystemPromptStatesMetadataRule(t *testing.T) {
	p := &llm.FakeProvider{Steps: []llm.FakeStep{{Text: "ok"}}}
	app := chatApp(t, p)
	app, cmd := typeAndSend(t, app, "hello")
	_, _ = drainTurn(t, app, cmd)
	if len(p.Calls) == 0 {
		t.Fatal("no provider call recorded")
	}
	sys := p.Calls[0].System
	if !strings.Contains(strings.ToLower(sys), "metadata") {
		t.Fatalf("system prompt must state the metadata-only rule:\n%s", sys)
	}
	_ = context.Background
	_ = repo.SnapshotInfo{}
}

// TestChatOverlay_InitSchedulesBlink: the ask field is constructed already
// focused (NewChatOverlay) and never blurred while the overlay exists, so
// there is no later Focus() transition to hang the blink on — Init is where
// it starts, mirroring Palette.Init for the same "focused from birth" shape.
//
// Init calls Focus() at CALL time (not at construction), so BlinkSpeed can
// be dropped first and the cmd genuinely executed — this asserts the cmd
// yields a blink, not merely that it is non-nil.
//
// No ui.FieldBox is asserted: the ask field already sits inside ui.ModalBox,
// the same "existing chrome" exception the palette and modal prompts take.
func TestChatOverlay_InitSchedulesBlink(t *testing.T) {
	c := NewChatOverlay(Deps{})
	c.input.Cursor.BlinkSpeed = time.Millisecond
	assertBlinkCmd(t, c.Init())
}

// TestChatOverlay_RoutesBlinkTicks: ticks must reach the ask field so the
// schedule continues. A bare cursor.BlinkMsg{} won't do — bubbles/cursor
// tags each scheduled tick and rejects one whose tag doesn't match its
// internal counter, which Focus() already advanced past zero — so the test
// mints a tag-matched tick from the field's own cursor.
func TestChatOverlay_RoutesBlinkTicks(t *testing.T) {
	c := NewChatOverlay(Deps{})
	c.input.Cursor.BlinkSpeed = time.Millisecond
	tick := c.input.Cursor.BlinkCmd()
	if _, cmd := c.Update(tick()); cmd == nil {
		t.Fatal("blink tick was not routed to the chat's ask field")
	}
}

// TestApp_ChatOpenSchedulesBlink: opening the overlay through the real App
// (ctrl+a) must return the blink-start cmd — ChatOverlay is not in m.views,
// so App.Init's batching never reaches its Init, exactly the production gap
// the palette had. A view-level test cannot catch this; only an App-level
// one can.
func TestApp_ChatOpenSchedulesBlink(t *testing.T) {
	app := newTestApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if cmd == nil {
		t.Fatal("ctrl+a must schedule the ask field's blink, got nil")
	}
}

// TestApp_ChatOverlayRoutesBlinkTicks: with the overlay open, a live tick
// must reach its ask field through App.Update's cursor.BlinkMsg case, which
// delivers to every focus owner at once rather than by precedence.
func TestApp_ChatOverlayRoutesBlinkTicks(t *testing.T) {
	app := newTestApp(t)
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	app = m.(App)
	if !app.chatOpen {
		t.Fatal("precondition: ctrl+a should have opened the chat overlay")
	}
	app.chat.input.Cursor.BlinkSpeed = time.Millisecond
	tick := app.chat.input.Cursor.BlinkCmd()
	if _, cmd := app.Update(tick()); cmd == nil {
		t.Fatal("the chat's blink chain died while the overlay was open")
	}
}

// TestApp_ChatReopenReArmsBlink: the chat overlay has the same
// construct-once / reopen-many shape as the palette, so it has the same
// single-use-cached-cmd hazard. See TestApp_PaletteReopenReArmsBlink for
// why a nil check on Init cannot catch it. The symptom bites hardest
// here: the ask field is where the operator waits while a reply streams,
// so a solid cursor reads as "the overlay is dead".
func TestApp_ChatReopenReArmsBlink(t *testing.T) {
	app := newTestApp(t)
	app.chat.input.Cursor.BlinkSpeed = time.Millisecond

	m, first := app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	app = m.(App)
	if first == nil {
		t.Fatal("first open must schedule a blink")
	}
	m, cont := app.Update(first())
	app = m.(App)
	if cont == nil {
		t.Fatal("the first open's tick must be accepted and reschedule")
	}

	app.chatOpen = false
	m, second := app.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	app = m.(App)
	if second == nil {
		t.Fatal("reopen must schedule a blink")
	}
	if _, cmd := app.Update(second()); cmd == nil {
		t.Fatal("the chat's blink chain did not re-arm on reopen — Init replayed a stale, single-use cmd")
	}
}
