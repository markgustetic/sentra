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
	"github.com/charmbracelet/lipgloss"

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
// wizard emit its startOpMsg.
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

// SetupWizardView drives the in-TUI setup wizard. It began as the TUI-native
// re-expression of the deleted huh cli wizard, and is now the only one:
// every huh step became an inline bubbles control because huh.Form.Run
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

	// history is the stack of stages the operator advanced through, so esc can
	// step back one at a time and retrace the exact path — skips included (AWS
	// vs S3-compatible). Update pushes the departed stage on every forward move;
	// goBack pops it. A restart (fresh wizard) starts with an empty history.
	history []setupStage

	plan setup.Plan

	// backend-stage cursor over the two backends.
	backendCursor int

	// backendLocked pins the backend to S3-compatible: set when the wizard is
	// seeded with an endpoint_url (e.g. `sentra local`), which is S3-compatible
	// by definition since AWS setup rejects endpoint_url. When locked the
	// backend stage offers only the S3-compatible option and cannot take the
	// AWS branch, so the seeded endpoint is never cleared.
	backendLocked bool

	// configuredProfile is the AWS profile the operator wrote into their own
	// sentra.yaml, empty when they never set one. Anything else in the profile
	// field came from DefaultPlan's smart defaults, and setup.ApplyBackendChoice
	// drops it once the backend turns out to be S3-compatible.
	configuredProfile string

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

	// backupUser is the "create dedicated backup user" toggle; backupProfile
	// the ~/.aws/credentials section it writes to. Both live only on the
	// actions stage and are re-seeded when the auth method changes.
	backupUser    bool
	backupProfile textinput.Model

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
// recommended-first: browser login is the easiest local path.
var setupAuthOrder = []setup.AWSAuthMethod{
	setup.AWSAuthLogin, setup.AWSAuthSSO, setup.AWSAuthExisting, setup.AWSAuthSkip,
}

// action-stage cursor rows: the auth select, the four toggles, then the
// backup-user toggle and its profile input. The last two are visible only
// for login/SSO (and the profile only while the toggle is on) — see
// actionRowVisible; the cursor never lands on a hidden row.
const (
	actionRowAuth = iota
	actionRowCreate
	actionRowBlock
	actionRowEncrypt
	actionRowInit
	actionRowBackupUser
	actionRowProfile
	actionRowCount
)

