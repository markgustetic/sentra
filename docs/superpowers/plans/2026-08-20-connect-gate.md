# Connect Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** When the configured repo fails to OPEN at launch (expired AWS SSO, unreachable bucket), bare `sentra` opens the TUI on a new hidden "connect" gate offering retry / `aws sso login` / quit, instead of dying to stderr.

**Architecture:** A third launch gate beside wizard and unlock. `internal/tui` gains `ConnectView` (mirrors `UnlockView`'s state machine and `repoReadyMsg` handoff, reuses the wizard's `interactiveAWSAuthCommand` + `tea.ExecProcess` auth step). `internal/cli`'s `runUI` routes the dashboard-path open failure to the gate, injecting a retry closure through two new `tui.Deps` fields — the same closure-seam style as `NewStore`, so the import direction (`tui` never imports `cli`) holds.

**Tech Stack:** Go 1.25, Bubbletea/bubbles, cobra. Tests: stdlib `testing`; tui view tests drive `Update`/`View` directly (lipgloss Ascii profile — glyphs, not colors); cli tests use `uiFixture`/`chDir` from `ui_test.go`.

**Spec:** `docs/superpowers/specs/2026-08-20-connect-gate-design.md`

## Global Constraints

- TDD: failing test first, watch it fail for the right reason, minimal implementation, pass, commit.
- `internal/tui` must NEVER import `internal/cli` — the retry hook is a closure injected via `tui.Deps`.
- Only the dashboard-path `openRepoForConfig` failure routes to the gate. Config-load/probe errors, first-run, and locked keep their current behavior.
- The `l` login option renders ONLY when `Config.Repo.S3.EndpointURL == ""` (AWS proper). Never auto-run the login — one explicit keypress.
- The exec'd argv comes from `interactiveAWSAuthCommand` (fixed `aws` binary + config-sourced profile/region); never through a shell, never from remote data.
- No secrets in the view: the passphrase lives inside the retry closure, which zeroizes it; `ConnectError` text comes from SDK/repo layers.
- Views signal selection/affordances via glyphs and text, not color alone; never wrap an already-styled string in another style.
- Commit per task; `git add` only named files. While iterating, `-race` only the changed package; full `-race ./...` once before push (Task 3).

---

### Task 1: `ConnectView` in internal/tui

**Files:**
- Create: `internal/tui/connect.go`
- Modify: `internal/tui/app.go` (Deps fields near `InitialView` ~line 124; views slice ~line 304; `hiddenFromRail` ~line 326)
- Test: `internal/tui/connect_test.go`, `internal/tui/app_test.go`

**Interfaces:**
- Consumes: `repoReadyMsg{repo, config}` (app.go:429), `interactiveAWSAuthCommand(ctx, effects, method, profile, region) *exec.Cmd` (setup_wizard.go:1131), `setup.AWSAuthSSO`, `ctxOrBackground`, `ui.Primary/Muted/Danger` styles.
- Produces: `tui.Deps.ConnectError error` and `tui.Deps.OpenRepo func(context.Context) (*repo.Repo, *config.Config, error)` — Task 2 sets both; view id `"connect"` — Task 2 sets `InitialView: "connect"`.

- [ ] **Step 1: Write the failing tests**

Create `internal/tui/connect_test.go`:

