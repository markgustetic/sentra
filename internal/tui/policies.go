package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// policiesStage tracks the Policies view's position. The read-only skeleton
// only uses policiesList; the ADD form and RUN flow add running/form stages
// in later tasks.
type policiesStage int

const (
	policiesList policiesStage = iota
	policiesForm
	policiesRunning
	policiesRunDone
)

// Confirm-modal IDs tie a pushed modal back to this view. ADD and REMOVE
// use the simple ConfirmModal (config-only, reversible edits); RUN uses the
// simple or TYPED confirm depending on the policy's prune mode.
const (
	policyAddConfirmID     = "policy-add"
	policyReplaceConfirmID = "policy-replace"
	policyRemoveConfirmID  = "policy-remove"
	policyRunConfirmID     = "policy-run"
)

// PoliciesView lists the named backup policies from sentra.yaml, shows the
// selected one inline, and drives three actions: ADD/edit and REMOVE are
// config-only (they rewrite sentra.yaml via config.Update and reload — NO
// repo lock, NO op guard), while RUN a policy takes the mutating-op guard
// (it calls repo.CreateSnapshot per path). The view hydrates by loading
// deps.ConfigPath, the same way PruneView hydrates from the repo.
type PoliciesView struct {
	deps     Deps
	stage    policiesStage
	names    []string
	policies map[string]config.PolicyConfig
	selected int
	loadErr  string
	notice   string // transient banner (op rejection, reload error)
	width    int

	// form + run state are declared here but only driven by later tasks.
	form   policyForm
	run    policyRunState
	result policyRunDoneMsg
}

func NewPoliciesView(deps Deps) PoliciesView {
	v := PoliciesView{deps: deps}
	if deps.ConfigPath == "" {
		v.loadErr = "no config file configured"
		return v
	}
	v.reload()
	return v
}

// reload re-reads deps.ConfigPath and repopulates the sorted name list and
// policy map. Called at construction and after every config.Update so the
// picker reflects the file on disk. A load error is surfaced as loadErr
// (construction) or notice (post-edit) by the caller; reload itself only
// sets loadErr because it is also the construction path.
func (v *PoliciesView) reload() {
	cfg, err := config.Load(v.deps.ConfigPath)
	if err != nil {
		v.loadErr = err.Error()
		return
	}
	v.loadErr = ""
	v.policies = cfg.Policies
	v.names = make([]string, 0, len(cfg.Policies))
	for name := range cfg.Policies {
		v.names = append(v.names, name)
	}
	sort.Strings(v.names)
	if v.selected >= len(v.names) {
		v.selected = len(v.names) - 1
	}
	if v.selected < 0 {
		v.selected = 0
	}
}

func (PoliciesView) Init() tea.Cmd { return nil }

// ConsumesArrows: the list stage moves a cursor over named policies. The form
// stage is text entry (see CapturesText); the run stages take single keys.
func (v PoliciesView) ConsumesArrows() bool {
	return v.stage == policiesList && len(v.names) > 0
}

func (v PoliciesView) Title() string { return "Policies" }

// CapturesText is true only on the add-form stage, where the name/path/schedule
// text inputs are focused and tab moves between them. The list stage uses
// single-key commands (a/d/r, arrows) and must keep the globals, so it does not
// capture; the running/done stages have no input.
func (v PoliciesView) CapturesText() bool { return v.stage == policiesForm }

// ConsumesEscape: esc abandons the add/edit form. The list stage leaves it to
// the shell.
func (v PoliciesView) ConsumesEscape() bool { return v.stage == policiesForm }

func (v PoliciesView) ShortHelp() []key.Binding {
	if v.stage != policiesList || len(v.names) == 0 {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
	}
}

