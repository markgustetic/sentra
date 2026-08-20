# Config Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bare `sentra` (and every repo-facing command) finds the operator's config from any directory: `./sentra.yaml` when present, else `~/.config/sentra/sentra.yaml`, so the TUI opens the production repo instead of the first-run wizard.

**Architecture:** One discovery function in `internal/config` (`DiscoverPath`), one run-time resolver in `internal/cli` (`resolveConfigPath`) called at the top of every run body, and `MkdirAll` in `config.Write` so the home path is writable on a fresh machine. No cobra hooks — the root command already owns `PersistentPreRunE` for slog and per-command hooks would shadow it.

**Tech Stack:** Go 1.25, cobra, koanf. Tests: stdlib `testing` with `t.TempDir`/`t.Setenv`/`t.Chdir` (cli package tests use the existing `chDir` helper from `init_test.go`).

**Spec:** `docs/superpowers/specs/2026-08-20-config-discovery-design.md`

## Global Constraints

- TDD: write the failing test first, watch it fail for the right reason, then implement the minimum to pass.
- `internal/tui` must never import `internal/cli`. `DiscoverPath` lives in `internal/config` for this reason.
- `sentra init` stays cwd-only (its design doc: "init never reaches outside cwd"). Do NOT touch `internal/cli/init.go` except its `configFileName` const doc if needed.
- `sentra local` keeps its explicit `.sentra-local.yaml`; discovery must never fire for it.
- Never write passphrases, credentials, or secrets into any test fixture or config file.
- Resolution happens at RunE time, never at wiring time: `Flags().Changed` is only meaningful after cobra parses argv, and tests `chDir` after building commands.
- `config.Update`/`config.Load` semantics are untouched — only the default path they are handed changes.
- Commit per task; `git add` only the files named in the task (the tree may hold an unrelated leftover `sentra.yaml` — never `git add .`).
- While iterating, run `-race` tests only for the package you changed; the full gate runs once in Task 7.
- Home fallback is `$XDG_CONFIG_HOME/sentra/sentra.yaml`, defaulting `XDG_CONFIG_HOME` to `~/.config` — the gh-CLI convention, deliberately NOT `os.UserConfigDir()`.

---

### Task 1: `config.DiscoverPath()`

**Files:**
- Create: `internal/config/discover.go`
- Test: `internal/config/discover_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `func DiscoverPath() string` in package `config` — no arguments, never errors, never touches the file beyond `os.Stat`. Task 3 calls it.

- [ ] **Step 1: Write the failing test**

Create `internal/config/discover_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

