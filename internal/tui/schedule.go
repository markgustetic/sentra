package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/ui"
)

// scheduleInstallID / scheduleUninstallID tie a confirm modal back to this
// flow. Both use the simple ConfirmModal: installing or removing a scheduler
// entry is reversible (the inverse action restores the prior state), so the
// typed-confirm gate reserved for destructive/irreversible operations does
// not apply here.
const (
	scheduleInstallID   = "schedule-install"
	scheduleUninstallID = "schedule-uninstall"
)

// scheduleRow is one policy's line in the status table.
type scheduleRow struct {
	name      string
	spec      string // policy.FormatScheduleSpec
	installed bool
	manual    bool
}

// scheduleDoneMsg carries the result of a quick install/uninstall/refresh.
// This is a filesystem-only action that NEVER takes the repo lock, so it is
// deliberately NOT an opResultMsg — it does not contend for the mutating-op
// guard and can run alongside a backup.
type scheduleDoneMsg struct {
	notice string
	err    error
}

// ScheduleView lists the configured policies with their cadence and whether
// their OS scheduler entry is installed, and lets the user install/uninstall
// one entry (each behind a simple confirm). It is filesystem-only: it reads
// deps.Config.Policies, stats the scheduler files, and writes/removes them
// under the user's home dir. It never opens the repository or takes the repo
// lock — hence the read-only view pattern (no op guard).
type ScheduleView struct {
	deps   Deps
	tbl    table.Model
	rows   []scheduleRow
	notice string
	width  int

	// osOverride/homeOverride/exeOverride pin the target platform, home dir,
	// and executable for tests. Zero values let the scheduler package fall
	// back to runtime.GOOS / os.UserHomeDir / os.Executable in production.
	osOverride   string
	homeOverride string
	exeOverride  string
}

func NewScheduleView(deps Deps) ScheduleView {
	v := ScheduleView{deps: deps}
	v.tbl = table.New(
		table.WithColumns(scheduleColumns(pickerIdealWidth)),
		table.WithFocused(true),
	)
	v.reload()
	return v
}

func (ScheduleView) Init() tea.Cmd { return nil }

// ConsumesArrows: the policy table only has a cursor when policies exist.
func (v ScheduleView) ConsumesArrows() bool { return len(v.rows) > 0 }

func (v ScheduleView) Title() string { return "Schedule" }

func (v ScheduleView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
		key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install")),
		key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "uninstall")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	}
}

