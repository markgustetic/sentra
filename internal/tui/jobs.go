package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/ui"
)

// normalizeJobPath resolves a policy path the way the walker records
// snapshot roots (filepath.Abs + Clean), expanding a leading ~ against
// home, so job paths and SnapshotInfo.Root compare equal.
func normalizeJobPath(p, home string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return filepath.Clean(abs)
}

// hasPolicyTag reports whether the space-joined tag string carries the
// exact token "policy:<name>" — token equality, not substring, so
// "policy:home" never matches job "hom" or tag "my-policy:home".
func hasPolicyTag(tag, name string) bool {
	want := "policy:" + name
	for _, f := range strings.Fields(tag) {
		if f == want {
			return true
		}
	}
	return false
}

// newestJobSnapshot is the drill-in resolver: the newest snapshot rooted
// at pathAbs, preferring ones tagged policy:<name> — an ad-hoc backup of
// the same directory must not shadow the job's own snapshots, but with
// no tagged snapshot yet (the ctrl+e repeat flow's first backup runs
// under the user's tag) any snapshot of the path is the honest answer.
func newestJobSnapshot(name, pathAbs string, snaps []repo.SnapshotInfo) (repo.SnapshotInfo, bool) {
	var best repo.SnapshotInfo
	var found, tagged bool
	for _, s := range snaps {
		if s.Root != pathAbs {
			continue
		}
		st := hasPolicyTag(s.Tag, name)
		switch {
		case st && !tagged:
			best, found, tagged = s, true, true
		case st == tagged && (!found || s.CreatedAt.After(best.CreatedAt)):
			best, found = s, true
		}
	}
	return best, found
}

// lastJobRun is the Last-run column: the newest snapshot tagged
// policy:<name> anywhere (a run is a run, even for a path since edited
// out), falling back to the newest snapshot rooted at any of the job's
// paths when the job has never run under its own tag.
func lastJobRun(name string, pathsAbs []string, snaps []repo.SnapshotInfo) (repo.SnapshotInfo, bool) {
	var best repo.SnapshotInfo
	found := false
	for _, s := range snaps {
		if hasPolicyTag(s.Tag, name) && (!found || s.CreatedAt.After(best.CreatedAt)) {
			best, found = s, true
		}
	}
	if found {
		return best, true
	}
	roots := make(map[string]bool, len(pathsAbs))
	for _, p := range pathsAbs {
		roots[p] = true
	}
	for _, s := range snaps {
		if roots[s.Root] && (!found || s.CreatedAt.After(best.CreatedAt)) {
			best, found = s, true
		}
	}
	return best, found
}

