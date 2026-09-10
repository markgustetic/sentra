package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// Confirm-modal IDs for JobsView's run/install/uninstall/delete flows.
// jobRunConfirmID drives a simple-vs-typed split (armRun below); install
// and uninstall are always reversible filesystem edits, so they stay on
// the simple ConfirmModal — the same split the deleted ScheduleView used
// for its own install/uninstall. jobDeleteConfirmID is always simple too:
// deleting a job removes the policy from sentra.yaml and uninstalls its
// timer, but snapshots are untouched (see runDelete), so there is nothing
// here that needs the typed gate's extra friction.
const (
	jobRunConfirmID       = "job-run"
	jobInstallConfirmID   = "job-install"
	jobUninstallConfirmID = "job-uninstall"
	jobDeleteConfirmID    = "job-delete"
)

// jobTimerMsg carries an install/uninstall/delete result. It is an
// opResult even though timer files never touch the repo lock: the work
// behind it execs launchctl/systemctl (15s per call, up to a minute
// across bootout/bootstrap/fallback), so it runs under the App's one-op
// guard — serialized, cancellable with esc, and off the UI goroutine —
// and the guard clears on this message.
type jobTimerMsg struct {
	notice string
	err    error
}

func (jobTimerMsg) opResult() {}

// Op names for the guarded config/timer mutations, distinct from
// "job-run" so an opRejectedMsg bounces exactly the flow that was
// refused.
const (
	jobInstallOpName   = "job-install"
	jobUninstallOpName = "job-uninstall"
	jobDeleteOpName    = "job-delete"
	jobSaveOpName      = "job-save"
)

// startBusyOp enters the jobsBusy stage for a config/timer mutation and
// hands run to the App's one-op guard. The form's fields are blurred
// because nothing renders them on the busy stage; the result handlers
// (jobTimerMsg / jobSavedMsg) and the opRejectedMsg bounce leave the
// stage through leaveBusy. The spinner's tick is batched with the start
// the way backup batches its first opTickMsg: bubbletea only redraws on
// messages, so without the seed the spinner would never move.
func (v JobsView) startBusyOp(op, label string, run func(ctx context.Context) tea.Msg) (tea.Model, tea.Cmd) {
	v.stage = jobsBusy
	v.busyOp = op
	v.busyLabel = label
	v.notice = ""
	v.form.blurAll()
	start := startOpMsg{name: op, run: run}
	return v, tea.Batch(func() tea.Msg { return start }, v.spin.Tick)
}

// leaveBusy returns the view to its list once the busy op has resolved
// (or was refused). A no-op off the busy stage, so a jobTimerMsg
// broadcast while the operator is elsewhere never yanks them to the list.
func (v *JobsView) leaveBusy() {
	if v.stage != jobsBusy {
		return
	}
	v.stage = jobsList
	v.busyOp = ""
	v.busyLabel = ""
}

// policyRunState tracks the in-flight run for the running-stage View().
// Drives buildPolicyRunOp under the one-op guard; JobsView is its sole
// owner now — the name predates the deletion of the PoliciesView it used
// to be shared with.
type policyRunState struct {
	reporter *opReporter
	name     string
}

// policyRunDoneMsg is the RUN flow's terminal, guard-clearing message
// returned by buildPolicyRunOp's run func. JobsView is its sole producer
// and consumer now; the name predates the deletion of the PoliciesView it
// used to be shared with.
type policyRunDoneMsg struct {
	name      string
	snapshots int
	// skipped sums SnapshotStats.Skipped across the policy's paths: the
	// folders the walk dropped for a denied listing. A job run has no
	// terminal to print each one to as it happens, so the count on the
	// done screen is how an operator learns a snapshot is short.
	skipped int
	err     error
}

func (policyRunDoneMsg) opResult() {}

