# Welcome / Logo Splash Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a centered `✦ sentra` welcome splash for ~1 second on every TUI launch (any key skips it), with a persisted opt-out toggle in the Settings view.

**Architecture:** An App-level overlay. `App` gains a `splashActive` flag; `Init()` starts a `tea.Tick`, `View()` renders the splash instead of the frame while active, and `routeKey` consumes the first keystroke to dismiss it. Nothing about the views slice, command registry, `InitialView` routing, op guard, or key-routing precedence changes beyond one early return. The opt-out persists as a new `ui.hide_splash` config field, toggled from the Settings view.

**Tech Stack:** Go 1.25; Bubbletea v1.3.10 (`tea.Tick`), Lipgloss v1.1.0 (`lipgloss.Place`), koanf-backed `internal/config`.

**Spec:** `docs/superpowers/specs/2026-07-09-welcome-splash-design.md`

---

## Execution notes

1. **Branch.** Work on `feature/welcome-splash` (already created; the spec is committed there). Commit per task with the messages shown.
2. **`Deps{}` must keep the splash off.** `ShowSplash` defaults to `false`, so the entire existing `internal/tui` suite renders the normal frame and stays green without modification. Do not flip that default.
3. **Gate after every task:** `go build ./... && go test ./internal/config/ ./internal/tui/ ./internal/cli/ ./cmd/sentra/ -count=1 && gofmt -l cmd internal && go vet ./... && golangci-lint run ./internal/config/... ./internal/tui/... ./internal/cli/... ./cmd/sentra/...` — lint must report `0 issues`.
4. **`cat`/`tail`/`head` are aliased to `bat`** — use `command tail -n N` or your file tools.

## File structure

- **Modify** `internal/config/config.go` — new `UI` section on `Config`.
- **Modify** `internal/config/render.go` — emit the `ui:` block.
- **Modify** `internal/tui/app.go` — `Deps.ShowSplash/Version/Commit`; `splashActive`; `splashDoneMsg`; `Init`/`Update`/`routeKey`/`View`; `renderSplash`/`versionLine`.
- **Modify** `internal/cli/ui.go` — `UIDeps.Version/Commit`; compute `ShowSplash` in `runUI`; pass into both `tui.NewApp` sites.
- **Modify** `cmd/sentra/main.go`, `cmd/sentra/commands.go` — thread `version`/`commit`.
- **Modify** `internal/tui/settings.go` — the toggle row.
- **Tests** in `internal/config/render_test.go`, `internal/tui/app_test.go`, `internal/tui/settings_test.go`, `internal/cli/ui_test.go`.

---

### Task 1: Config field `ui.hide_splash`

**Files:**
- Modify: `internal/config/config.go` (append a section to `Config`, after `Passphrase`)
- Modify: `internal/config/render.go:42-90` (the `Render` template and its args)
- Test: `internal/config/render_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/render_test.go` (it already imports `os`, `path/filepath`, `strings`, `testing`):

```go
// TestRender_EmitsUIHideSplash pins the new ui: section into the rendered file
// so a config rewrite (setup, policy edit) round-trips the operator's choice.
func TestRender_EmitsUIHideSplash(t *testing.T) {
	var cfg Config
	cfg.UI.HideSplash = true
	body := string(Render(&cfg))
	for _, want := range []string{"ui:", "hide_splash: true"} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered config missing %q:\n%s", want, body)
		}
	}
}

// TestLoad_MissingUISectionDefaultsToSplashOn is the reason the field is named
// HideSplash rather than ShowSplash: bool's zero value is false, so a config
// written before this field existed must load as "don't hide" — splash on.
func TestLoad_MissingUISectionDefaultsToSplashOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	legacy := "repo:\n  s3:\n    bucket: \"b\"\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.UI.HideSplash {
		t.Error("a config with no ui: section must load HideSplash=false (splash shows)")
	}
}

// TestWrite_RoundTripsHideSplash proves the toggle survives Write -> Load.
func TestWrite_RoundTripsHideSplash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	var cfg Config
	cfg.Repo.S3.Bucket = "b"
	cfg.UI.HideSplash = true
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got.UI.HideSplash {
		t.Error("HideSplash did not round-trip through Write -> Load")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestRender_EmitsUIHideSplash|TestLoad_MissingUISectionDefaultsToSplashOn|TestWrite_RoundTripsHideSplash' -count=1`
Expected: FAIL — build error `cfg.UI undefined (type Config has no field or method UI)`.