// relAge renders a compact "how long ago" for the Last-run column.
func relAge(t, now time.Time) string {
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// jobsStage tracks the JobsView's position. jobsList is the table;
// jobsDetail is the drill-in; jobsForm hosts add/edit; the run stages
// mirror the old PoliciesView run flow.
type jobsStage int

const (
	jobsList jobsStage = iota
	jobsDetail
	jobsForm
	jobsRunning
	jobsRunDone
)

// jobRow is one policy's line in the jobs table.
type jobRow struct {
	name      string
	spec      string
	manual    bool
	installed bool
	next      time.Time
	nextOK    bool
	lastID    string
	lastAt    time.Time
}

// JobsView is the scheduled-backups manager: every named policy with
// its cadence, timer state, computed next run, and last run, plus
// drill-in / add / edit / run / install / uninstall / delete. It
// replaces the split Policies and Schedule views — see
// docs/superpowers/specs/2026-08-29-jobs-view-design.md.
type JobsView struct {
	deps     Deps
	stage    jobsStage
	tbl      table.Model
	rows     []jobRow
	policies map[string]config.PolicyConfig
	names    []string
	snaps    []repo.SnapshotInfo
	loadErr  string
	notice   string
	width    int
	height   int

	// run + result carry the in-flight/most-recent run-now state, shared
	// with PoliciesView via buildPolicyRunOp (see jobs_run.go).
	run    policyRunState
	result policyRunDoneMsg

	// form + editName drive the add/edit stage (see jobs_form.go).
	// editName is non-empty in edit mode, which keeps the name field
	// read-only (never focused) and reconciles the OS timer on save.
	form     policyForm
	editName string

	// Test seams. osOverride/homeOverride/exeOverride pin the scheduler
	// platform/home/executable (zero values fall back to runtime); now
	// pins the clock for next-run/last-run rendering; homeDir feeds ~
	// expansion in normalizeJobPath.
	osOverride   string
	homeOverride string
	exeOverride  string
	now          func() time.Time
	homeDir      func() (string, error)
}

func NewJobsView(deps Deps) JobsView {
	v := JobsView{
		deps:    deps,
		now:     time.Now,
		homeDir: os.UserHomeDir,
	}
	v.tbl = table.New(
		table.WithColumns(jobsColumns(pickerIdealWidth)),
		table.WithFocused(true),
	)
	snaps, _ := initialSnapshots(deps)
	v.snaps = snaps
	v.reload()
	return v
}

func (JobsView) Init() tea.Cmd { return nil }

func (v JobsView) Title() string { return "Scheduled backups" }

func (v JobsView) ConsumesArrows() bool {
	return (v.stage == jobsList && len(v.rows) > 0) || v.stage == jobsDetail
}

// CapturesText is true only on the add/edit form stage, where the
// name/path/tags/schedule text inputs are focused and tab moves between
// them. The list stage uses single-key commands and must keep the globals.
func (v JobsView) CapturesText() bool { return v.stage == jobsForm }

// ConsumesEscape: esc abandons the add/edit form, or backs out of the
// drill-in detail page.
func (v JobsView) ConsumesEscape() bool {
	return v.stage == jobsForm || v.stage == jobsDetail
}

// jobsHome resolves the home dir used for both ~ expansion and the
// scheduler stat, honoring the test override.
func (v JobsView) jobsHome() string {
	if v.homeOverride != "" {
		return v.homeOverride
	}
	home, err := v.homeDir()
	if err != nil {
		return ""
	}
	return home
}

// reload rebuilds rows from the on-disk config (falling back to
// deps.Config when no path is configured — bare test fixtures), re-stats
// each policy's timer files, and recomputes next/last run. Called at
// construction, after every action, and on R.
func (v *JobsView) reload() {
	var policies map[string]config.PolicyConfig
	switch {
	case v.deps.ConfigPath != "":
		cfg, err := config.Load(v.deps.ConfigPath)
		if err != nil {
			v.loadErr = err.Error()
			return
		}
		policies = cfg.Policies
	case v.deps.Config != nil:
		policies = v.deps.Config.Policies
	default:
		v.loadErr = "no config file configured"
		return
	}
	v.loadErr = ""
	v.policies = policies
	v.names = make([]string, 0, len(policies))
	for name := range policies {
		v.names = append(v.names, name)
	}
	sort.Strings(v.names)

	home := v.jobsHome()
	nowT := v.now()
	rows := make([]jobRow, 0, len(v.names))
	tblRows := make([]table.Row, 0, len(v.names))
	for _, name := range v.names {
		p := policies[name]
		norm := policycfg.NormalizeSchedule(p.Schedule)
		row := jobRow{
			name:   name,
			spec:   policycfg.FormatScheduleSpec(p.Schedule),
			manual: norm.Cadence == policycfg.CadenceManual,
		}
		if !row.manual {
			if paths, err := scheduler.PathsFor(v.osOverride, v.homeOverride, name); err == nil {
				if installed, sErr := scheduler.Installed(paths); sErr == nil {
					row.installed = installed
				}
			}
		}
		if row.installed {
			row.next, row.nextOK = policycfg.NextRun(p.Schedule, nowT)
		}
		abs := make([]string, 0, len(p.Paths))
		for _, path := range p.Paths {
			abs = append(abs, normalizeJobPath(path, home))
		}
		if last, ok := lastJobRun(name, abs, v.snaps); ok {
			row.lastID, row.lastAt = last.ID, last.CreatedAt
		}
		rows = append(rows, row)
		tblRows = append(tblRows, table.Row{
			name, row.spec, jobTimerLabel(row), jobNextLabel(row), jobLastLabel(row, nowT),
		})
	}
	v.rows = rows
	cursor := v.tbl.Cursor()
	v.tbl.SetRows(tblRows)
	if cursor >= len(tblRows) {
		cursor = len(tblRows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	v.tbl.SetCursor(cursor)
}

func jobTimerLabel(r jobRow) string {
	switch {
	case r.manual:
		return "—"
	case r.installed:
		return "installed"
	default:
		return "not installed"
	}
}

func jobNextLabel(r jobRow) string {
	if !r.installed || !r.nextOK {
		return "—"
	}
	return r.next.Format("Jan 2 15:04")
}

func jobLastLabel(r jobRow, now time.Time) string {
	if r.lastID == "" {
		return "never"
	}
	return relAge(r.lastAt, now)
}

func (v JobsView) ShortHelp() []key.Binding {
	if v.stage != jobsList {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "job")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "detail")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
	}
}

func (v JobsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.tbl.SetColumns(jobsColumns(pickerContentWidth(v.width)))
		v.tbl.SetHeight(max(msg.Height-8, 3))
		return v, nil

	case tea.KeyMsg:
		switch v.stage {
		case jobsList:
			return v.handleListKey(msg)
		case jobsForm:
			return v.updateForm(msg)
		case jobsRunDone:
			if msg.Type == tea.KeyEnter {
				v.stage = jobsList
				v.notice = ""
				return v, nil
			}
			return v, nil
		default:
			return v, nil
		}

	case jobTimerMsg:
		v.notice = msg.notice
		if msg.err != nil {
			v.notice = msg.err.Error()
		}
		v.reload()
		return v, nil

	case confirmedMsg:
		switch msg.id {
		case jobInstallConfirmID:
			return v.runTimerInstall()
		case jobUninstallConfirmID:
			return v.runTimerUninstall()
		case jobRunConfirmID:
			return v.startRun()
		case jobAddConfirmID:
			return v.saveForm(false)
		case jobReplaceConfirmID:
			return v.saveForm(true)
		case jobEditConfirmID:
			return v.saveForm(true)
		}
		return v, nil

	case policyRunDoneMsg:
		v.stage = jobsRunDone
		v.result = msg
		v.reload()
		return v, nil

	case opRejectedMsg:
		if v.stage == jobsRunning && msg.name == "job-run" {
			v.stage = jobsList
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == jobsRunning {
			return v, opTick()
		}
		return v, nil
	}
	return v, nil
}

// handleListKey routes single-key job actions (install/uninstall timer, run
// now, refresh) before falling through to table navigation.
func (v JobsView) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'R':
			v.notice = ""
			v.reload()
			return v, nil
		case 'a':
			v.stage = jobsForm
			v.editName = ""
			v.form = newPolicyForm()
			v.notice = ""
			return v, nil
		case 'e':
			if row, ok := v.currentJob(); ok {
				v.stage = jobsForm
				v.editName = row.name
				v.form = prefilledPolicyForm(row.name, v.policies[row.name])
				v.notice = ""
				return v, nil
			}
		case 'i':
			if row, ok := v.currentJob(); ok {
				v.notice = ""
				body := fmt.Sprintf("Install the OS scheduler entry for job %q?\nThis writes files under your home directory only.", row.name)
				modal := NewConfirmModal("Install timer", body, jobInstallConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
			}
		case 'u':
			if row, ok := v.currentJob(); ok {
				v.notice = ""
				body := fmt.Sprintf("Remove the scheduler entry for job %q?", row.name)
				modal := NewConfirmModal("Uninstall timer", body, jobUninstallConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
			}
		case 'r':
			if _, ok := v.currentJob(); ok {
				return v.armRun()
			}
		}
	}
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

// currentJob returns the row under the cursor.
func (v JobsView) currentJob() (jobRow, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return jobRow{}, false
	}
	return v.rows[i], true
}