func (v PoliciesView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case tea.KeyMsg:
		switch v.stage {
		case policiesForm:
			return v.updateForm(msg)
		case policiesList:
			switch msg.Type {
			case tea.KeyUp:
				if v.selected > 0 {
					v.selected--
				}
				v.notice = ""
				return v, nil
			case tea.KeyDown:
				if v.selected < len(v.names)-1 {
					v.selected++
				}
				v.notice = ""
				return v, nil
			case tea.KeyRunes:
				if len(msg.Runes) != 1 {
					return v, nil
				}
				switch msg.Runes[0] {
				case 'a':
					v.stage = policiesForm
					v.form = newPolicyForm()
					v.notice = ""
					// newPolicyForm focuses the name field (policies.go
					// below) — this keypress is the form's first-focus
					// activation, so it must start the blink.
					return v, textinput.Blink
				case 'd':
					if len(v.names) > 0 {
						name := v.names[v.selected]
						body := fmt.Sprintf("Remove policy %q from sentra.yaml?\nThis edits local config only — no snapshots are touched.", name)
						modal := NewConfirmModal("Confirm remove", body, policyRemoveConfirmID, 80, 24)
						return v, func() tea.Msg { return pushModalMsg{modal: modal} }
					}
				case 'r':
					if len(v.names) > 0 {
						return v.armRun()
					}
				}
				return v, nil
			}
			return v, nil
		case policiesRunDone:
			if msg.Type == tea.KeyEnter {
				v.stage = policiesList
				v.notice = ""
				return v, nil
			}
			return v, nil
		case policiesRunning:
			return v, nil
		default:
			return v, nil
		}

	case confirmedMsg:
		switch msg.id {
		case policyRemoveConfirmID:
			return v.removeSelected()
		case policyAddConfirmID:
			return v.addFromForm(false)
		case policyReplaceConfirmID:
			return v.addFromForm(true)
		case policyRunConfirmID:
			return v.startRun()
		}
		return v, nil

	case policyRunDoneMsg:
		v.stage = policiesRunDone
		v.result = msg
		v.reload() // retention prune may have changed nothing on disk, but
		// keeps the view consistent if a future action mutates config.
		return v, nil

	case opRejectedMsg:
		if v.stage == policiesRunning && msg.name == "policy-run" {
			v.stage = policiesList
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == policiesRunning {
			return v, opTick()
		}
		return v, nil

	case cursor.BlinkMsg:
		// Only the form stage has focused text fields, and at most one of
		// name/path/tags/schedule is focused at a time (the check/prune
		// toggle steps have no cursor to blink).
		if v.stage != policiesForm {
			return v, nil
		}
		var cmd tea.Cmd
		switch {
		case v.form.name.Focused():
			v.form.name, cmd = v.form.name.Update(msg)
		case v.form.path.Focused():
			v.form.path, cmd = v.form.path.Update(msg)
		case v.form.tags.Focused():
			v.form.tags, cmd = v.form.tags.Update(msg)
		case v.form.schedule.Focused():
			v.form.schedule, cmd = v.form.schedule.Update(msg)
		}
		return v, cmd
	}
	return v, nil
}

func (v PoliciesView) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		v.stage = policiesList
		return v, nil
	case tea.KeyTab:
		v.form.focus = (v.form.focus + 1) % policyFormFields
		v.form.name.Blur()
		v.form.path.Blur()
		v.form.tags.Blur()
		v.form.schedule.Blur()
		switch v.form.focus {
		case 0:
			v.form.name.Focus()
			return v, textinput.Blink
		case 1:
			v.form.path.Focus()
			return v, textinput.Blink
		case 2:
			v.form.tags.Focus()
			return v, textinput.Blink
		case 3:
			v.form.schedule.Focus()
			return v, textinput.Blink
		}
		// Landed on the check/prune toggle steps: no text field is
		// focused, so no blink to (re)start.
		return v, nil
	case tea.KeyEnter:
		name, _, err := v.form.build()
		if err != nil {
			v.form.err = err.Error()
			return v, nil
		}
		body := fmt.Sprintf("Add policy %q to sentra.yaml?\nThis edits local config only.", name)
		modal := NewConfirmModal("Confirm add", body, policyAddConfirmID, 80, 24)
		return v, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	if isSpace && v.form.focus >= 4 {
		switch v.form.focus {
		case 4:
			v.form.check = !v.form.check
		case 5:
			// Cycle the prune mode the way `policy add --prune` enumerates it.
			switch v.form.prune {
			case policycfg.PruneOff:
				v.form.prune = policycfg.PruneDryRun
			case policycfg.PruneDryRun:
				v.form.prune = policycfg.PruneApply
			default:
				v.form.prune = policycfg.PruneOff
			}
		}
		return v, nil
	}
	var cmd tea.Cmd
	switch v.form.focus {
	case 0:
		v.form.name, cmd = v.form.name.Update(msg)
	case 1:
		v.form.path, cmd = v.form.path.Update(msg)
	case 2:
		v.form.tags, cmd = v.form.tags.Update(msg)
	case 3:
		v.form.schedule, cmd = v.form.schedule.Update(msg)
	}
	v.form.err = "" // typing clears the last validation error
	return v, cmd
}