```go
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// connectDeps builds Deps for the gate: an AWS-proper config (no endpoint,
// profile "sentra"), a canned open error, and an OpenRepo stub.
func connectDeps(openRepo func(context.Context) (*repo.Repo, *config.Config, error)) Deps {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-mg-002"
	cfg.Repo.S3.Profile = "sentra"
	return Deps{
		RepoName:     "sentra-mg-002",
		Config:       &cfg,
		ConnectError: errors.New("login session has expired, please reauthenticate"),
		OpenRepo:     openRepo,
	}
}

// The gate's first frame must tell the operator what failed and exactly
// what pressing l will run — the command is the whole point of the view.
func TestConnect_RendersErrorRetryAndLoginCommand(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	view := v.View()
	for _, want := range []string{
		"login session has expired",
		"retry",
		"aws sso login --profile sentra",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("connect view missing %q:\n%s", want, view)
		}
	}
}

// S3-compatible backends (MinIO, R2) have nothing SSO can fix: the login
// affordance must not render and l must be inert.
func TestConnect_HidesLoginForEndpointBackends(t *testing.T) {
	deps := connectDeps(nil)
	deps.Config.Repo.S3.EndpointURL = "http://127.0.0.1:9000"
	v := NewConnectView(deps)
	if strings.Contains(v.View(), "aws sso login") {
		t.Fatal("login hint rendered for an endpoint backend")
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd != nil {
		t.Fatal("l produced a command on an endpoint backend")
	}
	if m.(ConnectView).stage != connectIdle {
		t.Fatal("l changed stage on an endpoint backend")
	}
}

// r → OpenRepo succeeds → the gate forwards repoReadyMsg with the live
// repo, exactly like unlock: the App swap is the success path.
func TestConnect_RetrySuccessEmitsRepoReady(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("repo.Close: %v", err)
	}
	deps := connectDeps(func(ctx context.Context) (*repo.Repo, *config.Config, error) {
		live, err := repo.Open(ctx, store, []byte("hunter2"))
		if err != nil {
			return nil, nil, err
		}
		cfg := config.Defaults()
		return live, &cfg, nil
	})
	v := NewConnectView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r did not start an open attempt")
	}
	if m.(ConnectView).stage != connectOpening {
		t.Fatal("r did not enter connectOpening")
	}
	res := cmd() // run the open closure
	m2, fwd := m.(ConnectView).Update(res)
	if fwd == nil {
		t.Fatal("successful open produced no follow-up command")
	}
	ready, ok := fwd().(repoReadyMsg)
	if !ok {
		t.Fatalf("expected repoReadyMsg, got %T", fwd())
	}
	if ready.repo == nil {
		t.Fatal("repoReadyMsg carried a nil repo")
	}
	_ = m2
	_ = ready.repo.Close()
}

// r → OpenRepo fails → the NEW error replaces the old one and the gate
// returns to idle for another attempt.
func TestConnect_RetryFailureShowsFreshError(t *testing.T) {
	deps := connectDeps(func(context.Context) (*repo.Repo, *config.Config, error) {
		return nil, nil, errors.New("dial tcp: network is unreachable")
	})
	v := NewConnectView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	res := cmd()
	m2, _ := m.(ConnectView).Update(res)
	got := m2.(ConnectView)
	if got.stage != connectIdle {
		t.Fatal("failed open did not return to idle")
	}
	if !strings.Contains(got.View(), "network is unreachable") {
		t.Fatalf("view does not show the fresh open error:\n%s", got.View())
	}
}

// A successful login child auto-retries the open — the operator should
// not have to press r after authenticating.
func TestConnect_AuthSuccessAutoRetries(t *testing.T) {
	called := false
	deps := connectDeps(func(context.Context) (*repo.Repo, *config.Config, error) {
		called = true
		return nil, nil, errors.New("still failing")
	})
	v := NewConnectView(deps)
	v.stage = connectAuthing // as if ExecProcess just returned
	m, cmd := v.Update(connectAuthDoneMsg{})
	if m.(ConnectView).stage != connectOpening {
		t.Fatal("auth success did not start an open attempt")
	}
	if cmd == nil {
		t.Fatal("auth success produced no open command")
	}
	_ = cmd()
	if !called {
		t.Fatal("auto-retry did not invoke OpenRepo")
	}
}

// A failed login child surfaces ITS error and idles — no auto-retry that
// would just repeat the credential failure.
func TestConnect_AuthFailureShowsAuthError(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	v.stage = connectAuthing
	m, cmd := v.Update(connectAuthDoneMsg{err: errors.New("aws: command not found")})
	got := m.(ConnectView)
	if cmd != nil {
		t.Fatal("failed auth should not auto-retry")
	}
	if got.stage != connectIdle {
		t.Fatal("failed auth did not return to idle")
	}
	if !strings.Contains(got.View(), "aws: command not found") {
		t.Fatalf("view does not show the auth error:\n%s", got.View())
	}
}

// Keys are ignored while an attempt is in flight — one action at a time.
func TestConnect_KeysIgnoredWhileOpening(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	v.stage = connectOpening
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil || m.(ConnectView).stage != connectOpening {
		t.Fatal("r was not ignored while opening")
	}
}
```

