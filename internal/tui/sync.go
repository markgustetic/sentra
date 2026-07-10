package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// syncStage is the sync flow's state-machine position.
type syncStage int

const (
	syncConfigure syncStage = iota
	syncRunning
	syncDone
)

// syncConfirmID ties the confirmation modal back to this flow. Sync
// spreads the wrapped repo key to a new bucket on --init-dest, so a real
// (non-dry-run) run is gated behind a simple y/n confirm — the App
// broadcasts confirmedMsg{syncConfirmID} back here on enter.
const syncConfirmID = "sync-apply"

// syncField tracks which configure-stage control owns keystrokes: the
// destination-path text input or one of the boolean toggles.
type syncField int

const (
	syncFieldPath syncField = iota
	syncFieldInitDest
	syncFieldDryRun
	syncFieldCount // sentinel: number of focusable fields
)

// syncDoneMsg is the flow's terminal message. It implements opResultMsg
// because sync is a MUTATING op (it takes the dest's meta/lock and writes
// blobs) — the App clears its one-op guard on this marker.
type syncDoneMsg struct {
	stats repo.SyncStats
	err   error
}

func (syncDoneMsg) opResult() {}

// SyncView drives configure → (confirm) → running → done for replicating
// this repository to a clone destination. The dest store is built from a
// second sentra.yaml via deps.NewStore; the actual copy runs in the
// App-managed op goroutine (repo.SyncTo), and this view renders a
// byte-progress bar polled through opTick.
//
// Deliberately NOT wired into NewApp's views/categories here — Task 20
// (this file) only adds the view and its constructor. Registration in
// app.go's views slice and Operations category map is Task 27 (Part 9),
// which registers every Phase 2c flow in one pass.
type SyncView struct {
	deps  Deps
	stage syncStage

	dstPath  textinput.Model
	field    syncField
	initDest bool
	dryRun   bool
	pathErr  string
	notice   string // transient banner, e.g. after an op rejection

	// dstStore is resolved during validation (enter) and reused by the
	// op goroutine so the store is built exactly once per run.
	dstStore blobstore.Store

	reporter *opReporter
	bar      progress.Model
	result   syncDoneMsg
	width    int
	height   int
}

func NewSyncView(deps Deps) SyncView {
	path := textinput.New()
	path.Prompt = "dst>  "
	path.Placeholder = "path to the destination's sentra.yaml"
	path.Focus()
	return SyncView{
		deps:    deps,
		dstPath: path,
		bar:     progress.New(progress.WithDefaultGradient()),
	}
}

func (SyncView) Init() tea.Cmd { return nil }

func (v SyncView) Title() string { return "Sync" }

// CapturesText is true on the configure stage: it hosts the dest-config path
// input plus tab-navigated toggles, so every rune (path characters) and tab
// (field navigation) must reach the view. The running/done stages take only
// single-key commands.
func (v SyncView) CapturesText() bool { return v.stage == syncConfigure }

// ConsumesEscape: only while the copy is running, where esc cancels it.
func (v SyncView) ConsumesEscape() bool { return v.stage == syncRunning }

// ConfirmsClose: the configure stage collects the destination and toggles.
func (v SyncView) ConfirmsClose() bool { return v.stage == syncConfigure }

func (v SyncView) ShortHelp() []key.Binding {
	switch v.stage {
	case syncRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case syncDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "sync")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		}
	}
}

