package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

// connectStage is the position in the connect gate's small state machine.
type connectStage int

const (
	connectIdle    connectStage = iota // showing the error, awaiting r/l/q
	connectOpening                     // OpenRepo running in a returned cmd
	connectAuthing                     // the aws login child owns the terminal
)

// connectResultMsg carries an open attempt's outcome back into the view.
// A mirror of unlockResultMsg, private to this view: launch-path opens
// take no advisory lock, so the App's one-op guard is not involved.
type connectResultMsg struct {
	repo   *repo.Repo
	config *config.Config
	err    error
}

// connectAuthDoneMsg carries the aws login child's exit status.
// Deliberately distinct from the wizard's awsAuthDoneMsg — separate views
// must not share private message types, or one view's ExecProcess wakes
// the other.
type connectAuthDoneMsg struct{ err error }

// ConnectView is the launch gate for a configured-but-unreachable repo:
// the config exists and a passphrase source answered, but opening the
// repository failed (expired AWS credentials, unreachable bucket, network
// down). The other launch states already live in the TUI (first-run →
// wizard, locked → unlock); this one shows the failure and puts the fix —
// rerunning the profile's aws login command — one keypress away, instead
// of exiting to a dead CLI error. On a successful retry it hands the live repo to the App
// via repoReadyMsg, exactly like unlock.
type ConnectView struct {
	deps       Deps
	stage      connectStage
	openErr    error         // the launch failure, then each retry's failure
	authErr    error         // the login child's failure, if any
	authOut    *bytes.Buffer // the login child's captured stderr, if any
	authMethod setup.AWSAuthMethod
	cursor     int // selected row of the idle menu
	width      int
}

// connectMenuAction identifies one selectable row of the idle menu. The
// rows mirror the r/l/q hotkeys exactly — arrows+enter and the hotkeys are
// two affordances for the same three actions, never separate feature sets.
type connectMenuAction int

const (
	connectMenuRetry connectMenuAction = iota
	connectMenuLogin
	connectMenuQuit
)

// menu returns the idle rows in render order. Login appears only when
// canReauth does — the menu must never offer an action the hotkey forbids.
func (v ConnectView) menu() []connectMenuAction {
	if v.canReauth() {
		return []connectMenuAction{connectMenuRetry, connectMenuLogin, connectMenuQuit}
	}
	return []connectMenuAction{connectMenuRetry, connectMenuQuit}
}

// selectAction parks the cursor on the given action's row, so a hotkey
// press leaves the marker on the action it ran — after a failed attempt
// returns to idle, the menu shows what was last tried.
func (v *ConnectView) selectAction(a connectMenuAction) {
	for i, row := range v.menu() {
		if row == a {
			v.cursor = i
			return
		}
	}
}

// NewConnectView seeds the gate with the launch's open error and resolves
// which reauth command fits the profile. `aws sso login` against a
// login_session profile fails instantly — and unreadably, since the
// terminal flips back before the error can be read — so the method is
// chosen from what the AWS CLI config actually says: SSO-configured
// profiles get `aws sso login`, everything else the browser `aws login`
// flow (a probe error included: login is the command that can establish a
// session from nothing). With no effects to probe through, SSO stands, as
// it always did.
func NewConnectView(deps Deps) ConnectView {
	v := ConnectView{deps: deps, openErr: deps.ConnectError,
		authMethod: setup.AWSAuthSSO}
	if deps.SetupEffects != nil && deps.Config != nil {
		profile := strings.TrimSpace(deps.Config.Repo.S3.Profile)
		sso, err := deps.SetupEffects.CheckAWSSSOConfigured(
			ctxOrBackground(deps.Ctx), profile)
		if err != nil || !sso {
			v.authMethod = setup.AWSAuthLogin
		}
	}
	return v
}

func (ConnectView) Init() tea.Cmd { return nil }

func (v ConnectView) Title() string { return "Connect" }

// canReauth reports whether the login affordance applies: AWS proper
// only. An S3-compatible endpoint (MinIO, R2, Wasabi) authenticates with
// static keys — no aws login flow can fix it, so the hint would only
// mislead.
func (v ConnectView) canReauth() bool {
	return v.deps.Config != nil && v.deps.Config.Repo.S3.EndpointURL == ""
}

// loginLabel is the exact command l will run, shown before it is pressed:
// an operator should never trigger a subprocess they haven't seen named.
// It is derived from the same argv the keypress executes, so the label and
// the command can never drift apart.
func (v ConnectView) loginLabel() string {
	c, _ := newAuthCmd(context.Background(), v.authMethod,
		v.authProfile(), v.authRegion())
	return strings.Join(c.Args, " ")
}

func (v ConnectView) authProfile() string {
	if v.deps.Config == nil {
		return ""
	}
	return strings.TrimSpace(v.deps.Config.Repo.S3.Profile)
}

func (v ConnectView) authRegion() string {
	if v.deps.Config == nil {
		return ""
	}
	return strings.TrimSpace(v.deps.Config.Repo.S3.Region)
}

// newAuthCmd builds the interactive reauth child with its stderr teed into
// a capture buffer. The stream is pre-set deliberately: tea.ExecProcess
// only fills nil streams, so the tee survives it — the operator still sees
// live output on the terminal, and after the alt-screen restore erases the
// scrollback, the gate can show what the child printed. Stdout/stdin stay
// nil (the real TTY): the AWS CLI's browser flows check tty-ness there.
func newAuthCmd(ctx context.Context, method setup.AWSAuthMethod, profile, region string) (*exec.Cmd, *bytes.Buffer) {
	c := interactiveAWSAuthCommand(ctx, nil, method, profile, region)
	buf := &bytes.Buffer{}
	c.Stderr = io.MultiWriter(os.Stderr, buf)
	return c, buf
}