func (v JobsView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	if v.stage == jobsForm {
		var b strings.Builder
		title := "New job"
		if v.editName != "" {
			title = "Edit job"
		}
		fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render(title))
		if v.editName != "" {
			// Never wrap an already-styled string: build the plain name
			// line ourselves rather than wrapping v.form.name.View()
			// (which textinput has already styled) in ui.Muted.
			fmt.Fprintf(&b, "%s\n", ui.Muted.Render(fmt.Sprintf("name>     %s", v.editName)))
		} else {
			fmt.Fprintf(&b, "%s\n", v.form.name.View())
		}
		fmt.Fprintf(&b, "%s\n", v.form.path.View())
		fmt.Fprintf(&b, "%s\n", v.form.tags.View())
		fmt.Fprintf(&b, "%s\n", v.form.schedule.View())
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
		fmt.Fprintf(&b, "\n%s", ui.ActionLine("save the job", "tab field · esc cancel"))
		return b.String()
	}
	if v.stage == jobsRunning {
		var b strings.Builder
		b.WriteString(ui.Primary.Render("Running job " + v.run.name + "…"))
		if v.run.reporter != nil {
			total, done := v.run.reporter.Snapshot()
			fmt.Fprintf(&b, "\n\n  %s / %s uploaded", ui.FormatBytes(done), ui.FormatBytes(total))
		}
		return b.String()
	}
	if v.stage == jobsRunDone {
		var b strings.Builder
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Job run failed"))
			fmt.Fprintf(&b, "\n\n%s", humanizeErr(v.result.err))
		} else {
			b.WriteString(ui.Success.Render("Job run complete"))
			fmt.Fprintf(&b, "\n\n  job        %s\n  snapshots  %d", v.result.name, v.result.snapshots)
		}
		fmt.Fprintf(&b, "\n\n%s", ui.ActionLine("return to the job list", ""))
		return b.String()
	}
	if len(v.rows) == 0 {
		return ui.Muted.Render("no scheduled backups — press a to add one, or ctrl+e in Backup")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("Scheduled backups"))
	fmt.Fprintf(&b, "%s\n\n", v.tbl.View())
	if v.notice != "" {
		fmt.Fprintf(&b, "%s\n\n", ui.Warn.Render(v.notice))
	}
	b.WriteString(ui.Muted.Render("⏎ detail · a add · e edit · r run · i/u timer · d delete · R refresh"))
	return b.String()
}

// jobsColumns lays out the five columns in the interior width, the same
// budget split scheduleColumns used.
func jobsColumns(width int) []table.Column {
	if width < 56 {
		width = 56
	}
	sched, timer, next, last := 16, 13, 12, 10
	name := width - sched - timer - next - last
	if name < 8 {
		name = 8
	}
	return []table.Column{
		{Title: "Job", Width: name},
		{Title: "Schedule", Width: sched},
		{Title: "Timer", Width: timer},
		{Title: "Next run", Width: next},
		{Title: "Last run", Width: last},
	}
}