// errPolicyExists signals the replace-confirm gate from inside the
// config.Update closure: the duplicate check runs against the on-disk
// map, the same base `policy add` checks.
var errPolicyExists = errors.New("policy exists")

// addFromForm rebuilds + revalidates the form, writes the new policy into
// sentra.yaml, and reloads. Config-only: no repo lock, no op guard.
// replace=false refuses an existing name (pushing the replace confirm);
// replace=true overwrites while carrying the existing policy's
// config-authored Hooks forward, matching `policy add --replace`.
func (v PoliciesView) addFromForm(replace bool) (tea.Model, tea.Cmd) {
	name, p, err := v.form.build()
	if err != nil {
		v.stage = policiesForm
		v.form.err = err.Error()
		return v, nil
	}
	// config.Update rewrites against the on-disk sentra.yaml, so adding a
	// policy can't persist this process's SENTRA_* overrides into repo.s3.
	err = config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if existing, exists := cfg.Policies[name]; exists {
			if !replace {
				return errPolicyExists
			}
			p.Hooks = existing.Hooks
		}
		cfg.Policies[name] = p
		return nil
	})
	if errors.Is(err, errPolicyExists) {
		body := fmt.Sprintf("Policy %q already exists.\nReplace it? Config-authored hooks are preserved.", name)
		modal := NewConfirmModal("Replace policy", body, policyReplaceConfirmID, 80, 24)
		return v, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	if err != nil {
		// Covers both a bad on-disk base and a failed write; the wrapped
		// error names which.
		v.notice = "save failed: " + err.Error()
		v.stage = policiesList
		return v, nil
	}
	v.stage = policiesList
	v.reload()
	v.notice = fmt.Sprintf("added %q", name)
	return v, nil
}

// armRun pushes the RUN confirmation modal for the selected policy. A
// prune mode of "apply" is destructive (it deletes snapshots + GCs), so it
// gets the TYPED confirm; every other mode (off, dry-run, or check-only)
// gets the simple confirm. The modal id is policyRunConfirmID either way,
// so the confirmedMsg handler starts the op regardless of which was shown.
//
// It validates the policy first (mirroring the CLI's runPolicy, which calls
// policycfg.Validate before doing any work). This is load-bearing for the
// confirm gate: policyPruneModeOrOff only lowercases/trims, so a corrupted
// prune mode (a typo like "aply", a stale hand-edit) is NOT PruneApply and
// would otherwise get the SIMPLE confirm — which never mentions deletion —
// while the run would still reach the delete path. Refusing an invalid
// policy here keeps a destructive run from ever hiding behind the
// non-destructive-looking confirm.
func (v PoliciesView) armRun() (tea.Model, tea.Cmd) {
	name := v.names[v.selected]
	p := v.policies[name]
	if err := policycfg.Validate(name, p); err != nil {
		v.notice = "cannot run: " + err.Error()
		return v, nil
	}
	mode := policyPruneModeOrOff(p.AfterBackup.Prune)
	var modal Modal
	if mode == policycfg.PruneApply {
		body := fmt.Sprintf("Run policy %q now?\nAfter backup it will DELETE snapshots outside the retention policy and reclaim their chunks.", name)
		modal = NewTypedConfirmModal("Confirm policy run", body, "run", policyRunConfirmID, 80, 24)
	} else {
		body := fmt.Sprintf("Run policy %q now?\nThis creates a snapshot for each of its paths.", name)
		modal = NewConfirmModal("Confirm policy run", body, policyRunConfirmID, 80, 24)
	}
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startRun launches the selected policy under the App op guard. The run
// closure walks the CLI's runPolicy sequence: CreateSnapshot per path,
// optional Check, optional retention prune. It honors ctx cancellation
// (CreateSnapshot/Check/GC all take ctx) and returns policyRunDoneMsg,
// which implements opResult() so the guard clears.
//
// Retention limits come from deps.Config (the resolved config, same source
// PruneView reads). GC's live set is still derived from the manifests
// present under the repo lock — keepIDs only marks the deliberate-prune
// path, exactly as the CLI and PruneView do.
func (v PoliciesView) startRun() (tea.Model, tea.Cmd) {
	if v.deps.Repo == nil {
		v.notice = "no repository configured"
		return v, nil
	}
	name := v.names[v.selected]
	p := v.policies[name]
	r := v.deps.Repo
	reporter := newOpReporter()
	v.run = policyRunState{reporter: reporter, name: name}
	v.stage = policiesRunning

	var wopts walker.Options
	var retention repo.RetentionPolicy
	if v.deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    v.deps.Config.Backup.IgnoreFile,
			ExcludeCaches: v.deps.Config.Backup.ExcludeCaches,
			Concurrency:   v.deps.Config.Backup.Concurrency,
		}
		retention = repo.RetentionPolicy{
			KeepLast:    v.deps.Config.Retention.KeepLast,
			KeepDaily:   v.deps.Config.Retention.KeepDaily,
			KeepWeekly:  v.deps.Config.Retention.KeepWeekly,
			KeepMonthly: v.deps.Config.Retention.KeepMonthly,
		}
	}
	paths := append([]string(nil), p.Paths...)
	tag := policyRunTag(name, p.Tags)
	doCheck := p.AfterBackup.Check
	pruneMode := policyPruneModeOrOff(p.AfterBackup.Prune)
	hooks := p.Hooks

	start := startOpMsg{
		name: "policy-run",
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
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
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

// removeSelected deletes the selected policy from sentra.yaml and reloads.
// This is a config-only edit: it rewrites the file via config.Update and
// never takes the repo lock or the op guard, matching `sentra policy remove`.
func (v PoliciesView) removeSelected() (tea.Model, tea.Cmd) {
	if v.selected < 0 || v.selected >= len(v.names) {
		return v, nil
	}
	name := v.names[v.selected]
	// On-disk base, as in addFromForm: dropping a policy must not persist
	// this process's SENTRA_* overrides into repo.s3.
	err := config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		delete(cfg.Policies, name)
		return nil
	})
	if err != nil {
		v.notice = "save failed: " + err.Error()
		return v, nil
	}
	v.reload()
	v.notice = fmt.Sprintf("removed %q", name)
	return v, nil
}

