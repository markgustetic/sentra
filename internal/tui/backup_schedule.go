package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/ui"
)

// scheduleOneShot is the wizard's word for "no policy": the first row of
// the cadence list. It never reaches disk — build() returns an empty
// schedule for it and the confirm step installs nothing.
const scheduleOneShot = "one-shot"

// scheduleCadences is the Schedule step's list, top to bottom. manual is
// deliberately absent: a policy that never fires on its own is what the
// Scheduled backups tab's add form is for, not a backup you are taking now.
var scheduleCadences = []string{
	scheduleOneShot,
	policycfg.CadenceHourly,
	policycfg.CadenceDaily,
	policycfg.CadenceWeekly,
	policycfg.CadenceMonthly,
}

// scheduleWeekdays are the policy package's weekday tokens, in ←/→ order.
var scheduleWeekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// scheduleControl names which control of the Schedule step owns the keyboard.
type scheduleControl int

const (
	schedCadence scheduleControl = iota
	schedName
	schedAt
	schedWeekday
)

// scheduleForm is the Schedule step's state: a cadence list plus the fields
// that cadence needs. It owns its widgets' focus so the step flag and
// Focused() cannot disagree (refocus/blur are the only paths).
type scheduleForm struct {
	dir        string
	policies   map[string]config.PolicyConfig
	cadenceIdx int // index into scheduleCadences
	name       textinput.Model
	at         textinput.Model
	weekday    int // index into scheduleWeekdays
	focus      scheduleControl
	err        string
}

// newScheduleForm builds the step for dir. Nothing is focused: the caller
// focuses on stage entry (constructors and Init focus nothing). The name is
// prefilled with the sanitized basename, uniquified against policies so the
// default can never collide with a policy pointing elsewhere.
func newScheduleForm(dir string, policies map[string]config.PolicyConfig) scheduleForm {
	name := textinput.New()
	name.Prompt = "name>  "
	name.Placeholder = "policy name"
	name.SetValue(uniquePolicyName(repeatPolicyName(filepath.Base(dir)), dir, policies))
	at := textinput.New()
	at.Prompt = "time>  "
	at.Placeholder = "HH:MM"
	at.SetValue("02:00")
	return scheduleForm{
		dir:      dir,
		policies: policies,
		name:     name,
		at:       at,
		weekday:  len(scheduleWeekdays) - 1, // sun, matching the old ctrl+e default
	}
}

// uniquePolicyName returns base, or base-2, base-3, … — the first name that
// is free or already backs up exactly dir (that one is reused, not renamed).
func uniquePolicyName(base, dir string, policies map[string]config.PolicyConfig) string {
	name := base
	for i := 2; ; i++ {
		existing, exists := policies[name]
		if !exists || (len(existing.Paths) == 1 && existing.Paths[0] == dir) {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, i)
	}
}

func (f scheduleForm) cadence() string { return scheduleCadences[f.cadenceIdx] }
func (f scheduleForm) oneShot() bool   { return f.cadence() == scheduleOneShot }

// controls lists the visible controls in tab order. One-shot shows only the
// list; hourly adds the name; daily/monthly add the time; weekly adds the
// weekday too.
func (f scheduleForm) controls() []scheduleControl {
	c := []scheduleControl{schedCadence}
	switch f.cadence() {
	case scheduleOneShot:
	case policycfg.CadenceHourly:
		c = append(c, schedName)
	case policycfg.CadenceWeekly:
		c = append(c, schedName, schedAt, schedWeekday)
	default:
		c = append(c, schedName, schedAt)
	}
	return c
}

func (f scheduleForm) capturesText() bool {
	return (f.focus == schedName && f.name.Focused()) || (f.focus == schedAt && f.at.Focused())
}
func (f scheduleForm) consumesArrows() bool { return f.focus == schedCadence }

// refocus blurs both fields, focuses the one f.focus names, and returns its
// Focus() cmd — nil when the keyboard is on the list or the weekday row,
// which have no cursor to blink.
func (f *scheduleForm) refocus() tea.Cmd {
	f.blur()
	switch f.focus {
	case schedName:
		return f.name.Focus()
	case schedAt:
		return f.at.Focus()
	}
	return nil
}

func (f *scheduleForm) blur() {
	f.name.Blur()
	f.at.Blur()
}