func NewSetupWizardView(deps Deps) SetupWizardView {
	cfg := config0(deps)
	var eng *setup.Engine
	if deps.SetupEffects != nil {
		eng = setup.NewEngine(deps.SetupEffects)
	}
	plan := setup.DefaultPlan(cfg, setup.DefaultEnvProbe())

	// A seeded endpoint_url means the target is S3-compatible by definition
	// (MinIO/LocalStack/self-managed): AWS setup rejects endpoint_url outright,
	// so there is no AWS option to offer. Lock the backend here — this also
	// covers the case where DefaultPlan's inference did NOT fire because the
	// env-credential half was absent (endpoint present, no ambient creds), so
	// the endpoint alone is authoritative.
	backendLocked := strings.TrimSpace(plan.Config.Repo.S3.EndpointURL) != ""
	if backendLocked {
		plan.Backend = setup.BackendS3Compatible
	}

	// The operator's own profile, if any. Anything else in the profile field was
	// invented by DefaultPlan and must not follow an S3-compatible backend.
	configuredProfile := strings.TrimSpace(cfg.Repo.S3.Profile)

	// Seed the backend cursor from the (possibly inferred/locked) backend so the
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
	placeholders := []string{
		"globally-unique bucket name", "sentra/", "us-east-1",
		"default", "http://localhost:9000",
	}
	values := []string{
		plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix,
		plan.Config.Repo.S3.Region, plan.Config.Repo.S3.Profile,
		plan.Config.Repo.S3.EndpointURL,
	}
	for i := range fields {
		ti := textinput.New()
		// No prompt: the row's label IS the prompt, so the selection marker
		// lands on the line being typed into rather than one line above it.
		// Width is bounded so a long bucket name cannot shove the layout.
		ti.Prompt = ""
		ti.Width = 40
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

	backupProfile := textinput.New()
	backupProfile.Prompt = ""
	backupProfile.Width = 24
	backupProfile.Placeholder = setup.DefaultBackupUserProfile

	v := SetupWizardView{
		deps:              deps,
		engine:            eng,
		plan:              plan,
		backendCursor:     backendCursor,
		backendLocked:     backendLocked,
		configuredProfile: configuredProfile,
		fields:            fields,
		printIAM:          plan.PrintIAMPolicy,
		createBucket:      plan.CreateBucket,
		blockPublic:       plan.BlockPublicAccess,
		defaultEnc:        plan.DefaultEncryption,
		initRepo:          plan.InitRepo,
		backupProfile:     backupProfile,
		newPass:           newPass,
		confirmPass:       confirmPass,
		savePass:          plan.SavePassphrase,
	}
	return v.seedBackupUserDefault()
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

// ConsumesArrows: the backend selector, the actions rows, and the IAM preview's
// viewport all use up/down. The details and passphrase stages are text entry
// (see CapturesText) and navigate with tab; review onward take single keys.
//
// On first run the wizard is a startup gate and routeKey hands it every key
// before this is consulted, so a first-run operator can never arrow away.
func (v SetupWizardView) ConsumesArrows() bool {
	switch v.stage {
	case stageBackend, stageActions, stageIAMPreview:
		return true
	default:
		return false
	}
}

func (v SetupWizardView) Title() string { return "Setup" }

// CapturesText reports the stages that focus a text input: the details stage
// (the S3 bucket/prefix/region/… fields), the passphrase stage (the masked
// new/confirm fields), and the actions stage while the backup-user profile
// row is focused. On those the shell must route every rune here so a bucket
// name digit, a passphrase 'q', or a profile name isn't stolen by a global
// binding.
func (v SetupWizardView) CapturesText() bool {
	if v.stage == stageActions {
		return v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile)
	}
	return v.stage == stageDetails || v.stage == stagePassphrase
}

// ConsumesEscape: esc means something to the wizard on most stages — stepping
// back a stage where it can (canGoBack), and restarting on the IAM preview and
// error screens — so the shell must not treat it as "leave to the rail". On
// first run the wizard is a startup gate, so routeKey hands it every key before
// this is consulted; from Settings it is an ordinary view, and this is what
// keeps esc-to-go-back working there too. Only the backend stage (nothing
// behind it) lets esc fall through to leave the view.
func (v SetupWizardView) ConsumesEscape() bool {
	return v.canGoBack() || v.stage == stageIAMPreview || v.stage == stageError
}

func (v SetupWizardView) ShortHelp() []key.Binding {
	switch v.stage {
	case stageProvision:
		return nil
	case stageDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "restart"))}
	case stageError:
		// enter retries (via passphrase re-entry) and esc is the restart, so the
		// two terminal stages cannot share one binding.
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "retry")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "restart")),
		}
	default:
		keys := []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "next")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		}
		if v.canGoBack() {
			keys = append(keys, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")))
		}
		return keys
	}
}

// Update wraps the per-message dispatch to maintain the stage history: every
// message flows through here (keys AND async results like awsAuthDoneMsg), so a
// single rule — "when the stage advanced, remember the stage we left" — records
// the operator's exact path without instrumenting each transition site. Backward
// moves (goBack) and restarts (a fresh wizard at stage 0) never increase the
// stage, so they never push; esc then pops this stack to retrace the path.
func (v SetupWizardView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	before := v.stage
	model, cmd := v.updateInner(msg)
	if nv, ok := model.(SetupWizardView); ok && nv.stage > before {
		nv.history = append(nv.history, before)
		return nv, cmd
	}
	return model, cmd
}