Append to `internal/tui/app_test.go` (mirroring `TestApp_UnlockRegisteredAsView` at app_test.go:84):

```go
// TestApp_ConnectRegisteredAsView: the connect gate is a registered view
// so InitialView routing can land on it.
func TestApp_ConnectRegisteredAsView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	found := false
	for _, v := range app.views {
		if v.id == "connect" {
			found = true
		}
	}
	if !found {
		t.Fatal("connect view not registered in NewApp")
	}
}

// TestApp_ConnectHiddenFromRail: connect is a startup gate like unlock —
// it must never appear in the rail/palette surface.
func TestApp_ConnectHiddenFromRail(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if strings.Contains(m.(App).View(), "Connect") {
		t.Fatal("connect gate leaked into the rail")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestConnect|TestApp_Connect' 2>&1 | head -20`
Expected: FAIL to compile — "undefined: NewConnectView" (and friends).

- [ ] **Step 3: Add the Deps fields**

In `internal/tui/app.go`, directly after the `InitialView` field (~line 129):

```go
	// ConnectError is the repo-open failure that routed this launch to the
	// connect gate. The gate renders it on its first frame; nil on every
	// other launch path.
	ConnectError error

	// OpenRepo retries the launch-path repo open for the connect gate.
	// runUI wires it to the CLI's open path with the launch's command and
	// config captured, so a retry re-resolves the passphrase chain
	// (env / file / keyring) exactly like the original attempt; the
	// closure owns the secret's lifecycle and zeroizes it internally —
	// no passphrase ever reaches the TUI. Injected as a closure (like
	// NewStore) so internal/tui never imports internal/cli.
	OpenRepo func(ctx context.Context) (*repo.Repo, *config.Config, error)
```

(`context`, `repo`, `config` are already imported by app.go.)

- [ ] **Step 4: Implement ConnectView**

Create `internal/tui/connect.go`:

```go
package tui

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

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
```

Add the missing imports to the file's import block: `"github.com/markgustetic/sentra/internal/config"` and `"github.com/markgustetic/sentra/internal/repo"` (used by `connectResultMsg`).

- [ ] **Step 5: Register the view**

In `internal/tui/app.go`: add to the views slice, directly after the `unlock` entry (~line 304):

```go
		{id: "connect", model: NewConnectView(deps)},
```

and extend the hidden map (~line 326):

```go
	hiddenFromRail := map[string]bool{"unlock": true, "connect": true}
```

Update the comment above it: unlock AND connect are login/repair screens, not navigable operations.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/tui/ -run 'TestConnect|TestApp_Connect' -v`
Expected: all PASS. Then the whole package: `go test -race ./internal/tui/` — no regressions (in particular the golden/smoke tests and the unlock suite).

- [ ] **Step 7: Commit**

```bash
git add internal/tui/connect.go internal/tui/connect_test.go internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): connect gate for configured-but-unreachable repos"
```

---

### Task 2: Route runUI's open failure to the gate

**Files:**
- Modify: `internal/cli/ui.go:225-228` (the dashboard-path error return)
- Test: `internal/cli/ui_test.go`

**Interfaces:**
- Consumes: `tui.Deps.ConnectError` / `tui.Deps.OpenRepo` / view id `"connect"` (Task 1), `openRepoForConfig` (repo_open.go:35), `launchState` from `probeLaunchState`, `providerForLaunch`, `setupEffectsForLaunch`, `crypto.Zeroize`.
- Produces: `launchConnectGate(...)` — internal to ui.go; nothing downstream.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/ui_test.go`:

