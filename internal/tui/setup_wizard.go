package tui

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
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

// setupReviewConfirmID ties the review ConfirmModal result back to this
// flow. Provisioning is gated behind it: the App broadcasts
// confirmedMsg{setupReviewConfirmID} on enter, and only then does the
// wizard emit its startOpMsg. Mirrors HuhSetupReviewConfirm.
const setupReviewConfirmID = "setup-apply"

// setupProgress tracks which provisioning checklist items completed, for
// the checklist rendered during stageProvision and stageDone.
type setupProgress struct {
	bucketCreated bool
	publicBlocked bool
	encryptionOn  bool
	repoInited    bool
}

// setupDoneMsg is the wizard's terminal op result. Setup PERFORMS AWS
// provisioning + config write + repo init, all under the repo advisory
// lock (repo.Init), so it is a mutating op: implementing opResult() clears
// the App's one-op guard.
//
// On a FIRST-RUN provision (the wizard launched with no live repo), the op
// re-opens the just-initialized repository and carries it here so the wizard
// can hand it to the App via repoReadyMsg — mirroring the unlock flow — instead
// of dead-ending on a shell whose views still hold a nil repo. repo/config are
// nil on a Settings re-entry (a repo is already live) and on any failure path.
type setupDoneMsg struct {
	steps  setupProgress
	auth   *setup.AWSAuthReport
	prep   *setup.AWSPrepareReport
	init   *setup.InitResult
	repo   *repo.Repo
	config *config.Config
	err    error
}

func (setupDoneMsg) opResult() {}

// awsAuthDoneMsg reports that the interactive `aws` child process launched
// via tea.ExecProcess has exited. tea.ExecProcess suspends the program,
// gives the child the terminal, and resumes on exit, delivering the
// process's error through the callback wrapped in this message. On success
// the wizard resumes the pre-auth flow (→ passphrase or review).
type awsAuthDoneMsg struct{ err error }

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

	// passphrase-stage masked inputs (new + confirm), focus flag, keyring
	// toggle, and last validation error.
	newPass     textinput.Model
	confirmPass textinput.Model
	focusConf   bool
	savePass    bool
	passErr     string
	// pass holds the verified passphrase between stagePassphrase and the
	// provisioning op; zeroized after the op consumes it.
	pass []byte

	// iamText is the rendered IAM policy for the stageIAMPreview short-circuit,
	// shown in a scrollable viewport.
	iamText     string
	iamViewport viewport.Model

	// provisioning progress + terminal result.
	reporter *opReporter
	steps    setupProgress
	result   setupDoneMsg
	notice   string

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

	// Seed the backend cursor from the (possibly inferred) backend so the
	// selector opens on the right row. DefaultPlan infers BackendS3Compatible
	// for a `sentra local` seed (endpoint_url + minioadmin env creds); without
	// this the cursor stays 0 (AWS) and advanceFromBackend's cursor==0 branch
	// overwrites the inferred plan with AWS defaults AND wipes the seeded
	// endpoint field.
	backendCursor := 0
	if plan.Backend == setup.BackendS3Compatible {
		backendCursor = 1
	}

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
		deps:          deps,
		engine:        eng,
		plan:          plan,
		backendCursor: backendCursor,
		fields:        fields,
		printIAM:      plan.PrintIAMPolicy,
		createBucket:  plan.CreateBucket,
		blockPublic:   plan.BlockPublicAccess,
		defaultEnc:    plan.DefaultEncryption,
		initRepo:      plan.InitRepo,
		newPass:       newPass,
		confirmPass:   confirmPass,
		savePass:      plan.SavePassphrase,
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
	case setupDoneMsg:
		if msg.err != nil {
			v.stage = stageError
			v.result = msg
			return v, nil
		}
		v.stage = stageDone
		v.steps = msg.steps
		v.result = msg
		// First-run success carries a live repo: forward repoReadyMsg so the
		// App rebuilds the shell against the now-open repo and lands on the
		// dashboard (mirrors unlock.go). Without a repo (Settings re-entry, or a
		// handoff open-miss), keep today's stageDone-only behavior — the app.go
		// generic opResultMsg handler still clears the one-op guard.
		if msg.repo != nil {
			ready := repoReadyMsg{repo: msg.repo, config: msg.config}
			return v, func() tea.Msg { return ready }
		}
		return v, nil

	case opRejectedMsg:
		if v.stage == stageProvision && msg.name == "setup" {
			// The App dropped our start closure without running it, so its
			// deferred crypto.Zeroize(pass) never fired and v.pass still holds
			// the plaintext. Wipe it here — otherwise the secret stays resident
			// indefinitely if the user abandons the wizard. Route back to
			// passphrase (NOT review): a nil v.pass at review would let the
			// confirm re-arm startProvision against an empty passphrase, so we
			// require re-entry to re-populate v.pass via commitPassphrase.
			crypto.Zeroize(v.pass)
			v.pass = nil
			v.stage = stagePassphrase
			v.newPass.Focus()
			v.confirmPass.Blur()
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == stageProvision {
			return v, opTick()
		}
		return v, nil

	case confirmedMsg:
		if msg.id != setupReviewConfirmID || v.stage != stageReview {
			return v, nil
		}
		v.notice = ""
		return v.startProvision()

	case awsAuthDoneMsg:
		if v.stage != stageActions {
			return v, nil
		}
		if msg.err != nil {
			// Interactive auth failed: show advice and stay on actions so the
			// operator can pick another method.
			v.notice = "AWS sign-in did not complete — pick another method or fix credentials"
			return v, nil
		}
		return v.afterAuth()

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
	case stagePassphrase:
		return v.handlePassphraseKey(msg)
	case stageReview:
		if msg.Type == tea.KeyEnter {
			return v.pushReviewConfirm()
		}
		return v, nil
	case stageDone:
		if msg.Type == tea.KeyEnter {
			fresh := NewSetupWizardView(v.deps)
			fresh.width, fresh.height = v.width, v.height
			return fresh, nil
		}
		return v, nil
	case stageError:
		return v.handleErrorKey(msg)
	}
	return v, nil
}