func (v SetupWizardView) updateInner(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		return v, nil
	case setupDoneMsg:
		if msg.err != nil {
			v.stage = stageError
			v.result = msg
			// Drop the stash on the way to the failure screen. The op aliased
			// v.pass and zeroized it on return, but crypto.Zeroize wipes IN
			// PLACE without truncating — so what survives here is a full-length
			// run of zero bytes, which review's len(v.pass) == 0 confirm guard
			// happily accepts and would derive the repository key from. Nil it
			// so that guard means what it says on every route out of a failure.
			crypto.Zeroize(v.pass)
			v.pass = nil
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
			// the plaintext. Route back through enterPassphraseStage: it wipes
			// the stash (otherwise the secret stays resident indefinitely if the
			// user abandons the wizard), re-establishes it from whichever source
			// armed it in the first place, and truncates the history so esc
			// cannot walk forward past the re-entry. Re-entry is mandatory only
			// when that source was the operator — a nil v.pass at review would
			// let the confirm re-arm startProvision against an empty passphrase,
			// and prompting someone whose passphrase came from the environment
			// would invite them to type a different one.
			v = v.enterPassphraseStage()
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
		// Review must never arm repo initialization without a verified
		// passphrase in hand — an empty one would silently derive the
		// repository key from "". Whatever path lands here with the stash
		// wiped goes back through re-entry instead.
		//
		// Scoped to InitRepo because that is the only consumer of the stash:
		// a config-only plan (init-repo toggled off, or the skip auth method)
		// legitimately reaches review having never visited stagePassphrase,
		// and demanding one there would strand that path on a prompt whose
		// answer provisioning never reads.
		if v.plan.InitRepo && len(v.pass) == 0 {
			v.stage = stagePassphrase
			v.focusConf = false
			v.newPass.Focus()
			v.confirmPass.Blur()
			v.notice = "enter the repository passphrase to continue"
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
	// esc steps back one stage on the linear data-entry path, retracing
	// v.history and keeping every entry, so a too-short passphrase or a mistyped
	// bucket is a step back rather than a restart. The terminal-ish stages keep
	// their own esc: the IAM-policy preview and the error screen restart (the
	// error path also zeroizes the passphrase), and nothing sits behind backend.
	if msg.Type == tea.KeyEsc && v.canGoBack() {
		return v.goBack(), nil
	}

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

// canGoBack reports whether esc should step back a stage: true on the linear
// data-entry stages once the operator has advanced past backend. The IAM
// preview and error stages own esc themselves, and backend has nothing behind
// it, so they are excluded.
func (v SetupWizardView) canGoBack() bool {
	switch v.stage {
	case stageDetails, stageActions, stagePassphrase, stageReview:
		return len(v.history) > 0
	}
	return false
}

// goBack pops the stage history, returning to the previous stage with entries
// intact so the operator can fix an earlier answer (a mistyped bucket, a too-
// short passphrase) rather than restarting. It re-establishes input focus for
// the target stage; returning to passphrase zeroizes any stashed secret so it is
// re-entered, matching the flow's plaintext-residency discipline. With an empty
// history (the backend stage) it is a no-op.
func (v SetupWizardView) goBack() SetupWizardView {
	n := len(v.history)
	if n == 0 {
		return v
	}
	v.stage = v.history[n-1]
	v.history = v.history[:n-1]
	v.notice = ""
	v.detailErr = ""
	v.passErr = ""
	switch v.stage {
	case stageDetails:
		v.focusOnlyField(v.fieldCursor)
	case stagePassphrase:
		crypto.Zeroize(v.pass)
		v.pass = nil
		v.focusConf = false
		v.newPass.Focus()
		v.confirmPass.Blur()
	}
	return v
}

func (v SetupWizardView) handleErrorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Retry: back to the passphrase, NOT straight to review. The failed op
		// already consumed the stash (see enterPassphraseStage), so a retry that
		// re-entered at review would let the very next keypress provision the
		// repository under a run of zero bytes instead of the operator's
		// passphrase. The masked fields keep what was typed, so re-confirming is
		// one keypress once the credentials or bucket name are fixed.
		v = v.enterPassphraseStage()
		v.notice = "confirm the repository passphrase to retry"
		return v, nil
	case tea.KeyEsc:
		// Abandon the wizard. Both the op's deferred zeroize and the failure
		// handler have already wiped the stash by now — keep this explicit so a
		// future refactor of either can't silently resurrect a plaintext-
		// residency leak.
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

// pushReviewConfirm raises the gate that arms provisioning. On a reconfigure it
// names the file about to be replaced IN THE MODAL, not only on the stage
// behind it: ConfirmModal's enter confirms, so a bare Enter applies, where the
// huh confirm this flow replaced initialized to false and a bare Enter meant
// Cancel. A body identical on a first run and on a reconfigure would let that
// reflex overwrite an existing config.
func (v SetupWizardView) pushReviewConfirm() (tea.Model, tea.Cmd) {
	body := "Apply this setup: prepare AWS (if selected), write the config, and initialize the repository.\n" +
		"No secrets are written to sentra.yaml, logs, or the setup draft."
	if v.deps.Reconfigure {
		body += fmt.Sprintf("\nThis overwrites the existing config at %s.", v.deps.ConfigPath)
	}
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
// setup.ApplyPassphraseConfig. The two throwaway compare copies are zeroized
// on return.
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
	if v.backendLocked {
		// Only the S3-compatible row exists; there is nothing to move to.
		return v, nil
	}
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

// advanceFromBackend records the chosen backend into the plan and seeds the
// details defaults for that branch.
func (v SetupWizardView) advanceFromBackend() (tea.Model, tea.Cmd) {
	backend := setup.BackendS3Compatible
	if !v.backendLocked && v.backendCursor == 0 {
		backend = setup.BackendAWS
	}
	// The backend's field hygiene — AWS forbids endpoint_url, S3-compatible
	// drops an inferred AWS profile — lives in setup.ApplyBackendChoice. This
	// is its only production caller: DefaultPlan's inference path upholds the
	// same invariant a different way, by settling the backend before
	// inferring a profile and inferring none for S3-compatible targets. Both
	// mechanisms drop only an *inferred* profile — one the operator wrote
	// into their own config still stands.
	setup.ApplyBackendChoice(&v.plan, backend, v.configuredProfile)

	if backend == setup.BackendAWS {
		// AWS defaults: sentra/ prefix, us-east-1 region if unset.
		if strings.TrimSpace(v.fields[setupFieldRegion].Value()) == "" {
			v.fields[setupFieldRegion].SetValue("us-east-1")
		}
		if strings.TrimSpace(v.fields[setupFieldPrefix].Value()) == "" {
			v.fields[setupFieldPrefix].SetValue("sentra/")
		}
	}
	// Mirror the settled plan back into the inputs the operator is about to see.
	v.fields[setupFieldEndpoint].SetValue(v.plan.Config.Repo.S3.EndpointURL)
	v.fields[setupFieldProfile].SetValue(v.plan.Config.Repo.S3.Profile)

	v.stage = stageDetails
	v.fieldCursor = setupFieldBucket
	v.focusOnlyField(setupFieldBucket)
	return v, nil
}

// detailFieldCount is 5 for S3-compatible (endpoint shown) and 4 for AWS
// (endpoint suppressed — AWS setup rejects endpoint_url, so the field would be
// an invitation to enter something commitDetails must then clear).
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
// config, and routes to the next stage. Its bucket-required +
// setup.ValidateBucketName pair is the ONLY bucket gate in the product, so
// neither branch may be dropped on the assumption that something downstream
// re-checks — nothing does.
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
		// S3-compatible never touches AWS: config-only + no actions stage.
		v.plan.PrepareAWS = false
		v.plan.AWSAuthMethod = setup.AWSAuthSkip
		v.plan.CreateBucket = false
		v.plan.BlockPublicAccess = false
		v.plan.DefaultEncryption = false
		return v.enterPassphraseStage(), nil
	}
	v.stage = stageActions
	return v, nil
}

func (v SetupWizardView) handleActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	onProfile := v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile)
	switch {
	case msg.Type == tea.KeyTab || msg.Type == tea.KeyDown:
		return v.moveActionCursor(+1), nil
	case msg.Type == tea.KeyUp:
		return v.moveActionCursor(-1), nil
	case msg.Type == tea.KeyLeft && v.actionCursor == actionRowAuth:
		if v.authCursor > 0 {
			v.authCursor--
		}
		return v.seedBackupUserDefault(), nil
	case msg.Type == tea.KeyRight && v.actionCursor == actionRowAuth:
		if v.authCursor < len(setupAuthOrder)-1 {
			v.authCursor++
		}
		return v.seedBackupUserDefault(), nil
	case isSpace && !onProfile:
		switch v.actionCursor {
		case actionRowCreate:
			v.createBucket = !v.createBucket
		case actionRowBlock:
			v.blockPublic = !v.blockPublic
		case actionRowEncrypt:
			v.defaultEnc = !v.defaultEnc
		case actionRowInit:
			v.initRepo = !v.initRepo
		case actionRowBackupUser:
			v.backupUser = !v.backupUser
		}
		return v.syncProfileFocus(), nil
	case msg.Type == tea.KeyEnter:
		return v.advanceFromActions()
	}
	if onProfile {
		var cmd tea.Cmd
		v.backupProfile, cmd = v.backupProfile.Update(msg)
		v.notice = ""
		return v, cmd
	}
	return v, nil
}

