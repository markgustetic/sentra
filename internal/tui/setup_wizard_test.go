package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupWizard_InitialStageIsBackendSelect(t *testing.T) {
	cfg := &config.Config{}
	v := NewSetupWizardView(Deps{Config: cfg})
	if v.Title() != "Setup" {
		t.Fatalf("Title() = %q, want %q", v.Title(), "Setup")
	}
	// Feed a window size so the view has render dimensions.
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	out := v.View()
	if !strings.Contains(out, "Storage backend") {
		t.Fatalf("initial view must show the backend selector, got:\n%s", out)
	}
	if !strings.Contains(out, "AWS S3") || !strings.Contains(out, "S3-compatible") {
		t.Fatalf("backend selector must list both backends, got:\n%s", out)
	}
}

func TestSetupWizard_NilEffectsRendersGuard(t *testing.T) {
	// A view with no SetupEffects wired can still render but reports it
	// cannot provision, so a first-run gate never crashes.
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	if got := v.View(); got == "" {
		t.Fatal("View() must never be empty even without SetupEffects")
	}
}

func TestSetupWizard_BackendEnterOpensDetails(t *testing.T) {
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // choose AWS S3 (cursor 0)
	v = m.(SetupWizardView)
	if v.stage != stageDetails {
		t.Fatalf("stage = %v, want stageDetails", v.stage)
	}
	if !strings.Contains(v.View(), "S3 bucket") {
		t.Fatalf("details stage must prompt for the bucket, got:\n%s", v.View())
	}
}

func TestSetupWizard_DetailsRejectsInvalidBucket(t *testing.T) {
	v := setupAtDetails(t, 0)           // AWS backend
	v = setupTypeField(v, "UPPER_CASE") // invalid: not DNS-compatible
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageDetails {
		t.Fatalf("invalid bucket must keep the view on details, got stage %v", v.stage)
	}
	if v.detailErr == "" {
		t.Fatal("invalid bucket must set a detail error")
	}
}

func TestSetupWizard_AWSDetailsAdvancesToActions(t *testing.T) {
	v := setupAtDetails(t, 0)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageActions {
		t.Fatalf("valid AWS bucket must advance to stageActions, got %v", v.stage)
	}
	if v.plan.Config.Repo.S3.Bucket != "my-sentra-bucket" {
		t.Fatalf("bucket not captured into plan: %q", v.plan.Config.Repo.S3.Bucket)
	}
}

func TestSetupWizard_CompatibleDetailsAdvancesToPassphrase(t *testing.T) {
	v := setupAtDetails(t, 1) // S3-compatible backend
	v = setupTypeField(v, "existing-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("S3-compatible skips actions, want stagePassphrase, got %v", v.stage)
	}
	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("plan.Backend = %v, want S3-compatible", v.plan.Backend)
	}
}

// setupAtDetails drives the wizard to stageDetails on the given backend
// cursor (0=AWS, 1=S3-compatible).
func setupAtDetails(t *testing.T, backendCursor int) SetupWizardView {
	t.Helper()
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	v.backendCursor = backendCursor
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return m.(SetupWizardView)
}

// setupTypeField types s into the focused details field (bucket by default).
func setupTypeField(v SetupWizardView, s string) SetupWizardView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SetupWizardView)
	}
	return v
}

func TestSetupWizard_PrintIAMShortCircuitsToPreview(t *testing.T) {
	v := setupAtDetails(t, 0) // AWS
	v = setupTypeField(v, "my-sentra-bucket")
	// Move cursor onto the IAM toggle (past the 4 AWS fields) and toggle it.
	v.fieldCursor = v.detailFieldCount()
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(SetupWizardView)
	if !v.printIAM {
		t.Fatal("space must toggle printIAM on")
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageIAMPreview {
		t.Fatalf("printIAM enter must go to stageIAMPreview, got %v", v.stage)
	}
	out := v.View()
	if !strings.Contains(out, "my-sentra-bucket") || !strings.Contains(out, "s3:PutObject") {
		t.Fatalf("IAM preview must render the policy for the bucket, got:\n%s", out)
	}
}

func TestSetupWizard_IAMPreviewMatchesEngine(t *testing.T) {
	var want strings.Builder
	if err := setup.WriteIAMPolicy(&want, "my-sentra-bucket", "sentra/"); err != nil {
		t.Fatal(err)
	}
	got := renderIAMPolicy("my-sentra-bucket", "sentra/")
	if got != want.String() {
		t.Fatalf("renderIAMPolicy diverged from setup.WriteIAMPolicy:\n got=%q\nwant=%q", got, want.String())
	}
}

func TestSetupWizard_ActionsToPassphraseWhenInitRepo(t *testing.T) {
	v := setupAtActions(t) // AWS, valid bucket, initRepo default true
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("init-repo on must go to stagePassphrase, got %v", v.stage)
	}
	if !v.plan.PrepareAWS {
		t.Fatal("default AWS actions must set PrepareAWS")
	}
}

