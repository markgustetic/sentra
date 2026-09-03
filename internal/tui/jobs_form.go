package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// Confirm-modal IDs for JobsView's add/edit form. Named distinctly from
// the deleted PoliciesView's policy-add/policy-replace ids, back when the
// two views coexisted and shared this file's form machinery.
const (
	jobAddConfirmID     = "job-add"
	jobReplaceConfirmID = "job-replace"
	jobEditConfirmID    = "job-edit"
)

// policyForm is the inline ADD form: name + path + optional schedule
// shorthand ("daily@03:00", "manual", …). It stays deliberately minimal —
// the same fields the CLI's `policy add` exposes for the common case;
// power users still edit sentra.yaml directly. A built policy is validated
// with policycfg.Validate before the confirm modal, so a bad entry never
// reaches disk.
//
// JobsView drives it through its own add/edit form; it was moved here
// from policies.go — back when the deleted PoliciesView also drove it
// through its own form — so JobsView's edit-mode prefill
// (prefilledPolicyForm) lives next to the type it fills in.
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

// refocus blurs all four text inputs, focuses the one f.focus names, and
// returns Focus()'s blink cmd — nil on the check/prune steps (4/5), which
// have no cursor to blink. Tab and every other path that puts the keyboard
// on the form go through here so f.focus and the inputs' Focused() cannot
// disagree.
func (f *policyForm) refocus() tea.Cmd {
	f.blurAll()
	switch f.focus {
	case 0:
		return f.name.Focus()
	case 1:
		return f.path.Focus()
	case 2:
		return f.tags.Focus()
	case 3:
		return f.schedule.Focus()
	}
	return nil
}

// blurAll blurs every text input. Leaving the form stage — esc, or a
// confirmed save — renders none of them, and a focused field nobody renders
// keeps its blink chain alive while Focused() lies to every guard.
func (f *policyForm) blurAll() {
	f.name.Blur()
	f.path.Blur()
	f.tags.Blur()
	f.schedule.Blur()
}

