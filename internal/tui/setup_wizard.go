package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

// setupStage is the wizard's state-machine position. The S3-compatible
// backend skips stageActions (no AWS provisioning to configure), and
// stageDetails can short-circuit to stageIAMPreview when the operator
// asks to see the IAM policy before any side effects run.
type setupStage int

const (
	stageBackend setupStage = iota
	stageDetails
	stageIAMPreview
	stageActions
	stagePassphrase
	stageReview
	stageProvision
	stageDone
	stageError
)

// SetupWizardView drives the in-TUI setup wizard. It is the TUI-native
// re-expression of the huh cli wizard (internal/cli/setup_wizard.go):
// every huh step becomes an inline bubbles control because huh.Form.Run
// owns os.Stdin and cannot run inside a live tea.Program. The pure
// decisions and the provisioning sequence live in internal/setup; this
// view only collects input, gates on a review confirm, and drives the
// engine through the App's one-op guard.
//
// Fields are introduced in the task that first consumes them (backend +
// details defaults here; the actions/passphrase/provision state lands with
// its stage) so the package stays unused-clean at every step.
type SetupWizardView struct {
	deps   Deps
	engine *setup.Engine
	stage  setupStage

	plan setup.Plan

	// backend-stage cursor over the two backends.
	backendCursor int

	// details-stage text inputs (bucket/prefix/region/profile/endpoint).
	fields []textinput.Model

	// printIAM toggles the "print IAM policy and stop" details control.
	printIAM bool

	// actions-stage toggle state, seeded from the plan's smart defaults.
	createBucket bool
	blockPublic  bool
	defaultEnc   bool
	initRepo     bool

	// passphrase-stage masked inputs (new + confirm) and the keyring toggle.
	newPass     textinput.Model
	confirmPass textinput.Model
	savePass    bool

	width  int
	height int
}

// setupFieldIdx names the details-stage text inputs by position.
const (
	setupFieldBucket = iota
	setupFieldPrefix
	setupFieldRegion
	setupFieldProfile
	setupFieldEndpoint
	setupFieldCount
)

func NewSetupWizardView(deps Deps) SetupWizardView {
	cfg := config0(deps)
	var eng *setup.Engine
	if deps.SetupEffects != nil {
		eng = setup.NewEngine(deps.SetupEffects)
	}
	plan := setup.DefaultPlan(cfg, setup.DefaultEnvProbe())

	fields := make([]textinput.Model, setupFieldCount)
	prompts := []string{"bucket>   ", "prefix>   ", "region>   ", "profile>  ", "endpoint> "}
	placeholders := []string{
		"globally-unique bucket name", "sentra/", "us-east-1",
		"default", "http://localhost:9000 (S3-compatible only)",
	}
	values := []string{
		plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix,
		plan.Config.Repo.S3.Region, plan.Config.Repo.S3.Profile,
		plan.Config.Repo.S3.EndpointURL,
	}
	for i := range fields {
		ti := textinput.New()
		ti.Prompt = prompts[i]
		ti.Placeholder = placeholders[i]
		ti.SetValue(values[i])
		fields[i] = ti
	}
	fields[setupFieldBucket].Focus()

	newPass := textinput.New()
	newPass.Prompt = "pass>    "
	newPass.Placeholder = "repository passphrase"
	newPass.EchoMode = textinput.EchoPassword
	newPass.EchoCharacter = '•'
	confirmPass := textinput.New()
	confirmPass.Prompt = "confirm> "
	confirmPass.Placeholder = "retype passphrase"
	confirmPass.EchoMode = textinput.EchoPassword
	confirmPass.EchoCharacter = '•'

	return SetupWizardView{
		deps:         deps,
		engine:       eng,
		plan:         plan,
		fields:       fields,
		printIAM:     plan.PrintIAMPolicy,
		createBucket: plan.CreateBucket,
		blockPublic:  plan.BlockPublicAccess,
		defaultEnc:   plan.DefaultEncryption,
		initRepo:     plan.InitRepo,
		newPass:      newPass,
		confirmPass:  confirmPass,
		savePass:     plan.SavePassphrase,
	}
}

// config0 returns deps.Config dereferenced, or a zero config when nil, so
// the wizard renders (and computes defaults) even against an unconfigured
// TUI (first-run gate, tests).
func config0(deps Deps) config.Config {
	if deps.Config != nil {
		return *deps.Config
	}
	return config.Config{}
}

func (SetupWizardView) Init() tea.Cmd { return nil }

func (v SetupWizardView) Title() string { return "Setup" }

func (v SetupWizardView) ShortHelp() []key.Binding {
	switch v.stage {
	case stageProvision:
		return nil
	case stageDone, stageError:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "restart"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		}
	}
}

func (v SetupWizardView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil
	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// handleKey is filled in per-stage by later tasks. The skeleton only
// routes the backend stage so the view is hostable from the start.
func (v SetupWizardView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.stage == stageBackend {
		return v.handleBackendKey(msg)
	}
	return v, nil
}

func (v SetupWizardView) handleBackendKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if v.backendCursor > 0 {
			v.backendCursor--
		}
	case tea.KeyDown:
		if v.backendCursor < 1 {
			v.backendCursor++
		}
	}
	return v, nil
}

func (v SetupWizardView) View() string {
	var b strings.Builder
	switch v.stage {
	case stageBackend:
		b.WriteString(ui.Primary.Render("Sentra setup") + "\n\n")
		b.WriteString(ui.Muted.Render("Storage backend") + "\n\n")
		b.WriteString(v.backendLine(0, "AWS S3",
			"Sentra provisions and prepares the bucket for you."))
		b.WriteString("\n")
		b.WriteString(v.backendLine(1, "S3-compatible or existing bucket",
			"MinIO, LocalStack, or a bucket you manage yourself."))
		b.WriteString("\n\n" + ui.Muted.Render("↑/↓ choose · ⏎ next"))
	default:
		b.WriteString(ui.Muted.Render("setup"))
	}
	return b.String()
}

func (v SetupWizardView) backendLine(idx int, label, help string) string {
	cursor := "  "
	if v.backendCursor == idx {
		cursor = "> "
	}
	line := cursor + label + "  " + ui.Muted.Render(help)
	if v.backendCursor == idx {
		return ui.Primary.Render(line)
	}
	return line
}