- [ ] **Step 3: Add the field**

In `internal/config/config.go`, add a section to `Config` immediately after the `Passphrase` block (before the closing `}` of the struct):

```go
	// UI contains optional presentation settings for the TUI. Stored under
	// "ui" in the YAML; carries no secrets.
	//
	// HideSplash is negated deliberately. Go's zero value for bool is false,
	// so a sentra.yaml written before this field existed loads as "don't
	// hide" — the welcome splash shows by default, with no migration and no
	// pointer field.
	UI struct {
		HideSplash bool `koanf:"hide_splash"`
	} `koanf:"ui"`
```

- [ ] **Step 4: Emit it from `Render`**

In `internal/config/render.go`, inside the `fmt.Sprintf` template, replace the trailing passphrase block:

```
passphrase:
  use_keyring: %t
%s`,
```

with:

```
passphrase:
  use_keyring: %t

ui:
  hide_splash: %t         # true skips the welcome splash at launch
%s`,
```

and in the argument list, insert `cfg.UI.HideSplash` between `cfg.Passphrase.UseKeyring` and `renderPoliciesYAML(cfg.Policies)`:

```go
		cfg.Passphrase.UseKeyring,
		cfg.UI.HideSplash,
		renderPoliciesYAML(cfg.Policies),
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/config/ ./internal/cli/ -count=1`
Expected: PASS. (`internal/cli` is included because `setup_test.go` round-trips rendered configs; it must stay green.)

- [ ] **Step 6: Commit**

```bash
git add internal/config/config.go internal/config/render.go internal/config/render_test.go
git commit -m "feat(config): add ui.hide_splash setting"
```

---

### Task 2: `tui.Deps` carries ShowSplash, Version, Commit

