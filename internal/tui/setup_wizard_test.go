package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
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

// TestSetupWizard_SeededEndpointStartsOnS3Compatible pins the MinIO-seeding
// bug: `sentra local` hands the wizard a config with an endpoint_url plus
// minioadmin env credentials, so DefaultPlan infers BackendS3Compatible. The
// wizard must open the backend selector on that row (cursor 1) and, on Enter,
// keep the inferred backend AND the seeded endpoint — the AWS branch (cursor 0)
// would overwrite the backend and wipe the endpoint field.
func TestSetupWizard_SeededEndpointStartsOnS3Compatible(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "minioadmin")
	cfg := &config.Config{}
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	cfg.Repo.S3.Bucket = "sentra-test"

	v := NewSetupWizardView(Deps{Config: cfg})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)

	// (a) the backend cursor must start on S3-compatible (index 1), matching
	// the inferred plan.Backend — not the default AWS (index 0).
	if v.backendCursor != 1 {
		t.Fatalf("backendCursor = %d, want 1 (S3-compatible) for an endpoint+creds seed", v.backendCursor)
	}

	// (b) Enter on the backend stage must keep the inferred S3-compatible
	// backend and preserve the seeded endpoint (the AWS branch would wipe it).
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("plan.Backend = %v, want S3-compatible after Enter on the backend stage", v.plan.Backend)
	}
	if got := v.fields[setupFieldEndpoint].Value(); got != "http://localhost:9000" {
		t.Fatalf("endpoint field = %q, want http://localhost:9000 preserved", got)
	}
}

// TestSetupWizard_EndpointLocksBackendToS3Compatible: a config carrying an
// endpoint_url is S3-compatible by definition (AWS setup rejects endpoint_url),
// so the wizard locks the backend and never offers AWS — even when no ambient
// credentials are present, so DefaultPlan's inference did NOT fire and left
// Backend=AWS. The backend stage renders only the S3-compatible option, pins
// the cursor, and Enter can only preserve the endpoint.
func TestSetupWizard_EndpointLocksBackendToS3Compatible(t *testing.T) {
	// Defeat both env-credential paths so DefaultPlan's inference does NOT fire;
	// the lock must come from the endpoint alone.
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_ROLE_ARN", "")
	cfg := &config.Config{}
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"

	v := NewSetupWizardView(Deps{Config: cfg})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)

	if !v.backendLocked {
		t.Fatal("a seeded endpoint_url must lock the backend to S3-compatible")
	}
	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("locked plan.Backend = %v, want S3-compatible", v.plan.Backend)
	}
	if v.backendCursor != 1 {
		t.Fatalf("locked backendCursor = %d, want 1", v.backendCursor)
	}

	// The backend view offers ONLY the S3-compatible option.
	out := v.View()
	if strings.Contains(out, "AWS S3") {
		t.Fatalf("locked backend view must not offer the AWS option, got:\n%s", out)
	}
	if !strings.Contains(out, "S3-compatible") {
		t.Fatalf("locked backend view must still show the S3-compatible option, got:\n%s", out)
	}

	// up/down are no-ops when locked (there is only one row).
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyUp})
	v = m.(SetupWizardView)
	if v.backendCursor != 1 {
		t.Fatalf("locked cursor moved to %d; up/down must be no-ops", v.backendCursor)
	}

	// Enter can only take the S3-compatible branch, preserving the endpoint.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("locked Enter set Backend=%v, want S3-compatible", v.plan.Backend)
	}
	if got := v.fields[setupFieldEndpoint].Value(); got != "http://localhost:9000" {
		t.Fatalf("locked Enter wiped the endpoint: got %q", got)
	}
}

