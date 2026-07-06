package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/ui"
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
	policyAddConfirmID    = "policy-add"
	policyRemoveConfirmID = "policy-remove"
	policyRunConfirmID    = "policy-run"
)

// PoliciesView lists the named backup policies from sentra.yaml, shows the
// selected one inline, and drives three actions: ADD/edit and REMOVE are
// config-only (they rewrite sentra.yaml via config.Write and reload — NO
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
// policy map. Called at construction and after every config.Write so the
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

func (v PoliciesView) Title() string { return "Policies" }

func (v PoliciesView) ShortHelp() []key.Binding {
	if v.stage != policiesList || len(v.names) == 0 {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
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
		if v.stage != policiesList {
			return v, nil // form/run handling added in later tasks
		}
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
			if len(msg.Runes) == 1 && msg.Runes[0] == 'd' && len(v.names) > 0 {
				name := v.names[v.selected]
				body := fmt.Sprintf("Remove policy %q from sentra.yaml?\nThis edits local config only — no snapshots are touched.", name)
				modal := NewConfirmModal("Confirm remove", body, policyRemoveConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
			}
			return v, nil
		}
		return v, nil

	case confirmedMsg:
		switch msg.id {
		case policyRemoveConfirmID:
			return v.removeSelected()
		}
		return v, nil
	}
	return v, nil
}

// removeSelected deletes the selected policy from sentra.yaml and reloads.
// This is a config-only edit: it rewrites the file via config.Write and
// never takes the repo lock or the op guard, matching `sentra policy remove`.
func (v PoliciesView) removeSelected() (tea.Model, tea.Cmd) {
	if v.selected < 0 || v.selected >= len(v.names) {
		return v, nil
	}
	name := v.names[v.selected]
	cfg, err := config.Load(v.deps.ConfigPath)
	if err != nil {
		v.notice = "reload failed: " + err.Error()
		return v, nil
	}
	delete(cfg.Policies, name)
	if err := config.Write(v.deps.ConfigPath, cfg); err != nil {
		v.notice = "write failed: " + err.Error()
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
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Backup policies"))
	if v.notice != "" {
		b.WriteString("  " + ui.Warn.Render(v.notice))
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
	b.WriteString("\n" + v.renderDetail())
	b.WriteString("\n" + ui.Muted.Render("↑↓ select · r run · d remove"))
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
	b.WriteString(ui.Primary.Render(name) + "\n")
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

// policyForm and policyRunState are placeholders wired by the ADD and RUN
// tasks; declared here so the PoliciesView struct compiles as one unit.
type policyForm struct{}

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

// hydrateCtx is the timeout-bounded context the view uses for its
// construction-time reads, matching PruneView/RestoreView.
func (v PoliciesView) hydrateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctxOrBackground(v.deps.Ctx), hydrateTimeout)
}
