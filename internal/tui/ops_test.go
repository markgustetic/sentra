package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeDoneMsg is a minimal opResultMsg for guard tests.
type fakeDoneMsg struct{ err error }

func (fakeDoneMsg) opResult() {}

// execCmds runs a tea.Cmd and flattens any BatchMsg into its messages.
// Flows that start an operation return tea.Batch(start, opTick()); the
// App unwraps that BatchMsg to find the startOpMsg. Tests need the same
// flattening to inspect both the startOpMsg AND the seeded opTickMsg in
// one batch — a direct type assertion on cmd() would only ever see the
// opaque BatchMsg. A nil cmd yields no messages; a non-batch cmd yields
// exactly its single message.
func execCmds(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	msg := cmd()
	batch, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var msgs []tea.Msg
	for _, sub := range batch {
		if sub == nil {
			continue
		}
		msgs = append(msgs, sub())
	}
	return msgs
}

func startFakeOp(name string, block <-chan struct{}) startOpMsg {
	return startOpMsg{
		name: name,
		run: func(ctx context.Context) tea.Msg {
			select {
			case <-block:
				return fakeDoneMsg{}
			case <-ctx.Done():
				return fakeDoneMsg{err: ctx.Err()}
			}
		},
	}
}

func TestOpGuard_StartSetsRunningAndStatusBarShowsIt(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{})
	m, cmd := app.Update(startFakeOp("backup", block))
	if cmd == nil {
		t.Fatal("start must return the op command")
	}
	a := m.(App)
	if a.opRunning != "backup" {
		t.Fatalf("opRunning = %q, want backup", a.opRunning)
	}
	if !strings.Contains(a.View(), "backup") {
		t.Error("status bar must show the running operation")
	}
	close(block)
	// Drain: the op cmd resolves to fakeDoneMsg; delivering it clears the guard.
	m, _ = a.Update(cmd())
	if got := m.(App).opRunning; got != "" {
		t.Fatalf("opRunning after result = %q, want empty", got)
	}
}

func TestOpGuard_RejectsSecondOpWithErrorModal(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{})
	defer close(block)
	m, _ := app.Update(startFakeOp("backup", block))
	m2, cmd2 := m.(App).Update(startFakeOp("prune", block))
	a := m2.(App)
	if a.opRunning != "backup" {
		t.Fatalf("second op must not replace the first; running = %q", a.opRunning)
	}
	if cmd2 != nil {
		t.Fatal("rejected op must not return a run command")
	}
	if len(a.modals) == 0 || !strings.Contains(a.modals[len(a.modals)-1].View(), "in progress") {
		t.Fatal("rejection must push an operation-in-progress error modal")
	}
}

func TestOpGuard_CancelMsgCancelsContext(t *testing.T) {
	app := newTestApp(t)
	block := make(chan struct{}) // never closed: only cancel can finish the op
	m, cmd := app.Update(startFakeOp("backup", block))
	m2, _ := m.(App).Update(cancelOpMsg{})
	// The op goroutine observes ctx.Done and returns an error result.
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case msg := <-done:
		res, ok := msg.(fakeDoneMsg)
		if !ok || !errors.Is(res.err, context.Canceled) {
			t.Fatalf("expected canceled result, got %#v", msg)
		}
		m2, _ = m2.(App).Update(msg)
		if m2.(App).opRunning != "" {
			t.Fatal("guard must clear after canceled result")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelOpMsg did not cancel the op context")
	}
}

func TestOpReporter_SnapshotIsConcurrencySafe(t *testing.T) {
	r := newOpReporter()
	r.Total(100)
	donech := make(chan struct{})
	go func() {
		for i := 0; i < 50; i++ {
			r.Add(1)
		}
		close(donech)
	}()
	for {
		total, _ := r.Snapshot()
		if total != 100 {
			t.Fatalf("total = %d, want 100", total)
		}
		select {
		case <-donech:
			if _, d := r.Snapshot(); d != 50 {
				t.Fatalf("done = %d, want 50", d)
			}
			return
		default:
		}
	}
}