// advanceFromActions records the selected auth method and toggles into the
// plan and routes onward: skip → config-only, otherwise PrepareAWS with the
// chosen actions.
func (v SetupWizardView) advanceFromActions() (tea.Model, tea.Cmd) {
	method := setupAuthOrder[v.authCursor]
	v.plan.AWSAuthMethod = method
	v.plan.CreateBucket = v.createBucket
	v.plan.BlockPublicAccess = v.blockPublic
	v.plan.DefaultEncryption = v.defaultEnc
	v.plan.InitRepo = v.initRepo
	v.plan.PrepareAWS = true
	v.plan.ProvisionBackupUser = v.backupUserOffered() && v.backupUser
	v.plan.BackupUserProfile = ""
	if v.plan.ProvisionBackupUser {
		profile := strings.TrimSpace(v.backupProfile.Value())
		if profile == "" {
			profile = setup.DefaultBackupUserProfile
		}
		if err := setup.ValidateBackupUserProfile(profile); err != nil {
			// Stay here with the input focused: the profile is the only thing
			// the operator can fix, so put the cursor on it.
			v.notice = err.Error()
			v.actionCursor = actionRowProfile
			return v.syncProfileFocus(), nil
		}
		v.plan.BackupUserProfile = profile
	}
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
		return v.enterPassphraseStage(), nil
	}
	v.plan.SavePassphrase = false
	setup.ApplyPassphraseConfig(&v.plan)
	v.stage = stageReview
	return v, nil
}