// TestSetupWizard_UnlockedBackendOffersBothAndAWSBranchWorks is the regression
// guard for the endpoint-lock: a wizard with no endpoint_url is unlocked, so it
// renders BOTH backend options, up/down move the cursor, and the AWS branch
// (cursor 0, Enter) still selects AWS and clears the endpoint — exactly as
// before the lock was added.
func TestSetupWizard_UnlockedBackendOffersBothAndAWSBranchWorks(t *testing.T) {
	v := NewSetupWizardView(Deps{Config: &config.Config{}})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	if v.backendLocked {
		t.Fatal("an empty-endpoint config must NOT lock the backend")
	}
	out := v.View()
	if !strings.Contains(out, "AWS S3") || !strings.Contains(out, "S3-compatible") {
		t.Fatalf("unlocked backend view must offer both options, got:\n%s", out)
	}
	// up/down still move the cursor when unlocked.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(SetupWizardView)
	if v.backendCursor != 1 {
		t.Fatalf("unlocked down must move the cursor to 1, got %d", v.backendCursor)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyUp})
	v = m.(SetupWizardView)
	if v.backendCursor != 0 {
		t.Fatalf("unlocked up must move the cursor back to 0, got %d", v.backendCursor)
	}
	// The AWS branch: cursor 0 + Enter selects AWS and forbids an endpoint.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.plan.Backend != setup.BackendAWS {
		t.Fatalf("unlocked cursor-0 Enter must select AWS, got %v", v.plan.Backend)
	}
	if got := v.fields[setupFieldEndpoint].Value(); got != "" {
		t.Fatalf("AWS branch must clear the endpoint field, got %q", got)
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

// TestSetupWizard_FirstRunProvisionEmitsRepoReady drives a first-run wizard
// (Deps.Repo == nil) to a successful provision and asserts the flow hands the
// App a live repo so it lands on the dashboard instead of dead-ending with a
// nil repo. The provisioning op must open the repo and carry it in setupDoneMsg,
// and the wizard's Update must forward a repoReadyMsg{repo,...} on the success
// path — mirroring the unlock flow. The passphrase must still be zeroized.
func TestSetupWizard_FirstRunProvisionEmitsRepoReady(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	// A single shared in-memory store so the repo the op initializes is the
	// same one it re-opens for the handoff.
	store := blobstore.NewMemory()
	deps := Deps{
		Config:     &config.Config{}, // first run: no live repo yet
		ConfigPath: cfgPath,
		// Deps.Repo is nil — this is the first-run path.
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			return store, nil
		},
		SetupEffects: sharedStoreEffects{store: store},
	}
	if deps.Repo != nil {
		t.Fatal("precondition: first-run wizard must start with a nil Repo")
	}

	v := NewSetupWizardView(deps)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	// Drive to review: AWS backend, valid bucket, initRepo default on.
	v.backendCursor = 0
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // details
	v = m.(SetupWizardView)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // actions
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // passphrase
	v = m.(SetupWizardView)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // review
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push modal
	v = m.(SetupWizardView)
	m, cmd := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)

	// Capture the stashed passphrase backing array so we can assert it is wiped
	// after the op runs (the closure's deferred zeroize).
	captured := append([]byte(nil), v.pass...)
	if string(captured) != "correcthorse" {
		t.Fatalf("precondition: v.pass should hold the passphrase, got %q", captured)
	}

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
		t.Fatalf("first-run provision failed: %v", done.err)
	}
	// The op must carry a live repo for the handoff.
	if done.repo == nil {
		t.Fatal("first-run provision must open the repo and carry it in setupDoneMsg for the dashboard handoff")
	}
	t.Cleanup(func() { done.repo.Close() })

	// Feeding the setupDoneMsg back into the wizard must forward a repoReadyMsg
	// carrying the live repo, exactly like the unlock flow, so app.go's handler
	// rebuilds the shell to the dashboard.
	m2, cmd2 := v.Update(done)
	v = m2.(SetupWizardView)
	if v.stage != stageDone {
		t.Fatalf("success setupDoneMsg must move to stageDone, got %v", v.stage)
	}
	if cmd2 == nil {
		t.Fatal("first-run success must forward a repoReadyMsg command")
	}
	ready, ok := cmd2().(repoReadyMsg)
	if !ok {
		t.Fatalf("expected repoReadyMsg, got %T", cmd2())
	}
	if ready.repo == nil {
		t.Fatal("repoReadyMsg carried a nil repo")
	}

	// Security: the stashed passphrase must have been zeroized by the op.
	for i, b := range v.pass {
		if b != 0 {
			t.Fatalf("passphrase backing array not zeroized after provision: byte %d = %d", i, b)
		}
	}
}