// buildPolicyRunOp assembles the one-op-guarded policy run: hooks,
// CreateSnapshot per path, optional check, optional retention prune —
// the CLI's runPolicy sequence. opName is a caller-supplied label so an
// opRejectedMsg reports which view's guard was rejected; JobsView is the
// only caller now (opName "job-run"), left as a parameter because a
// second run-taking view once shared this function ("policy-run", the
// deleted PoliciesView).
//
// The policy's paths are resolved the way the timer's `policy run`
// resolves them — policycfg.ResolvePathFrom against the config file's
// directory: "~/docs" or a relative dir stored by an older form save
// reached CreateSnapshot raw and failed, and a relative path anchored to
// this process's cwd would snapshot a different tree than the CLI run
// of the same policy.
//
// The retention prune plans around the repo's pin set, loaded inside the
// op through policycfg.RetentionFromConfig (it is a blobstore read).
// Without it a pinned snapshot beyond keep_last was planned for
// deletion, DeleteSnapshot refused it at the choke point, and every run
// of the job failed — the CLI and the prune view build retention there;
// the job run must too.
func buildPolicyRunOp(deps Deps, opName, name string, p config.PolicyConfig, reporter *opReporter) startOpMsg {
	r := deps.Repo
	cfg := deps.Config
	wopts := policycfg.BackupWalkerOptions(cfg)
	// Resolve the stored paths now, on the UI goroutine; a tilde with no
	// home is a run failure (reported through the ordinary done message,
	// failure hooks included), never a snapshot of <cwd>/docs under the
	// policy's tag. Dir("") is ".", so a config-less Deps (tests) anchors
	// to the cwd exactly as the CLI's policyConfigDir would.
	paths := make([]string, 0, len(p.Paths))
	cfgDir, pathErr := filepath.Abs(filepath.Dir(deps.ConfigPath))
	for _, path := range p.Paths {
		if pathErr != nil {
			break
		}
		if abs, err := policycfg.ResolvePathFrom(path, cfgDir); err != nil {
			pathErr = err
		} else {
			paths = append(paths, abs)
		}
	}
	tag := policyRunTag(name, p.Tags)
	doCheck := p.AfterBackup.Check
	pruneMode := policyPruneModeOrOff(p.AfterBackup.Prune)
	hooks := p.Hooks
	notifyRun, notifyOn := deps.Notify, cfg == nil || !cfg.Notify.DisableDesktop

	return startOpMsg{
		name: opName,
		run: func(ctx context.Context) tea.Msg {
			// Hooks run exactly as the CLI's `policy run` runs them
			// (internal/policy owns the execution, below both surfaces)
			// — a TUI run that skipped an operator's pg_dump before
			// hook would back up different data. Hook output goes to a
			// buffer whose tail rides along on failure.
			var hookOut bytes.Buffer
			count, skipped := 0, 0
			outcome := policycfg.BackupOutcome{Name: name}
			runErr := func() error {
				if pathErr != nil {
					return pathErr
				}
				if hooks.Before != "" {
					if err := policycfg.RunHook(ctx, &hookOut, "before", hooks.Before, hooks.OnFailureWebhookEnv); err != nil {
						return err
					}
				}
				for _, path := range paths {
					info, err := r.CreateSnapshot(ctx, path, repo.SnapshotOptions{
						Tag:      tag,
						Progress: reporter,
						Walker:   wopts,
					})
					if err != nil {
						return fmt.Errorf("snapshot %s: %w", path, err)
					}
					count++
					skipped += info.Stats.Skipped
					outcome.Files += info.Stats.Files
					outcome.NewBytes += info.Stats.NewBytes
					outcome.Skipped += info.Stats.Skipped
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
				if pruneMode == policycfg.PruneApply {
					// Pins keep snapshots unconditionally; a plan that
					// cannot see them would drop one and fail at the
					// choke point anyway, so a load failure fails the
					// run here, named — as the CLI's run does.
					retention, err := policycfg.RetentionFromConfig(ctx, r, cfg)
					if err != nil {
						return err
					}
					if err := runPolicyRetentionPrune(ctx, r, retention, pruneMode); err != nil {
						return err
					}
				}
				if hooks.After != "" {
					if err := policycfg.RunHook(ctx, &hookOut, "after", hooks.After, hooks.OnFailureWebhookEnv); err != nil {
						return err
					}
				}
				return nil
			}()
			if runErr != nil {
				policycfg.FireFailureHooks(ctx, &hookOut, name, hooks, runErr)
			}
			// The same notification the CLI's run posts, from the same
			// place — a run announces itself whichever surface ran it.
			policycfg.NotifyBackup(ctx, &hookOut, notifyRun, notifyOn, outcome, runErr)
			if runErr != nil {
				return policyRunDoneMsg{name: name, snapshots: count, skipped: skipped, err: runErr}
			}
			return policyRunDoneMsg{name: name, snapshots: count, skipped: skipped}
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
		// Same rule as the CLI surfaces: apply still reclaims orphaned
		// blobs when retention drops nothing (crashed backups leave
		// chunks no manifest references). The nil-keepIDs bare pass
		// refuses a zero-snapshot store (ErrEmptyRepo) — treat that as
		// the no-op it is rather than failing the whole job run.
		if _, err := r.GC(ctx, nil); err != nil && !errors.Is(err, repo.ErrEmptyRepo) {
			return fmt.Errorf("gc: %w", err)
		}
		return nil
	}
	if len(keep) == 0 {
		return errors.New("policy prune would drop every snapshot; refusing automatic apply")
	}
	for _, id := range drop {
		// Already gone is fine, and so is a pin placed between planning
		// and deleting: the choke point refused it, the snapshot stays,
		// and GC computes its live set from what is present, so nothing
		// of it is reaped. Neither is a reason to fail an unattended run.
		if err := r.DeleteSnapshot(ctx, id); err != nil &&
			!errors.Is(err, blobstore.ErrNotFound) && !errors.Is(err, repo.ErrSnapshotPinned) {
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
// gets the simple confirm — the same gate contract the deleted
// PoliciesView.armRun used.
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
// the App's one-op guard (opName "job-run", scoped so only JobsView's own
// opRejectedMsg case matches its rejected start).
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

// runTimerInstall renders, writes, and activates the selected job's
// scheduler files under the one-op guard. A manual cadence is refused up
// front (mirrors the CLI) without taking the guard — there is nothing to
// run; any render/write/activate error rides back in the jobTimerMsg.
func (v JobsView) runTimerInstall() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	p := v.policies[name]
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		v.notice = fmt.Sprintf("job %q has a manual schedule; set a cadence before installing", name)
		return v, nil
	}
	goos := v.osOverride
	home := v.homeOverride
	exeOverride := v.exeOverride
	runner := v.deps.SchedulerRunner
	run := func(ctx context.Context) tea.Msg {
		// Files alone wait for the next login (launchd) or forever
		// (systemd): InstallFor loads them too. On failure the files stay
		// and the error carries the command to run by hand.
		if _, err := scheduler.InstallFor(ctx, goos, home, exeOverride, cfgPath, name, p.Schedule, runner); err != nil {
			return jobTimerMsg{err: err}
		}
		return jobTimerMsg{notice: fmt.Sprintf("installed timer for %q; now active", name)}
	}
	return v.startBusyOp(jobInstallOpName, fmt.Sprintf("Installing timer for %q…", name), run)
}

// runTimerUninstall unloads and removes the selected job's scheduler
// files under the one-op guard.
func (v JobsView) runTimerUninstall() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	goos := v.osOverride
	home := v.homeOverride
	runner := v.deps.SchedulerRunner
	run := func(ctx context.Context) tea.Msg {
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return jobTimerMsg{err: err}
		}
		// Unload first (systemd resolves `disable` from the unit on disk),
		// then remove the files either way so a headless session can
		// still clean up; the error names the command to finish with.
		deactErr := scheduler.Deactivate(ctx, paths, runner)
		if err := scheduler.Uninstall(paths); err != nil {
			return jobTimerMsg{err: err}
		}
		if deactErr != nil {
			return jobTimerMsg{err: deactErr}
		}
		return jobTimerMsg{notice: fmt.Sprintf("removed timer for %q", name)}
	}
	return v.startBusyOp(jobUninstallOpName, fmt.Sprintf("Removing timer for %q…", name), run)
}

// runDelete removes the selected job: the policy leaves sentra.yaml
// (config.Update, on-disk base) and the timer files are uninstalled —
// in that order, so a half-failure can only leave a policy-less timer
// briefly, never a timer-less zombie policy the table would still show.
// Snapshots are deliberately untouched: data deletion belongs to
// retention/prune, not a config view. Uninstall tolerates absent files,
// so it runs unconditionally. Runs under the one-op guard like the
// other timer mutations.
func (v JobsView) runDelete() (tea.Model, tea.Cmd) {
	row, ok := v.currentJob()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	goos, home := v.osOverride, v.homeOverride
	runner := v.deps.SchedulerRunner
	run := func(ctx context.Context) tea.Msg {
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
		// A job the OS still holds would keep running `policy run` on
		// the deleted name: unload it, then drop the files.
		deactErr := scheduler.Deactivate(ctx, paths, runner)
		if err := scheduler.Uninstall(paths); err != nil {
			return jobTimerMsg{notice: fmt.Sprintf("deleted %q, but removing its timer failed: %v", name, err)}
		}
		if deactErr != nil {
			return jobTimerMsg{notice: fmt.Sprintf("deleted %q; %v", name, deactErr)}
		}
		return jobTimerMsg{notice: fmt.Sprintf("deleted %q — policy and timer removed; snapshots kept", name)}
	}
	return v.startBusyOp(jobDeleteOpName, fmt.Sprintf("Deleting job %q…", name), run)
}