```go
// The headline routing: a configured repo whose OPEN fails (expired
// credentials, dead network) must land the operator on the TUI connect
// gate with the error and a working retry hook — not exit to stderr.
func TestRunUI_OpenFailureRoutesToConnectGate(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2") // passphrase available → dashboard path
	deps, captured := uiFixture(t, "hunter2")
	deps.NewStore = func(context.Context, *config.Config) (blobstore.Store, error) {
		return nil, errors.New("login session has expired")
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute should launch the gate, not fail: %v", err)
	}
	d := captured.Deps()
	if d.InitialView != "connect" {
		t.Fatalf("InitialView = %q, want \"connect\"", d.InitialView)
	}
	if d.Repo != nil {
		t.Fatal("gate launch must not carry a repo")
	}
	if d.ConnectError == nil || !strings.Contains(d.ConnectError.Error(), "login session has expired") {
		t.Fatalf("ConnectError = %v, want the open failure", d.ConnectError)
	}
	if d.OpenRepo == nil {
		t.Fatal("OpenRepo retry hook not wired")
	}
}

// The retry closure must re-run the whole open path — store construction
// and passphrase chain — so a fixed environment succeeds on retry.
func TestRunUI_ConnectGateRetryClosureReopens(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")
	deps, captured := uiFixture(t, "hunter2")
	working := deps.NewStore // uiFixture's in-memory store with a real repo
	calls := 0
	deps.NewStore = func(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("transient outage")
		}
		return working(ctx, cfg)
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	r, cfg, err := d.OpenRepo(context.Background())
	if err != nil {
		t.Fatalf("retry closure failed against a recovered store: %v", err)
	}
	if r == nil || cfg == nil {
		t.Fatal("retry closure returned nil repo or config")
	}
	_ = r.Close()
}

// Scope pin: a config that cannot LOAD is a fix-the-file problem, not a
// TUI state — it must still exit to the CLI without constructing an App.
func TestRunUI_ConfigLoadFailureStillExitsToCLI(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile("sentra.yaml", []byte(":\tnot yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	deps, _ := uiFixture(t, "hunter2")
	ran := false
	deps.Run = func(_ tui.App) error { ran = true; return nil }
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected a config load error")
	}
	if ran {
		t.Fatal("a broken config must not launch the TUI")
	}
}
```

(`errors`, `os`, `io`, `context`, `strings`, `blobstore`, `config`, `tui` are already in ui_test.go's imports.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunUI_OpenFailure|TestRunUI_ConnectGate|TestRunUI_ConfigLoadFailure' -v`
Expected: the first two FAIL (execute currently returns the open error; no gate). The scope-pin test should already PASS — verify it does, for the right reason (load error propagates).

- [ ] **Step 3: Implement the routing**

In `internal/cli/ui.go`, replace the dashboard-path error return (lines 225-228):

```go
	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		// Configured, passphrase available, but the repo would not OPEN
		// (expired AWS credentials, unreachable bucket, network down).
		// The TUI owns the other launch states — first-run lands on the
		// wizard, locked on unlock — so this one lands on the connect
		// gate with the fix a keypress away, instead of dying to stderr.
		// Config-load/probe failures never reach here: probeLaunchState
		// already returned those above.
		return launchConnectGate(cmd, deps, cfgPath, absCfgPath, st, showSplash, passphraseFile, err)
	}
	defer crypto.Zeroize(pass)
	defer r.Close()