// sharedStoreEffects is a stubEffects whose NewStore always returns the SAME
// in-memory store, so a repo initialized during provisioning can be re-opened
// for the first-run dashboard handoff.
type sharedStoreEffects struct {
	stubEffects
	store blobstore.Store
}

func (s sharedStoreEffects) NewStore(context.Context, *config.Config) (blobstore.Store, error) {
	return s.store, nil
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

// TestSetupWizard_OpRejected_EscCannotSkipPassphraseReentry continues the
// scenario above one keypress further. The forced route back to passphrase
// is a stage DECREASE, which the history wrapper never records — without
// cleanup the stack still ends with the entry pushed on review→provision,
// so esc would pop it and walk FORWARD to review, skipping the re-entry
// the reject handler just made mandatory (and arming a confirm that would
// provision with a nil passphrase).
func TestSetupWizard_OpRejected_EscCannotSkipPassphraseReentry(t *testing.T) {
	v := setupAtReview(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push confirm modal
	v = m.(SetupWizardView)
	m, _ = v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	m, _ = v.Update(opRejectedMsg{name: "setup"})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("precondition: reject must land on passphrase, got %v", v.stage)
	}

	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	v = m.(SetupWizardView)
	if v.stage >= stagePassphrase {
		t.Fatalf("esc from forced passphrase re-entry must go backward, got stage %v (forward skip past mandatory re-entry)", v.stage)
	}
}

// TestSetupWizard_ConfirmWithEmptyPassphraseDoesNotProvision pins the
// review-stage invariant directly: whatever path lands on review with the
// passphrase stash empty, the confirm must route back to re-entry instead
// of arming the setup op — an empty passphrase would silently derive the
// repository key from "".
func TestSetupWizard_ConfirmWithEmptyPassphraseDoesNotProvision(t *testing.T) {
	v := setupAtReview(t)
	if !v.plan.InitRepo {
		t.Fatal("precondition: this guard only applies to a plan that initializes the repo")
	}
	crypto.Zeroize(v.pass)
	v.pass = nil

	m, _ := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	if v.stage == stageProvision {
		t.Fatal("confirm with empty passphrase must not reach stageProvision")
	}
	if v.stage != stagePassphrase {
		t.Fatalf("confirm with empty passphrase should route to re-entry, got %v", v.stage)
	}
}

// TestSetupWizard_ConfirmWithInitOffProvisions pins the other side of that
// guard. A plan with init-repo off never derives a repository key, so there is
// no passphrase to demand — and demanding one strands the config-only setup
// path entirely (see TestSetupWizard_ActionsInitOffGoesToReview): the operator
// is sent to a prompt whose answer provisioning then ignores, with no way
// forward. The empty stash is only dangerous when InitRepo is true.
func TestSetupWizard_ConfirmWithInitOffProvisions(t *testing.T) {
	v := setupAtActions(t)
	// Drive the toggle the way an operator does: walk the cursor to the
	// init-repo row and press space.
	for i := 0; i < actionRowCount && v.actionCursor != actionRowInit; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SetupWizardView)
	}
	if v.actionCursor != actionRowInit {
		t.Fatalf("precondition: cursor never reached the init-repo row, got %d", v.actionCursor)
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageReview || v.plan.InitRepo {
		t.Fatalf("precondition: want review with init-repo off, got stage=%v InitRepo=%v", v.stage, v.plan.InitRepo)
	}
	if len(v.pass) != 0 {
		t.Fatalf("precondition: the init-off route skips passphrase entry, got %d stashed bytes", len(v.pass))
	}

	m, cmd := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)
	if v.stage != stageProvision {
		t.Fatalf("confirm with init-repo off must provision, got stage %v", v.stage)
	}
	var foundStart bool
	for _, msg := range execCmds(t, cmd) {
		if s, ok := msg.(startOpMsg); ok && s.name == "setup" {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatal("confirm with init-repo off must emit startOpMsg{name:setup}")
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

// TestSetupWizardDetailsRowShowsSelection guards the regression that made the
// wizard hard to read: the details stage rendered every field label in the same
// muted style, so the ONLY difference between the selected row and the rest was
// a two-character "> " prefix. Unit tests run under lipgloss's Ascii profile,
// where no ANSI is emitted at all — so the "▍" marker is the only thing that can
// carry selection here, which is exactly why the marker is the contract.
func TestSetupWizardDetailsRowShowsSelection(t *testing.T) {
	v := setupAtDetails(t, 0)

	v.fieldCursor = 0
	first := v.View()
	v.fieldCursor = 1
	second := v.View()

	if first == second {
		t.Fatal("moving the field cursor did not change the rendered view")
	}
	if !strings.Contains(first, "▍ S3 bucket") {
		t.Errorf("selected row must carry the marker:\n%s", first)
	}
	if strings.Contains(first, "▍ S3 key prefix") {
		t.Errorf("unselected row must not carry the marker:\n%s", first)
	}
	if !strings.Contains(second, "▍ S3 key prefix") {
		t.Errorf("cursor move did not move the marker:\n%s", second)
	}
}

// wizardLine returns the first rendered line containing needle.
func wizardLine(t *testing.T, view, needle string) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, needle) {
			return l
		}
	}
	t.Fatalf("no line containing %q in:\n%s", needle, view)
	return ""
}