// newPolicyForm builds the ADD form with name focused, and also returns
// the cmd Focus() produced — the caller (the 'a' key handler) needs it to
// start the cursor blinking. Focusing here is fine where a view's
// constructor must not: the form is rebuilt fresh on every 'a' press,
// inside a view that is already on screen, so its field is never focused
// off screen.
func newPolicyForm() (policyForm, tea.Cmd) {
	name := textinput.New()
	name.Prompt = "name>     "
	name.Placeholder = "policy name"
	// Focus()'s own return is the real, tag-matched blink cmd; the
	// textinput.Blink sentinel resolves to cursor's unexported bootstrap
	// message, which no Update switch can name, so it is a dead end.
	cmd := name.Focus()
	path := textinput.New()
	path.Prompt = "paths>    "
	path.Placeholder = "directories to back up, comma-separated"
	tags := textinput.New()
	tags.Prompt = "tags>     "
	tags.Placeholder = "optional tags, comma-separated"
	schedule := textinput.New()
	schedule.Prompt = "schedule> "
	schedule.Placeholder = "manual | daily@03:00 | weekly@mon:03:00"
	return policyForm{name: name, path: path, tags: tags, schedule: schedule, prune: policycfg.PruneOff}, cmd
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

// errPolicyExists signals the replace-confirm gate from inside
// JobsView.saveForm's config.Update closure: the duplicate check runs
// against the on-disk map, the same base `policy add` checks. Ported from
// the deleted PoliciesView.addFromForm, which used the same sentinel the
// same way.
var errPolicyExists = errors.New("policy exists")

// prefilledPolicyForm seeds the shared form from an existing policy for
// edit mode, returning the form and the blink cmd its focused field
// produced. Focus starts on paths: the name is identity, not content —
// renaming would orphan the timer files and the policy:<name> snapshot
// tags, so edit keeps it read-only (rename = delete + re-add).
func prefilledPolicyForm(name string, p config.PolicyConfig) (policyForm, tea.Cmd) {
	f, _ := newPolicyForm() // name's blink cmd is discarded: refocus blurs it below
	f.name.SetValue(name)
	f.path.SetValue(strings.Join(p.Paths, ", "))
	f.tags.SetValue(strings.Join(p.Tags, ", "))
	f.schedule.SetValue(policycfg.FormatScheduleSpec(p.Schedule))
	f.check = p.AfterBackup.Check
	f.prune = policyPruneModeOrOff(p.AfterBackup.Prune)
	f.focus = 1
	// path is the field edit lands on, so its Focus() cmd is the one the
	// 'e' handler must return to start the blink.
	cmd := f.refocus()
	return f, cmd
}

// updateForm drives JobsView's add/edit form: tab cycles focus (skipping
// the read-only name field in edit mode), space toggles the check/prune
// fields at focus 4/5, esc abandons the form, and enter validates + pushes
// the appropriate confirm (job-edit in edit mode, job-add otherwise). It is
// a port of PoliciesView.updateForm with those two edit-mode differences.
func (v JobsView) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		v.stage = jobsList
		v.editName = ""
		v.form.blurAll()
		return v, nil
	case tea.KeyTab:
		v.form.focus = (v.form.focus + 1) % policyFormFields
		if v.editName != "" && v.form.focus == 0 {
			v.form.focus = 1
		}
		cmd := v.form.refocus()
		return v, cmd
	case tea.KeyEnter:
		name, _, err := v.form.build()
		if err != nil {
			v.form.err = err.Error()
			return v, nil
		}
		if v.editName != "" {
			body := fmt.Sprintf("Save changes to job %q?", name)
			modal := NewConfirmModal("Confirm edit", body, jobEditConfirmID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		}
		body := fmt.Sprintf("Add job %q to sentra.yaml?\nThis edits local config only.", name)
		modal := NewConfirmModal("Confirm add", body, jobAddConfirmID, 80, 24)
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

// saveForm rebuilds + revalidates the form, writes the policy into
// sentra.yaml, and reloads. Config-only: no repo lock, no op guard — a port
// of PoliciesView.addFromForm, shared by both JobsView's ADD and EDIT
// confirms. replace=false refuses an existing name (pushing the replace
// confirm); replace=true overwrites while carrying the existing policy's
// config-authored Hooks forward, matching `policy add --replace`.
//
// Edit mode always calls this with replace=true: the job being edited
// already exists under this name (the name field is read-only, so it can't
// have changed), and the edit confirm already asked "save changes?" — a
// second replace confirm would be redundant. Whenever the save overwrote
// an existing job — EDIT, or ADD routed through the replace confirm — it
// reconciles the OS timer via syncTimerAfterSave, using the schedule spec
// captured from the on-disk policy before the overwrite; without that, a
// replace-via-add would leave an installed timer firing on the old
// cadence while sentra.yaml says the new one.
func (v JobsView) saveForm(replace bool) (tea.Model, tea.Cmd) {
	name, p, err := v.form.build()
	if err != nil {
		v.stage = jobsForm
		v.form.err = err.Error()
		return v, nil
	}
	editing := v.editName != ""
	var oldSpec string
	var replaced bool
	// config.Update rewrites against the on-disk sentra.yaml, so saving a
	// job can't persist this process's SENTRA_* overrides into repo.s3.
	err = config.Update(v.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if existing, exists := cfg.Policies[name]; exists {
			if !replace {
				return errPolicyExists
			}
			oldSpec = policycfg.FormatScheduleSpec(existing.Schedule)
			replaced = true
			p.Hooks = existing.Hooks
		}
		cfg.Policies[name] = p
		return nil
	})
	if errors.Is(err, errPolicyExists) {
		body := fmt.Sprintf("Job %q already exists.\nReplace it? Config-authored hooks are preserved.", name)
		modal := NewConfirmModal("Replace job", body, jobReplaceConfirmID, 80, 24)
		return v, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	if err != nil {
		// Covers both a bad on-disk base and a failed write; the wrapped
		// error names which.
		v.notice = "save failed: " + err.Error()
		v.stage = jobsList
		v.editName = ""
		v.form.blurAll()
		return v, nil
	}
	v.stage = jobsList
	v.editName = ""
	v.form.blurAll()
	notice := fmt.Sprintf("added %q", name)
	switch {
	case editing:
		notice = fmt.Sprintf("saved %q", name)
	case replaced:
		notice = fmt.Sprintf("replaced %q", name)
	}
	if editing || replaced {
		syncNotice, syncErr := v.syncTimerAfterSave(name, oldSpec, p)
		switch {
		case syncErr != nil:
			notice = syncErr.Error()
		case syncNotice != "":
			notice = syncNotice
		}
	}
	v.reload()
	v.notice = notice
	return v, nil
}

// syncTimerAfterSave reconciles the OS timer after a save that overwrote
// an existing job (edit, or add-with-replace): not installed -> nothing;
// unchanged spec -> nothing; new cadence manual -> uninstall (a manual
// job must not keep firing on the old cadence); otherwise re-render +
// reinstall. Returns a human notice.
func (v JobsView) syncTimerAfterSave(name, oldSpec string, p config.PolicyConfig) (string, error) {
	newSpec := policycfg.FormatScheduleSpec(p.Schedule)
	if newSpec == oldSpec {
		return "", nil
	}
	paths, err := scheduler.PathsFor(v.osOverride, v.homeOverride, name)
	if err != nil {
		return "", err
	}
	installed, err := scheduler.Installed(paths)
	if err != nil || !installed {
		return "", err
	}
	ctx, runner := ctxOrBackground(v.deps.Ctx), v.deps.SchedulerRunner
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		// Unload before removing the files, or the OS keeps firing the
		// old cadence until logout.
		if err := scheduler.Deactivate(ctx, paths, runner); err != nil {
			return "", err
		}
		if err := scheduler.Uninstall(paths); err != nil {
			return "", err
		}
		return "timer uninstalled (schedule is now manual)", nil
	}
	exe, err := scheduler.Executable(v.exeOverride)
	if err != nil {
		return "", err
	}
	files, err := scheduler.Render(paths, exe, v.deps.ConfigPath, name, p.Schedule)
	if err != nil {
		return "", err
	}
	if err := scheduler.Install(files); err != nil {
		return "", err
	}
	// launchd keeps running the OLD plist until the label is bootstrapped
	// again; systemd needs a daemon-reload to see the new OnCalendar.
	if err := scheduler.Activate(ctx, paths, runner); err != nil {
		return "", err
	}
	return "timer reinstalled for " + newSpec, nil
}