func (v SetupWizardView) handleErrorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Retry: return to review so the operator can re-confirm after
		// fixing credentials or the bucket name.
		v.stage = stageReview
		v.notice = ""
		return v, nil
	case tea.KeyEsc:
		// Abandon the wizard. The completed op already zeroized the shared
		// passphrase array, so v.pass is incidentally wiped — but make it
		// explicit so a future refactor of that aliasing can't silently
		// resurrect a plaintext-residency leak.
		crypto.Zeroize(v.pass)
		v.pass = nil
		fresh := NewSetupWizardView(v.deps)
		fresh.width, fresh.height = v.width, v.height
		return fresh, nil
	}
	return v, nil
}

func (v SetupWizardView) checklistLine(done bool, label string) string {
	mark := ui.Muted.Render("○")
	if done {
		mark = ui.Success.Render("●")
	}
	return "  " + mark + " " + label + "\n"
}

func (v SetupWizardView) pushReviewConfirm() (tea.Model, tea.Cmd) {
	body := "Apply this setup: prepare AWS (if selected), write the config, and initialize the repository.\n" +
		"No secrets are written to sentra.yaml, logs, or the setup draft."
	modal := NewConfirmModal("Review setup", body, setupReviewConfirmID, v.width, v.height)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startProvision moves into the provisioning stage and emits the single
// setup op. The op closure is finalized in the provisioning task; this
// establishes the stage transition and the startOpMsg name contract.
func (v SetupWizardView) startProvision() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.stage = stageProvision
	start := v.buildSetupOp()
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

// buildSetupOp is the single provisioning op. It sequences the engine:
// WriteDraft → (PrepareAWS) → WriteConfig → (InitRepo) → RemoveDraft, all
// off the UI goroutine. Interactive AWS auth (login/sso) is NOT run here —
// it needs the terminal and is issued via tea.ExecProcess before this op;
// by the time this runs, credentials are expected to be present so
// engine.PrepareAWS only touches S3. The stashed passphrase is the only
// long-lived secret copy and is zeroized on return.
func (v SetupWizardView) buildSetupOp() startOpMsg {
	eng := v.engine
	plan := v.plan // value copy: config + flags
	cfgPath := v.deps.ConfigPath
	pass := v.pass
	// firstRun captures whether the wizard launched WITHOUT a live repo (the
	// no-sentra.yaml landing). Only then does a successful init hand a live repo
	// to the App; a Settings re-entry (repo already open) keeps today's
	// stageDone-only behavior. newStore opens the just-initialized repo for that
	// handoff — nil in tests that don't exercise the first-run dashboard path.
	firstRun := v.deps.Repo == nil
	newStore := v.deps.NewStore
	return startOpMsg{
		name: "setup",
		run: func(ctx context.Context) tea.Msg {
			defer crypto.Zeroize(pass)
			if eng == nil {
				return setupDoneMsg{err: errors.New("setup engine unavailable (no effects wired)")}
			}
			var (
				steps setupProgress
				auth  *setup.AWSAuthReport
				prep  *setup.AWSPrepareReport
			)
			if err := eng.WriteDraft(cfgPath, &plan.Config); err != nil {
				return setupDoneMsg{err: err}
			}
			if plan.PrepareAWS {
				a, p, err := eng.PrepareAWS(ctx, &plan)
				if err != nil {
					return setupDoneMsg{err: err}
				}
				auth, prep = &a, &p
				steps.bucketCreated = p.BucketCreated || p.BucketExisted
				steps.publicBlocked = p.PublicAccessBlocked
				steps.encryptionOn = p.DefaultEncryptionEnabled
			}
			if err := eng.WriteConfig(cfgPath, &plan); err != nil {
				return setupDoneMsg{err: err}
			}
			var initRes *setup.InitResult
			if plan.InitRepo {
				res, err := eng.InitRepo(ctx, &plan.Config, pass, plan.SavePassphrase)
				if err != nil {
					return setupDoneMsg{err: err}
				}
				initRes = &res
				steps.repoInited = true
			}
			eng.RemoveDraft(cfgPath)
			done := setupDoneMsg{steps: steps, auth: auth, prep: prep, init: initRes}

			// First-run handoff: Engine.InitRepo opened+verified+CLOSED the repo
			// and returned only a report, so every view still holds a nil repo.
			// Re-open it here — while pass is still valid, BEFORE the deferred
			// zeroize fires on return (repo.Open derives its own key, so wiping
			// pass afterward is fine) — and carry the live repo so the wizard can
			// emit repoReadyMsg, exactly like unlock. We do NOT fail the setup on
			// an open error: provisioning already succeeded, so a handoff miss
			// just leaves the user on stageDone (they can relaunch), which is
			// strictly better than reporting a spurious setup failure.
			if firstRun && initRes != nil && newStore != nil {
				cfg := plan.Config // local copy: address is stable, not aliased to the closure's plan field
				if store, err := newStore(ctx, &cfg); err == nil {
					if r, err := repo.Open(ctx, store, pass); err == nil {
						done.repo = r
						done.config = &cfg
					}
				}
			}
			return done
		},
	}
}

func (v SetupWizardView) handlePassphraseKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	switch {
	case msg.Type == tea.KeyTab:
		// Cycle new → confirm → back to new; the keyring toggle uses its
		// own key (space) so a lone space isn't typed into a masked field.
		v.focusConf = !v.focusConf
		if v.focusConf {
			v.newPass.Blur()
			v.confirmPass.Focus()
		} else {
			v.confirmPass.Blur()
			v.newPass.Focus()
		}
		return v, nil
	case isSpace:
		// space toggles keyring storage without typing into the fields
		// (both fields mask; a lone space toggle mirrors sync.go's guard).
		v.savePass = !v.savePass
		return v, nil
	case msg.Type == tea.KeyEnter:
		return v.commitPassphrase()
	}
	var cmd tea.Cmd
	if v.focusConf {
		v.confirmPass, cmd = v.confirmPass.Update(msg)
	} else {
		v.newPass, cmd = v.newPass.Update(msg)
	}
	v.passErr = ""
	v.notice = "" // typing clears the reject/retry banner
	return v, cmd
}

