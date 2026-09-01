package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/ui"
)

// ChatOverlay is the conversational command palette (ctrl+a): a modal
// chat whose model can answer questions from repository METADATA and
// compile intents into the exact messages the UI already routes — so
// every existing confirm gate applies by construction. The model never
// executes anything itself. See
// docs/superpowers/specs/2026-08-28-tui-chat-design.md.
type ChatOverlay struct {
	deps  Deps
	input textinput.Model

	// transcript is the rendered conversation, newest last.
	transcript []string
	// history is the provider-facing message log (plain turns only —
	// per-turn tool rounds are not carried across turns).
	history []llm.Message

	busy    bool   // one in-flight turn at a time
	partial string // streamed tokens of the in-flight reply
	cancel  context.CancelFunc
	width   int
	height  int

	// initBlink is the cmd in.Focus() returned at construction, captured
	// so Init can return it. Focus() (not textinput.Blink) is the only
	// source of a REAL, tag-matched blink cmd — see unlock.go's initBlink
	// doc comment for why the bootstrap sentinel is a dead end. As with
	// Palette, Init() has a value receiver and is called fresh on every
	// ctrl+a open, long after construction, so it can only ever return
	// what was captured once, back when Focus() actually ran.
	initBlink tea.Cmd
}

// chatEventMsg is the turn goroutine's channel into the update loop:
// token deltas while streaming, then exactly one done event carrying the
// final text, any UI intents, and the error if the turn failed.
type chatEventMsg struct {
	token   string
	done    bool
	text    string
	intents []tea.Msg
	err     error
	events  chan chatEventMsg // re-arm handle; nil once done
}

// chatBackupMsg is the start_backup intent: the App routes it to the
// Backup view, which raises its EXISTING confirmation modal — the chat
// cannot skip the gate because it never had another path.
type chatBackupMsg struct {
	dir string
	tag string
}

// chatRoundLimit bounds tool round-trips per turn so a confused model
// cannot loop forever.
const chatRoundLimit = 4

func NewChatOverlay(deps Deps) ChatOverlay {
	in := textinput.New()
	in.Prompt = "you> "
	in.Placeholder = "ask, or say what to do — actions still confirm"
	cmd := in.Focus()
	return ChatOverlay{deps: deps, input: in, initBlink: cmd}
}

// Init starts the ask field's cursor blinking. The field is constructed
// already focused (NewChatOverlay) and never blurred for as long as the
// overlay exists, so — mirroring Palette.Init, the same "focused from
// birth" shape — Init is where the blink schedule starts rather than a
// later Focus() transition. Not a tea.Model method: the App holds the
// overlay directly and calls this when ctrl+a makes it visible.
//
// No ui.FieldBox here: the ask field already sits inside dedicated chrome
// (ui.ModalBox in View), the same exception the palette and the modal
// prompts take — a second frame would be noise, not an affordance.
func (c ChatOverlay) Init() tea.Cmd { return c.initBlink }

// chatSystemPrompt states the ground rules the tools enforce anyway —
// stating them steers the model before it tries something the registry
// would refuse.
const chatSystemPrompt = `You are Sentra's in-app assistant, living inside the backup tool's TUI.
You can see repository METADATA only — snapshot ids, tags, dates, file
names and sizes — never file contents and never secrets.
Answer questions using the tools. To act, use the action tools: they hand
an intent to the UI where the operator confirms it in a dialog — nothing
runs on your say-so, so state clearly that confirmation happens there.
Be brief; this is a terminal.`