// The step counter must be derived from the plan, not hardcoded: the AWS path
// runs five stages, but an S3-compatible target skips "Setup actions" entirely.
// A fixed "of 5" would leave `sentra local` stuck at "Step 4 of 5".
func TestSetupWizardStepCounterDependsOnBackend(t *testing.T) {
	aws := setupAtDetails(t, 0)
	if got := len(wizardStages(aws.plan)); got != 5 {
		t.Errorf("aws stages = %d, want 5", got)
	}
	if !strings.Contains(aws.View(), "Step 2 of 5") {
		t.Errorf("aws details should be step 2 of 5:\n%s", aws.View())
	}

	compat := setupAtDetails(t, 1)
	if got := len(wizardStages(compat.plan)); got != 4 {
		t.Errorf("s3-compatible stages = %d, want 4", got)
	}
	if !strings.Contains(compat.View(), "Step 2 of 4") {
		t.Errorf("s3-compatible details should be step 2 of 4:\n%s", compat.View())
	}
}

// The action line must name where Enter actually goes, including the two places
// the state machine diverges from "the next numbered stage".
func TestSetupWizardNextActionNamesTheDestination(t *testing.T) {
	awsDetails := setupAtDetails(t, 0)
	compatDetails := setupAtDetails(t, 1)

	iam := setupAtDetails(t, 0)
	iam.printIAM = true

	actions := setupAtDetails(t, 0)
	actions.stage = stageActions

	pass := setupAtDetails(t, 0)
	pass.stage = stagePassphrase

	review := setupAtDetails(t, 0)
	review.stage = stageReview

	for _, tc := range []struct {
		name string
		v    SetupWizardView
		want string
	}{
		{"aws details", awsDetails, "continue to Setup actions"},
		{"s3-compatible details skips actions", compatDetails, "continue to Repository passphrase"},
		{"print-IAM short-circuits", iam, "show the IAM policy and stop"},
		{"actions", actions, "continue to Repository passphrase"},
		{"passphrase", pass, "continue to Review"},
		{"review applies", review, "apply setup…"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.v.nextAction(); got != tc.want {
				t.Errorf("nextAction() = %q, want %q", got, tc.want)
			}
			if !strings.Contains(tc.v.View(), tc.want) {
				t.Errorf("view does not show the action %q:\n%s", tc.want, tc.v.View())
			}
		})
	}
}