```

Add below `runUI` (before `loadSetupDraft`):

```go
// launchConnectGate builds the App for the connect gate: no repo, the
// open failure for the gate to render, and a retry closure that re-runs
// the launch open end-to-end. The closure re-resolves the passphrase
// chain (env / file / keyring) on every call and zeroizes the secret
// before returning — the repo's derived keys are what the TUI needs, the
// passphrase never crosses the seam. Closure injection keeps the
// tui→cli import direction clean, same as Deps.NewStore.
func launchConnectGate(cmd *cobra.Command, deps UIDeps, cfgPath, absCfgPath string, st launchState, showSplash bool, passphraseFile string, openErr error) error {
	app := tui.NewApp(tui.Deps{
		Provider:                providerForLaunch(deps, st.Config),
		RepoName:                st.Config.Repo.S3.Bucket,
		Config:                  st.Config,
		Ctx:                     cmd.Context(),
		ConfigPath:              absCfgPath,
		NewStore:                deps.NewStore,
		Actions:                 deps.Actions,
		SaveKeyringPassphrase:   deps.SavePassphrase,
		DeleteKeyringPassphrase: deps.DeletePassphrase,
		SetupEffects:            setupEffectsForLaunch(deps),
		PassphraseFile:          passphraseFile,
		InitialView:             "connect",
		ShowSplash:              showSplash,
		Version:                 deps.Version,
		Commit:                  deps.Commit,
		ConnectError:            openErr,
		OpenRepo: func(_ context.Context) (*repo.Repo, *config.Config, error) {
			// openRepoForConfig reads the command's context itself; the
			// parameter exists for the seam's shape, and the command's
			// context is the same cancellation tree the TUI runs under.
			r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
			if err != nil {
				return nil, nil, err
			}
			crypto.Zeroize(pass)
			return r, cfg, nil
		},
	})
	if deps.Run == nil {
		return fmt.Errorf("ui: no Run hook configured")
	}
	return deps.Run(app)
}
```

Add `"context"` and `"github.com/markgustetic/sentra/internal/repo"` to ui.go's imports if not present.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/cli/ -run 'TestRunUI|TestUI|TestRoot' -v 2>&1 | tail -15`
Expected: the two new tests PASS; every existing runUI/launch test still passes (dashboard, wizard, unlock, local, splash, bare-dispatch).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/ui.go internal/cli/ui_test.go
git commit -m "feat(cli): route launch repo-open failures to the TUI connect gate"
```

---

### Task 3: Docs, full gate, reinstall

**Files:**
- Modify: `AGENTS.md` (launch-state / surface-contract material), `CLAUDE.md` (TUI paragraph in "What this is")

**Interfaces:**
- Consumes: Tasks 1–2 finished.
- Produces: documented launch-state contract; a pushed, installed binary.

- [ ] **Step 1: AGENTS.md**

Find the section describing the TUI launch states (first-run → wizard, locked → unlock; near the surface-contract / config-resolution material) and add:

```markdown
Configured but unreachable — the config loads and a passphrase source
answers, but the repository fails to open (expired AWS credentials,
unreachable bucket) — lands on the **connect gate**: it shows the open
error and offers `r` retry and, for AWS-proper backends only (no
`endpoint_url`), `l` to run `aws sso login` (with `--profile` when the
config names one) via a suspended terminal, auto-retrying on return. A
successful open swaps the live repo in and lands on the dashboard.
Config-load errors still exit to the CLI. The login never auto-runs.
```

- [ ] **Step 2: CLAUDE.md**

In the TUI paragraph of "What this is" (after the sentence about wizard/unlock/dashboard landing), add:

```markdown
A configured repo that fails to *open* (expired AWS SSO, unreachable
bucket) lands on the connect gate — retry or run `aws sso login` from
inside the TUI; only config-file errors exit to the CLI.
```

- [ ] **Step 3: Full gate**

```bash
GOFLAGS=-timeout=40m just check
go mod tidy -diff
git diff --check
```

Expected: all clean. If golangci-lint reports findings with `../../../` paths or caret/source mismatches, run `golangci-lint cache clean` and retry before treating them as real.

- [ ] **Step 4: Commit and push**

```bash
git add AGENTS.md CLAUDE.md
git commit -m "docs: connect gate launch state"
git push
```

- [ ] **Step 5: Reinstall the binary**

```bash
just install
```

Then verify the installed binary carries the gate: `sentra 2>&1 | head -2` from the repo directory in a NON-tty shell still prints the terminal-required error (the gate needs a TTY like every TUI path) — but `sentra ui --help` exists and the tui tests prove the gate. The real-terminal check is the operator's: run `sentra`, see the connect gate, press `l`.
