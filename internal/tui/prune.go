package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type pruneStage int

const (
	prunePreview pruneStage = iota
	pruneRunning
	pruneDone
)

// pruneConfirmID ties the typed-confirm modal back to this flow.
const pruneConfirmID = "prune-apply"

// pruneGCConfirmID ties the GC-only confirm (empty drop set) back to
// this flow. Distinct from pruneConfirmID so a stale confirmation from
// one gate can never trigger the other path.
const pruneGCConfirmID = "prune-gc-only"

type pruneDoneMsg struct {
	deleted int
	stats   repo.GCStats
	err     error
	// gcOnly marks a run that deleted no snapshots by design (empty
	// drop set); the done stage renders GC stats without a snapshot
	// count. emptyRepo marks GC's zero-snapshot refusal (ErrEmptyRepo),
	// which is a calm no-op here, not a failure.
	gcOnly    bool
	emptyRepo bool
}

func (pruneDoneMsg) opResult() {}

// PruneView shows the retention preview and, after a TYPED confirmation
// ("prune"), deletes the dropped snapshots and runs GC. It follows the CLI
// prune --apply sequence: DeleteSnapshot per drop (skipping already-gone
// snapshots, as the CLI does), then GC with the keep-set (GC's live set is
// derived from the store under its lock; keepIDs only marks the
// deliberate-prune path). An empty drop set still offers a GC-only run
// behind a plain confirm — the CLI's runPruneGCOnly rule, in TUI form —
// so orphaned blobs stay collectable from this surface too.
type PruneView struct {
	deps      Deps
	stage     pruneStage
	decisions []repo.RetentionDecision
	keep      []string
	drop      []string
	loadErr   string
	notice    string // transient banner, e.g. after an op rejection

	result pruneDoneMsg
	width  int
}

func NewPruneView(deps Deps) PruneView {
	v := PruneView{deps: deps}
	if deps.Repo == nil {
		v.loadErr = "no repository configured"
		return v
	}
	snaps, err := initialSnapshots(deps) // shared load
	if err != nil {
		v.loadErr = err.Error()
		return v
	}
	policy := repo.RetentionPolicy{}
	if deps.Config != nil {
		policy = repo.RetentionPolicy{
			KeepLast:    deps.Config.Retention.KeepLast,
			KeepDaily:   deps.Config.Retention.KeepDaily,
			KeepWeekly:  deps.Config.Retention.KeepWeekly,
			KeepMonthly: deps.Config.Retention.KeepMonthly,
		}
	}
	// Pins keep snapshots unconditionally; a load failure degrades to
	// planning without them — DeleteSnapshot still refuses pinned IDs
	// at apply time, so the guard holds either way.
	if pins, err := deps.Repo.Pins(ctxOrBackground(deps.Ctx)); err == nil {
		policy.Pinned = pins
	}
	v.decisions = repo.PlanRetentionExplain(snaps, policy)
	for _, d := range v.decisions {
		if d.Keep {
			v.keep = append(v.keep, d.Snapshot.ID)
		} else {
			v.drop = append(v.drop, d.Snapshot.ID)
		}
	}
	return v
}

func (PruneView) Init() tea.Cmd { return nil }

func (v PruneView) Title() string { return "Prune" }

func (v PruneView) ShortHelp() []key.Binding {
	if v.stage != prunePreview || v.loadErr != "" {
		return nil
	}
	if len(v.drop) > 0 {
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "prune…"))}
	}
	return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run GC…"))}
}

