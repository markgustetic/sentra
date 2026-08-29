package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/walker"
)

// Confirm-modal IDs for JobsView's run/install/uninstall flows. jobRunConfirmID
// mirrors policyRunConfirmID's simple-vs-typed split (armRun below); install
// and uninstall are always reversible filesystem edits, so they stay on the
// simple ConfirmModal, matching ScheduleView.
const (
	jobRunConfirmID       = "job-run"
	jobInstallConfirmID   = "job-install"
	jobUninstallConfirmID = "job-uninstall"
	jobDeleteConfirmID    = "job-delete"
)

// jobTimerMsg carries an install/uninstall/delete filesystem result.
// Deliberately NOT an opResult: timer files never touch the repo lock.
type jobTimerMsg struct {
	notice string
	err    error
}

// policyRunState tracks the in-flight run for the running-stage View().
// Shared by PoliciesView and JobsView, which both drive buildPolicyRunOp
// under the one-op guard.
type policyRunState struct {
	reporter *opReporter
	name     string
}

// policyRunDoneMsg is the RUN flow's terminal, guard-clearing message.
// Shared by PoliciesView and JobsView (see buildPolicyRunOp) — Bubbletea
// only routes a message to the active view, so both may define a case for
// it without colliding.
type policyRunDoneMsg struct {
	name      string
	snapshots int
	err       error
}

func (policyRunDoneMsg) opResult() {}

// buildPolicyRunOp assembles the one-op-guarded policy run: hooks,
// CreateSnapshot per path, optional check, optional retention prune —
// the CLI's runPolicy sequence. opName distinguishes the guard owner
// ("policy-run" for the legacy view, "job-run" for JobsView) so each
// view recognizes its own opRejectedMsg.
func buildPolicyRunOp(deps Deps, opName, name string, p config.PolicyConfig, reporter *opReporter) startOpMsg {
	r := deps.Repo
	var wopts walker.Options
	var retention repo.RetentionPolicy
	if deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    deps.Config.Backup.IgnoreFile,
			ExcludeCaches: deps.Config.Backup.ExcludeCaches,
			Concurrency:   deps.Config.Backup.Concurrency,
		}
		retention = repo.RetentionPolicy{
			KeepLast:    deps.Config.Retention.KeepLast,
			KeepDaily:   deps.Config.Retention.KeepDaily,
			KeepWeekly:  deps.Config.Retention.KeepWeekly,
			KeepMonthly: deps.Config.Retention.KeepMonthly,
		}
	}
	paths := append([]string(nil), p.Paths...)
	tag := policyRunTag(name, p.Tags)
	doCheck := p.AfterBackup.Check
	pruneMode := policyPruneModeOrOff(p.AfterBackup.Prune)
	hooks := p.Hooks

	return startOpMsg{
		name: opName,
		run: func(ctx context.Context) tea.Msg {
			// Hooks run exactly as the CLI's `policy run` runs them
			// (internal/policy owns the execution, below both surfaces)
			// — a TUI run that skipped an operator's pg_dump before
			// hook would back up different data. Hook output goes to a
			// buffer whose tail rides along on failure.
			var hookOut bytes.Buffer
			count := 0
			runErr := func() error {
				if hooks.Before != "" {
					if err := policycfg.RunHook(ctx, &hookOut, "before", hooks.Before); err != nil {
						return err
					}
				}
				for _, path := range paths {
					if _, err := r.CreateSnapshot(ctx, path, repo.SnapshotOptions{
						Tag:      tag,
						Progress: reporter,
						Walker:   wopts,
					}); err != nil {
						return fmt.Errorf("snapshot %s: %w", path, err)
					}
					count++
				}
				if doCheck {
					report, err := r.Check(ctx, repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
					if err != nil {
						return fmt.Errorf("check: %w", err)
					}
					if !report.Healthy() {
						return errors.New("post-backup check found integrity issues")
					}
				}
				if err := runPolicyRetentionPrune(ctx, r, retention, pruneMode); err != nil {
					return err
				}
				if hooks.After != "" {
					if err := policycfg.RunHook(ctx, &hookOut, "after", hooks.After); err != nil {
						return err
					}
				}
				return nil
			}()
			if runErr != nil {
				policycfg.FireFailureHooks(ctx, &hookOut, name, hooks, runErr)
				return policyRunDoneMsg{name: name, snapshots: count, err: runErr}
			}
			return policyRunDoneMsg{name: name, snapshots: count}
		},
	}
}