// touchDiscoverFile creates path (and its parents) with non-secret content.
func touchDiscoverFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("repo:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The table exercises the precedence RULE, not one happy path: cwd wins
// when present, the XDG home is the fallback and the first-run write
// target, and a directory that merely shares the name does not count.
func TestDiscoverPath(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, cwd, xdg string)
		want    func(cwd, xdg string) string
	}{
		{
			name: "cwd config wins",
			arrange: func(t *testing.T, cwd, xdg string) {
				touchDiscoverFile(t, filepath.Join(cwd, "sentra.yaml"))
			},
			want: func(cwd, xdg string) string { return "sentra.yaml" },
		},
		{
			name:    "no cwd config falls back to XDG home",
			arrange: func(t *testing.T, cwd, xdg string) {},
			want: func(cwd, xdg string) string {
				return filepath.Join(xdg, "sentra", "sentra.yaml")
			},
		},
		{
			name: "both present cwd wins",
			arrange: func(t *testing.T, cwd, xdg string) {
				touchDiscoverFile(t, filepath.Join(cwd, "sentra.yaml"))
				touchDiscoverFile(t, filepath.Join(xdg, "sentra", "sentra.yaml"))
			},
			want: func(cwd, xdg string) string { return "sentra.yaml" },
		},
		{
			name: "directory named sentra.yaml does not count",
			arrange: func(t *testing.T, cwd, xdg string) {
				if err := os.Mkdir(filepath.Join(cwd, "sentra.yaml"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: func(cwd, xdg string) string {
				return filepath.Join(xdg, "sentra", "sentra.yaml")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			xdg := t.TempDir()
			t.Chdir(cwd)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			tt.arrange(t, cwd, xdg)
			if got, want := DiscoverPath(), tt.want(cwd, xdg); got != want {
				t.Errorf("DiscoverPath() = %q, want %q", got, want)
			}
		})
	}
}

// Unset/empty XDG_CONFIG_HOME defaults to ~/.config (the gh-CLI
// convention). HOME is how os.UserHomeDir resolves on unix.
func TestDiscoverPath_DefaultsXDGToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "sentra", "sentra.yaml")
	if got := DiscoverPath(); got != want {
		t.Errorf("DiscoverPath() = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestDiscoverPath -v`
Expected: FAIL to compile — "undefined: DiscoverPath"

- [ ] **Step 3: Write minimal implementation**

Create `internal/config/discover.go`:

```go
package config

import (
	"os"
	"path/filepath"
)

// DiscoverPath returns the config path commands use when the operator did
// not pass --config explicitly:
//
//  1. ./sentra.yaml, when it exists as a regular file — a project-local
//     config always outranks the user-level one.
//  2. $XDG_CONFIG_HOME/sentra/sentra.yaml, defaulting XDG_CONFIG_HOME to
//     ~/.config. This is the gh-CLI convention: ~/.config even on macOS,
//     deliberately not os.UserConfigDir's ~/Library/Application Support.
//
// When neither file exists the home path is still returned — it is the
// write target a first-run setup should persist to, so bare `sentra` from
// any directory lands on the wizard once and the dashboard forever after.
// If the home directory cannot be determined, fall back to the
// cwd-relative name (the pre-discovery behavior) rather than failing.
//
// DiscoverPath only names the path; it never reads or writes the file.
// The "sentra.yaml" literal mirrors internal/cli's configFileName — the
// canonical file name is part of the surface contract (AGENTS.md).
func DiscoverPath() string {
	if info, err := os.Stat("sentra.yaml"); err == nil && info.Mode().IsRegular() {
		return "sentra.yaml"
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "sentra.yaml"
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sentra", "sentra.yaml")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/config/ -run TestDiscoverPath -v`
Expected: PASS (all five cases)

- [ ] **Step 5: Commit**

```bash
git add internal/config/discover.go internal/config/discover_test.go
git commit -m "feat(config): DiscoverPath resolves cwd sentra.yaml, else XDG home fallback"
```

---

### Task 2: `config.Write` creates missing parent directories

**Files:**
- Modify: `internal/config/render.go:172-177` (func `Write`)
- Test: `internal/config/render_test.go`, `internal/setup/engine_config_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `config.Write(path string, cfg *Config) error` now succeeds when `filepath.Dir(path)` does not exist, creating it 0o700. `setup.Engine.WriteDraft` inherits this for free (it calls `config.Write`).

- [ ] **Step 1: Write the failing tests**

Append to `internal/config/render_test.go`:

```go
// The user-level fallback (~/.config/sentra/sentra.yaml) may be the first
// thing ever written there; Write must create the directory rather than
// fail on a fresh machine, and must create it private.
func TestWrite_CreatesMissingParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sentra", "nested", "sentra.yaml")
	cfg := Defaults()
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write into missing dir: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat created dir: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Errorf("config dir perms = %o, want 0700", got)
	}
	if _, err := Load(path); err != nil {
		t.Errorf("round-trip Load: %v", err)
	}
}
```

(Add `"os"` / `"path/filepath"` to the test file's imports if not present.)

Append to `internal/setup/engine_config_test.go`:

```go
// A first run from a random directory targets ~/.config/sentra/, which may
// not exist yet — the resumability draft must not be the thing that fails.
func TestWriteDraft_CreatesMissingParentDirs(t *testing.T) {
	e := NewEngine(nil)
	cfgPath := filepath.Join(t.TempDir(), "sentra", "sentra.yaml")
	cfg := config.Defaults()
	if err := e.WriteDraft(cfgPath, &cfg); err != nil {
		t.Fatalf("WriteDraft into missing dir: %v", err)
	}
	if _, err := os.Stat(e.DraftPath(cfgPath)); err != nil {
		t.Errorf("draft not written: %v", err)
	}
}
```

(Match that file's existing imports; add `"os"`, `"path/filepath"`, and the `config` import if missing.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run TestWrite_CreatesMissingParentDirs -v && go test ./internal/setup/ -run TestWriteDraft_CreatesMissingParentDirs -v`
Expected: both FAIL with a path error like "no such file or directory"

- [ ] **Step 3: Write minimal implementation**

In `internal/config/render.go`, change `Write` (add `"path/filepath"` to imports):

```go
func Write(path string, cfg *Config) error {
	// The user-level fallback path (~/.config/sentra/sentra.yaml) may be
	// the first thing ever written there; create the directory rather
	// than failing on a fresh machine. 0o700 matches the file's 0o600.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, Render(cfg), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
```

Also extend `Write`'s doc comment (above the existing rationale): "Write creates path's parent directory (0o700) when missing — the user-level config location may not exist on a fresh machine."

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/config/ ./internal/setup/ -run 'TestWrite_CreatesMissingParentDirs|TestWriteDraft_CreatesMissingParentDirs' -v`
Expected: PASS. Then run both full packages: `go test -race ./internal/config/ ./internal/setup/` — no regressions.

- [ ] **Step 5: Commit**

```bash
git add internal/config/render.go internal/config/render_test.go internal/setup/engine_config_test.go
git commit -m "feat(config): Write creates missing parent dirs for the home config path"
```

---

### Task 3: `resolveConfigPath` in internal/cli

**Files:**
- Create: `internal/cli/config_path.go`
- Test: `internal/cli/config_path_test.go`

**Interfaces:**
- Consumes: `config.DiscoverPath()` (Task 1); the existing `configFileName` const (`internal/cli/init.go:45`).
- Produces: `func resolveConfigPath(cmd *cobra.Command, cfgPath string) string` — Tasks 4–6 call it as the first statement of run bodies.

- [ ] **Step 1: Write the failing test**

Create `internal/cli/config_path_test.go`:

```go
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// The rule under test: an explicit flag wins; a programmatic non-default
// value wins; only the untouched default falls through to discovery.
func TestResolveConfigPath(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)
	home := filepath.Join(xdg, "sentra", "sentra.yaml")

	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "x"}
		var cfgPath string
		cmd.Flags().StringVar(&cfgPath, "config", configFileName, "")
		return cmd
	}

	t.Run("default resolves via discovery", func(t *testing.T) {
		if got := resolveConfigPath(newCmd(), configFileName); got != home {
			t.Errorf("got %q, want %q", got, home)
		}
	})

	t.Run("explicit flag bypasses discovery even at the default value", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("config", configFileName); err != nil {
			t.Fatal(err)
		}
		if got := resolveConfigPath(cmd, configFileName); got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})

	t.Run("programmatic non-default value is left alone", func(t *testing.T) {
		cmd := &cobra.Command{Use: "local"} // no --config flag registered
		if got := resolveConfigPath(cmd, ".sentra-local.yaml"); got != ".sentra-local.yaml" {
			t.Errorf("got %q, want .sentra-local.yaml", got)
		}
	})

	t.Run("cwd config wins over home", func(t *testing.T) {
		chDir(t, t.TempDir())
		if err := os.WriteFile("sentra.yaml", []byte("repo:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveConfigPath(newCmd(), configFileName); got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestResolveConfigPath -v`
Expected: FAIL to compile — "undefined: resolveConfigPath"

- [ ] **Step 3: Write minimal implementation**

Create `internal/cli/config_path.go`:

```go
package cli

import (
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
)

// resolveConfigPath applies config discovery to a --config value at run
// time. An explicitly passed flag always wins; so does any programmatic
// non-default value (`sentra local` hands runUI .sentra-local.yaml
// without registering a --config flag). Only the untouched default falls
// through to config.DiscoverPath: ./sentra.yaml when present, else the
// user-level ~/.config/sentra/sentra.yaml.
//
// It must run at RunE time, not wiring time: Flags().Changed is only
// meaningful after cobra parses argv, and tests chDir after building
// commands. Root's PersistentPreRunE is spoken for (slog setup in
// cmd/sentra), and a per-command PersistentPreRunE would shadow it — so
// this is a plain call at the top of each run body, not a cobra hook.
func resolveConfigPath(cmd *cobra.Command, cfgPath string) string {
	if f := cmd.Flags().Lookup("config"); f != nil && f.Changed {
		return cfgPath
	}
	if cfgPath != configFileName {
		return cfgPath
	}
	return config.DiscoverPath()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/cli/ -run TestResolveConfigPath -v`
Expected: PASS (all four subtests)

- [ ] **Step 5: Commit**

```bash
git add internal/cli/config_path.go internal/cli/config_path_test.go
git commit -m "feat(cli): resolveConfigPath applies discovery to unchanged --config defaults"
```

---

### Task 4: Bare `sentra` / `sentra ui` first-run targets the home config

**Files:**
- Modify: `internal/cli/ui.go:126-145` (func `runUI`)
- Test: `internal/cli/ui_test.go`

**Interfaces:**
- Consumes: `resolveConfigPath` (Task 3); existing test helpers `uiFixture` (`ui_test.go:28`) and `chDir` (`init_test.go:40`).
- Produces: `runUI` resolves `cfgPath` before `probeLaunchState`, so `tui.Deps.ConfigPath` (and the wizard's write target) is the discovered path. No signature changes.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/ui_test.go` (imports already cover everything used):

```go
// The headline behavior: `sentra` from a directory with no sentra.yaml
// routes to the first-run wizard TARGETING the user-level config path, so
// completing setup once makes bare `sentra` work from anywhere after.
func TestUI_FirstRunFromAnywhereTargetsHomeConfig(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	d := captured.Deps()
	want := filepath.Join(xdg, "sentra", "sentra.yaml")
	if d.ConfigPath != want {
		t.Errorf("ConfigPath = %q, want discovered home path %q", d.ConfigPath, want)
	}
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want \"setup\" (first run)", d.InitialView)
	}
}
```

Note: `tui.Deps.ConfigPath` is absolutized by `runUI` via `filepath.Abs`; `want` is already absolute because `t.TempDir` returns absolute paths, so exact string equality is correct. If the field name differs, check `captured.Deps()` usage at `ui_test.go:188-194` and match it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestUI_FirstRunFromAnywhereTargetsHomeConfig -v`
Expected: FAIL — `ConfigPath` is the absolutized cwd `sentra.yaml` (something like `/tmp/TestUI.../sentra.yaml`), not the XDG path.

- [ ] **Step 3: Write minimal implementation**

In `runUI` (`internal/cli/ui.go`), directly after the existing `if cfgPath == "" { cfgPath = configFileName }` block, insert:

```go
	// Discovery: the untouched default falls back to the user-level
	// config when the cwd has none, so bare `sentra` opens the
	// configured repo from anywhere. Explicit --config (and `sentra
	// local`'s programmatic path) pass through untouched.
	cfgPath = resolveConfigPath(cmd, cfgPath)
```

Ordering note: resolution goes BEFORE `probeLaunchState` and before the `filepath.Abs` block, so routing, the wizard's write target, and the draft lookup all agree on one path. Do not reorder against the `""` normalization above it — `--config ""` keeps meaning the literal cwd default (the flag is Changed, so discovery correctly skips it).

- [ ] **Step 4: Run tests to verify they pass (and nothing regressed)**

Run: `go test -race ./internal/cli/ -run 'TestUI|TestSetup|TestLocal' -v`
Expected: new test PASSES; every existing UI/setup/local test still passes (they all `chDir` into a temp dir first, and those with a cwd config keep resolving to it — that is the regression proof for "cwd still wins").

- [ ] **Step 5: Commit**

```bash
git add internal/cli/ui.go internal/cli/ui_test.go
git commit -m "feat(cli): bare sentra / sentra ui discover the home config from anywhere"
```

---

### Task 5: Sweep discovery through every CLI run body

**Files:**
- Modify: `internal/cli/backup.go`, `restore.go`, `snapshots.go`, `ls.go`, `stats.go`, `check.go`, `diff.go`, `pin.go`, `recovery_kit.go`, `prune.go`, `agent.go`, `agent_ignore.go`, `policy.go`, `passwd.go`, `sync.go`, `doctor.go`, `schedule.go` (run bodies + flag help strings)
- Test: `internal/cli/doctor_test.go`, `internal/cli/schedule_test.go`

**Interfaces:**
- Consumes: `resolveConfigPath` (Task 3), `config.DiscoverPath` (Task 1), test helpers `chDir` (`init_test.go:40`) and `config.Write`.
- Produces: every repo-facing command resolves through discovery. No exported signatures change.

- [ ] **Step 1: Write the failing tests**

Append to `internal/cli/doctor_test.go`:

```go
// End-to-end discovery through a real command: with an empty cwd and a
// config under $XDG_CONFIG_HOME, doctor must load THAT file. The invalid
// bucket proves which file was read (a missed discovery fails earlier,
// with "Config load failed").
func TestDoctor_DiscoversHomeConfigFromAnywhere(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "Bad_Bucket"
	if err := config.Write(filepath.Join(xdg, "sentra", "sentra.yaml"), &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	out := &bytes.Buffer{}
	deps := DoctorDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error {
			t.Fatal("AWS identity check should not run for invalid bucket")
			return nil
		},
		Stdout: out,
	}
	cmd := NewDoctor(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--skip-repo"})
	err := cmd.Execute()
	if !errors.Is(err, ErrDoctorFailed) {
		t.Fatalf("error: got %v, want ErrDoctorFailed (invalid bucket from home config)", err)
	}
	if !strings.Contains(out.String(), "lowercase") {
		t.Fatalf("doctor did not validate the bucket from the discovered config:\n%s", out.String())
	}
}
```

Append to `internal/cli/schedule_test.go`:

```go
// The cron/launchd artifact must embed the discovered ABSOLUTE config
// path: cron's cwd is arbitrary, so a relative or undiscovered path would
// break every scheduled run.
func TestScheduleInstall_DiscoversHomeConfigAndEmbedsAbsolutePath(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)

	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"~/Documents"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfgPath := filepath.Join(xdg, "sentra", "sentra.yaml")
	if err := config.Write(cfgPath, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}

	home := filepath.Join(t.TempDir(), "home")
	out := &bytes.Buffer{}
	cmd := NewSchedule(ScheduleDeps{
		OS:         "darwin",
		HomeDir:    func() (string, error) { return home, nil },
		Executable: func() (string, error) { return "/usr/local/bin/sentra", nil },
		Stdout:     out,
	})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"install", "home"}) // no --config: discovery must find the XDG path
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(home, "Library", "LaunchAgents", "com.sentra.home.plist"))
	if err != nil {
		t.Fatalf("read plist: %v", err)
	}
	if !strings.Contains(string(raw), cfgPath) {
		t.Errorf("launch agent does not embed the discovered absolute config path %q:\n%s", cfgPath, raw)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/cli/ -run 'TestDoctor_DiscoversHomeConfigFromAnywhere|TestScheduleInstall_DiscoversHomeConfigAndEmbedsAbsolutePath' -v`
Expected: both FAIL — "Config load failed" / "load config" errors, because the commands still look at the empty cwd.

- [ ] **Step 3: Wire resolution into every run body**

Insert exactly one resolution statement as the FIRST statement of each function below (after `cmd.SilenceUsage = true` where that is the current first line — keep it first among cfgPath uses):

| File | Function | Exact statement to insert |
|---|---|---|
| `backup.go` | `runBackup` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `backup.go` | `runBackupPlan` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `backup.go` | `runBackupApply` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `restore.go` | `runRestore` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `snapshots.go` | `runSnapshots` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `ls.go` | `runLs` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `stats.go` | `runStats` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `check.go` | `runCheck` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `diff.go` | `runDiff` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `pin.go` | `runPin` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `recovery_kit.go` | `runRecoveryKit` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `doctor.go` | `runDoctor` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `agent_ignore.go` | `runAgentAdviseIgnore` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `schedule.go` | `runScheduleInstall` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `schedule.go` | `runScheduleStatus` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `schedule.go` | `runScheduleUninstall` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `prune.go` | `runPrune` | `flags.cfgPath = resolveConfigPath(cmd, flags.cfgPath)` |
| `agent.go` | `runAgentScan` | `flags.cfgPath = resolveConfigPath(cmd, flags.cfgPath)` |
| `policy.go` | `runPolicyList` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `policy.go` | `runPolicyShow` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `policy.go` | `runPolicyRemove` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `policy.go` | `runPolicy` | `cfgPath = resolveConfigPath(cmd, cfgPath)` |
| `policy.go` | `runPolicyAdd` | `*flags.configPath = resolveConfigPath(cmd, *flags.configPath)` |

Special cases (hard-coded paths, not flag defaults):

1. **`passwd.go` `runPasswd` (line ~142):** it registers `--config` into `flags.configPath` (line 128) but then IGNORES it, loading `config.Load(configFileName)` hard-coded — a pre-existing bug. Replace:

```go
	cfg, err := config.Load(configFileName)
	if err != nil {
		return fmt.Errorf("load %s: %w", configFileName, err)
	}
```

with:

```go
	// Honor --config (previously ignored here) and apply discovery.
	cfgPath := resolveConfigPath(cmd, flags.configPath)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load %s: %w", cfgPath, err)
	}
```

Mention the flag-ignored fix in the commit message.

2. **`passwd.go` `runPasswordForget` (line ~238):** after the existing `if cfgPath == "" { cfgPath = configFileName }` normalization, insert `cfgPath = resolveConfigPath(cmd, cfgPath)`.

3. **`sync.go` `runSync` (line ~112):** the SOURCE config is hard-coded to cwd. Replace `srcCfg, err := config.Load(configFileName)` with `srcCfg, err := config.Load(config.DiscoverPath())` (there is no `--config` flag on sync to consult). Update the two comments that say "from cwd" (lines ~54 and ~110) to say "via config discovery (cwd, else the user-level config)". `--dst-config` stays explicit-only.

- [ ] **Step 4: Verification sweep — no call site missed**

Run: `grep -n "openRepoForConfig(\|config\.Load(\|config\.Update(" internal/cli/*.go | grep -v _test`

For EVERY hit, the path argument must be one of:
- a variable resolved through `resolveConfigPath` earlier in the same function,
- an explicitly-flagged path with no discovery semantics (`--dst-config`, plan/out files),
- `init.go` (documented cwd-only exception) or `local.go` (explicit `.sentra-local.yaml`),
- `repo_open.go` / helpers that only receive already-resolved paths from callers.

Any other hit is a missed site — wire it with the same one-line pattern (this catches bodies not in the table, e.g. anything in `agent_apply.go`). Also run `grep -n "configFileName" internal/cli/*.go | grep -v _test` — remaining uses must be only: the const definition (`init.go:45`), flag-default registrations, `init.go`'s own logic, the two normalizations noted above, and `resolveConfigPath` itself.

- [ ] **Step 5: Update the flag help text**

Run, then `gofmt -l internal/cli` to confirm clean:

```bash
perl -pi -e 's/path to sentra\.yaml \(defaults to \.\/sentra\.yaml\)/path to sentra.yaml (default: .\/sentra.yaml, else ~\/.config\/sentra\/sentra.yaml)/g' internal/cli/*.go
```

Spot-check with `grep -rn "else ~/.config" internal/cli/*.go | grep -v _test` — expect one hit per registration site; `init.go` must NOT be among them (its help text stays cwd-only; if the perl sweep touched `init.go`, revert that hunk).

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test -race ./internal/cli/ -run 'TestDoctor_DiscoversHomeConfigFromAnywhere|TestScheduleInstall_DiscoversHomeConfigAndEmbedsAbsolutePath' -v`
Expected: PASS. Then the whole package: `go test -race ./internal/cli/` — every existing test still passes (they all create cwd configs, which keep winning).

- [ ] **Step 7: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): resolve --config through discovery in every run body

Also fixes passwd ignoring its own --config flag, and routes sync's
source config through discovery instead of hard-coded cwd."
```

Before staging, run `git status internal/cli/` and confirm only files this task touched are dirty.

---

### Task 6: Doctor prints the resolved config path

**Files:**
- Modify: `internal/cli/doctor.go:74` (the `printSetupOK(out, "Config loaded")` line)
- Test: `internal/cli/doctor_test.go`

**Interfaces:**
- Consumes: Task 5's resolved `cfgPath` in `runDoctor`.
- Produces: output line `Config loaded (<abs path>)` — the first-class answer to "which config am I on?". Keeps the `Config loaded` prefix so existing `strings.Contains` assertions hold.

- [ ] **Step 1: Write the failing test**

Append to `internal/cli/doctor_test.go`, inside `TestDoctor_DiscoversHomeConfigFromAnywhere` (from Task 5), after the existing assertions:

```go
	if !strings.Contains(out.String(), filepath.Join(xdg, "sentra", "sentra.yaml")) {
		t.Errorf("doctor should print the resolved config path:\n%s", out.String())
	}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestDoctor_DiscoversHomeConfigFromAnywhere -v`
Expected: FAIL on the new assertion — output says only "Config loaded".

- [ ] **Step 3: Write minimal implementation**

In `runDoctor` (`internal/cli/doctor.go`), replace `printSetupOK(out, "Config loaded")` with:

```go
	// Two possible config locations exist since discovery; naming the
	// one actually loaded is doctor's answer to "which config am I on?".
	absCfg := cfgPath
	if p, err := filepath.Abs(cfgPath); err == nil {
		absCfg = p
	}
	printSetupOK(out, fmt.Sprintf("Config loaded (%s)", absCfg))
```

(Add `"path/filepath"` to doctor.go's imports if missing.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test -race ./internal/cli/ -run TestDoctor -v`
Expected: all doctor tests PASS (existing ones assert `Contains "Config loaded"` at most, which still matches).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/doctor.go internal/cli/doctor_test.go
git commit -m "feat(doctor): print the resolved config path"
```

---

### Task 7: Documentation + full gate

**Files:**
- Modify: `AGENTS.md`, `CLAUDE.md`, `docs/QUICKSTART.md`, `README.md`

**Interfaces:**
- Consumes: everything above, finished.
- Produces: the documented surface contract.

- [ ] **Step 1: AGENTS.md — new "Config resolution" section**

Add near the surface-contract material (place it beside where `--config` / per-command behavior is described):

```markdown
## Config resolution

Every repo-facing command resolves its config path the same way:

1. An explicit `--config <path>` is used verbatim — no discovery.
2. Otherwise `./sentra.yaml`, when it exists as a regular file.
3. Otherwise `$XDG_CONFIG_HOME/sentra/sentra.yaml`, with unset/empty
   `XDG_CONFIG_HOME` defaulting to `~/.config` (the gh-CLI convention,
   not `os.UserConfigDir`).

When neither file exists, the home path is still the resolved target: a
first run from any directory lands on the TUI setup wizard, which
persists `~/.config/sentra/sentra.yaml`, so bare `sentra` opens the
configured repo from anywhere afterwards. `config.Write` creates the
missing parent directory (0700).

Exceptions: `sentra init` writes `./sentra.yaml` only (scripting /
recovery surface; never reaches outside cwd). `sentra local` always uses
`.sentra-local.yaml`. `sentra sync` resolves its *source* config through
discovery; its destination comes only from `--dst-config`.

Implementation: `config.DiscoverPath()` (internal/config/discover.go),
applied by `resolveConfigPath` (internal/cli/config_path.go) as the first
statement of every run body — at RunE time, because `Flags().Changed` is
only meaningful after argv parsing. `sentra doctor` prints the resolved
path.
```

- [ ] **Step 2: CLAUDE.md — quick-reference line**

In the "What this is" section, after the paragraph describing the TUI-as-default surface, add:

```markdown
Config discovery: with no `--config`, commands use `./sentra.yaml` when
present, else `$XDG_CONFIG_HOME/sentra/sentra.yaml` (default
`~/.config`); first-run setup writes the home path, so bare `sentra`
works from any directory. `init` (cwd-only) and `local`
(`.sentra-local.yaml`) are the exceptions.
```

- [ ] **Step 3: README / QUICKSTART**

In `docs/QUICKSTART.md` (and the README section that introduces `sentra.yaml`), add one short paragraph where the config file is first mentioned:

```markdown
Sentra looks for `sentra.yaml` in the current directory first, then falls
back to `~/.config/sentra/sentra.yaml` (honoring `XDG_CONFIG_HOME`).
First-run setup writes the home location, so after setting up once you
can run `sentra` from any directory. Pass `--config` to use a specific
file.
```

Adjust surrounding prose if it asserts "sentra.yaml in the current directory" as the only behavior.

- [ ] **Step 4: Full gate**

```bash
GOFLAGS=-timeout=40m just check
go mod tidy -diff
git diff --check
```

Expected: all clean. (The 40m timeout is deliberate — `internal/cli` can exceed Go's default 600s under contention.)

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md CLAUDE.md docs/QUICKSTART.md README.md
git commit -m "docs: config discovery (cwd first, XDG home fallback)"
```

- [ ] **Step 6: Gate each commit in isolation** (the tree may hold an unrelated leftover `sentra.yaml`)

For the series' final ref:

```bash
git worktree add -q --detach /tmp/chk HEAD && (cd /tmp/chk && go build ./... && go vet ./...)
git worktree remove --force /tmp/chk
```

Expected: builds clean.