func (v SyncView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		return v, nil

	case syncDoneMsg:
		v.stage = syncDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our optimistic start was refused; leave running so we don't hang.
		if v.stage == syncRunning && msg.name == "sync" {
			v.stage = syncConfigure
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case confirmedMsg:
		if msg.id != syncConfirmID || v.stage != syncConfigure {
			return v, nil
		}
		v.notice = ""
		return v.startSync()

	case opTickMsg:
		if v.stage == syncRunning {
			return v, opTick() // keep ticking while running
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// resetTo returns a fresh view carrying the window size so the progress
// bar keeps its width (bubbletea does not re-emit WindowSizeMsg after a
// model swap).
func (v SyncView) resetTo() (tea.Model, tea.Cmd) {
	return NewSyncView(v.deps).Update(tea.WindowSizeMsg{Width: v.width, Height: v.height})
}

func (v SyncView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case syncRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	case syncDone:
		if msg.Type == tea.KeyEnter {
			return v.resetTo()
		}
		return v, nil

	default: // syncConfigure
		v.notice = ""
		// A bare space press toggles a boolean field rather than being
		// typed into the path input. Bubbletea's real input reader
		// reports a lone space as KeySpace (key.go: a single-rune " "
		// bunch is retyped to KeySpace before it ever reaches Update),
		// but constructing a tea.KeyMsg by hand — as tests and any
		// programmatic driver do — naturally produces KeyRunes with
		// Runes == []rune{' '} instead. Recognizing both keeps the
		// toggle working under real terminal input and under direct
		// KeyMsg construction alike.
		isSpace := msg.Type == tea.KeySpace ||
			(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
		switch {
		case msg.Type == tea.KeyTab:
			v.field = (v.field + 1) % syncFieldCount
			if v.field == syncFieldPath {
				v.dstPath.Focus()
			} else {
				v.dstPath.Blur()
			}
			return v, nil
		case msg.Type == tea.KeyEnter:
			return v.validateAndConfirm()
		case isSpace && v.field != syncFieldPath:
			switch v.field {
			case syncFieldInitDest:
				v.initDest = !v.initDest
			case syncFieldDryRun:
				v.dryRun = !v.dryRun
			}
			return v, nil
		}
		if v.field == syncFieldPath {
			var cmd tea.Cmd
			v.dstPath, cmd = v.dstPath.Update(msg)
			v.pathErr = "" // typing clears the last validation error
			return v, cmd
		}
		return v, nil
	}
}

// validateAndConfirm checks the dest path, loads its config, guards
// against a same-location target, and builds the dest store. On a real
// run it pushes a y/n confirm; a dry-run (which writes nothing) starts
// immediately. All refusals short-circuit before any store is built,
// except store construction itself, which is the last validation step.
func (v SyncView) validateAndConfirm() (tea.Model, tea.Cmd) {
	dst := strings.TrimSpace(v.dstPath.Value())
	if dst == "" {
		v.pathErr = "destination config path is required"
		return v, nil
	}
	if info, err := os.Stat(dst); err != nil || info.IsDir() {
		v.pathErr = fmt.Sprintf("destination config not found: %s", dst)
		return v, nil
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return v, nil
	}
	if v.deps.NewStore == nil {
		v.pathErr = "store factory unavailable"
		return v, nil
	}

	dstCfg, err := config.Load(dst)
	if err != nil {
		v.pathErr = fmt.Sprintf("load %s: %v", dst, err)
		return v, nil
	}
	// Re-implement the CLI's sameS3Location guard (internal/cli/sync.go:190)
	// inline: refuse a dest that resolves to the source's bucket+prefix
	// BEFORE building any store. deps.Config is the source's config.
	if syncSameLocation(v.deps.Config, dstCfg) {
		v.pathErr = fmt.Sprintf("source and destination resolve to the same S3 location (bucket=%q)",
			dstCfg.Repo.S3.Bucket)
		return v, nil
	}

	ctx := ctxOrBackground(v.deps.Ctx)
	store, err := v.deps.NewStore(ctx, dstCfg)
	if err != nil {
		v.pathErr = fmt.Sprintf("open destination blobstore: %v", err)
		return v, nil
	}
	v.dstStore = store

	// A dry-run performs no writes on the destination, so it needs no
	// confirmation gate; start it directly.
	if v.dryRun {
		return v.startSync()
	}

	body := "Copy every snapshot, chunk, and config to the destination clone.\nSubsequent syncs are incremental."
	if v.initDest {
		body += "\n\n" + "init-dest is ON: this bootstraps an empty bucket and spreads the wrapped repo key to it. Point it at a bucket you control."
	}
	modal := NewConfirmModal("Confirm sync", body, syncConfirmID, v.width, v.height)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startSync enters the running stage and emits startOpMsg{name:"sync"}.
// The dest store was resolved during validation and captured on v.
func (v SyncView) startSync() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.stage = syncRunning
	r := v.deps.Repo
	reporter := v.reporter
	dest := v.dstStore // blobstore.Store, resolved during validation
	opts := repo.SyncOptions{
		InitDest: v.initDest,
		DryRun:   v.dryRun,
		Progress: reporter,
	}
	start := startOpMsg{
		name: "sync",
		run: func(ctx context.Context) tea.Msg {
			stats, err := r.SyncTo(ctx, dest, opts)
			return syncDoneMsg{stats: stats, err: err}
		},
	}
	// Seed the first opTickMsg alongside the start so the progress bar's
	// repaint self-loop begins (bubbletea only redraws on messages).
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v SyncView) View() string {
	var b strings.Builder
	switch v.stage {
	case syncRunning:
		total, done := v.reporter.Snapshot()
		b.WriteString(ui.Primary.Render("Syncing…"))
		b.WriteString("\n\n")
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(v.bar.ViewAs(pct))
		fmt.Fprintf(&b, "\n\n%s / %s copied",
			ui.FormatBytes(done), ui.FormatBytes(total))
		b.WriteString("\n" + ui.Muted.Render("esc cancel"))

	case syncDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Sync failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			s := v.result.stats
			if s.DryRun {
				b.WriteString(ui.Success.Render("Dry-run complete (no writes performed)"))
			} else {
				b.WriteString(ui.Success.Render("Sync complete"))
			}
			boot := "no"
			if s.Bootstrapped {
				boot = "yes (destination config was empty)"
			}
			fmt.Fprintf(&b, "\n\n  bootstrap   %s\n  copied      %d blobs (%s)\n  skipped     %d (already on destination)\n  elapsed     %s",
				boot, s.CopiedBlobs, ui.FormatBytes(s.CopiedBytes), s.SkippedBlobs, s.Elapsed)
		}
		b.WriteString("\n\n" + ui.ActionLine("run another sync", ""))

	default:
		b.WriteString(ui.Primary.Render("Replicate to a clone destination"))
		if v.notice != "" {
			b.WriteString("\n" + ui.Warn.Render(v.notice))
		}
		b.WriteString("\n\n" + v.dstPath.View())
		b.WriteString("\n\n" + v.toggleLine(syncFieldInitDest, "init-dest", v.initDest,
			"bootstrap an empty destination"))
		b.WriteString("\n" + v.toggleLine(syncFieldDryRun, "dry-run", v.dryRun,
			"list what would be copied, write nothing"))
		if v.pathErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.pathErr))
		}
		b.WriteString("\n\n" + ui.ActionLine("start the sync", "tab field · space toggle"))
	}
	return b.String()
}

// toggleLine renders one boolean toggle, marking the focused one and its
// on/off state.
// toggleLine renders one checkbox row. The help text is appended OUTSIDE the
// styled row: nesting it would embed an ANSI reset that terminates the row's own
// style partway along the line.
func (v SyncView) toggleLine(f syncField, label string, on bool, help string) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	row := fmt.Sprintf("%s %-10s", box, label)
	return ui.SelectRow(v.field == f, row) + " " + ui.Muted.Render(help)
}

// syncSameLocation mirrors internal/cli/sync.go's sameS3Location: two
// configs match when their bucket+prefix are equal and the bucket is
// non-empty. A nil source config (tests, unconfigured) never matches, so
// the guard fails open — the dest store's factory surfaces any real
// misconfiguration.
func syncSameLocation(src, dst *config.Config) bool {
	if src == nil || dst == nil {
		return false
	}
	return src.Repo.S3.Bucket == dst.Repo.S3.Bucket &&
		src.Repo.S3.Prefix == dst.Repo.S3.Prefix &&
		src.Repo.S3.Bucket != ""
}