// runPolicyRetentionPrune applies the policy's post-backup prune. It
// mirrors the CLI's runPolicyPrune (internal/cli/policy.go:331): off is a
// no-op; dry-run computes but deletes nothing; apply deletes the dropped
// snapshots (skipping already-gone ones) and runs GC. Apply refuses to
// drop every snapshot — the same guard the CLI enforces.
//
// The mode switch is FAIL-CLOSED: only the three known constants trigger
// their behavior, and anything else (an unrecognized/corrupt mode) is
// treated as off — a no-op — rather than falling through to the delete
// path. Callers already validate the policy (policycfg.Validate rejects
// unknown prune modes) before reaching here, so this is defense in depth:
// even if an invalid mode slips through, it can never silently delete.
func runPolicyRetentionPrune(ctx context.Context, r *repo.Repo, policy repo.RetentionPolicy, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	// Only apply performs deletions. off, dry-run, and any unrecognized
	// value are no-ops here (dry-run's preview is surfaced elsewhere).
	if mode != policycfg.PruneApply {
		return nil
	}
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	decisions := repo.PlanRetentionExplain(snaps, policy)
	var keep, drop []string
	for _, d := range decisions {
		if d.Keep {
			keep = append(keep, d.Snapshot.ID)
		} else {
			drop = append(drop, d.Snapshot.ID)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	if len(keep) == 0 {
		return errors.New("policy prune would drop every snapshot; refusing automatic apply")
	}
	for _, id := range drop {
		if err := r.DeleteSnapshot(ctx, id); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("delete snapshot %s: %w", id, err)
		}
	}
	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	if _, err := r.GC(ctx, keepIDs); err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	return nil
}

// policyRunTag mirrors the CLI's policySnapshotTag: "policy:<name>" plus
// any configured tags, space-joined.
func policyRunTag(name string, tags []string) string {
	parts := []string{"policy:" + name}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			parts = append(parts, tag)
		}
	}
	return strings.Join(parts, " ")
}

// policyPruneModeOrOff normalizes an empty prune string to "off" for
// display, matching the CLI's policyPruneMode.
func policyPruneModeOrOff(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return policycfg.PruneOff
	}
	return mode
}

// armRun pushes the RUN confirmation modal for the job under the cursor. A
// prune mode of "apply" is destructive (it deletes snapshots + GCs), so it
// gets the TYPED confirm; every other mode (off, dry-run, or check-only)
// gets the simple confirm — mirroring PoliciesView.armRun's gate contract.
//
// It validates the policy first (mirroring the CLI's runPolicy, which calls
// policycfg.Validate before doing any work): policyPruneModeOrOff only
// lowercases/trims, so a corrupted prune mode must not slip past the typed
// gate by failing validation silently.
func (v JobsView) armRun() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	p := v.policies[name]
	if err := policycfg.Validate(name, p); err != nil {
		v.notice = "cannot run: " + err.Error()
		return v, nil
	}
	mode := policyPruneModeOrOff(p.AfterBackup.Prune)
	var modal Modal
	if mode == policycfg.PruneApply {
		body := fmt.Sprintf("Run job %q now?\nAfter backup it will DELETE snapshots outside the retention policy and reclaim their chunks.", name)
		modal = NewTypedConfirmModal("Confirm job run", body, "run", jobRunConfirmID, 80, 24)
	} else {
		body := fmt.Sprintf("Run job %q now?\nThis creates a snapshot for each of its paths.", name)
		modal = NewConfirmModal("Confirm job run", body, jobRunConfirmID, 80, 24)
	}
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startRun launches the job under the cursor via buildPolicyRunOp, under
// the App's one-op guard (opName "job-run" — distinct from PoliciesView's
// "policy-run" so the two views' opRejectedMsg handlers never cross wires).
func (v JobsView) startRun() (tea.Model, tea.Cmd) {
	if v.deps.Repo == nil {
		v.notice = "no repository configured"
		return v, nil
	}
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	p := v.policies[name]
	reporter := newOpReporter()
	v.run = policyRunState{reporter: reporter, name: name}
	v.stage = jobsRunning

	return v, tea.Batch(func() tea.Msg {
		return buildPolicyRunOp(v.deps, "job-run", name, p, reporter)
	}, opTick())
}

