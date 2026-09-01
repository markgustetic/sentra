package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/cursor"
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

// jobDetailMsg carries a finished manifest load for the drill-in
// detail stage back to the view. name+pathIdx echo the request the
// load cmd was built from, so Update can drop a result the operator
// has since navigated away from — closed the detail page, or cycled
// to a different one of the job's paths.
type jobDetailMsg struct {
	name    string
	pathIdx int
	snapID  string
	man     repo.Manifest
	err     error
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

	// Drill-in detail stage. detailName/detailPathIdx identify the job
	// and which of its paths is on screen; loading/snapID/man/err carry
	// the async manifest load for that path's newest snapshot, mirroring
	// snapshots.go's detail state machine. loader is the manifest fetch
	// hook — production wires it to repo.LoadSnapshot, tests inject a
	// canned closure.
	detailName    string
	detailPathIdx int
	detailLoading bool
	detailSnapID  string
	detailMan     repo.Manifest
	detailErr     error
	loader        detailLoader

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
	// Mirrors NewSnapshots' production loader wiring: repo.LoadSnapshot
	// behind a 10s timeout derived from deps.Ctx, or an error stub when
	// no repo is configured (keeps the view constructible in tests that
	// only exercise navigation).
	loader := func(_ string) (repo.Manifest, error) {
		return repo.Manifest{}, fmt.Errorf("jobs: no repo configured")
	}
	if deps.Repo != nil {
		loader = func(id string) (repo.Manifest, error) {
			ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 10*time.Second)
			defer cancel()
			return deps.Repo.LoadSnapshot(ctx, id)
		}
	}
	v := JobsView{
		deps:    deps,
		now:     time.Now,
		homeDir: os.UserHomeDir,
		loader:  loader,
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

// ConsumesTab: on the detail stage tab is a third way to cycle which of the
// job's paths is shown (alongside ←/→ — see handleDetailKey), matching
// BackupView's use of the same interface for its folder picker. Without
// this the shell's global Focus binding (tab) intercepts the key before
// the view ever sees it, so the drill-in's "tab cycles paths" promise
// (docs/superpowers/specs/2026-08-29-jobs-view-design.md) was dead in the
// real app. Scoped to jobsDetail only: the list and form stages must keep
// tab as the ordinary focus toggle / field-hop (form stage gets tab via
// CapturesText instead).
func (v JobsView) ConsumesTab() bool { return v.stage == jobsDetail }

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
		key.NewBinding(key.WithKeys("i", "u"), key.WithHelp("i/u", "timer")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "delete")),
		key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "refresh")),
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
		case jobsDetail:
			return v.handleDetailKey(msg)
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
		// A delete (or any other timer/policy op) resolving while the
		// deleted job is the one on screen in detail would otherwise
		// leave a ghost page: viewDetail rendering a zero-value summary
		// over the last-loaded manifest, with left/right/tab a no-op
		// (n==0 short-circuits) so only esc could recover. reload()
		// above has already rebuilt v.policies, so an absent detailName
		// means exactly that.
		if v.stage == jobsDetail {
			if _, ok := v.policies[v.detailName]; !ok {
				v.stage = jobsList
				v.detailName = ""
				v.detailPathIdx = 0
				v.detailSnapID = ""
				v.detailMan = repo.Manifest{}
				v.detailErr = nil
				v.detailLoading = false
			}
		}
		return v, nil

	case jobDetailMsg:
		// Stale-result guard: drop a load the operator has since
		// navigated away from (esc back to the list, cycled to a
		// different path, or a fresh load for the same name+pathIdx
		// superseded this one — snapID is the actual resource identity,
		// the way snapshots.go's analogous guard keys on detailID; name
		// and pathIdx alone don't uniquely pin which load this is).
		if msg.name != v.detailName || msg.pathIdx != v.detailPathIdx ||
			msg.snapID != v.detailSnapID || v.stage != jobsDetail {
			return v, nil
		}
		v.detailLoading = false
		v.detailMan = msg.man
		v.detailErr = msg.err
		return v, nil

	case confirmedMsg:
		switch msg.id {
		case jobInstallConfirmID:
			return v.runTimerInstall()
		case jobUninstallConfirmID:
			return v.runTimerUninstall()
		case jobRunConfirmID:
			return v.startRun()
		case jobDeleteConfirmID:
			return v.runDelete()
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

	case cursor.BlinkMsg:
		// Only the form stage has focused text fields, and at most one of
		// name/path/tags/schedule is focused at a time (the check/prune
		// toggle steps have no cursor to blink, and edit mode keeps name
		// blurred because it is read-only).
		if v.stage != jobsForm {
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
	if msg.Type == tea.KeyEnter {
		if row, ok := v.currentJob(); ok {
			v.detailName = row.name
			v.detailPathIdx = 0
			v.notice = ""
			// openDetail as its own statement, not inlined into the return:
			// the Go spec only orders function/method calls relative to
			// each other within a return statement, not relative to a
			// plain operand like the leading v — combining them would
			// leave whether v reflects openDetail's mutations unspecified.
			cmd := v.openDetail()
			return v, cmd
		}
		return v, nil
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'R':
			v.notice = ""
			v.reload()
			return v, nil
		case 'a':
			v.stage = jobsForm
			v.editName = ""
			// newPolicyForm focuses the name field; this keypress is the
			// form's first-focus activation, so its cmd starts the blink.
			var cmd tea.Cmd
			v.form, cmd = newPolicyForm()
			v.notice = ""
			return v, cmd
		case 'e':
			if row, ok := v.currentJob(); ok {
				v.stage = jobsForm
				v.editName = row.name
				// Edit lands focused on paths (name is read-only), so this
				// keypress is that field's first focus — start the blink.
				var cmd tea.Cmd
				v.form, cmd = prefilledPolicyForm(row.name, v.policies[row.name])
				v.notice = ""
				return v, cmd
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
		case 'd':
			if row, ok := v.currentJob(); ok {
				v.notice = ""
				body := fmt.Sprintf("Delete job %q?\nRemoves the policy from sentra.yaml AND uninstalls its OS timer.\nExisting snapshots are kept and age out via retention.", row.name)
				modal := NewConfirmModal("Delete job", body, jobDeleteConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
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

// rowByName finds a job's row by name (linear scan of v.rows — the
// table is small, one row per policy). Used by viewDetail, which
// knows the job by name (v.detailName) rather than by table cursor.
func (v JobsView) rowByName(name string) (jobRow, bool) {
	for _, r := range v.rows {
		if r.name == name {
			return r, true
		}
	}
	return jobRow{}, false
}

// openDetail resolves the newest snapshot for the job at v.detailName
// and the path at v.detailPathIdx — both already set by the caller:
// enter-in-list sets detailPathIdx to 0, path-cycling sets it to the
// cycled index. No snapshot at that path leaves detailSnapID empty,
// which viewDetail renders as the "not backed up yet" placeholder —
// there is nothing to load. Otherwise it arms the loading state and
// returns the load cmd; the manifest fetch itself never runs inline
// here, which would freeze the whole TUI while S3 responds.
func (v *JobsView) openDetail() tea.Cmd {
	v.stage = jobsDetail
	v.detailErr = nil
	v.detailLoading = false
	v.detailSnapID = ""
	v.detailMan = repo.Manifest{}
	p := v.policies[v.detailName]
	if v.detailPathIdx >= len(p.Paths) {
		return nil
	}
	pathAbs := normalizeJobPath(p.Paths[v.detailPathIdx], v.jobsHome())
	snap, ok := newestJobSnapshot(v.detailName, pathAbs, v.snaps)
	if !ok {
		return nil
	}
	v.detailSnapID = snap.ID
	v.detailLoading = true
	return v.loadDetailCmd()
}

// loadDetailCmd builds the tea.Cmd that fetches v.detailSnapID's
// manifest via v.loader. Captures the request's identity (name,
// pathIdx, id) by value so the resulting jobDetailMsg can be matched
// back against the view's current state even if the operator has
// since navigated elsewhere.
func (v JobsView) loadDetailCmd() tea.Cmd {
	name, idx, id, loader := v.detailName, v.detailPathIdx, v.detailSnapID, v.loader
	return func() tea.Msg {
		man, err := loader(id)
		return jobDetailMsg{name: name, pathIdx: idx, snapID: id, man: man, err: err}
	}
}

// handleDetailKey routes keys on the drill-in detail stage: esc backs
// out to the list; left/right/tab cycle which of the job's paths is
// shown, wrapping modulo the path count; e/d/r delegate to the list's
// own handlers, but first move the table cursor to the detail job's
// row so currentJob() (which those handlers read) resolves to the job
// actually on screen rather than whatever row the cursor was last
// left on.
func (v JobsView) handleDetailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		v.stage = jobsList
		v.detailLoading = false
		v.detailErr = nil
		return v, nil
	case tea.KeyLeft, tea.KeyRight, tea.KeyTab:
		n := len(v.policies[v.detailName].Paths)
		if n == 0 {
			return v, nil
		}
		if msg.Type == tea.KeyLeft {
			v.detailPathIdx = (v.detailPathIdx - 1 + n) % n
		} else {
			v.detailPathIdx = (v.detailPathIdx + 1) % n
		}
		cmd := v.openDetail()
		return v, cmd
	}
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'e', 'd', 'r':
			for i, row := range v.rows {
				if row.name == v.detailName {
					v.tbl.SetCursor(i)
					break
				}
			}
			return v.handleListKey(msg)
		}
	}
	return v, nil
}

func (v JobsView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	if v.stage == jobsDetail {
		return v.viewDetail()
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
			fmt.Fprintf(&b, "%s\n", boxedField(v.form.name))
		}
		fmt.Fprintf(&b, "%s\n", boxedField(v.form.path))
		fmt.Fprintf(&b, "%s\n", boxedField(v.form.tags))
		fmt.Fprintf(&b, "%s\n", boxedField(v.form.schedule))
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

// viewDetail renders the drill-in stage: a policy summary (schedule,
// the active path, timer/next/last) followed by the newest snapshot's
// file tree for that path — mirroring snapshots.go's viewDetail state
// machine (loading / error / placeholder / tree), with the summary
// block and path-cycling footer a job view adds on top.
func (v JobsView) viewDetail() string {
	name := v.detailName
	p := v.policies[name]
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", ui.Primary.Render(name), ui.Subtle.Render(policycfg.FormatScheduleSpec(p.Schedule)))
	path := ""
	if v.detailPathIdx < len(p.Paths) {
		path = p.Paths[v.detailPathIdx]
	}
	fmt.Fprintf(&b, "  path %d/%d: %s\n", v.detailPathIdx+1, len(p.Paths), path)
	// Inline empty->"-" substitution here rather than importing cli's
	// emptyDash (which stays put per the extraction contract) — mirrors
	// the deleted PoliciesView.renderDetail, the only other read-only
	// surface that showed a policy's tags and prune mode (see
	// docs/superpowers/specs/2026-08-29-jobs-view-design.md: the drill-in
	// summary must carry "paths, tags, schedule spec, timer state,
	// next/last run, check/prune").
	tags := strings.Join(p.Tags, ", ")
	if tags == "" {
		tags = "-"
	}
	fmt.Fprintf(&b, "  tags: %s\n", tags)
	row, _ := v.rowByName(name)
	fmt.Fprintf(&b, "  timer: %s   next: %s   last: %s\n",
		jobTimerLabel(row), jobNextLabel(row), jobLastLabel(row, v.now()))
	fmt.Fprintf(&b, "  check: %t   prune: %s\n\n",
		p.AfterBackup.Check, policyPruneModeOrOff(p.AfterBackup.Prune))
	switch {
	case v.detailSnapID == "":
		b.WriteString(ui.Muted.Render("not backed up yet — run the job to take its first snapshot"))
	case v.detailLoading:
		b.WriteString(ui.Subtle.Render("loading snapshot " + v.detailSnapID + " …"))
	case v.detailErr != nil:
		b.WriteString(ui.Danger.Render("error loading snapshot: ") + v.detailErr.Error())
	default:
		fmt.Fprintf(&b, "%s\n", ui.Subtle.Render("snapshot "+v.detailMan.ID))
		textW := maxInt(v.width-4, 24)
		for _, line := range renderDirTree(buildDirTree(v.detailMan.Tree), textW) {
			fmt.Fprintf(&b, "%s\n", ui.Subtle.Render(line))
		}
	}
	return ui.Panel.Render(b.String()) + "\n" + ui.Subtle.Render("←→ path · e edit · r run · d delete · esc back") + "\n"
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