func TestSetupWizard_ActionsSkipAppliesConfigOnly(t *testing.T) {
	v := setupAtActions(t)
	// Move the auth cursor to "skip" (index 3) and toggle init-repo off is
	// implied by ApplyAWSConfigOnly.
	v.authCursor = 3 // login=0, sso=1, existing=2, skip=3
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageReview {
		t.Fatalf("skip must go straight to review, got %v", v.stage)
	}
	if v.plan.PrepareAWS || v.plan.InitRepo {
		t.Fatalf("skip must be config-only: PrepareAWS=%v InitRepo=%v", v.plan.PrepareAWS, v.plan.InitRepo)
	}
	if v.plan.AWSAuthMethod != setup.AWSAuthSkip {
		t.Fatalf("skip must set AWSAuthSkip, got %v", v.plan.AWSAuthMethod)
	}
}

func TestSetupWizard_ActionsInitOffGoesToReview(t *testing.T) {
	v := setupAtActions(t)
	v.initRepo = false // toggled off
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageReview {
		t.Fatalf("init-repo off (auth != skip) must go to review, got %v", v.stage)
	}
	if !v.plan.PrepareAWS {
		t.Fatal("a non-skip auth still prepares AWS even when init-repo is off")
	}
}

// setupAtActions drives an AWS wizard to stageActions with a valid bucket.
func setupAtActions(t *testing.T) SetupWizardView {
	t.Helper()
	v := setupAtDetails(t, 0)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(SetupWizardView)
	if got.stage != stageActions {
		t.Fatalf("setup precondition: want stageActions, got %v", got.stage)
	}
	return got
}

func TestSetupWizard_PassphraseMismatchBlocks(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypePass(v, "correcthorse", "batterystaple")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("mismatch must stay on passphrase, got %v", v.stage)
	}
	if v.passErr == "" {
		t.Fatal("mismatch must set passErr")
	}
}

func TestSetupWizard_PassphraseTooShortBlocks(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypePass(v, "short", "short")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase || v.passErr == "" {
		t.Fatalf("short passphrase must block with an error, stage=%v err=%q", v.stage, v.passErr)
	}
}

func TestSetupWizard_PassphraseMatchAdvancesToReview(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageReview {
		t.Fatalf("matching passphrase must advance to review, got %v", v.stage)
	}
	if string(v.pass) != "correcthorse" {
		t.Fatal("verified passphrase must be stashed for provisioning")
	}
}

func TestSetupWizard_PassphraseNeverRendered(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	if strings.Contains(v.View(), "correcthorse") {
		t.Fatal("masked passphrase must never appear in the rendered view")
	}
}

// setupAtPassphrase drives an AWS wizard (init-repo on) to stagePassphrase.
func setupAtPassphrase(t *testing.T) SetupWizardView {
	t.Helper()
	v := setupAtActions(t) // initRepo default true, login auth default
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(SetupWizardView)
	if got.stage != stagePassphrase {
		t.Fatalf("setup precondition: want stagePassphrase, got %v", got.stage)
	}
	return got
}

// setupTypePass fills the new + confirm masked fields.
func setupTypePass(v SetupWizardView, newPass, confirm string) SetupWizardView {
	for _, r := range newPass {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SetupWizardView)
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(SetupWizardView)
	for _, r := range confirm {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SetupWizardView)
	}
	return v
}