// update handles every key but enter and esc, which belong to the wizard's
// stage machine. Tab cycles the visible controls, falling back to the first
// one when the current focus fell out of the (possibly now-shrunk) visible
// set — the only re-clamp this form needs, since focus can only land on the
// cadence list itself, never fall off it, when the cadence changes; ↑↓ move
// the cadence list when focus is on it; ←/→ cycle the weekday; everything
// else types into the focused field.
func (f scheduleForm) update(msg tea.KeyMsg) (scheduleForm, tea.Cmd) {
	f.err = ""
	switch {
	case msg.Type == tea.KeyTab:
		visible := f.controls()
		next := 0
		for i, c := range visible {
			if c == f.focus {
				next = (i + 1) % len(visible)
			}
		}
		f.focus = visible[next]
		cmd := f.refocus()
		return f, cmd
	case f.focus == schedCadence && msg.Type == tea.KeyUp:
		f.cadenceIdx = max(f.cadenceIdx-1, 0)
		return f, nil
	case f.focus == schedCadence && msg.Type == tea.KeyDown:
		f.cadenceIdx = min(f.cadenceIdx+1, len(scheduleCadences)-1)
		return f, nil
	case f.focus == schedWeekday && msg.Type == tea.KeyRight:
		f.weekday = (f.weekday + 1) % len(scheduleWeekdays)
		return f, nil
	case f.focus == schedWeekday && msg.Type == tea.KeyLeft:
		f.weekday = (f.weekday + len(scheduleWeekdays) - 1) % len(scheduleWeekdays)
		return f, nil
	case f.focus == schedName:
		var cmd tea.Cmd
		f.name, cmd = f.name.Update(msg)
		return f, cmd
	case f.focus == schedAt:
		var cmd tea.Cmd
		f.at, cmd = f.at.Update(msg)
		return f, cmd
	}
	return f, nil
}

// schedule assembles the PolicySchedule the current controls describe.
func (f scheduleForm) schedule() config.PolicySchedule {
	s := config.PolicySchedule{Cadence: f.cadence()}
	switch f.cadence() {
	case policycfg.CadenceDaily, policycfg.CadenceMonthly:
		s.At = strings.TrimSpace(f.at.Value())
	case policycfg.CadenceWeekly:
		s.At = strings.TrimSpace(f.at.Value())
		s.Weekday = scheduleWeekdays[f.weekday]
	}
	return policycfg.NormalizeSchedule(s)
}

// build validates the step. One-shot yields empties and no error. Otherwise
// the name and schedule go through policy.Validate (name rules, HH:MM,
// weekday), then the wizard's own rule: a name that exists for a DIFFERENT
// directory is refused naming that directory; the same directory is reused
// (reuses=true) so the confirm step can say "updates the existing policy".
func (f scheduleForm) build() (name string, sched config.PolicySchedule, reuses bool, err error) {
	if f.oneShot() {
		return "", config.PolicySchedule{}, false, nil
	}
	name = strings.TrimSpace(f.name.Value())
	sched = f.schedule()
	p := config.PolicyConfig{Paths: []string{f.dir}, Schedule: sched}
	if err := policycfg.Validate(name, p); err != nil {
		return "", config.PolicySchedule{}, false, err
	}
	if existing, ok := f.policies[name]; ok {
		if len(existing.Paths) == 1 && existing.Paths[0] == f.dir {
			return name, sched, true, nil
		}
		return "", config.PolicySchedule{}, false,
			fmt.Errorf("policy %q already backs up %s — choose another name", name, strings.Join(existing.Paths, ", "))
	}
	return name, sched, false, nil
}

// describe renders the cadence for the confirm summary and the done screen.
func (f scheduleForm) describe() string {
	s := f.schedule()
	switch f.cadence() {
	case scheduleOneShot:
		return scheduleOneShot
	case policycfg.CadenceHourly:
		return "hourly"
	case policycfg.CadenceWeekly:
		return fmt.Sprintf("weekly on %s at %s", s.Weekday, s.At)
	case policycfg.CadenceMonthly:
		return "monthly on the 1st at " + s.At
	default:
		return "daily at " + s.At
	}
}

// setWidth sizes both fields from the pane interior, reserving the box.
func (f *scheduleForm) setWidth(interior int) {
	w := max(interior-7-1-ui.FieldBoxOverhead, 10) // 7-cell prompt, 1 cursor
	f.name.Width = w
	f.at.Width = w
}

func (f scheduleForm) view() string {
	var b strings.Builder
	b.WriteString(ui.Muted.Render("How often should this directory be backed up?"))
	b.WriteString("\n\n")
	for i, c := range scheduleCadences {
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(f.focus == schedCadence && i == f.cadenceIdx, "  "+c))
	}
	for _, c := range f.controls() {
		switch c {
		case schedName:
			fmt.Fprintf(&b, "\n%s", boxedField(f.name))
		case schedAt:
			fmt.Fprintf(&b, "\n%s", boxedField(f.at))
		case schedWeekday:
			fmt.Fprintf(&b, "\n%s", ui.SelectRow(f.focus == schedWeekday,
				"  weekday: "+scheduleWeekdays[f.weekday]+"   (←/→ to change)"))
		}
	}
	if f.err != "" {
		fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(f.err))
	}
	return b.String()
}