// The whole point: the marker must land on the row you type into, not a line
// above it.
func TestSetupWizardDetailsRowCarriesItsInput(t *testing.T) {
	v := setupAtDetails(t, 0)
	v.fieldCursor = 2
	v.fields[2].SetValue("us-west-2")

	line := wizardLine(t, v.View(), "▍ AWS region")
	if !strings.Contains(line, "us-west-2") {
		t.Errorf("marker and its input are on different rows; row was:\n%q", line)
	}
}

// Values line up in one column regardless of label length.
func TestSetupWizardDetailsValueColumnAligns(t *testing.T) {
	v := setupAtDetails(t, 1) // s3-compatible: all five fields visible
	labels := []string{"S3 bucket", "S3 key prefix", "AWS region", "AWS profile", "S3 endpoint URL"}
	for i := range labels {
		v.fields[i].SetValue(fmt.Sprintf("VAL%d", i))
	}
	view := v.View()
	for i, label := range labels {
		line := wizardLine(t, view, label)
		want := fmt.Sprintf("VAL%d", i)
		col := utf8.RuneCountInString(line[:strings.Index(line, want)])
		if col != 2+setupLabelCol {
			t.Errorf("%s: value starts at column %d, want %d", label, col, 2+setupLabelCol)
		}
	}
}

// wizardWithAWSProfileInEnv builds a wizard whose DefaultPlan infers an AWS
// profile, as it does on any machine with a [profile sentra] in ~/.aws/config.
func wizardWithInferredProfile(t *testing.T, cfg *config.Config) SetupWizardView {
	t.Helper()
	t.Setenv("AWS_PROFILE", "inferred-profile")
	v := NewSetupWizardView(Deps{Config: cfg})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	if v.fields[setupFieldProfile].Value() != "inferred-profile" {
		t.Fatalf("precondition: profile should be inferred, got %q", v.fields[setupFieldProfile].Value())
	}
	return v
}

// Choosing the S3-compatible backend by hand must drop an AWS profile the user
// never chose. aws-sdk-go-v2 resolves a shared-config profile BEFORE static env
// credentials, so carrying an inferred profile into a MinIO/R2 plan hands the
// endpoint the wrong credentials entirely — the same failure that broke
// `sentra local`, reached through the manual path instead of inference.
func TestSetupWizardS3CompatibleDropsInferredProfile(t *testing.T) {
	v := wizardWithInferredProfile(t, &config.Config{})
	v.backendCursor = 1 // S3-compatible
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)

	if v.plan.Backend != setup.BackendS3Compatible {
		t.Fatalf("backend = %q, want s3-compatible", v.plan.Backend)
	}
	if got := v.fields[setupFieldProfile].Value(); got != "" {
		t.Errorf("s3-compatible plan kept inferred AWS profile %q; it must be dropped", got)
	}
}

// Only the INFERRED value is dropped. A profile the operator wrote into
// sentra.yaml is theirs — R2 and Wasabi credentials legitimately live in one.
func TestSetupWizardS3CompatibleKeepsConfiguredProfile(t *testing.T) {
	cfg := &config.Config{}
	cfg.Repo.S3.Profile = "wasabi"
	v := NewSetupWizardView(Deps{Config: cfg})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)

	v.backendCursor = 1
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)

	if got := v.fields[setupFieldProfile].Value(); got != "wasabi" {
		t.Errorf("configured profile = %q, want wasabi (must survive)", got)
	}
}

// The AWS branch keeps clearing the endpoint, which the S3-compatible branch
// mirrors for the profile.
func TestSetupWizardAWSStillClearsEndpoint(t *testing.T) {
	cfg := &config.Config{}
	v := NewSetupWizardView(Deps{Config: cfg})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	v.fields[setupFieldEndpoint].SetValue("http://localhost:9000")
	v.backendCursor = 0
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if got := v.fields[setupFieldEndpoint].Value(); got != "" {
		t.Errorf("aws backend must clear endpoint, got %q", got)
	}
}