// enterPassphraseStage is the ONE way into the passphrase stage, forward
// (advanceFromBackend, afterAuth) and backward (a rejected op, an error retry).
// It first asks the non-interactive sources — --passphrase-file, then
// SENTRA_PASSPHRASE — and when one answers it stashes that secret, records the
// source label for the review screen, and goes straight to review without ever
// showing an entry field.
//
// That skip is the point, not an optimization. `sentra setup` honored those
// sources before the wizard became its only surface, and QUICKSTART still tells
// operators to export SENTRA_PASSPHRASE. Prompting anyway would let them type a
// different secret: the repo initializes under what they typed, while every
// later command resolves the env var (which outranks the keyring) and fails to
// decrypt — a silent, arbitrarily delayed failure whose error never mentions
// setup.
//
// A named-but-unusable file (missing, or group/world-readable) is NOT a clean
// miss: it lands on the entry stage with the reason shown, so the operator
// either fixes the file or types a passphrase deliberately, rather than being
// silently prompted for a source they thought was configured.
//
// The keyring choice is untouched by any of this — it is a separate decision
// (the deleted huh wizard asked it independently of the passphrase source), so
// the plan keeps whatever v.savePass holds and review states the outcome.
//
// Wiping first is also what makes review's len(v.pass) == 0 confirm guard mean
// what it says on the failure routes. buildSetupOp aliases v.pass and zeroizes
// it on return, and crypto.Zeroize wipes IN PLACE without truncating — so a
// "used" stash is a full-length run of zero bytes, not an empty slice, which
// would sail through that guard and derive the repository key (and, with saving
// on, the keyring entry) from those zeros.
//
// The backward callers arrive with the history stack still ending at the entry
// pushed on the way into provisioning, because a forced return is a stage
// DECREASE and the Update wrapper only records increases. Truncating to the
// stages strictly behind passphrase is what stops esc popping that stale entry
// and walking FORWARD to review, skipping the re-entry this just made
// mandatory. It is a no-op on the forward path, where nothing at or past
// stagePassphrase has been pushed yet.
func (v SetupWizardView) enterPassphraseStage() SetupWizardView {
	// Any earlier stash is dead the moment we re-enter this stage (esc back to
	// details, then forward again). Wipe before overwriting so an abandoned
	// copy cannot outlive the step that made it.
	crypto.Zeroize(v.pass)
	v.pass = nil
	v.plan.PassphraseSource = ""

	// Ahead of the non-interactive branch, so BOTH exits — straight to review
	// and the entry field — leave a history the back-stack can walk.
	for len(v.history) > 0 && v.history[len(v.history)-1] >= stagePassphrase {
		v.history = v.history[:len(v.history)-1]
	}

	pass, source, err := config.ResolveNonInteractive(v.deps.PassphraseFile)
	switch {
	case err != nil:
		v.passErr = err.Error()
	case len(pass) > 0:
		v.pass = pass
		v.plan.PassphraseSource = source
		v.plan.SavePassphrase = v.savePass
		setup.ApplyPassphraseConfig(&v.plan)
		v.passErr = ""
		v.stage = stageReview
		return v
	}

	v.stage = stagePassphrase
	v.focusConf = false
	v.newPass.Focus()
	v.confirmPass.Blur()
	return v
}