func chatTools() []llm.Tool {
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	return []llm.Tool{
		{Name: "list_snapshots", Description: "List snapshots: id, created, tag, files, bytes. Metadata only.",
			Schema: obj(map[string]any{})},
		{Name: "repo_stats", Description: "Repository totals: snapshot count, logical vs stored bytes.",
			Schema: obj(map[string]any{})},
		{Name: "open_view", Description: "Open a view in the TUI (dashboard, backup, snapshots, maintenance, settings, help, or a flow like restore/diff/check/prune/sync/doctor/jobs).",
			Schema: obj(map[string]any{"id": map[string]any{"type": "string"}}, "id")},
		{Name: "start_backup", Description: "Hand a backup intent to the UI: the operator sees the directory and tag in a confirm dialog and decides there.",
			Schema: obj(map[string]any{
				"path": map[string]any{"type": "string", "description": "local directory to back up"},
				"tag":  map[string]any{"type": "string", "description": "optional snapshot tag"},
			}, "path")},
		{Name: "restore_snapshot", Description: "Open the restore flow with this snapshot chosen; the operator picks the destination and confirms.",
			Schema: obj(map[string]any{"id": map[string]any{"type": "string", "description": "snapshot id"}}, "id")},
	}
}

func (c ChatOverlay) Update(msg tea.Msg) (ChatOverlay, tea.Cmd) {
	switch msg := msg.(type) {
	case chatEventMsg:
		if msg.token != "" {
			c.partial += msg.token
			return c, listenChat(msg.events)
		}
		if !msg.done {
			return c, listenChat(msg.events)
		}
		c.busy = false
		c.partial = ""
		c.cancel = nil
		switch {
		case msg.err != nil:
			c.transcript = append(c.transcript, ui.Danger.Render("error: "+msg.err.Error()))
		case msg.text != "":
			c.transcript = append(c.transcript, msg.text)
			c.history = append(c.history, llm.Message{Role: llm.RoleAssistant, Content: msg.text})
		}
		if len(msg.intents) > 0 {
			intents := msg.intents
			cmds := make([]tea.Cmd, len(intents))
			for i, it := range intents {
				it := it
				cmds[i] = func() tea.Msg { return it }
			}
			return c, tea.Sequence(cmds...)
		}
		return c, nil

	case cursor.BlinkMsg:
		// The ask field is focused for as long as the overlay exists, so a
		// tick always has a home here; forwarding it keeps the blink
		// rescheduling itself.
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		return c, cmd

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter {
			return c.send()
		}
		var cmd tea.Cmd
		c.input, cmd = c.input.Update(msg)
		return c, cmd
	}
	return c, nil
}

// send launches one turn: the goroutine streams tokens and runs the tool
// loop; listenChat pumps its events back into Update.
func (c ChatOverlay) send() (ChatOverlay, tea.Cmd) {
	q := strings.TrimSpace(c.input.Value())
	if q == "" || c.busy || c.deps.Provider == nil {
		return c, nil
	}
	c.input.SetValue("")
	c.transcript = append(c.transcript, ui.Primary.Render("you> ")+q)
	c.history = append(c.history, llm.Message{Role: llm.RoleUser, Content: q})
	c.busy = true

	ctx, cancel := context.WithCancel(ctxOrBackground(c.deps.Ctx))
	c.cancel = cancel
	events := make(chan chatEventMsg, 64)
	go runChatTurn(ctx, c.deps, append([]llm.Message(nil), c.history...), events)
	return c, listenChat(events)
}

func listenChat(events chan chatEventMsg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg {
		ev, ok := <-events
		if !ok {
			return nil
		}
		ev.events = events
		return ev
	}
}

// runChatTurn is the whole turn, off the UI goroutine: rounds of
// Generate + tool execution until the model answers in plain text (or an
// intent tool fires and the round closes with its handoff text).
func runChatTurn(ctx context.Context, deps Deps, msgs []llm.Message, events chan chatEventMsg) {
	defer close(events)
	var intents []tea.Msg
	for round := 0; round < chatRoundLimit; round++ {
		stream := make(chan string, 64)
		fwdDone := make(chan struct{})
		go func() {
			for t := range stream {
				events <- chatEventMsg{token: t}
			}
			close(fwdDone)
		}()
		calls, text, err := deps.Provider.Generate(ctx, chatSystemPrompt, msgs, chatTools(), stream)
		close(stream)
		<-fwdDone
		if err != nil {
			events <- chatEventMsg{done: true, err: err}
			return
		}
		if len(calls) == 0 {
			events <- chatEventMsg{done: true, text: text, intents: intents}
			return
		}
		for _, call := range calls {
			msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: text,
				ToolUse: &llm.ToolUse{ID: call.ID, Name: call.Name, Input: call.Input}})
			result, intent := runChatTool(ctx, deps, call)
			if intent != nil {
				intents = append(intents, intent)
			}
			msgs = append(msgs, llm.Message{Role: llm.RoleTool,
				ToolResult: &llm.ToolResult{ID: call.ID, Content: result}})
		}
	}
	events <- chatEventMsg{done: true, err: fmt.Errorf("tool round limit (%d) reached", chatRoundLimit), intents: intents}
}