func TestSetupWizard_ReviewRendersPlanAndPushesConfirm(t *testing.T) {
	v := setupAtReview(t)
	if !strings.Contains(v.View(), "my-sentra-bucket") {
		t.Fatalf("review must render the plan (bucket), got:\n%s", v.View())
	}
	// Enter on review pushes the confirm modal — it does NOT start the op.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	msgs := execCmds(t, cmd)
	var pushed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed = true
			_ = pm
		}
		if _, ok := msg.(startOpMsg); ok {
			t.Fatal("review enter must NOT start provisioning before confirm")
		}
	}
	if !pushed {
		t.Fatalf("review enter must push a confirm modal, got %#v", msgs)
	}
	if v.stage != stageReview {
		t.Fatalf("review stage must persist until confirmed, got %v", v.stage)
	}
}

func TestSetupWizard_ReviewConfirmStartsProvisioning(t *testing.T) {
	v := setupAtReview(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push modal
	v = m.(SetupWizardView)
	// The App broadcasts confirmedMsg back; that is what starts the op.
	m, cmd := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	if v.stage != stageProvision {
		t.Fatalf("confirm must move to stageProvision, got %v", v.stage)
	}
	msgs := execCmds(t, cmd)
	var foundStart bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok && s.name == "setup" {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("confirm must emit startOpMsg{name:setup}, got %#v", msgs)
	}
}

func TestSetupWizard_ReviewConfirmWrongIDIgnored(t *testing.T) {
	v := setupAtReview(t)
	m, cmd := v.Update(confirmedMsg{id: "some-other-flow"})
	v = m.(SetupWizardView)
	if v.stage != stageReview {
		t.Fatalf("a foreign confirmedMsg must not start setup, got %v", v.stage)
	}
	if cmd != nil {
		if msgs := execCmds(t, cmd); len(msgs) > 0 {
			t.Fatalf("foreign confirm should be a no-op, got %#v", msgs)
		}
	}
}

// setupAtReview drives an AWS wizard through actions + passphrase to review.
func setupAtReview(t *testing.T) SetupWizardView {
	t.Helper()
	v := setupAtPassphrase(t)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(SetupWizardView)
	if got.stage != stageReview {
		t.Fatalf("setup precondition: want stageReview, got %v", got.stage)
	}
	return got
}

// stubEffects is a fully in-memory setup.Effects: no AWS, no keyring, an
// in-memory store, so the provisioning op can run end-to-end in a test.
type stubEffects struct {
	prepareErr error
	prepared   setup.AWSPrepareReport
}

func (s stubEffects) EnsureAWSCLI(ctx context.Context, confirm setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error) {
	return setup.AWSCLIInstallReport{AlreadyInstalled: true}, nil
}
func (s stubEffects) AWSLogin(ctx context.Context, profile, region string) error { return nil }
func (s stubEffects) CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return true, nil
}
func (s stubEffects) AWSConfigureSSO(ctx context.Context, profile string) error { return nil }
func (s stubEffects) AWSSSOLogin(ctx context.Context, profile string) error     { return nil }
func (s stubEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return nil
}
func (s stubEffects) PrepareAWS(ctx context.Context, cfg *config.Config, opts setup.AWSPrepareOptions) (setup.AWSPrepareReport, error) {
	return s.prepared, s.prepareErr
}
func (s stubEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	return blobstore.NewMemory(), nil
}
func (s stubEffects) SavePassphrase(cfg *config.Config, pass []byte) error { return nil }

func TestSetupWizard_DoneMsgRendersChecklist(t *testing.T) {
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(setupDoneMsg{
		steps: setupProgress{bucketCreated: true, publicBlocked: true, encryptionOn: true, repoInited: true},
	})
	v = m.(SetupWizardView)
	if v.stage != stageDone {
		t.Fatalf("setupDoneMsg (no err) must move to stageDone, got %v", v.stage)
	}
	out := v.View()
	for _, want := range []string{"bucket created", "public access blocked", "default encryption", "repository initialized"} {
		if !strings.Contains(out, want) {
			t.Fatalf("done checklist missing %q, got:\n%s", want, out)
		}
	}
}

