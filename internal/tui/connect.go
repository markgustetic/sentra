package tui

import (
	"errors"
	"fmt"
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
	connectAuthing                     // aws sso login owns the terminal
)

// connectResultMsg carries an open attempt's outcome back into the view.
// A mirror of unlockResultMsg, private to this view: launch-path opens
// take no advisory lock, so the App's one-op guard is not involved.
type connectResultMsg struct {
	repo   *repo.Repo
	config *config.Config
	err    error
}

// connectAuthDoneMsg carries the `aws sso login` child's exit status.
// Deliberately distinct from the wizard's awsAuthDoneMsg — separate views
// must not share private message types, or one view's ExecProcess wakes
// the other.
type connectAuthDoneMsg struct{ err error }

// ConnectView is the launch gate for a configured-but-unreachable repo:
// the config exists and a passphrase source answered, but opening the
// repository failed (expired AWS credentials, unreachable bucket, network
// down). The other launch states already live in the TUI (first-run →
// wizard, locked → unlock); this one shows the failure and puts the fix —
// rerunning `aws sso login` — one keypress away, instead of exiting to a
// dead CLI error. On a successful retry it hands the live repo to the App
// via repoReadyMsg, exactly like unlock.
type ConnectView struct {
	deps    Deps
	stage   connectStage
	openErr error // the launch failure, then each retry's failure
	authErr error // the login child's failure, if any
	width   int
}

// NewConnectView seeds the gate with the launch's open error.
func NewConnectView(deps Deps) ConnectView {
	return ConnectView{deps: deps, openErr: deps.ConnectError}
}

func (ConnectView) Init() tea.Cmd { return nil }

func (v ConnectView) Title() string { return "Connect" }

// canSSO reports whether the login affordance applies: AWS proper only.
// An S3-compatible endpoint (MinIO, R2, Wasabi) authenticates with static
// keys — `aws sso login` cannot fix it, so the hint would only mislead.
func (v ConnectView) canSSO() bool {
	return v.deps.Config != nil && v.deps.Config.Repo.S3.EndpointURL == ""
}

// loginLabel is the exact command l will run, shown before it is pressed:
// an operator should never trigger a subprocess they haven't seen named.
func (v ConnectView) loginLabel() string {
	profile := ""
	if v.deps.Config != nil {
		profile = strings.TrimSpace(v.deps.Config.Repo.S3.Profile)
	}
	if profile == "" {
		return "aws sso login"
	}
	return "aws sso login --profile " + profile
}

func (v ConnectView) ShortHelp() []key.Binding {
	if v.stage != connectIdle {
		return nil
	}
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "retry")),
	}
	if v.canSSO() {
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
			// credential failure the operator hasn't fixed yet.
			v.stage = connectIdle
			v.authErr = msg.err
			return v, nil
		}
		// Fresh credentials: retry without demanding another keypress.
		v.stage = connectIdle
		v.authErr = nil
		return v.startOpen()

	case tea.KeyMsg:
		if v.stage != connectIdle {
			return v, nil // one in-flight action at a time
		}
		switch msg.String() {
		case "r":
			return v.startOpen()
		case "l":
			return v.startAuth()
		}
	}
	return v, nil
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

// startAuth suspends the program and hands the terminal to
// `aws sso login`, reusing the wizard's argv builder: fixed binary,
// config-sourced profile/region, no shell. Completion returns as this
// view's own connectAuthDoneMsg.
func (v ConnectView) startAuth() (tea.Model, tea.Cmd) {
	if !v.canSSO() {
		return v, nil
	}
	v.stage = connectAuthing
	profile, region := "", ""
	if v.deps.Config != nil {
		profile = v.deps.Config.Repo.S3.Profile
		region = v.deps.Config.Repo.S3.Region
	}
	c := interactiveAWSAuthCommand(ctxOrBackground(v.deps.Ctx),
		v.deps.SetupEffects, setup.AWSAuthSSO, profile, region)
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
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render(v.openErr.Error()))
		}
		if v.authErr != nil {
			fmt.Fprintf(&b, "\n\n%s", ui.Danger.Render("login failed: "+v.authErr.Error()))
		}
		fmt.Fprintf(&b, "\n\n%s", "r  retry the connection")
		if v.canSSO() {
			fmt.Fprintf(&b, "\n%s  %s", "l  reauthenticate:", ui.Muted.Render(v.loginLabel()))
		}
		fmt.Fprintf(&b, "\n%s", ui.Muted.Render("q quits"))
	}
	return b.String()
}