// runChatTool executes one tool call. Read tools answer from repository
// metadata; action tools return the corresponding UI message as an intent
// — they never execute anything here.
func runChatTool(ctx context.Context, deps Deps, call llm.ToolCall) (string, tea.Msg) {
	str := func(key string) string {
		v, _ := call.Input[key].(string)
		return strings.TrimSpace(v)
	}
	switch call.Name {
	case "list_snapshots":
		snaps, err := initialSnapshots(deps)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		type row struct {
			ID      string `json:"id"`
			Created string `json:"created"`
			Tag     string `json:"tag,omitempty"`
			Files   int    `json:"files"`
			Bytes   int64  `json:"bytes"`
		}
		rows := make([]row, 0, len(snaps))
		for _, s := range snaps {
			rows = append(rows, row{ID: s.ID, Created: s.CreatedAt.UTC().Format("2006-01-02 15:04"),
				Tag: s.Tag, Files: s.Stats.Files, Bytes: s.Stats.Bytes})
		}
		b, _ := json.Marshal(rows)
		return string(b), nil

	case "repo_stats":
		if deps.Repo == nil {
			return "error: no repository open", nil
		}
		st, err := deps.Repo.Stats(ctx)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		b, _ := json.Marshal(st)
		return string(b), nil

	case "open_view":
		id := str("id")
		if id == "" {
			return "error: id required", nil
		}
		return "opening " + id, activateMsg{id: id}

	case "start_backup":
		path := str("path")
		if path == "" {
			return "error: path required", nil
		}
		return "handed to the UI — the operator confirms the backup there",
			chatBackupMsg{dir: path, tag: str("tag")}

	case "restore_snapshot":
		id := str("id")
		if id == "" {
			return "error: id required", nil
		}
		return "restore flow opened — the operator picks the destination and confirms",
			launchRestoreMsg{snapID: id}

	default:
		return "error: unknown tool " + call.Name, nil
	}
}

// Cancel aborts an in-flight turn (esc while streaming).
func (c ChatOverlay) Cancel() ChatOverlay {
	if c.cancel != nil {
		c.cancel()
		c.cancel = nil
		c.busy = false
		c.partial = ""
		c.transcript = append(c.transcript, ui.Muted.Render("(cancelled)"))
	}
	return c
}

func (c ChatOverlay) SetSize(w, h int) ChatOverlay { c.width, c.height = w, h; return c }

func (c ChatOverlay) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Assistant"))
	b.WriteString("\n")
	if c.deps.Provider == nil {
		b.WriteString(ui.Muted.Render("set ANTHROPIC_API_KEY to chat — everything else in the TUI works without it"))
	} else {
		b.WriteString(ui.Muted.Render("answers from snapshot metadata · actions always confirm · esc closes"))
	}
	// Last lines of the transcript, sized to the modal.
	lines := c.transcript
	if c.partial != "" {
		lines = append(append([]string(nil), lines...), c.partial)
	} else if c.busy {
		lines = append(append([]string(nil), lines...), ui.Muted.Render("thinking…"))
	}
	maxLines := max(c.height-10, 4)
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	for _, l := range lines {
		fmt.Fprintf(&b, "\n%s", l)
	}
	fmt.Fprintf(&b, "\n\n%s", c.input.View())
	box := ui.ModalBox.Width(min(c.width-8, 76)).Render(b.String())
	return lipgloss.Place(c.width, c.height, lipgloss.Center, lipgloss.Center, box)
}