func TestSetupWizard_DoneMsgErrorRendersAdvice(t *testing.T) {
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(setupDoneMsg{err: errors.New("BucketAlreadyExists: taken")})
	v = m.(SetupWizardView)
	if v.stage != stageError {
		t.Fatalf("setupDoneMsg with err must move to stageError, got %v", v.stage)
	}
	out := v.View()
	if !strings.Contains(out, "already owned") {
		t.Fatalf("error stage must render ErrorAdvice for the failure, got:\n%s", out)
	}
}

func TestSetupWizard_OpRejectedReturnsToPassphrase(t *testing.T) {
	v := setupAtReview(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	m, _ = v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView) // now stageProvision
	m, _ = v.Update(opRejectedMsg{name: "setup"})
	v = m.(SetupWizardView)
	// Reject routes to passphrase re-entry (not review): v.pass was zeroized
	// and niled, so review's confirm would otherwise re-arm provisioning
	// against an empty passphrase.
	if v.stage != stagePassphrase {
		t.Fatalf("rejection must return to passphrase re-entry, got %v", v.stage)
	}
	if v.notice == "" {
		t.Fatal("rejection must set a notice")
	}
}

func TestSetupWizard_ProvisionOpRunsEngineEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	deps := Deps{
		Config:       &config.Config{},
		ConfigPath:   cfgPath,
		SetupEffects: stubEffects{prepared: setup.AWSPrepareReport{BucketCreated: true, PublicAccessBlocked: true, DefaultEncryptionEnabled: true}},
	}
	v := NewSetupWizardView(deps)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	// Drive to review via the field/stage helpers.
	v.backendCursor = 0
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // details
	v = m.(SetupWizardView)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // actions
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // passphrase (initRepo default on)
	v = m.(SetupWizardView)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // review
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push modal
	v = m.(SetupWizardView)
	m, cmd := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	// Run the op closure directly (as the App's guard would).
	var op startOpMsg
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok {
			op = s
		}
	}
	if op.run == nil {
		t.Fatal("no startOpMsg with a run closure")
	}
	res := op.run(context.Background())
	done, ok := res.(setupDoneMsg)
	if !ok {
		t.Fatalf("op must return setupDoneMsg, got %T", res)
	}
	if done.err != nil {
		t.Fatalf("engine end-to-end run failed: %v", done.err)
	}
	if !done.steps.repoInited {
		t.Fatal("repo should have been initialized")
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("config should have been written to %s: %v", cfgPath, err)
	}
}

// execProbe is a stubEffects whose EnsureAWSCLI and CheckAWSSDKIdentity are
// configurable so the auth-routing decision can be exercised.
type execProbe struct {
	stubEffects
	cliErr      error // EnsureAWSCLI failure (missing aws)
	identityErr error // CheckAWSSDKIdentity failure (creds absent → need auth)
}

func (e execProbe) EnsureAWSCLI(ctx context.Context, confirm setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error) {
	if e.cliErr != nil {
		return setup.AWSCLIInstallReport{}, e.cliErr
	}
	return setup.AWSCLIInstallReport{AlreadyInstalled: true}, nil
}
func (e execProbe) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return e.identityErr
}

func TestSetupWizard_LoginMissingCredsIssuesExecAuth(t *testing.T) {
	deps := Deps{
		Config:       &config.Config{},
		SetupEffects: execProbe{identityErr: errors.New("no valid credential")},
	}
	v := driveToActions(t, deps)
	v.authCursor = 0 // login
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage == stagePassphrase {
		t.Fatal("login with absent creds must run interactive auth, not skip to passphrase")
	}
	if v.stage != stageActions {
		t.Fatalf("wizard should stay on actions while auth runs, got %v", v.stage)
	}
	if cmd == nil {
		t.Fatal("login with absent creds must emit an ExecProcess auth command")
	}
}

func TestSetupWizard_LoginWithCredsSkipsExecAuth(t *testing.T) {
	deps := Deps{
		Config:       &config.Config{},
		SetupEffects: execProbe{}, // identity ok
	}
	v := driveToActions(t, deps)
	v.authCursor = 0 // login
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("login with present creds should proceed to passphrase, got %v", v.stage)
	}
}