**Files:**
- Modify: `internal/tui/app.go` (the `Deps` struct, after `InitialView`)
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_DepsCarrySplashFields: the splash is opt-in via Deps so the whole
// existing suite (which constructs Deps{}) keeps rendering the normal frame.
func TestApp_DepsCarrySplashFields(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4"})
	if !app.deps.ShowSplash {
		t.Error("Deps.ShowSplash not carried through NewApp")
	}
	if app.deps.Version != "v1.2.0" || app.deps.Commit != "a1b2c3d4" {
		t.Errorf("version/commit not carried: %q %q", app.deps.Version, app.deps.Commit)
	}
	if NewApp(Deps{RepoName: "x"}).deps.ShowSplash {
		t.Error("Deps{} must default ShowSplash to false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestApp_DepsCarrySplashFields -count=1`
Expected: FAIL — `unknown field ShowSplash in struct literal of type Deps`.

- [ ] **Step 3: Add the fields**

In `internal/tui/app.go`, append to the `Deps` struct after `InitialView`:

```go
	// ShowSplash gates the launch splash. runUI sets it from the config's
	// ui.hide_splash; the zero value (false) keeps it off, so tests that build
	// a bare Deps{} render the normal frame.
	ShowSplash bool

	// Version and Commit identify the build on the splash. They are plain
	// display data, threaded from cmd/sentra. Commit may be the goreleaser
	// placeholder "none", in which case it is omitted from the rendered line.
	Version string
	Commit  string
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestApp_DepsCarrySplashFields -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): add ShowSplash/Version/Commit to Deps"
```

---

### Task 3: The splash overlay in `App`

**Files:**
- Modify: `internal/tui/app.go` (imports; `App` struct; `NewApp` return literal; `Init`; `Update`; `routeKey`; `View`; new `renderSplash`/`versionLine`)
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/app_test.go`:

```go
// splashApp builds a sized App with the splash armed.
func splashApp(t *testing.T) App {
	t.Helper()
	app := NewApp(Deps{RepoName: "x", ShowSplash: true, Version: "v1.2.0", Commit: "a1b2c3d4ef"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return sized.(App)
}

func TestApp_SplashRendersThenAutoDismisses(t *testing.T) {
	app := splashApp(t)
	if !strings.Contains(app.View(), "s  e  n  t  r  a") {
		t.Fatalf("splash wordmark not rendered:\n%s", app.View())
	}
	m, _ := app.Update(splashDoneMsg{})
	app = m.(App)
	if strings.Contains(app.View(), "s  e  n  t  r  a") {
		t.Error("splashDoneMsg must retire the splash")
	}
	if !strings.Contains(app.View(), "Dashboard") {
		t.Errorf("normal frame should render after the splash:\n%s", app.View())
	}
}

// The dismissing key is CONSUMED: it must not reach the active view.
func TestApp_SplashDismissedByAnyKeyAndConsumed(t *testing.T) {
	app := splashApp(t)
	before := app.active
	m, _ := app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	app = m.(App)
	if strings.Contains(app.View(), "s  e  n  t  r  a") {
		t.Error("any key must dismiss the splash")
	}
	if app.active != before {
		t.Errorf("the dismissing key must be consumed, not routed (active %d -> %d)", before, app.active)
	}
}

func TestApp_CtrlCQuitsDuringSplash(t *testing.T) {
	app := splashApp(t)
	_, cmd := app.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("ctrl+c must quit even while the splash is up")
	}
}

func TestApp_NoSplashByDefault(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	if strings.Contains(sized.(App).View(), "s  e  n  t  r  a") {
		t.Error("Deps{} must not show the splash")
	}
}

func TestApp_TooSmallBeatsSplash(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", ShowSplash: true})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 20, Height: 5})
	out := sized.(App).View()
	if !strings.Contains(out, "terminal too small") {
		t.Errorf("the too-small guard must outrank the splash:\n%s", out)
	}
}

func TestApp_VersionLine(t *testing.T) {
	tests := []struct {
		name    string
		version string
		commit  string
		want    string
	}{
		{name: "version and short commit", version: "v1.2.0", commit: "a1b2c3d4ef", want: "v1.2.0 · a1b2c3d"},
		{name: "commit none is omitted", version: "dev", commit: "none", want: "dev"},
		{name: "empty commit is omitted", version: "dev", commit: "", want: "dev"},
		{name: "no version renders nothing", version: "", commit: "abc", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := NewApp(Deps{Version: tt.version, Commit: tt.commit})
			if got := app.versionLine(); got != tt.want {
				t.Errorf("versionLine() = %q, want %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestApp_Splash|TestApp_CtrlCQuitsDuringSplash|TestApp_NoSplashByDefault|TestApp_TooSmallBeatsSplash|TestApp_VersionLine' -count=1`
Expected: FAIL — `undefined: splashDoneMsg` and `app.versionLine undefined`.

- [ ] **Step 3: Implement the overlay**

In `internal/tui/app.go`:

(a) Add `"time"` to the stdlib import group (the file imports `lipgloss` already, but not `time`).

(b) Near the other message types, add:

```go
// splashDuration is how long the launch splash lingers before it retires
// itself. Any keystroke dismisses it sooner, so this is a ceiling, not a wait.
const splashDuration = time.Second

// splashDoneMsg retires the launch splash when the tick fires.
type splashDoneMsg struct{}
```

(c) Add a field to the `App` struct, next to `paletteOpen`:

```go
	// splashActive is true while the launch splash covers the frame. It is
	// seeded from Deps.ShowSplash and cleared by the tick or any keystroke.
	splashActive bool
```

(d) In `NewApp`'s return literal, add:

```go
		splashActive: deps.ShowSplash,
```

(e) Replace `Init`:

```go
func (m App) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.views)+1)
	for _, v := range m.views {
		cmds = append(cmds, v.model.Init())
	}
	if m.splashActive {
		cmds = append(cmds, tea.Tick(splashDuration, func(time.Time) tea.Msg {
			return splashDoneMsg{}
		}))
	}
	return tea.Batch(cmds...)
}
```

(f) In `Update`'s type switch, add a case (WindowSizeMsg keeps flowing through, so the frame behind the splash is sized the instant it clears):

```go
	case splashDoneMsg:
		m.splashActive = false
		return m, nil
```

(g) In `routeKey`, insert immediately AFTER the `m.tooSmall()` guard and BEFORE the modal stack:

```go
	// The launch splash owns the first keystroke: any key dismisses it and the
	// key is consumed, so it never falls through to a view, modal, or nav
	// binding. ctrl+c (checked above) still quits.
	if m.splashActive {
		m.splashActive = false
		return m, nil
	}
```

(h) In `View`, insert immediately AFTER the too-small guard and BEFORE the modal check:

```go
	if m.splashActive {
		return m.renderSplash()
	}
```

(i) Add the two renderers at the end of the file:

```go
// renderSplash draws the centered launch lockup: the brand glyph, a
// letter-spaced wordmark, the tagline, and the build identity. It is drawn
// into the full terminal rectangle; before the first WindowSizeMsg the
// dimensions are zero, so we fall back to the unplaced body.
func (m App) renderSplash() string {
	brand := lipgloss.NewStyle().Foreground(ui.AccentPink).Bold(true)
	body := brand.Render("✦") + "\n\n" +
		brand.Render("s  e  n  t  r  a") + "\n\n" +
		ui.Muted.Render("Encrypted, deduplicated, agent-aware backups") + "\n" +
		ui.Muted.Render("for S3-compatible storage")
	if v := m.versionLine(); v != "" {
		body += "\n\n" + ui.Muted.Render(v)
	}
	if m.width == 0 || m.height == 0 {
		return body
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, body)
}

// versionLine renders "version · shortcommit". The commit is dropped when it
// is empty or the goreleaser placeholder "none" (a plain `go build`), and it
// is truncated to the conventional 7 characters.
func (m App) versionLine() string {
	v := strings.TrimSpace(m.deps.Version)
	c := strings.TrimSpace(m.deps.Commit)
	if v == "" {
		return ""
	}
	if c == "" || c == "none" {
		return v
	}
	if len(c) > 7 {
		c = c[:7]
	}
	return v + " · " + c
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS — the new tests plus the entire existing suite (unchanged, because `Deps{}` leaves `ShowSplash` false).

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): add the launch welcome splash overlay"
```

---

### Task 4: Wire version/commit and ShowSplash through the CLI

**Files:**
- Modify: `internal/cli/ui.go` (`UIDeps`; `runUI`'s two `tui.NewApp(tui.Deps{...})` sites at ~:152 and ~:191)
- Modify: `cmd/sentra/commands.go:15` (signature + `uiDeps`)
- Modify: `cmd/sentra/main.go:23` (call site)
- Test: `internal/cli/ui_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/ui_test.go`:

```go
// TestRunUI_SplashFollowsConfig proves runUI reads ui.hide_splash and threads
// the build identity, on the dashboard path.
func TestRunUI_SplashFollowsConfig(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")

	deps, captured := uiFixture(t, "hunter2")
	deps.Version = "v1.2.0"
	deps.Commit = "a1b2c3d4"

	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	d := captured.Deps()
	if !d.ShowSplash {
		t.Error("a config without ui.hide_splash must launch with the splash on")
	}
	if d.Version != "v1.2.0" || d.Commit != "a1b2c3d4" {
		t.Errorf("build identity not threaded: %q %q", d.Version, d.Commit)
	}
}

// TestRunUI_HideSplashDisablesSplash: the persisted opt-out wins.
func TestRunUI_HideSplashDisablesSplash(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")
	t.Setenv("SENTRA_PASSPHRASE", "hunter2")

	// Rewrite the config with the splash suppressed.
	cfg, err := config.Load("sentra.yaml")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cfg.UI.HideSplash = true
	if err := config.Write("sentra.yaml", cfg); err != nil {
		t.Fatalf("write: %v", err)
	}

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.Deps().ShowSplash {
		t.Error("ui.hide_splash: true must disable the splash")
	}
}

// TestRunUI_FirstRunShowsSplash: no config on disk, so the default applies.
func TestRunUI_FirstRunShowsSplash(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no sentra.yaml
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
		},
		Run: func(app tui.App) error { captured = app; return nil },
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !captured.Deps().ShowSplash {
		t.Error("first run (no config) must show the splash")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestRunUI_SplashFollowsConfig|TestRunUI_HideSplashDisablesSplash|TestRunUI_FirstRunShowsSplash' -count=1`
Expected: FAIL — `unknown field Version in struct literal of type UIDeps`, and `d.ShowSplash undefined`.

- [ ] **Step 3: Add `UIDeps` fields and compute `ShowSplash` in `runUI`**

In `internal/cli/ui.go`, append to `UIDeps`:

```go
	// Version and Commit identify the build; they reach the TUI's welcome
	// splash. Plain display data, threaded from cmd/sentra. Commit may be the
	// goreleaser placeholder "none".
	Version string
	Commit  string
```

In `runUI`, after the `absCfgPath` block and before the first-run/locked branch, add:

```go
	// The welcome splash is on unless the operator persisted the opt-out.
	// probeLaunchState already loaded the config on both paths, and
	// launchState.Config is non-nil on a nil error, so no extra load is needed.
	showSplash := true
	if st.ConfigExists {
		showSplash = !st.Config.UI.HideSplash
	}
```

Then add these three fields to **both** `tui.NewApp(tui.Deps{...})` literals (the gate branch and the dashboard branch):

```go
			ShowSplash: showSplash,
			Version:    deps.Version,
			Commit:     deps.Commit,
```

- [ ] **Step 4: Thread version/commit from `cmd/sentra`**

In `cmd/sentra/commands.go`, change the signature and the `uiDeps` literal:

```go
func addProductionCommands(root *cobra.Command, rootFlags *cli.RootFlags, version, commit string) {
```

and inside the `uiDeps := cli.UIDeps{...}` literal add:

```go
		Version: version,
		Commit:  commit,
```

In `cmd/sentra/main.go`, update the call:

```go
	addProductionCommands(root, rootFlags, version, commit)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/cli/ ./cmd/sentra/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/cli/ui.go internal/cli/ui_test.go cmd/sentra/commands.go cmd/sentra/main.go
git commit -m "feat(cli): thread build identity and ui.hide_splash into the TUI"
```

---

### Task 5: The Settings toggle

**Files:**
- Modify: `internal/tui/settings.go` (entry kinds; `NewSettingsView`; `Update`; `View`; new `toggleSplash`)
- Test: `internal/tui/settings_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/settings_test.go` (add imports `os`, `path/filepath`, and `github.com/markgustetic/sentra/internal/config` if absent):

```go
// settingsWithConfig writes a real config file and returns a view bound to it.
func settingsWithConfig(t *testing.T) (SettingsView, string, *config.Config) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sentra.yaml")
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "b"
	if err := config.Write(path, cfg); err != nil {
		t.Fatal(err)
	}
	return NewSettingsView(Deps{Config: cfg, ConfigPath: path}), path, cfg
}

// cursorTo moves the settings cursor onto the splash toggle row.
func cursorTo(v SettingsView, kind settingsEntryKind) SettingsView {
	for i, e := range v.entries {
		if e.kind == kind {
			v.cursor = i
		}
	}
	return v
}

func TestSettings_ToggleSplashPersists(t *testing.T) {
	v, path, cfg := settingsWithConfig(t)
	v = cursorTo(v, entryToggleSplash)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	if !cfg.UI.HideSplash {
		t.Error("toggling must flip the in-memory config after a successful write")
	}
	got, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !got.UI.HideSplash {
		t.Error("toggling must persist hide_splash to disk")
	}
	if !strings.Contains(v.View(), "[off]") {
		t.Errorf("view should show the splash as off:\n%s", v.View())
	}
}

func TestSettings_ToggleSplashDisabledWithoutConfig(t *testing.T) {
	v := cursorTo(NewSettingsView(Deps{}), entryToggleSplash)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)
	if !strings.Contains(v.View(), "available after setup") {
		t.Errorf("no config: the toggle must render a disabled hint:\n%s", v.View())
	}
}

// A failed write must not desync the in-memory config from disk.
func TestSettings_ToggleSplashWriteErrorKeepsMemory(t *testing.T) {
	cfg := &config.Config{}
	// A path inside a non-existent directory makes config.Write fail.
	bad := filepath.Join(t.TempDir(), "missing-dir", "sentra.yaml")
	v := cursorTo(NewSettingsView(Deps{Config: cfg, ConfigPath: bad}), entryToggleSplash)

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)

	if cfg.UI.HideSplash {
		t.Error("a failed write must leave the in-memory config unchanged")
	}
	if !strings.Contains(v.View(), "could not save") {
		t.Errorf("a write error should surface inline:\n%s", v.View())
	}
}

// Navigation entries still work.
func TestSettings_NavigateEntryStillEmitsActivate(t *testing.T) {
	v := cursorTo(NewSettingsView(Deps{}), entryNavigate)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("a navigate entry must emit a command")
	}
	if _, ok := cmd().(activateMsg); !ok {
		t.Error("a navigate entry must emit activateMsg")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run TestSettings_ -count=1`
Expected: FAIL — `undefined: settingsEntryKind`, `undefined: entryToggleSplash`, `e.kind undefined`.

- [ ] **Step 3: Implement the toggle**

In `internal/tui/settings.go`, add the `config` import, then replace the entry type, constructor, `Update`'s Enter case, and `View`:

```go
// settingsEntryKind distinguishes a row that navigates elsewhere from a row
// that mutates a setting in place.
type settingsEntryKind int

const (
	entryNavigate settingsEntryKind = iota
	entryToggleSplash
)

// settingsEntry is one actionable row in the Settings view. A navigate entry
// emits an activateMsg for targetID; a toggle entry mutates the config and
// persists it. Settings holds no secrets.
type settingsEntry struct {
	kind     settingsEntryKind
	label    string
	desc     string
	targetID string // navigate entries only
}
```

`NewSettingsView`:

```go
func NewSettingsView(deps Deps) SettingsView {
	return SettingsView{
		deps: deps,
		entries: []settingsEntry{
			{kind: entryNavigate, label: "Re-run setup", desc: "reconfigure the backend and repository", targetID: "setup"},
			{kind: entryNavigate, label: "Change passphrase", desc: "rotate the repository passphrase", targetID: "password"},
			{kind: entryToggleSplash, label: "Welcome splash", desc: "show the logo screen at launch (applies next launch)"},
		},
	}
}
```

Add an `err` field to `SettingsView` (next to `width`):

```go
	err string // inline failure text, e.g. a failed config write
```

`Update`'s `tea.KeyEnter` case becomes:

```go
		case tea.KeyEnter:
			e := v.entries[v.cursor]
			if e.kind == entryToggleSplash {
				return v.toggleSplash()
			}
			return v, func() tea.Msg { return activateMsg{id: e.targetID} }
```

Add `toggleSplash`:

```go
// toggleSplash flips ui.hide_splash and persists it. It mutates a COPY, writes
// that, and only adopts the value in memory once the file is on disk — a failed
// write must never leave the process disagreeing with sentra.yaml.
func (v SettingsView) toggleSplash() (tea.Model, tea.Cmd) {
	if v.deps.Config == nil || v.deps.ConfigPath == "" {
		v.err = "available after setup"
		return v, nil
	}
	next := *v.deps.Config
	next.UI.HideSplash = !next.UI.HideSplash
	if err := config.Write(v.deps.ConfigPath, &next); err != nil {
		v.err = "could not save: " + err.Error()
		return v, nil
	}
	*v.deps.Config = next
	v.err = ""
	return v, nil
}

// splashState renders the toggle's current value for the row label.
func (v SettingsView) splashState() string {
	if v.deps.Config == nil {
		return "—"
	}
	if v.deps.Config.UI.HideSplash {
		return "off"
	}
	return "on"
}
```

`View` — render the toggle's state and any error:

```go
func (v SettingsView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Settings") + "\n\n")
	b.WriteString(v.renderSummary() + "\n")
	for i, e := range v.entries {
		line := e.label
		if e.kind == entryToggleSplash {
			line = e.label + "   [" + v.splashState() + "]"
		}
		if i == v.cursor {
			b.WriteString(ui.SidebarActive.Render(line) + "\n")
		} else {
			b.WriteString(ui.SidebarItem.Render(line) + "\n")
		}
		b.WriteString("    " + ui.Muted.Render(e.desc) + "\n")
	}
	if v.err != "" {
		b.WriteString("\n" + ui.Danger.Render(v.err))
	}
	b.WriteString("\n" + ui.Muted.Render("↑↓ move   ⏎ open / toggle"))
	return b.String()
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/settings.go internal/tui/settings_test.go
git commit -m "feat(tui): add a persisted welcome-splash toggle to Settings"
```

---

### Task 6: Full gate

**Files:** none (verification only)

- [ ] **Step 1: Run the full CI-equivalent gate**

```bash
go build ./...
go vet ./...
gofmt -l cmd internal        # expect no output
go test -race -count=1 ./...
go test ./third_party/fastcdc-go/...
golangci-lint run ./...      # expect "0 issues"
go mod tidy -diff
git diff --check
```
Expected: every package `ok`; lint `0 issues`; tidy and diff-check clean.

- [ ] **Step 2: Commit (only if the gate produced changes; otherwise skip)**

```bash
git commit --allow-empty -m "chore: welcome splash full-gate green"
```

- [ ] **Step 3: Manual smoke test (human-run — cannot be automated)**

```bash
just local-reset && sentra local
```
Confirm: the `✦ / s e n t r a` lockup renders centered with the tagline and `dev` (a `go build` has `commit=none`, so the commit is omitted); it clears itself after about a second; pressing a key during it skips straight to the wizard and that key is not typed into the bucket field; `ctrl+c` during the splash quits. Then finish setup, open **Settings**, move to **Welcome splash**, press `⏎` — it should read `[off]` and persist; relaunch `sentra local` and confirm no splash; toggle it back on.