// interactiveAWSAuthCommand builds the `aws` subprocess for browser login
// or SSO login. It mirrors the effect layer's argument construction
// (setup.DefaultAWSLogin / setup.DefaultAWSSSOLogin) so tea.ExecProcess can own
// the terminal for the child directly — the effect funcs run the child
// themselves and cannot be suspended by the program.
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
		b.WriteString(v.wizardHeader())
		if v.backendLocked {
			// Endpoint seeded: only S3-compatible is valid, so offer just that
			// row (no AWS option) with a hint explaining why it's fixed.
			b.WriteString(v.backendLine(1, "S3-compatible or existing bucket",
				"MinIO, LocalStack, or a bucket you manage yourself."))
			fmt.Fprintf(&b, "\n%s", ui.Muted.Render("endpoint detected — S3-compatible"))
			fmt.Fprintf(&b, "\n%s", v.actionLine(""))
		} else {
			b.WriteString(v.backendLine(0, "AWS S3",
				"Sentra provisions and prepares the bucket for you."))
			b.WriteString("\n")
			b.WriteString(v.backendLine(1, "S3-compatible or existing bucket",
				"MinIO, LocalStack, or a bucket you manage yourself."))
			fmt.Fprintf(&b, "\n%s", v.actionLine("↑/↓ choose"))
		}
	case stageDetails:
		b.WriteString(v.wizardHeader())
		labels := []string{"S3 bucket", "S3 key prefix", "AWS region", "AWS profile", "S3 endpoint URL"}
		for i := 0; i < v.detailFieldCount(); i++ {
			fmt.Fprintf(&b, "%s\n", v.detailRow(i, labels[i]))
		}
		if v.plan.Backend == setup.BackendAWS {
			box := "[ ]"
			if v.printIAM {
				box = "[x]"
			}
			selected := v.fieldCursor == v.detailFieldCount()
			fmt.Fprintf(&b, "\n%s\n", ui.SelectRow(selected, box+" print IAM policy and stop before any changes"))
		}
		if v.detailErr != "" {
			fmt.Fprintf(&b, "\n%s", ui.Danger.Render(v.detailErr))
		}
		b.WriteString(v.actionLine("tab field · space toggle"))
	case stageIAMPreview:
		fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("IAM policy (no changes were made)"))
		b.WriteString(v.iamViewport.View())
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("↑/↓ scroll · ⏎/esc restart setup"))
	case stageActions:
		b.WriteString(v.wizardHeader())
		if v.notice != "" {
			fmt.Fprintf(&b, "%s\n\n", ui.Warn.Render(v.notice))
		}
		fmt.Fprintf(&b, "%s\n", ui.SelectRow(v.actionCursor == actionRowAuth,
			"AWS sign-in: "+setupAuthMethodLabel(setupAuthOrder[v.authCursor])))
		if v.actionCursor == actionRowAuth {
			fmt.Fprintf(&b, "  %s\n", ui.Muted.Render("←/→ change method"))
		}
		b.WriteString(v.actionToggle(actionRowCreate, "create missing bucket", v.createBucket))
		b.WriteString(v.actionToggle(actionRowBlock, "block public access", v.blockPublic))
		b.WriteString(v.actionToggle(actionRowEncrypt, "default encryption (AES-256)", v.defaultEnc))
		b.WriteString(v.actionToggle(actionRowInit, "initialize repository", v.initRepo))
		if v.actionRowVisible(actionRowBackupUser) {
			b.WriteString(v.actionToggle(actionRowBackupUser,
				"create dedicated backup user ("+setup.BackupUserName+")", v.backupUser))
		}
		if v.actionRowVisible(actionRowProfile) {
			// Label styled as a row, input appended after it — never wrap the
			// already-styled input inside the row style.
			fmt.Fprintf(&b, "%s%s\n", ui.SelectRow(v.actionCursor == actionRowProfile, "    profile: "), v.backupProfile.View())
		}
		b.WriteString(v.actionLine("↑/↓ row · ←/→ method · space toggle · type profile"))
	case stagePassphrase:
		b.WriteString(v.wizardHeader())
		if v.notice != "" {
			fmt.Fprintf(&b, "%s\n\n", ui.Warn.Render(v.notice))
		}
		// Mark the focused input with the ▍ selection glyph (and dim the other's
		// prompt), the same affordance as the details stage — a masked field with
		// only a cursor was too easy to lose track of.
		fmt.Fprintf(&b, "%s\n", v.passRow(v.newPass, !v.focusConf))
		fmt.Fprintf(&b, "%s\n\n", v.passRow(v.confirmPass, v.focusConf))
		box := "[ ]"
		if v.savePass {
			box = "[x]"
		}
		fmt.Fprintf(&b, "%s save passphrase in OS keyring (space toggles)\n", box)
		if v.passErr != "" {
			fmt.Fprintf(&b, "\n%s", ui.Danger.Render(v.passErr))
		}
		b.WriteString(v.actionLine("tab field · space keyring"))
	case stageReview:
		b.WriteString(v.wizardHeader())
		b.WriteString(setup.ReviewText(v.deps.ConfigPath, v.plan))
		if v.deps.Reconfigure {
			// Opened over an existing config: this stage is the only gate before
			// the file is rewritten, so name the path. Styling the plain string
			// and appending it — wrapping already-styled text would embed a
			// reset that kills the surrounding style mid-line.
			fmt.Fprintf(&b, "%s\n", ui.Warn.Render(
				fmt.Sprintf("completing setup overwrites %s", v.deps.ConfigPath)))
		}
		if v.notice != "" {
			fmt.Fprintf(&b, "%s\n", ui.Warn.Render(v.notice))
		}
		b.WriteString(v.actionLine(""))
	case stageProvision:
		fmt.Fprintf(&b, "%s\n\n", ui.Primary.Render("Applying setup…"))
		b.WriteString(v.checklistLine(v.steps.bucketCreated, "bucket created"))
		b.WriteString(v.checklistLine(v.steps.publicBlocked, "public access blocked"))
		b.WriteString(v.checklistLine(v.steps.encryptionOn, "default encryption on"))
		b.WriteString(v.checklistLine(v.steps.repoInited, "repository initialized"))
		fmt.Fprintf(&b, "\n%s", ui.Muted.Render("working under the repo lock…"))
	case stageDone:
		fmt.Fprintf(&b, "%s\n\n", ui.Success.Render("Setup complete"))
		b.WriteString(v.checklistLine(v.steps.bucketCreated, "bucket created"))
		b.WriteString(v.checklistLine(v.steps.publicBlocked, "public access blocked"))
		b.WriteString(v.checklistLine(v.steps.encryptionOn, "default encryption on"))
		b.WriteString(v.checklistLine(v.steps.repoInited, "repository initialized"))
		fmt.Fprintf(&b, "\n%s", ui.ActionLine("restart setup", ""))
	case stageError:
		fmt.Fprintf(&b, "%s\n\n", ui.Danger.Render("Setup failed"))
		if v.result.err != nil {
			fmt.Fprintf(&b, "%s\n", v.result.err.Error())
			for _, line := range setup.ErrorAdvice(v.result.err, v.plan.Config) {
				fmt.Fprintf(&b, "\n%s", ui.Subtle.Render(line))
			}
		}
		fmt.Fprintf(&b, "\n\n%s", ui.Muted.Render("⏎ retry (confirm passphrase) · esc restart"))
	default:
		b.WriteString(ui.Muted.Render("setup"))
	}
	return b.String()
}

