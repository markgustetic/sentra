package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
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

	// details-stage text inputs (bucket/prefix/region/profile/endpoint),
	// a cursor over them plus the "print IAM policy" toggle, and the last
	// validation error.
	fields      []textinput.Model
	fieldCursor int
	detailErr   string

	// printIAM toggles the "print IAM policy and stop" details control.
	printIAM bool

	// actions-stage cursor over the auth-method select and the four toggles,
	// with toggle state seeded from the plan's smart defaults.
	authCursor   int
	actionCursor int
	createBucket bool
	blockPublic  bool
	defaultEnc   bool
	initRepo     bool

	// passphrase-stage masked inputs (new + confirm) and the keyring toggle.
	newPass     textinput.Model
	confirmPass textinput.Model
	savePass    bool

	// iamText is the rendered IAM policy for the stageIAMPreview short-circuit,
	// shown in a scrollable viewport.
	iamText     string
	iamViewport viewport.Model

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

// setupAuthOrder lists the auth methods in stageActions cursor order,
// matching the cli wizard's option order
// (internal/cli/setup_wizard.go:386-393).
var setupAuthOrder = []setup.AWSAuthMethod{
	setup.AWSAuthLogin, setup.AWSAuthSSO, setup.AWSAuthExisting, setup.AWSAuthSkip,
}

// action-stage cursor rows: the auth select, then the four toggles.
const (
	actionRowAuth = iota
	actionRowCreate
	actionRowBlock
	actionRowEncrypt
	actionRowInit
	actionRowCount
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

// handleKey dispatches per-stage. Later tasks add the remaining cases.
func (v SetupWizardView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case stageBackend:
		if msg.Type == tea.KeyEnter {
			return v.advanceFromBackend()
		}
		return v.handleBackendKey(msg)
	case stageDetails:
		return v.handleDetailsKey(msg)
	case stageIAMPreview:
		return v.handleIAMKey(msg)
	case stageActions:
		return v.handleActionsKey(msg)
	}
	return v, nil
}

func (v SetupWizardView) handleIAMKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyEnter || msg.Type == tea.KeyEsc {
		// Restart the wizard: printing the policy is a terminal action, same
		// as the cli wizard returning after writeSetupIAMPolicy.
		fresh := NewSetupWizardView(v.deps)
		fresh.width, fresh.height = v.width, v.height
		return fresh, nil
	}
	var cmd tea.Cmd
	v.iamViewport, cmd = v.iamViewport.Update(msg)
	return v, cmd
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

// advanceFromBackend records the chosen backend into the plan and seeds
// details defaults, mirroring the cli wizard's runHuhAWSSetup /
// runHuhCompatibleSetup entry (internal/cli/setup_wizard.go:202-206).
func (v SetupWizardView) advanceFromBackend() (tea.Model, tea.Cmd) {
	if v.backendCursor == 0 {
		v.plan.Backend = setup.BackendAWS
		// AWS defaults: sentra/ prefix, us-east-1 region if unset
		// (internal/cli/setup_wizard.go:296-304).
		if strings.TrimSpace(v.fields[setupFieldRegion].Value()) == "" {
			v.fields[setupFieldRegion].SetValue("us-east-1")
		}
		if strings.TrimSpace(v.fields[setupFieldPrefix].Value()) == "" {
			v.fields[setupFieldPrefix].SetValue("sentra/")
		}
		v.fields[setupFieldEndpoint].SetValue("") // AWS backend forbids endpoint_url
	} else {
		v.plan.Backend = setup.BackendS3Compatible
	}
	v.stage = stageDetails
	v.fieldCursor = setupFieldBucket
	v.focusOnlyField(setupFieldBucket)
	return v, nil
}

// detailFieldCount is 5 for S3-compatible (endpoint shown) and 4 for AWS
// (endpoint suppressed — AWS setup rejects endpoint_url,
// internal/cli/setup.go:227-229).
func (v SetupWizardView) detailFieldCount() int {
	if v.plan.Backend == setup.BackendAWS {
		return setupFieldEndpoint // 4: bucket..profile
	}
	return setupFieldCount // 5: adds endpoint
}

func (v SetupWizardView) focusOnlyField(idx int) {
	for i := range v.fields {
		if i == idx {
			v.fields[i].Focus()
		} else {
			v.fields[i].Blur()
		}
	}
}