func (v ConnectView) ShortHelp() []key.Binding {
	if v.stage != connectIdle {
		return nil
	}
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "retry")),
	}
	if v.canReauth() {
		bindings = append(bindings,
			key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "log in")))
	}
	return bindings
}

func (v ConnectView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case connectResultMsg:
		if msg.err != nil {
			v.stage = connectIdle
			v.openErr = msg.err
			return v, nil
		}
		// Success: the App rebuilds against the live repo and lands on
		// the dashboard; this view's job is done.
		ready := repoReadyMsg{repo: msg.repo, config: msg.config}
		return v, func() tea.Msg { return ready }

	case connectAuthDoneMsg:
		if msg.err != nil {
			// The child failed (aws missing, login declined): show ITS
			// error and idle — retrying the open would just repeat the
			// credential failure the operator hasn't fixed yet. authOut
			// stays: the captured stderr is the only readable copy of
			// what the child printed before the alt-screen restore.
			v.stage = connectIdle
			v.authErr = msg.err
			return v, nil
		}
		// Fresh credentials: retry without demanding another keypress.
		v.stage = connectIdle
		v.authErr = nil
		v.authOut = nil
		return v.startOpen()

	case tea.KeyMsg:
		if v.stage != connectIdle {
			return v, nil // one in-flight action at a time
		}
		switch msg.Type {
		case tea.KeyUp:
			if v.cursor > 0 {
				v.cursor--
			}
			return v, nil
		case tea.KeyDown:
			if v.cursor < len(v.menu())-1 {
				v.cursor++
			}
			return v, nil
		case tea.KeyEnter:
			return v.activate(v.menu()[v.cursor])
		}
		switch msg.String() {
		case "r":
			v.selectAction(connectMenuRetry)
			return v.startOpen()
		case "l":
			v.selectAction(connectMenuLogin)
			return v.startAuth()
		case "q":
			// Startup gates bypass the shell's global quit binding; this view
			// owns q so a keystroke from the reachability gate reaches the quit
			// path. ctrl+c is still handled ahead of view routing and works
			// across all gates.
			return v, tea.Quit
		}
	}
	return v, nil
}

// activate runs the menu row enter landed on — the same handlers the
// hotkeys call, so the two affordances can never drift apart.
func (v ConnectView) activate(a connectMenuAction) (tea.Model, tea.Cmd) {
	switch a {
	case connectMenuRetry:
		return v.startOpen()
	case connectMenuLogin:
		return v.startAuth()
	default:
		return v, tea.Quit
	}
}

// startOpen runs the injected retry closure in a returned cmd. The
// closure re-resolves the passphrase chain itself; no secret enters this
// view.
func (v ConnectView) startOpen() (tea.Model, tea.Cmd) {
	v.stage = connectOpening
	deps := v.deps
	return v, func() tea.Msg {
		if deps.OpenRepo == nil {
			return connectResultMsg{err: errors.New("no repo opener configured")}
		}
		r, cfg, err := deps.OpenRepo(ctxOrBackground(deps.Ctx))
		if err != nil {
			return connectResultMsg{err: err}
		}
		return connectResultMsg{repo: r, config: cfg}
	}
}

// startAuth suspends the program and hands the terminal to the resolved
// reauth command (`aws login` or `aws sso login`), reusing the wizard's
// argv builder: fixed binary, config-sourced profile/region, no shell.
// Completion returns as this view's own connectAuthDoneMsg.
func (v ConnectView) startAuth() (tea.Model, tea.Cmd) {
	if !v.canReauth() {
		return v, nil
	}
	v.stage = connectAuthing
	c, buf := newAuthCmd(ctxOrBackground(v.deps.Ctx), v.authMethod,
		v.authProfile(), v.authRegion())
	v.authOut = buf
	return v, tea.ExecProcess(c, func(err error) tea.Msg {
		return connectAuthDoneMsg{err: err}
	})
}

func (v ConnectView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Repository unreachable"))
	fmt.Fprintf(&b, "\n%s", ui.Muted.Render(v.deps.RepoName))

	switch v.stage {
	case connectOpening:
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("opening the repository…"))
	case connectAuthing:
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("waiting for aws sso login…"))
	default:
		if v.openErr != nil {
			fmt.Fprintf(&b, "\n\n%s", humanizeErr(v.openErr))
		}
		if v.authErr != nil {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render("login failed: "+v.authErr.Error()))
			if v.authOut != nil {
				if out := strings.TrimSpace(v.authOut.String()); out != "" {
					fmt.Fprintf(&b, "\n%s", ui.Muted.Render(out))
				}
			}
		}
		b.WriteString("\n")
		for i, a := range v.menu() {
			selected := i == v.cursor
			switch a {
			case connectMenuRetry:
				fmt.Fprintf(&b, "\n%s", ui.SelectRow(selected, "r  retry the connection"))
			case connectMenuLogin:
				fmt.Fprintf(&b, "\n%s  %s", ui.SelectRow(selected, "l  reauthenticate"),
					ui.Muted.Render(v.loginLabel()))
			case connectMenuQuit:
				fmt.Fprintf(&b, "\n%s", ui.SelectRow(selected, "q  quit"))
			}
		}
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("↑/↓ select · ⏎ run"))
	}
	return b.String()
}