func TestSetupWizard_MissingAWSCLIPushesErrorModal(t *testing.T) {
	deps := Deps{
		Config:       &config.Config{},
		SetupEffects: execProbe{cliErr: errors.New("AWS CLI is required")},
	}
	v := driveToActions(t, deps)
	v.authCursor = 1 // sso — needs the CLI
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	var pushedErr bool
	for _, msg := range execCmds(t, cmd) {
		if pm, ok := msg.(pushModalMsg); ok {
			if _, isErr := pm.modal.(ErrorModal); isErr {
				pushedErr = true
			}
		}
	}
	if !pushedErr {
		t.Fatalf("missing aws CLI must push an ErrorModal, stage=%v", v.stage)
	}
}

func TestSetupWizard_AuthDoneReentersAtReview(t *testing.T) {
	deps := Deps{Config: &config.Config{}, SetupEffects: execProbe{identityErr: errors.New("no valid credential")}}
	v := driveToActions(t, deps)
	v.authCursor = 0
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	// Simulate the child process completing successfully.
	m, _ = v.Update(awsAuthDoneMsg{err: nil})
	v = m.(SetupWizardView)
	// initRepo default on → auth success continues to passphrase.
	if v.stage != stagePassphrase {
		t.Fatalf("auth completion must resume the flow (passphrase), got %v", v.stage)
	}
}

// driveToActions builds a wizard with the given deps and advances to
// stageActions with a valid bucket.
func driveToActions(t *testing.T, deps Deps) SetupWizardView {
	t.Helper()
	v := NewSetupWizardView(deps)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	v.backendCursor = 0
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	got := m.(SetupWizardView)
	if got.stage != stageActions {
		t.Fatalf("driveToActions precondition failed: stage %v", got.stage)
	}
	return got
}

func TestSetupWizard_OpRejectedZeroizesPassphrase(t *testing.T) {
	v := setupAtReview(t) // v.pass holds the verified "correcthorse"
	// Precondition: the verified passphrase is stashed.
	if string(v.pass) != "correcthorse" {
		t.Fatalf("precondition: v.pass should hold the passphrase, got %q", v.pass)
	}
	// Capture the backing array so we can observe whether it is wiped in
	// place (v.pass is a slice; the array is shared across value copies).
	captured := v.pass

	// Confirm review → provisioning (the op is emitted but, in the App, would
	// be rejected if another op holds the guard).
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push modal
	v = m.(SetupWizardView)
	m, _ = v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	if v.stage != stageProvision {
		t.Fatalf("precondition: confirm must reach stageProvision, got %v", v.stage)
	}

	// The App rejected the start (another op in flight): the run closure —
	// and thus its deferred zeroize — never fires, so the view MUST wipe the
	// stashed passphrase itself.
	m, _ = v.Update(opRejectedMsg{name: "setup"})
	v = m.(SetupWizardView)

	// Security assertion FIRST: the stashed plaintext must be wiped.
	for i, b := range captured {
		if b != 0 {
			t.Fatalf("passphrase backing array not zeroized after reject: byte %d = %d (residual plaintext)", i, b)
		}
	}
	if v.pass != nil {
		t.Fatalf("v.pass must be nil after reject-zeroize, got %v", v.pass)
	}
	if v.stage != stagePassphrase {
		t.Fatalf("rejection must route back to passphrase re-entry, got %v", v.stage)
	}
	if v.notice == "" {
		t.Fatal("rejection must keep the in-progress notice")
	}
}

func TestSetupWizard_ErrorEscZeroizesPassphrase(t *testing.T) {
	v := setupAtReview(t) // v.pass holds "correcthorse"
	captured := v.pass
	// Force the error stage while the passphrase is still stashed (the reset
	// path must not rely on a completed op having wiped the shared array).
	v.stage = stageError
	v.result = setupDoneMsg{err: errors.New("prepare AWS S3: boom")}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	fresh := m.(SetupWizardView)
	for i, b := range captured {
		if b != 0 {
			t.Fatalf("passphrase not zeroized on error-esc reset: byte %d = %d (residual plaintext)", i, b)
		}
	}
	if fresh.pass != nil {
		t.Fatalf("fresh wizard must start with nil passphrase, got %v", fresh.pass)
	}
	if fresh.stage != stageBackend {
		t.Fatalf("esc must restart at the backend stage, got %v", fresh.stage)
	}
}