func (v SetupWizardView) handleDetailsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// space toggles the "print IAM policy" control when it owns the cursor
	// (AWS backend only). fieldCursor == detailFieldCount() addresses that
	// pseudo-field just past the text inputs.
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	iamCursor := v.detailFieldCount()
	switch {
	case msg.Type == tea.KeyTab:
		limit := v.detailFieldCount()
		if v.plan.Backend == setup.BackendAWS {
			limit++ // include the IAM toggle pseudo-field
		}
		v.fieldCursor = (v.fieldCursor + 1) % limit
		if v.fieldCursor < v.detailFieldCount() {
			v.focusOnlyField(v.fieldCursor)
		} else {
			for i := range v.fields {
				v.fields[i].Blur()
			}
		}
		return v, nil
	case msg.Type == tea.KeyEnter:
		return v.commitDetails()
	case isSpace && v.plan.Backend == setup.BackendAWS && v.fieldCursor == iamCursor:
		v.printIAM = !v.printIAM
		return v, nil
	}
	if v.fieldCursor < v.detailFieldCount() {
		var cmd tea.Cmd
		v.fields[v.fieldCursor], cmd = v.fields[v.fieldCursor].Update(msg)
		v.detailErr = ""
		return v, cmd
	}
	return v, nil
}

// commitDetails validates the bucket, writes the S3 fields into the plan
// config, and routes to the next stage. It mirrors the cli wizard's
// bucket-required + validateSetupBucketName gate
// (internal/cli/setup_wizard.go:319-324) via setup.ValidateBucketName.
func (v SetupWizardView) commitDetails() (tea.Model, tea.Cmd) {
	bucket := strings.TrimSpace(v.fields[setupFieldBucket].Value())
	if bucket == "" {
		v.detailErr = "bucket is required"
		return v, nil
	}
	if err := setup.ValidateBucketName(bucket); err != nil {
		v.detailErr = err.Error()
		return v, nil
	}
	v.plan.Config.Repo.S3.Bucket = bucket
	v.plan.Config.Repo.S3.Prefix = strings.TrimSpace(v.fields[setupFieldPrefix].Value())
	v.plan.Config.Repo.S3.Region = strings.TrimSpace(v.fields[setupFieldRegion].Value())
	v.plan.Config.Repo.S3.Profile = strings.TrimSpace(v.fields[setupFieldProfile].Value())
	if v.plan.Backend == setup.BackendAWS {
		v.plan.Config.Repo.S3.EndpointURL = ""
	} else {
		v.plan.Config.Repo.S3.EndpointURL = strings.TrimSpace(v.fields[setupFieldEndpoint].Value())
	}
	setup.NormalizeConfig(&v.plan.Config)

	if v.plan.Backend == setup.BackendAWS && v.printIAM {
		v.iamText = renderIAMPolicy(bucket, v.plan.Config.Repo.S3.Prefix)
		// Size the viewport to the policy's own height so the whole
		// least-privilege document is visible without scrolling when the
		// terminal has the room; a shorter terminal still scrolls. The
		// operator must be able to read the entire policy before copying it.
		contentLines := strings.Count(v.iamText, "\n") + 1
		height := max(v.height-8, 6)
		if contentLines > height {
			height = contentLines
		}
		vp := viewport.New(max(v.width-8, 20), height)
		vp.SetContent(v.iamText)
		v.iamViewport = vp
		v.stage = stageIAMPreview
		return v, nil
	}
	if v.plan.Backend == setup.BackendS3Compatible {
		// S3-compatible never touches AWS: config-only + no actions stage
		// (internal/cli/setup_wizard.go:502-507).
		v.plan.PrepareAWS = false
		v.plan.AWSAuthMethod = setup.AWSAuthSkip
		v.plan.CreateBucket = false
		v.plan.BlockPublicAccess = false
		v.plan.DefaultEncryption = false
		v.stage = stagePassphrase
		v.newPass.Focus()
		v.confirmPass.Blur()
		return v, nil
	}
	v.stage = stageActions
	return v, nil
}

func (v SetupWizardView) handleActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	switch {
	case msg.Type == tea.KeyTab || msg.Type == tea.KeyDown:
		v.actionCursor = (v.actionCursor + 1) % actionRowCount
		return v, nil
	case msg.Type == tea.KeyUp:
		v.actionCursor = (v.actionCursor - 1 + actionRowCount) % actionRowCount
		return v, nil
	case msg.Type == tea.KeyLeft && v.actionCursor == actionRowAuth:
		if v.authCursor > 0 {
			v.authCursor--
		}
		return v, nil
	case msg.Type == tea.KeyRight && v.actionCursor == actionRowAuth:
		if v.authCursor < len(setupAuthOrder)-1 {
			v.authCursor++
		}
		return v, nil
	case isSpace:
		switch v.actionCursor {
		case actionRowCreate:
			v.createBucket = !v.createBucket
		case actionRowBlock:
			v.blockPublic = !v.blockPublic
		case actionRowEncrypt:
			v.defaultEnc = !v.defaultEnc
		case actionRowInit:
			v.initRepo = !v.initRepo
		}
		return v, nil
	case msg.Type == tea.KeyEnter:
		return v.advanceFromActions()
	}
	return v, nil
}