// commitPassphrase validates length and constant-time equality (mirroring
// password.go:187-205), stashes the verified secret on v.pass for the
// provisioning op, and records the keyring choice into the plan via
// setup.ApplyPassphraseConfig (mirrors promptSetupPassphraseStorage,
// internal/cli/setup_wizard.go:515-538). The two throwaway compare copies
// are zeroized on return.
func (v SetupWizardView) commitPassphrase() (tea.Model, tea.Cmd) {
	newVal := []byte(v.newPass.Value())
	confVal := []byte(v.confirmPass.Value())
	defer crypto.Zeroize(newVal)
	defer crypto.Zeroize(confVal)
	if len(newVal) < minPasswordLen {
		v.passErr = fmt.Sprintf("passphrase must be at least %d characters", minPasswordLen)
		return v, nil
	}
	if subtle.ConstantTimeCompare(newVal, confVal) != 1 {
		v.passErr = "passphrases do not match"
		return v, nil
	}
	// Stash the ONLY long-lived copy; the provisioning op zeroizes it.
	v.pass = append([]byte(nil), newVal...)
	v.plan.SavePassphrase = v.savePass
	setup.ApplyPassphraseConfig(&v.plan)
	v.passErr = ""
	v.stage = stageReview
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
	// login/sso may need an interactive browser flow. Existing-credential
	// methods never do. Probe identity first; if creds are already present,
	// skip straight ahead.
	if method == setup.AWSAuthLogin || method == setup.AWSAuthSSO {
		if cmd, needAuth := v.maybeStartInteractiveAuth(method); needAuth {
			return v, cmd // stay on stageActions; awsAuthDoneMsg resumes us
		}
	}
	return v.afterAuth()
}