// backendLine renders one backend choice. The help text is appended OUTSIDE the
// styled row: nesting it inside would embed an ANSI reset that terminates the
// row's own style partway along the line.
func (v SetupWizardView) backendLine(idx int, label, help string) string {
	return ui.SelectRow(v.backendCursor == idx, label) + "  " + ui.Muted.Render(help)
}

func (v SetupWizardView) actionToggle(row int, label string, on bool) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	return ui.SelectRow(v.actionCursor == row, box+" "+label) + "\n"
}

// backupUserOffered: the provisioning step exists only where a powerful
// session was just obtained. Existing-credentials and skip already chose a
// durable identity, and an IAM mutation they did not ask for is the worst
// surprise a setup wizard can spring.
func (v SetupWizardView) backupUserOffered() bool {
	m := setupAuthOrder[v.authCursor]
	return m == setup.AWSAuthLogin || m == setup.AWSAuthSSO
}

func (v SetupWizardView) actionRowVisible(row int) bool {
	switch row {
	case actionRowBackupUser:
		return v.backupUserOffered()
	case actionRowProfile:
		return v.backupUserOffered() && v.backupUser
	default:
		return true
	}
}

// moveActionCursor steps the cursor by delta, skipping hidden rows, and
// keeps the profile input's focus in step with the cursor.
func (v SetupWizardView) moveActionCursor(delta int) SetupWizardView {
	for i := 0; i < actionRowCount; i++ {
		v.actionCursor = (v.actionCursor + delta + actionRowCount) % actionRowCount
		if v.actionRowVisible(v.actionCursor) {
			break
		}
	}
	return v.syncProfileFocus()
}

func (v SetupWizardView) syncProfileFocus() SetupWizardView {
	if v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile) {
		v.backupProfile.Focus()
	} else {
		v.backupProfile.Blur()
	}
	return v
}

// seedBackupUserDefault applies the per-method default — ON for browser
// login (the expiry-trap path), OFF for SSO — and parks the cursor on a
// visible row if the method change hid the one it was on.
func (v SetupWizardView) seedBackupUserDefault() SetupWizardView {
	v.backupUser = setupAuthOrder[v.authCursor] == setup.AWSAuthLogin
	if !v.actionRowVisible(v.actionCursor) {
		v.actionCursor = actionRowAuth
	}
	return v.syncProfileFocus()
}