// runTimerInstall renders and writes the selected job's scheduler files in a
// quick tea.Cmd — a port of ScheduleView.runInstall. Rejects a manual
// cadence (mirrors the CLI) and folds any render/write error into the
// returned jobTimerMsg.
func (v JobsView) runTimerInstall() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	p := v.policies[name]
	goos := v.osOverride
	home := v.homeOverride
	exeOverride := v.exeOverride
	run := func() tea.Msg {
		if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
			return jobTimerMsg{err: fmt.Errorf("job %q has a manual schedule; set a cadence before installing", name)}
		}
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return jobTimerMsg{err: err}
		}
		exe, err := scheduler.Executable(exeOverride)
		if err != nil {
			return jobTimerMsg{err: err}
		}
		files, err := scheduler.Render(paths, exe, cfgPath, name, p.Schedule)
		if err != nil {
			return jobTimerMsg{err: err}
		}
		if err := scheduler.Install(files); err != nil {
			return jobTimerMsg{err: err}
		}
		return jobTimerMsg{notice: fmt.Sprintf("installed timer for %q", name)}
	}
	return v, run
}

// runTimerUninstall removes the selected job's scheduler files in a quick
// tea.Cmd — a port of ScheduleView.runUninstall.
func (v JobsView) runTimerUninstall() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	goos := v.osOverride
	home := v.homeOverride
	run := func() tea.Msg {
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return jobTimerMsg{err: err}
		}
		if err := scheduler.Uninstall(paths); err != nil {
			return jobTimerMsg{err: err}
		}
		return jobTimerMsg{notice: fmt.Sprintf("removed timer for %q", name)}
	}
	return v, run
}

// runDelete removes the selected job: the policy leaves sentra.yaml
// (config.Update, on-disk base) and the timer files are uninstalled —
// in that order, so a half-failure can only leave a policy-less timer
// briefly, never a timer-less zombie policy the table would still show.
// Snapshots are deliberately untouched: data deletion belongs to
// retention/prune, not a config view. Uninstall tolerates absent files,
// so it runs unconditionally.
func (v JobsView) runDelete() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	goos, home := v.osOverride, v.homeOverride
	run := func() tea.Msg {
		if err := config.Update(cfgPath, func(cfg *config.Config) error {
			delete(cfg.Policies, name)
			return nil
		}); err != nil {
			return jobTimerMsg{err: fmt.Errorf("remove policy: %w", err)}
		}
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return jobTimerMsg{notice: fmt.Sprintf("deleted %q (timer cleanup skipped: %v)", name, err)}
		}
		if err := scheduler.Uninstall(paths); err != nil {
			return jobTimerMsg{notice: fmt.Sprintf("deleted %q, but removing its timer failed: %v", name, err)}
		}
		return jobTimerMsg{notice: fmt.Sprintf("deleted %q — policy and timer removed; snapshots kept", name)}
	}
	return v, run
}