func (v PruneView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case pruneDoneMsg:
		v.stage = pruneDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our start was refused (another op holds the guard). Leave the
		// running stage we optimistically entered so we don't hang.
		if v.stage == pruneRunning && msg.name == "prune" {
			v.stage = prunePreview
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case confirmedMsg:
		if v.stage != prunePreview {
			return v, nil
		}
		switch msg.id {
		case pruneConfirmID:
			v.notice = ""
			return v.startPrune()
		case pruneGCConfirmID:
			v.notice = ""
			return v.startGCOnly()
		}
		return v, nil

	case tea.KeyMsg:
		if v.stage == prunePreview && msg.Type == tea.KeyEnter && len(v.drop) > 0 {
			v.notice = ""
			body := fmt.Sprintf("This deletes %d snapshot(s) and reclaims their unique chunks.\nChunks still referenced by kept snapshots are never touched.", len(v.drop))
			word := "prune"
			if len(v.keep) == 0 {
				// The CLI's --all rail, in TUI form: a policy that drops
				// EVERY snapshot must be confirmed with a distinct word —
				// an operator expecting a routine prune should trip here.
				body = fmt.Sprintf("This deletes ALL %d snapshot(s) — the repository will be EMPTY.\nType \"wipe\" to confirm.", len(v.drop))
				word = "wipe"
			}
			modal := NewTypedConfirmModal("Confirm prune", body, word, pruneConfirmID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		}
		if v.stage == prunePreview && msg.Type == tea.KeyEnter && v.loadErr == "" {
			// Empty drop set: retention keeps everything, but the surface
			// still owes the operator a GC pass — orphaned blobs (crashed
			// backups, out-of-band deletes) never enter the drop set, and
			// prune is the only sanctioned way to reclaim them. A plain
			// confirm suffices: GC deletes only unreferenced blobs, never
			// a snapshot, so the typed gate would overstate the stakes.
			v.notice = ""
			modal := NewConfirmModal("Run GC",
				"No snapshots to delete — retention keeps everything.\nGC reclaims blobs no snapshot references (crashed backups,\nout-of-band deletes). Snapshots are never touched.",
				pruneGCConfirmID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		}
		if v.stage == pruneDone && msg.Type == tea.KeyEnter {
			fresh := NewPruneView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil
	}
	return v, nil
}

func (v PruneView) startPrune() (tea.Model, tea.Cmd) {
	v.stage = pruneRunning
	r := v.deps.Repo
	drop := append([]string(nil), v.drop...)
	keep := append([]string(nil), v.keep...)
	start := startOpMsg{
		name: "prune",
		run: func(ctx context.Context) tea.Msg {
			deleted := 0
			for _, id := range drop {
				if err := r.DeleteSnapshot(ctx, id); err != nil {
					// Already gone (idempotent re-run / raced delete) is
					// fine — skip and keep pruning, matching the CLI. Any
					// other error aborts before GC.
					if errors.Is(err, blobstore.ErrNotFound) {
						continue
					}
					return pruneDoneMsg{deleted: deleted, err: err}
				}
				deleted++
			}
			keepIDs := make(map[string]bool, len(keep))
			for _, id := range keep {
				keepIDs[id] = true
			}
			stats, err := r.GC(ctx, keepIDs)
			return pruneDoneMsg{deleted: deleted, stats: stats, err: err}
		},
	}
	return v, func() tea.Msg { return start }
}

// startGCOnly runs GC with nil keepIDs — the bare-orphans mode — under
// the same one-op guard (and op name) as a full prune. nil keepIDs
// makes GC refuse a zero-snapshot store (ErrEmptyRepo) instead of
// treating "no manifests" as "every blob is garbage"; that refusal is
// reported as a calm no-op, not an error.
func (v PruneView) startGCOnly() (tea.Model, tea.Cmd) {
	v.stage = pruneRunning
	r := v.deps.Repo
	start := startOpMsg{
		name: "prune",
		run: func(ctx context.Context) tea.Msg {
			stats, err := r.GC(ctx, nil)
			if errors.Is(err, repo.ErrEmptyRepo) {
				return pruneDoneMsg{gcOnly: true, emptyRepo: true}
			}
			return pruneDoneMsg{gcOnly: true, stats: stats, err: err}
		},
	}
	return v, func() tea.Msg { return start }
}

func (v PruneView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	var b strings.Builder
	switch v.stage {
	case pruneRunning:
		b.WriteString(ui.Primary.Render("Pruning…"))
	case pruneDone:
		switch {
		case v.result.err != nil:
			header := "Prune failed"
			if v.result.gcOnly {
				header = "GC failed"
			}
			b.WriteString(ui.Danger.Render(header))
			fmt.Fprintf(&b, "\n\n%s", humanizeErr(v.result.err))
		case v.result.emptyRepo:
			b.WriteString(ui.Success.Render("GC skipped"))
			fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("The repository has no snapshots, so every blob would look\nunreferenced; GC refuses to reclaim from an empty repository."))
		case v.result.gcOnly:
			b.WriteString(ui.Success.Render("GC complete"))
			fmt.Fprintf(&b, "\n\n  reclaimed blobs    %d\n  reclaimed bytes    %s\n  live blobs         %d",
				v.result.stats.DeletedBlobs,
				ui.FormatBytes(v.result.stats.DeletedBytes), v.result.stats.LiveBlobs)
		default:
			b.WriteString(ui.Success.Render("Prune complete"))
			fmt.Fprintf(&b, "\n\n  deleted snapshots  %d\n  reclaimed blobs    %d\n  reclaimed bytes    %s\n  live blobs         %d",
				v.result.deleted, v.result.stats.DeletedBlobs,
				ui.FormatBytes(v.result.stats.DeletedBytes), v.result.stats.LiveBlobs)
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("recompute the retention preview", ""))
	default:
		b.WriteString(ui.Primary.Render("Retention preview"))
		if v.notice != "" {
			fmt.Fprintf(&b, "  %s", ui.Warn.Render(v.notice))
		}
		var freedEstimate int64
		for _, d := range v.decisions {
			if !d.Keep {
				freedEstimate += d.Snapshot.Stats.NewBytes
			}
		}
		fmt.Fprintf(&b, "  %s\n\n", ui.Muted.Render(
			fmt.Sprintf("keep %d · drop %d (~%s freed estimate)", len(v.keep), len(v.drop), ui.FormatBytes(freedEstimate))))
		for _, d := range v.decisions {
			verdictText := "keep"
			reason := strings.Join(d.Reasons, ", ")
			if !d.Keep {
				verdictText = "drop"
				if reason == "" {
					reason = "not selected by retention policy"
				}
			}
			// Keep each decision on one line: without this the ID + verdict
			// + (often long, multi-rule) reason ran past the panel interior
			// and reflowed. The reason is the expendable tail, so truncate it
			// to the width left after the fixed "  ID  verdict  " prefix.
			// width == 0 (pre-WindowSizeMsg) means unbounded.
			if v.width > 0 {
				prefix := len("  ") + lipgloss.Width(d.Snapshot.ID) + len("  ") + len(verdictText) + len("  ")
				reason = truncateToWidth(reason, pickerContentWidth(v.width)-prefix)
			}
			verdict := ui.Success.Render(verdictText)
			if !d.Keep {
				verdict = ui.Danger.Render(verdictText)
			}
			fmt.Fprintf(&b, "  %s  %s  %s\n", d.Snapshot.ID, verdict, ui.Muted.Render(reason))
		}
		if len(v.drop) > 0 {
			fmt.Fprintf(&b, "\n%s", ui.ActionLine("prune the flagged snapshots", "typed confirmation required"))
		} else {
			fmt.Fprintf(&b, "\n%s", ui.ActionLine("run GC and reclaim unreferenced storage", "no snapshots to delete"))
		}
	}
	return b.String()
}
