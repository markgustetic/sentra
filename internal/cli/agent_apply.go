package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// ErrAgentApplyFailed is returned after the apply summary is written
// when one or more approved actions failed to apply. The loop keeps
// going past individual failures, but the process must still exit
// non-zero — scripted runs check $?, not the "errors: N" stdout line.
var ErrAgentApplyFailed = errors.New("agent apply failed")

// writeRecsTable emits a styled lipgloss table of recommendations. An
// empty slice still prints the header row so the user sees the same
// frame regardless of contents.
func writeRecsTable(w io.Writer, recs []agent.Recommendation) error {
	headers := []string{"ID", "Severity", "Action", "Target", "Rationale"}
	rows := make([][]string, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, []string{
			r.ID,
			ui.Severity(r.Severity).Render(emptyDash(r.Severity)),
			emptyDash(r.Action),
			emptyDash(r.Target),
			truncateRationale(r.Rationale, 60),
		})
	}
	if _, err := fmt.Fprintln(w, ui.RenderTable(headers, rows)); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	if len(recs) == 0 {
		fmt.Fprintln(w, ui.Subtle.Render("No findings — all clear."))
	}
	return nil
}

// writeRecsJSON emits the recommendations as a JSON array. An empty
// slice emits `[]` so consumers can iterate without a nil check.
func writeRecsJSON(w io.Writer, recs []agent.Recommendation) error {
	out := recs
	if out == nil {
		out = []agent.Recommendation{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// applyRecommendations walks the recommendation list, prompting the
// user (skipped with --yes) and dispatching approved actions through
// the action handler map. Errors on individual actions are reported
// but don't abort the loop.
//
// Safety rail: tracks the remaining-snapshot count across the apply
// loop so a sequence of prune_snapshot actions can't silently empty
// the repo. If a prune would drop the last snapshot AND --allow-wipe
// is not set, the action is refused with a clear error pointing at
// the flag (mirrors `prune --all`'s contract).
func applyRecommendations(
	ctx context.Context,
	r *repo.Repo,
	recs []agent.Recommendation,
	deps AgentDeps,
	flags *agentFlags,
	out io.Writer,
	actions *action.Registry,
) error {
	if len(recs) == 0 {
		fmt.Fprintln(out, ui.Subtle.Render("No recommendations to apply."))
		return nil
	}

	// Snapshot count baseline: count what's currently in the repo. We
	// decrement as prune_snapshot actions succeed; the wipe-guard
	// triggers when the next prune would drive the count to zero.
	currentSnaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	remaining := len(currentSnaps)

	applied, declined, errs := 0, 0, 0
	for _, rec := range recs {
		// "none" is the model's "FYI, nothing to do here" — skip the
		// confirm and the apply but acknowledge it in the output.
		if rec.Action == "none" {
			fmt.Fprintf(out, "  - %s (%s): noted, nothing to apply\n", rec.ID, ui.Severity(rec.Severity).Render(rec.Severity))
			continue
		}

		// Per-recommendation confirm. --yes short-circuits this so
		// scripted runs (e.g. cron) don't deadlock on stdin.
		approved := flags.yes
		if !approved {
			prompt := fmt.Sprintf("Apply %s on %q?\n  rationale: %s",
				rec.Action, rec.Target, rec.Rationale)
			ok, err := deps.Confirm(prompt)
			if err != nil {
				return fmt.Errorf("confirm: %w", err)
			}
			approved = ok
		}
		if !approved {
			fmt.Fprintf(out, "  - %s: declined\n", rec.ID)
			declined++
			continue
		}

		// Wipe guard: an APPROVED prune that would empty the repo is
		// refused unless --allow-wipe was explicitly passed. Placing
		// the check AFTER confirm means a declined prune doesn't trip
		// the rail (the user already said no, no wipe is happening) —
		// only an approved action that crosses the safety line fails
		// the run. Mirrors `prune --all` semantics.
		if rec.Action == "prune_snapshot" && remaining-1 <= 0 && !flags.allowWipe {
			return fmt.Errorf(
				"refusing to apply %s on %q: this would prune every snapshot in the repo; pass --allow-wipe to confirm",
				rec.Action, rec.Target)
		}

		if err := dispatchAction(ctx, actions, r, rec, out); err != nil {
			fmt.Fprintf(out, "  - %s: %s\n", rec.ID, ui.Danger.Render("error: "+err.Error()))
			errs++
			continue
		}
		applied++
		// On a successful prune, decrement the live counter so the
		// guard fires correctly on the next prune action. Other
		// actions don't affect the snapshot count.
		if rec.Action == "prune_snapshot" {
			remaining--
		}
	}

	fmt.Fprintln(out, ui.Success.Render("Apply complete"))
	fmt.Fprintf(out, "  applied:  %d\n", applied)
	fmt.Fprintf(out, "  declined: %d\n", declined)
	fmt.Fprintf(out, "  errors:   %d\n", errs)
	if errs > 0 {
		return fmt.Errorf("%w: %d action(s) failed", ErrAgentApplyFailed, errs)
	}
	return nil
}

// dispatchAction maps a recommendation's Action to a side-effect via
// the action.Registry. Unknown actions surface as errors so the
// model's vocabulary can't silently fail to do anything.
//
// The handlers themselves live in internal/agent/action — adding a
// new action verb is a single file there + one line in
// action.NewDefaultRegistry. The CLI's role is only to construct the
// Env (formatter for byte counts, working directory) and dispatch.
func dispatchAction(
	ctx context.Context,
	registry *action.Registry,
	r *repo.Repo,
	rec agent.Recommendation,
	out io.Writer,
) error {
	cwd, _ := os.Getwd() // failure → handler falls back to "."
	env := action.Env{
		Repo:        r,
		Stdout:      out,
		Cwd:         cwd,
		FormatBytes: ui.FormatBytes,
	}
	return registry.Dispatch(ctx, env, action.Action(rec.Action),
		rec.ID, rec.Target, rec.Severity, rec.Rationale)
}

// truncateRationale shortens long rationale text for table display.
// The full text is still in the JSON output (and the model's stream).
func truncateRationale(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