// setupLabelCol is the column the details-stage values start in. Every label is
// padded to it so the inputs line up regardless of label length; "S3 endpoint
// URL" is the longest at 15 cells.
const setupLabelCol = 18

// wizardStages lists the numbered stages for a plan, in order. The AWS backend
// provisions a bucket and so gets stageActions; an S3-compatible target
// provisions nothing and skips it. Deriving the list from the plan is what keeps
// `sentra local` from reading "Step 4 of 5" and never reaching 5.
//
// Stages outside this list (IAM preview, provisioning, done, error) are not part
// of the numbered flow and carry no counter.
func wizardStages(p setup.Plan) []setupStage {
	stages := []setupStage{stageBackend, stageDetails}
	if p.Backend == setup.BackendAWS {
		stages = append(stages, stageActions)
	}
	return append(stages, stagePassphrase, stageReview)
}

// stageTitle names a stage for the header and for the action line's
// "Continue to …" — one source of truth, so the two can never disagree.
func stageTitle(s setupStage) string {
	switch s {
	case stageBackend:
		return "Storage backend"
	case stageDetails:
		return "Storage details"
	case stageActions:
		return "Setup actions"
	case stagePassphrase:
		return "Repository passphrase"
	case stageReview:
		return "Review"
	default:
		return ""
	}
}

// stepIndex is the current stage's position in the numbered flow, or -1 when the
// stage sits outside it.
func (v SetupWizardView) stepIndex() int {
	for i, s := range wizardStages(v.plan) {
		if s == v.stage {
			return i
		}
	}
	return -1
}

// wizardHeader renders the brand line with a right-aligned step counter, then
// the stage title. Empty for stages outside the numbered flow.
func (v SetupWizardView) wizardHeader() string {
	idx := v.stepIndex()
	if idx < 0 {
		return ""
	}
	left := ui.Muted.Render("Sentra setup")
	right := ui.Muted.Render(fmt.Sprintf("Step %d of %d", idx+1, len(wizardStages(v.plan))))
	gap := v.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right + "\n\n" +
		ui.Primary.Render(stageTitle(v.stage)) + "\n\n"
}

// nextAction names where enter actually goes. It reads the destination out of
// wizardStages, so a newly inserted stage cannot leave it stale, and handles the
// two places the state machine diverges from "the next numbered stage":
// stageDetails short-circuits to the IAM preview when printIAM is armed, and
// stageReview applies rather than continuing.
func (v SetupWizardView) nextAction() string {
	switch v.stage {
	case stageDetails:
		if v.printIAM {
			return "show the IAM policy and stop"
		}
	case stageReview:
		// The ellipsis marks a confirmation step, matching the password flow.
		return "apply setup…"
	}
	stages := wizardStages(v.plan)
	idx := v.stepIndex()
	if idx < 0 || idx+1 >= len(stages) {
		return ""
	}
	return "continue to " + stageTitle(stages[idx+1])
}

// actionLine renders the primary action in the accent style with the secondary
// keys demoted beneath it, so "what enter does" never carries the same weight as
// "space toggles". secondary may be empty.
func (v SetupWizardView) actionLine(secondary string) string {
	line := ui.ActionLine(v.nextAction(), secondary)
	if line == "" {
		return ""
	}
	return "\n" + line
}

// detailRow renders one details-stage field as a single row: the selection
// marker, the label padded to the value column, then the input itself. The
// marker therefore lands on the line the operator types into, rather than one
// line above it. The input is appended OUTSIDE the styled label so its ANSI
// reset cannot terminate the row's style, and a blurred field dims so the
// focused one stands out.
func (v SetupWizardView) detailRow(i int, label string) string {
	f := v.fields[i]
	if v.fieldCursor != i {
		f.TextStyle = ui.Subtle
	}
	return ui.SelectRow(v.fieldCursor == i, fmt.Sprintf("%-*s", setupLabelCol, label)) + f.View()
}

// passRow renders a passphrase input with the ▍ selection glyph when it's the
// focused field (and a dimmed prompt otherwise), so the active field is obvious
// even though both are masked. Mirrors detailRow's affordance.
func (v SetupWizardView) passRow(f textinput.Model, focused bool) string {
	if !focused {
		f.PromptStyle = ui.Muted
	}
	return ui.SelectRow(focused, "") + f.View()
}

// setupAuthMethodLabel is the TUI select row's label for an auth method.
// Deliberately not setup.AWSAuthMethodLabel: these strings carry the
// "(Recommended)" hint and the row's own phrasing.
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