func (v PoliciesView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	if v.stage == policiesForm {
		var b strings.Builder
		fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("New policy"))
		// The box IS the focus affordance: only the field tab currently
		// owns carries the frame.
		boxed := func(f textinput.Model) string {
			s := f.View()
			if f.Focused() {
				s = ui.FieldBox.Render(s)
			}
			return s
		}
		fmt.Fprintf(&b, "%s\n", boxed(v.form.name))
		fmt.Fprintf(&b, "%s\n", boxed(v.form.path))
		fmt.Fprintf(&b, "%s\n", boxed(v.form.tags))
		fmt.Fprintf(&b, "%s\n", boxed(v.form.schedule))
		checkMark, pruneMark := "[ ]", "  "
		if v.form.check {
			checkMark = "[x]"
		}
		checkRow := fmt.Sprintf("%s check after backup", checkMark)
		pruneRow := fmt.Sprintf("%s prune after backup: %s", pruneMark, v.form.prune)
		checkRow = ui.SelectRow(v.form.focus == 4, checkRow)
		pruneRow = ui.SelectRow(v.form.focus == 5, pruneRow)
		fmt.Fprintf(&b, "%s\n%s\n", checkRow, pruneRow)
		if v.form.err != "" {
			fmt.Fprintf(&b, "\n%s\n", ui.Danger.Render(v.form.err))
		}
		fmt.Fprintf(&b, "\n%s", ui.ActionLine("save the policy", "tab field · esc cancel"))
		return b.String()
	}
	if v.stage == policiesRunning {
		var b strings.Builder
		b.WriteString(ui.Primary.Render("Running policy " + v.run.name + "…"))
		if v.run.reporter != nil {
			total, done := v.run.reporter.Snapshot()
			fmt.Fprintf(&b, "\n\n  %s / %s uploaded", ui.FormatBytes(done), ui.FormatBytes(total))
		}
		return b.String()
	}
	if v.stage == policiesRunDone {
		var b strings.Builder
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Policy run failed"))
			fmt.Fprintf(&b, "\n\n%s", v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Policy run complete"))
			fmt.Fprintf(&b, "\n\n  policy     %s\n  snapshots  %d", v.result.name, v.result.snapshots)
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("return to the policy list", ""))
		return b.String()
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Backup policies"))
	if v.notice != "" {
		fmt.Fprintf(&b, "  %s", ui.Warn.Render(v.notice))
	}
	b.WriteString("\n\n")
	if len(v.names) == 0 {
		b.WriteString(ui.Muted.Render("No policies configured."))
		return b.String()
	}
	for i, name := range v.names {
		marker := "  "
		label := name
		if i == v.selected {
			marker = ui.Primary.Render("▸ ")
			label = ui.Primary.Render(name)
		}
		p := v.policies[name]
		fmt.Fprintf(&b, "%s%s  %s\n", marker, label,
			ui.Muted.Render(policycfg.FormatScheduleSpec(p.Schedule)))
	}
	fmt.Fprintf(&b, "\n%s", v.renderDetail())
	fmt.Fprintf(&b, "\n%s", ui.Muted.Render("↑↓ select · r run · d remove"))
	return b.String()
}