// maybeStartInteractiveAuth checks the AWS CLI is present and whether
// credentials already resolve. If the CLI is missing it pushes an
// ErrorModal built from setup.ErrorAdvice (NO brew auto-install in the
// TUI). If credentials are absent it returns a tea.ExecProcess command
// that suspends the program to run `aws` login/sso interactively; the
// returned bool is true when the flow must wait for awsAuthDoneMsg.
func (v SetupWizardView) maybeStartInteractiveAuth(method setup.AWSAuthMethod) (tea.Cmd, bool) {
	eff := v.deps.SetupEffects
	if eff == nil {
		return nil, false // no effects: fall through, provisioning op guards nil
	}
	ctx := ctxOrBackground(v.deps.Ctx)
	// C2: the TUI never brew-installs; pass a nil confirm and surface a
	// missing binary as an ErrorAdvice modal rather than attempting a fix.
	if _, err := eff.EnsureAWSCLI(ctx, nil); err != nil {
		advice := strings.Join(setup.ErrorAdvice(err, v.plan.Config), "\n")
		modal := NewErrorModal(err, advice, v.width, v.height)
		return func() tea.Msg { return pushModalMsg{modal: modal} }, true
	}
	// Credentials already available? Then no interactive step is needed.
	if err := eff.CheckAWSSDKIdentity(ctx, &v.plan.Config); err == nil {
		return nil, false
	}
	profile := v.plan.Config.Repo.S3.Profile
	region := v.plan.Config.Repo.S3.Region
	// Build the interactive child. tea.ExecProcess runs it with the
	// terminal, then delivers the exit error via awsAuthDoneMsg.
	c := interactiveAWSAuthCommand(ctx, eff, method, profile, region)
	return tea.ExecProcess(c, func(err error) tea.Msg { return awsAuthDoneMsg{err: err} }), true
}