// TestSetupWizard_EscStepsBackAWS: on the AWS path (backend→details→actions→
// passphrase) esc steps back one stage at a time, retracing the exact path, and
// stops at backend (nothing behind it) rather than restarting.
func TestSetupWizard_EscStepsBackAWS(t *testing.T) {
	v := setupAtPassphrase(t)
	for _, want := range []setupStage{stageActions, stageDetails, stageBackend} {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc})
		v = m.(SetupWizardView)
		if v.stage != want {
			t.Fatalf("esc back: got %v, want %v", v.stage, want)
		}
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // nothing behind backend
	if got := m.(SetupWizardView).stage; got != stageBackend {
		t.Fatalf("esc at backend must stay put, got %v", got)
	}
}

// TestSetupWizard_EscStepsBackS3SkipsActions: S3-compatible has no actions
// stage, so esc from passphrase steps straight back to details — the history
// stack retraces the skip automatically.
func TestSetupWizard_EscStepsBackS3SkipsActions(t *testing.T) {
	v := setupAtDetails(t, 1) // S3-compatible
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("precondition: S3 details→passphrase, got %v", v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.(SetupWizardView).stage; got != stageDetails {
		t.Fatalf("esc from S3 passphrase must skip actions back to details, got %v", got)
	}
}

// TestSetupWizard_EscBackPreservesEntries: stepping back keeps what was typed.
func TestSetupWizard_EscBackPreservesEntries(t *testing.T) {
	v := setupAtDetails(t, 1)
	v = setupTypeField(v, "keep-this-bucket")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // → passphrase
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back → details
	v = m.(SetupWizardView)
	if got := v.fields[setupFieldBucket].Value(); got != "keep-this-bucket" {
		t.Errorf("esc back lost the bucket entry: %q", got)
	}
}

// TestSetupWizard_EscBackAfterTooShortPassphrase is the reported case: a too-
// short passphrase is rejected, and esc still steps back instead of trapping.
func TestSetupWizard_EscBackAfterTooShortPassphrase(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypeField(v, "short") // < minPasswordLen
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.passErr == "" || v.stage != stagePassphrase {
		t.Fatalf("precondition: short passphrase rejected, staying put (err=%q stage=%v)", v.passErr, v.stage)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.(SetupWizardView).stage; got != stageActions {
		t.Fatalf("esc after a too-short passphrase must step back, got %v", got)
	}
}

// TestSetupWizard_EscBackFromReviewClearsStashedPassphrase: returning to the
// passphrase stage zeroizes the stashed secret so it is re-entered, matching the
// flow's plaintext-residency discipline.
func TestSetupWizard_EscBackFromReviewClearsStashedPassphrase(t *testing.T) {
	v := setupAtPassphrase(t)
	v = setupTypeField(v, "correct-horse")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → confirm field
	v = m.(SetupWizardView)
	v = setupTypeField(v, "correct-horse")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // commit → review
	v = m.(SetupWizardView)
	if v.stage != stageReview || len(v.pass) == 0 {
		t.Fatalf("precondition: at review with a stashed secret (stage=%v passLen=%d)", v.stage, len(v.pass))
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEsc}) // back → passphrase
	v = m.(SetupWizardView)
	if v.stage != stagePassphrase {
		t.Fatalf("esc from review must go back to passphrase, got %v", v.stage)
	}
	if v.pass != nil {
		t.Error("going back to passphrase must clear the stashed secret")
	}
}

// TestSetupWizard_AdvertisesBackOnPassphrase: the footer must offer esc back so
// the way out is discoverable.
func TestSetupWizard_AdvertisesBackOnPassphrase(t *testing.T) {
	v := setupAtPassphrase(t)
	for _, b := range v.ShortHelp() {
		for _, k := range b.Keys() {
			if k == "esc" {
				return
			}
		}
	}
	t.Error("passphrase stage must advertise esc back in ShortHelp")
}