// reload rebuilds the rows from deps.Config.Policies and re-stats each
// policy's scheduler files. Called at construction, after every mutating
// action, and on 'r'. A stat error for one policy is folded into notice
// rather than aborting the whole table.
func (v *ScheduleView) reload() {
	names := make([]string, 0)
	if v.deps.Config != nil {
		for name := range v.deps.Config.Policies {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	rows := make([]scheduleRow, 0, len(names))
	tblRows := make([]table.Row, 0, len(names))
	for _, name := range names {
		p := v.deps.Config.Policies[name]
		norm := policycfg.NormalizeSchedule(p.Schedule)
		row := scheduleRow{
			name:   name,
			spec:   policycfg.FormatScheduleSpec(p.Schedule),
			manual: norm.Cadence == policycfg.CadenceManual,
		}
		if !row.manual {
			paths, err := schedulerPathsFor(*v, name)
			if err == nil {
				if installed, sErr := schedulerInstalled(paths); sErr == nil {
					row.installed = installed
				}
			}
		}
		rows = append(rows, row)
		tblRows = append(tblRows, table.Row{name, row.spec, scheduleStateLabel(row)})
	}
	v.rows = rows
	// Preserve the cursor across a reload where possible.
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

func scheduleStateLabel(r scheduleRow) string {
	switch {
	case r.manual:
		return "—"
	case r.installed:
		return "installed"
	default:
		return "not installed"
	}
}

// selectPolicy moves the cursor to the named policy; used by tests and by
// the reload cursor-preservation logic.
func (v *ScheduleView) selectPolicy(name string) {
	for i, r := range v.rows {
		if r.name == name {
			v.tbl.SetCursor(i)
			return
		}
	}
}

func (v ScheduleView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.tbl.SetColumns(scheduleColumns(pickerContentWidth(v.width)))
		v.tbl.SetHeight(max(msg.Height-8, 3))
		return v, nil

	case scheduleDoneMsg:
		v.notice = msg.notice
		if msg.err != nil {
			v.notice = msg.err.Error()
		}
		v.reload()
		return v, nil

	case confirmedMsg:
		switch msg.id {
		case scheduleInstallID:
			return v.runInstall()
		case scheduleUninstallID:
			return v.runUninstall()
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v ScheduleView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'i':
			row, ok := v.current()
			if !ok {
				return v, nil
			}
			v.notice = ""
			body := fmt.Sprintf("Install the %s scheduler entry for policy %q?\nThis writes files under your home directory only.",
				v.osLabel(), row.name)
			modal := NewConfirmModal("Install schedule", body, scheduleInstallID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		case 'u':
			row, ok := v.current()
			if !ok {
				return v, nil
			}
			v.notice = ""
			body := fmt.Sprintf("Remove the scheduler entry for policy %q?", row.name)
			modal := NewConfirmModal("Uninstall schedule", body, scheduleUninstallID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		case 'r':
			v.notice = ""
			v.reload()
			return v, nil
		}
	}
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

// runInstall renders and writes the selected policy's scheduler files in a
// quick tea.Cmd. Rejects a manual cadence (mirrors the CLI) and folds any
// render/write error into the notice.
func (v ScheduleView) runInstall() (tea.Model, tea.Cmd) {
	row, ok := v.current()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	p := v.deps.Config.Policies[name]
	goos := v.osValue()
	home := v.homeOverride
	exeOverride := v.exeOverride
	run := func() tea.Msg {
		if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
			return scheduleDoneMsg{err: fmt.Errorf("policy %q has a manual schedule; set a cadence before installing", name)}
		}
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		exe, err := scheduler.Executable(exeOverride)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		files, err := scheduler.Render(paths, exe, cfgPath, name, p.Schedule)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		if err := scheduler.Install(files); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{notice: fmt.Sprintf("installed schedule for %q", name)}
	}
	return v, run
}

// runUninstall removes the selected policy's scheduler files in a quick
// tea.Cmd.
func (v ScheduleView) runUninstall() (tea.Model, tea.Cmd) {
	row, ok := v.current()
	if !ok {
		return v, nil
	}
	name := row.name
	goos := v.osValue()
	home := v.homeOverride
	run := func() tea.Msg {
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		if err := scheduler.Uninstall(paths); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{notice: fmt.Sprintf("removed schedule for %q", name)}
	}
	return v, run
}

// current returns the row under the cursor.
func (v ScheduleView) current() (scheduleRow, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return scheduleRow{}, false
	}
	return v.rows[i], true
}

// osValue is the effective GOOS for scheduler calls ("" → runtime.GOOS).
func (v ScheduleView) osValue() string { return v.osOverride }

// osLabel is the human label for the target platform in confirm bodies.
func (v ScheduleView) osLabel() string {
	switch v.osOverride {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd"
	default:
		return "OS"
	}
}

func (v ScheduleView) View() string {
	if v.deps.Config == nil || len(v.rows) == 0 {
		return ui.Muted.Render("no policies configured — add one with `sentra policy add`")
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Policy schedules") + "\n\n")
	b.WriteString(v.tbl.View() + "\n\n")
	if v.notice != "" {
		b.WriteString(ui.Warn.Render(v.notice) + "\n\n")
	}
	b.WriteString(ui.Muted.Render("i install · u uninstall · r refresh"))
	return b.String()
}

// scheduleColumns lays out the status table columns within the given
// interior width, mirroring how snapshotPickerColumns splits its budget.
func scheduleColumns(width int) []table.Column {
	if width < 24 {
		width = 24
	}
	state := 14
	spec := 18
	name := width - state - spec
	if name < 8 {
		name = 8
	}
	return []table.Column{
		{Title: "Policy", Width: name},
		{Title: "Schedule", Width: spec},
		{Title: "State", Width: state},
	}
}

// schedulerPathsFor resolves the scheduler.Paths for one policy under the
// view's overrides. A thin adapter so tests (and reload) share one call.
func schedulerPathsFor(v ScheduleView, name string) (scheduler.Paths, error) {
	return scheduler.PathsFor(v.osValue(), v.homeOverride, name)
}

// schedulerInstalled is a package-local alias so tests don't import the
// scheduler package directly.
func schedulerInstalled(paths scheduler.Paths) (bool, error) {
	return scheduler.Installed(paths)
}