// renderDetail shows the selected policy read-only. Inline empty->"-"
// substitution here rather than importing cli's emptyDash (which stays put
// per the extraction contract).
func (v PoliciesView) renderDetail() string {
	if v.selected < 0 || v.selected >= len(v.names) {
		return ""
	}
	name := v.names[v.selected]
	p := v.policies[name]
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", ui.Primary.Render(name))
	b.WriteString("  paths:\n")
	for _, path := range p.Paths {
		fmt.Fprintf(&b, "    - %s\n", path)
	}
	fmt.Fprintf(&b, "  tags:     %s\n", dash(strings.Join(p.Tags, ", ")))
	fmt.Fprintf(&b, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	fmt.Fprintf(&b, "  check:    %t\n", p.AfterBackup.Check)
	fmt.Fprintf(&b, "  prune:    %s", policyPruneModeOrOff(p.AfterBackup.Prune))
	return b.String()
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

// policyForm is the inline ADD form: name + path + optional schedule
// shorthand ("daily@03:00", "manual", …). It stays deliberately minimal —
// the same fields the CLI's `policy add` exposes for the common case;
// power users still edit sentra.yaml directly. A built policy is validated
// with policycfg.Validate before the confirm modal, so a bad entry never
// reaches disk.
type policyForm struct {
	name     textinput.Model
	path     textinput.Model
	tags     textinput.Model
	schedule textinput.Model
	// check and prune mirror `policy add`'s --check / --prune so the
	// TUI form carries the same policy shape as the CLI.
	check bool
	prune string // off | dry-run | apply
	focus int    // 0=name, 1=path, 2=tags, 3=schedule, 4=check, 5=prune
	err   string
}

// policyFormFields is the tab cycle length: four text inputs plus the
// check toggle and the prune-mode cycle.
const policyFormFields = 6

func newPolicyForm() policyForm {
	name := textinput.New()
	name.Prompt = "name>     "
	name.Placeholder = "policy name"
	name.Focus()
	path := textinput.New()
	path.Prompt = "paths>    "
	path.Placeholder = "directories to back up, comma-separated"
	tags := textinput.New()
	tags.Prompt = "tags>     "
	tags.Placeholder = "optional tags, comma-separated"
	schedule := textinput.New()
	schedule.Prompt = "schedule> "
	schedule.Placeholder = "manual | daily@03:00 | weekly@mon:03:00"
	return policyForm{name: name, path: path, tags: tags, schedule: schedule, prune: policycfg.PruneOff}
}

// splitCommaList turns a comma-separated field into trimmed entries,
// dropping empties — "a, b," parses as ["a", "b"].
func splitCommaList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// build assembles a config.PolicyConfig from the form and validates it.
// Returns the built name + policy, or a non-nil error to display inline.
func (f policyForm) build() (string, config.PolicyConfig, error) {
	name := strings.TrimSpace(f.name.Value())
	spec := strings.TrimSpace(f.schedule.Value())
	if spec == "" {
		spec = policycfg.CadenceManual
	}
	sched, err := policycfg.ParseScheduleSpec(spec)
	if err != nil {
		return "", config.PolicyConfig{}, err
	}
	p := config.PolicyConfig{
		Paths:    splitCommaList(f.path.Value()),
		Tags:     splitCommaList(f.tags.Value()),
		Schedule: sched,
		AfterBackup: config.PolicyAfterBackup{
			Check: f.check,
			Prune: f.prune,
		},
	}
	if err := policycfg.Validate(name, p); err != nil {
		return "", config.PolicyConfig{}, err
	}
	return name, p, nil
}

type policyRunState struct {
	reporter *opReporter
	name     string
}

// policyRunDoneMsg is the RUN flow's terminal, guard-clearing message.
// Defined here (the struct field references it); the RUN task fills its
// body and the startOpMsg that produces it.
type policyRunDoneMsg struct {
	name      string
	snapshots int
	err       error
}

func (policyRunDoneMsg) opResult() {}