// afterAuth continues the post-actions flow once credentials are settled:
// init-repo on → passphrase, off → review.
func (v SetupWizardView) afterAuth() (tea.Model, tea.Cmd) {
	if v.plan.InitRepo {
		v.stage = stagePassphrase
		v.newPass.Focus()
		v.confirmPass.Blur()
		return v, nil
	}
	v.plan.SavePassphrase = false
	setup.ApplyPassphraseConfig(&v.plan)
	v.stage = stageReview
	return v, nil
}

// interactiveAWSAuthCommand builds the `aws` subprocess for browser login
// or SSO login. It mirrors the effect layer's argument construction
// (internal/cli/setup_awscli.go DefaultAWSLogin / DefaultAWSSSOLogin) so
// tea.ExecProcess can own the terminal for the child directly — the effect
// funcs run the child themselves and cannot be suspended by the program.
func interactiveAWSAuthCommand(ctx context.Context, _ setup.Effects, method setup.AWSAuthMethod, profile, region string) *exec.Cmd {
	var args []string
	switch method {
	case setup.AWSAuthSSO:
		args = []string{"sso", "login"}
	default: // login
		args = []string{"login"}
		if r := strings.TrimSpace(region); r != "" {
			args = append(args, "--region", r)
		}
	}
	if p := strings.TrimSpace(profile); p != "" {
		args = append(args, "--profile", p)
	}
	return exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary; profile/region are user-selected AWS values.
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
		if v.notice != "" {
			b.WriteString(ui.Warn.Render(v.notice) + "\n\n")
		}
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
	case stagePassphrase:
		b.WriteString(ui.Primary.Render("Repository passphrase") + "\n\n")
		if v.notice != "" {
			b.WriteString(ui.Warn.Render(v.notice) + "\n\n")
		}
		b.WriteString(v.newPass.View() + "\n")
		b.WriteString(v.confirmPass.View() + "\n\n")
		box := "[ ]"
		if v.savePass {
			box = "[x]"
		}
		b.WriteString(box + " save passphrase in OS keyring (space toggles)\n")
		if v.passErr != "" {
			b.WriteString("\n" + ui.Danger.Render(v.passErr))
		}
		b.WriteString("\n" + ui.Muted.Render("⏎ next · tab field · space keyring"))
	case stageReview:
		b.WriteString(setup.ReviewText(v.deps.ConfigPath, v.plan))
		if v.notice != "" {
			b.WriteString(ui.Warn.Render(v.notice) + "\n")
		}
		b.WriteString("\n" + ui.Muted.Render("⏎ review & apply"))
	case stageProvision:
		b.WriteString(ui.Primary.Render("Applying setup…") + "\n\n")
		b.WriteString(v.checklistLine(v.steps.bucketCreated, "bucket created"))
		b.WriteString(v.checklistLine(v.steps.publicBlocked, "public access blocked"))
		b.WriteString(v.checklistLine(v.steps.encryptionOn, "default encryption on"))
		b.WriteString(v.checklistLine(v.steps.repoInited, "repository initialized"))
		b.WriteString("\n" + ui.Muted.Render("working under the repo lock…"))
	case stageDone:
		b.WriteString(ui.Success.Render("Setup complete") + "\n\n")
		b.WriteString(v.checklistLine(v.steps.bucketCreated, "bucket created"))
		b.WriteString(v.checklistLine(v.steps.publicBlocked, "public access blocked"))
		b.WriteString(v.checklistLine(v.steps.encryptionOn, "default encryption on"))
		b.WriteString(v.checklistLine(v.steps.repoInited, "repository initialized"))
		b.WriteString("\n" + ui.Muted.Render("⏎ restart setup"))
	case stageError:
		b.WriteString(ui.Danger.Render("Setup failed") + "\n\n")
		if v.result.err != nil {
			b.WriteString(v.result.err.Error() + "\n")
			for _, line := range setup.ErrorAdvice(v.result.err, v.plan.Config) {
				b.WriteString("\n" + ui.Subtle.Render(line))
			}
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ back to review · esc restart"))
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