// advanceFromActions records the selected auth method and toggles into the
// plan and routes onward. It mirrors runHuhAWSSetup's tail
// (internal/cli/setup_wizard.go:423-434): skip → config-only, otherwise
// PrepareAWS with the chosen actions.
func (v SetupWizardView) advanceFromActions() (tea.Model, tea.Cmd) {
	method := setupAuthOrder[v.authCursor]
	v.plan.AWSAuthMethod = method
	v.plan.CreateBucket = v.createBucket
	v.plan.BlockPublicAccess = v.blockPublic
	v.plan.DefaultEncryption = v.defaultEnc
	v.plan.InitRepo = v.initRepo
	v.plan.PrepareAWS = true
	if method == setup.AWSAuthSkip {
		setup.ApplyAWSConfigOnly(&v.plan)
		setup.NormalizeConfig(&v.plan.Config)
		v.stage = stageReview
		return v, nil
	}
	setup.NormalizeConfig(&v.plan.Config)
	if v.plan.InitRepo {
		v.stage = stagePassphrase
		v.newPass.Focus()
		v.confirmPass.Blur()
		return v, nil
	}
	// init-repo off: no passphrase to collect.
	v.plan.SavePassphrase = false
	setup.ApplyPassphraseConfig(&v.plan)
	v.stage = stageReview
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
	case stageDetails:
		b.WriteString(ui.Primary.Render("Storage details") + "\n\n")
		labels := []string{"S3 bucket", "S3 key prefix", "AWS region", "AWS profile", "S3 endpoint URL"}
		for i := 0; i < v.detailFieldCount(); i++ {
			cursor := "  "
			if v.fieldCursor == i {
				cursor = "> "
			}
			b.WriteString(cursor + ui.Muted.Render(labels[i]) + "\n")
			b.WriteString("  " + v.fields[i].View() + "\n")
		}
		if v.plan.Backend == setup.BackendAWS {
			box := "[ ]"
			if v.printIAM {
				box = "[x]"
			}
			cursor := "  "
			if v.fieldCursor == v.detailFieldCount() {
				cursor = "> "
			}
			b.WriteString(cursor + box + " print IAM policy and stop before any changes\n")
		}
		if v.detailErr != "" {
			b.WriteString("\n" + ui.Danger.Render(v.detailErr))
		}
		b.WriteString("\n" + ui.Muted.Render("⏎ next · tab field · space toggle"))
	case stageIAMPreview:
		b.WriteString(ui.Primary.Render("IAM policy (no changes were made)") + "\n\n")
		b.WriteString(v.iamViewport.View())
		b.WriteString("\n\n" + ui.Muted.Render("↑/↓ scroll · ⏎/esc restart setup"))
	case stageActions:
		b.WriteString(ui.Primary.Render("Setup actions") + "\n\n")
		authCursor := "  "
		if v.actionCursor == actionRowAuth {
			authCursor = "> "
		}
		b.WriteString(authCursor + ui.Muted.Render("AWS sign-in: ") +
			setupAuthMethodLabel(setupAuthOrder[v.authCursor]) + "\n")
		if v.actionCursor == actionRowAuth {
			b.WriteString("  " + ui.Muted.Render("←/→ change method") + "\n")
		}
		b.WriteString(v.actionToggle(actionRowCreate, "create missing bucket", v.createBucket))
		b.WriteString(v.actionToggle(actionRowBlock, "block public access", v.blockPublic))
		b.WriteString(v.actionToggle(actionRowEncrypt, "default encryption (AES-256)", v.defaultEnc))
		b.WriteString(v.actionToggle(actionRowInit, "initialize repository", v.initRepo))
		b.WriteString("\n" + ui.Muted.Render("⏎ next · ↑/↓ row · ←/→ method · space toggle"))
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

func (v SetupWizardView) actionToggle(row int, label string, on bool) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	cursor := "  "
	if v.actionCursor == row {
		cursor = "> "
	}
	line := cursor + box + " " + label + "\n"
	if v.actionCursor == row {
		return ui.Primary.Render(line)
	}
	return line
}

// setupAuthMethodLabel mirrors setupAWSAuthMethodLabel
// (internal/cli/setup_summary.go:132) for the TUI select row.
func setupAuthMethodLabel(m setup.AWSAuthMethod) string {
	switch m {
	case setup.AWSAuthLogin:
		return "browser login"
	case setup.AWSAuthSSO:
		return "IAM Identity Center / SSO"
	case setup.AWSAuthExisting:
		return "existing credentials"
	case setup.AWSAuthSkip:
		return "config only"
	default:
		return string(m)
	}
}

// renderIAMPolicy formats the least-privilege policy for the bucket/prefix
// using the engine's writer, so the TUI preview and the cli/`setup
// iam-policy` output are byte-identical.
func renderIAMPolicy(bucket, prefix string) string {
	var sb strings.Builder
	if err := setup.WriteIAMPolicy(&sb, bucket, prefix); err != nil {
		return "failed to render IAM policy: " + err.Error()
	}
	return sb.String()
}
