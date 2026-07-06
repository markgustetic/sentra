# TUI Rewrite Phase 2c: Final 7 Flows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the final seven CLI operations — **doctor, recovery-kit, policies, schedule, sync, password, and agent-apply** — into the Sentra TUI, reaching full CLI/TUI parity, backed by three package extractions and one config helper that break the `cli → tui` import cycle.

**Architecture:** The TUI (`internal/tui`) is a Bubbletea Elm-architecture app whose root `App` owns a sidebar, command palette, status bar, a modal stack, and a slice of registered views. `internal/cli` imports `internal/tui` (via `runUI` in `internal/cli/ui.go`), so **`internal/tui` must never import `internal/cli`** — that is the load-bearing constraint of this whole phase. Every piece of logic a new view needs that today lives unexported in `internal/cli` is therefore either (a) called directly from a lower package it already can reach (`internal/repo`, `internal/policy`, `internal/agent/action`), or (b) extracted into a new lower package. This plan adds four `tui.Deps` fields (all threaded mechanically from the existing `UIDeps`/`RepoDeps` at the `runUI` construction site) and four extractions: `config.Render`/`config.Write` into the already-cycle-free `internal/config`, a new `internal/recoverykit`, a new `internal/scheduler`, and a new `internal/diag`. Read-only flows (doctor, recovery-kit, schedule) use the Phase 2b spinner pattern (plain `tea.Cmd` + `bubbles/spinner`, no op guard); repo-mutating flows (sync, password, policy-run, agent-apply) use the Phase 2a op protocol (`startOpMsg`/`opResultMsg`/`opReporter`, one-op-at-a-time, `TypedConfirmModal` for the destructive ones).

**Tech Stack:** Go 1.25; Bubbletea v1.3.10, Bubbles v1.0.0 (`spinner`, `table`, `textinput`, `viewport`, `progress`, `key`), Lipgloss v1.1.0, Huh v1.0.0; existing `internal/repo`, `internal/config`, `internal/policy`, `internal/agent/action`, `internal/crypto`, `internal/blobstore`.

---

## Resolved decisions (from Phase 2c brainstorming)

- **One combined plan, agent-apply included** — all seven flows ship on a single `feature/tui-phase2c` branch (not split into sub-plans).
- **Masked passphrase input** via two `bubbles/textinput` fields with `EchoMode = textinput.EchoPassword` — a `huh` password form cannot coexist with a running Bubbletea program (both fight for `os.Stdin`), so the literal "huh form" from the design spec is realized as inline masked textinputs with a `crypto/subtle` compare and zeroize.
- **Doctor AWS diagnostics extracted to a new `internal/diag`** (rather than injected as `Deps` callbacks): the `AWSInspectReport` type is currently stranded in `internal/cli` and must move regardless, and Phase 3's setup wizard will reuse the same inspect code — so `tui` imports `internal/diag` directly, no callback needed.
- **New package names:** `internal/recoverykit`, `internal/scheduler`, `internal/diag`. `config.Render`/`config.Write` live in the existing `internal/config` (which imports no internal package — keep it that way).
- **Config writes** go through `config.Render`/`config.Write` (shared by `cli` and `tui`), not a `Deps` callback.

## Security invariants (must hold in every ported flow)

- No secrets (passphrases, wrapped keys, salts, MAC material, AWS credentials) written to `sentra.yaml`, logs, recovery kits, tests, or fixtures.
- The recovery kit reads **only** non-secret repo/config data (repo ID, timestamps, bucket/prefix/endpoint/region, KDF cost params) — never `Salt`, `WrappedRepoKey`, or MAC.
- The agent/LLM path is untouched: it still sees summaries only, never file contents or secret values; `flag_secret` is notify-only and writes no secret.
- Repo-mutating flows serialize on the single `meta/lock`; GC's live set stays derived from present manifests under that lock.
- Passphrase input is masked, never rendered or logged, and zeroized after rotation.

## Execution notes — READ BEFORE STARTING

1. **Branch.** Create `feature/tui-phase2c` from `main` before Task 1. Commit per task with the messages shown.
2. **View registration is cumulative — insert, do not replace.** Several tasks add a line to the `views := []viewEntry{...}` slice in `internal/tui/app.go` (and, for the "Operations" flows, an entry to the `categories` map). The full-slice code blocks inside those tasks show *that task's* expected end-state assuming the current view is the only new one; when executing in sequence, **insert your view's `{id: ...}` line into the current slice** rather than pasting the snippet wholesale. The authoritative final slice + categories map is in the **last task ("Register all Phase 2c views + full-branch gate")** — treat that as the source of truth for the end-state and for the sidebar ordering.
3. **Extractions before consumers.** The task order below already places each extraction (config/recoverykit/scheduler/diag) before the view that consumes it. Keep the order.
4. **Gate after every task.** Each task ends green (`go build ./...`, its tests, `gofmt -l cmd internal`, `go vet ./...`). Some units include an intermediate package gate; the final task runs the full CI-equivalent gate (`go test -race ./...`, the FastCDC module tests, `golangci-lint run`, `go mod tidy -diff`, `git diff --check`).
5. **`cat`/`tail`/`head` are aliased to `bat`** in this environment — use `command tail -n N` (or your file-reader tool) when a step pipes long output.

## File structure (created / modified)

**New packages**
- `internal/config/render.go` — `Render(cfg *Config) []byte`, `Write(path string, cfg *Config) error` (+ moved YAML helpers and `defaultAgentProvider`/`defaultAgentModel` consts).
- `internal/recoverykit/recoverykit.go` — `Kit`, `Build`, `RenderMarkdown`, `MarshalJSON`.
- `internal/scheduler/scheduler.go` — pure render/paths/install/status/uninstall (no cobra).
- `internal/diag/diag.go` — `AWSReport`, `CheckSDKIdentity`, `Inspect`, `ValidateBucketName`.

**New TUI views**
- `internal/tui/doctor.go`, `internal/tui/recoverykit.go`, `internal/tui/policies.go`, `internal/tui/schedule.go`, `internal/tui/sync.go`, `internal/tui/password.go` (+ their `_test.go`); `internal/tui/agent.go` extended in place.

**Modified**
- `internal/tui/app.go` — 4 new `Deps` fields; final views-slice + categories registration.
- `internal/cli/ui.go`, `cmd/sentra/commands.go` — thread the 4 new Deps fields.
- `internal/cli/init.go`, `internal/cli/policy.go` — call `config.Render`/`config.Write`.
- `internal/cli/recovery_kit.go`, `internal/cli/schedule.go`, `internal/cli/doctor.go`, `internal/cli/setup_awss3*.go`, `internal/cli/setup_summary.go` — thin wrappers delegating to the new packages.

---


## Part 1 — Deps threading (shared prerequisite)

**Published API:** No extraction package in this unit. This unit only pins the 4 new `tui.Deps` fields (all in `internal/tui/app.go`) that every downstream unit references by these exact names/types:

```go
ConfigPath            string                                                              // absolute sentra.yaml path
NewStore              func(ctx context.Context, cfg *config.Config) (blobstore.Store, error) // build a store from any config (sync dest)
Actions               *action.Registry                                                     // internal/agent/action registry (agent-apply)
SaveKeyringPassphrase func(cfg *config.Config, pass []byte) error                          // re-save rotated pass to OS keyring; may be nil
```

---

### Task 1: Add the 4 new fields to `tui.Deps` and prove they survive `NewApp`

**Files:**
- Modify: `internal/tui/app.go:17-30` (imports), `internal/tui/app.go:40-69` (Deps struct)
- Test: `internal/tui/app_test.go` (new test function appended)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`. It constructs `Deps` with the four new fields populated (stub func values + a path + a real registry), calls `NewApp`, and asserts `App.deps` retains each one — following the existing `TestApp_DepsCarryConfig` pattern at `app_test.go:19-26` that reads `app.deps.<Field>` directly.

```go
// TestApp_DepsCarryNewFields: Unit-1 plumbing. Deps must carry the four
// action/store/config-path/keyring fields through NewApp so the ported
// operation flows (sync, agent-apply, password, setup) can reach them.
// These are call-time function values and plain data — never resolved
// secrets — so a stub that records its call is a faithful test double.
func TestApp_DepsCarryNewFields(t *testing.T) {
	var newStoreCalled, saveKeyringCalled bool

	newStore := func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
		newStoreCalled = true
		return blobstore.NewMemory(), nil
	}
	saveKeyring := func(_ *config.Config, _ []byte) error {
		saveKeyringCalled = true
		return nil
	}
	reg := action.NewDefaultRegistry()

	app := NewApp(Deps{
		ConfigPath:            "/abs/path/sentra.yaml",
		NewStore:              newStore,
		Actions:               reg,
		SaveKeyringPassphrase: saveKeyring,
	})

	if app.deps.ConfigPath != "/abs/path/sentra.yaml" {
		t.Errorf("Deps.ConfigPath not carried: got %q", app.deps.ConfigPath)
	}
	if app.deps.Actions != reg {
		t.Error("Deps.Actions not carried through NewApp")
	}
	if app.deps.NewStore == nil {
		t.Fatal("Deps.NewStore not carried through NewApp")
	}
	if app.deps.SaveKeyringPassphrase == nil {
		t.Fatal("Deps.SaveKeyringPassphrase not carried through NewApp")
	}

	// Prove the carried func values are the ones we passed (identity via
	// side effect): invoking them flips the sentinels.
	if _, err := app.deps.NewStore(context.Background(), nil); err != nil {
		t.Fatalf("carried NewStore returned error: %v", err)
	}
	if err := app.deps.SaveKeyringPassphrase(nil, nil); err != nil {
		t.Fatalf("carried SaveKeyringPassphrase returned error: %v", err)
	}
	if !newStoreCalled || !saveKeyringCalled {
		t.Error("carried func values are not the ones passed to Deps")
	}
}
```

This test needs two new imports in `app_test.go` — add `"github.com/markgustetic/sentra/internal/agent/action"` and `"github.com/markgustetic/sentra/internal/blobstore"` to the import block at `app_test.go:3-15` (which currently imports `context`, `config`, `agent`, etc. but neither of these).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestApp_DepsCarryNewFields -count=1`
Expected: FAIL — compile error `app.deps.ConfigPath undefined (type Deps has no field or method ConfigPath)` (and likewise for `Actions`, `NewStore`, `SaveKeyringPassphrase`).

- [ ] **Step 3: Write the minimal implementation**

First extend the import block in `internal/tui/app.go:17-30` to bring in `blobstore` and `action` (needed for the new field types):

```go
import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)
```

(No import cycle: `internal/agent/action` imports only `internal/repo`; `internal/blobstore` is a leaf. Neither imports `internal/tui`.)

Then add the four fields to the `Deps` struct. Insert them after the `Ctx` field, before the closing brace at `internal/tui/app.go:68-69`:

```go
	// Ctx is the parent context for all TUI-driven I/O. NewApp
	// derives a cancellable child from this and threads the child
	// back into every sub-view's Deps via DepsForChildren — so when
	// the user presses 'q' the App's cleanup cancels every in-flight
	// blobstore call. Nil falls back to context.Background() so tests
	// using `Deps{}` keep working.
	Ctx context.Context

	// ConfigPath is the absolute path to the sentra.yaml the TUI was
	// launched against. Flows that rewrite config (setup, policy edits,
	// schedule install, recovery kit) need the on-disk location to write
	// back to; it is plain data, never a resolved secret. Empty when the
	// TUI runs against an in-memory/unconfigured repo (tests).
	ConfigPath string

	// NewStore builds a blobstore.Store from an arbitrary config. The
	// sync flow uses it to open the *destination* store, which differs
	// from the repo's own source store, so we take a factory rather than
	// a live handle. It is a call-time function value — invoked only when
	// a flow runs — and resolves no secrets itself. May be nil in tests.
	NewStore func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)

	// Actions is the agent action registry (prune_snapshot, add_to_ignore,
	// flag_secret, none). The agent-apply flow looks up and runs a handler
	// through it after the user confirms a recommendation. Read-only by
	// default: nothing here executes without an explicit confirm. May be
	// nil (agent-apply then reports "no action registry configured").
	Actions *action.Registry

	// SaveKeyringPassphrase re-saves a rotated passphrase to the OS
	// keyring after the password flow changes it, so the user isn't
	// prompted on the next open. It is a call-time function value that
	// receives the new passphrase bytes only at rotation time — the bytes
	// are never retained in Deps. May be nil when no keyring is wired
	// (the password flow then skips the keyring update).
	SaveKeyringPassphrase func(cfg *config.Config, pass []byte) error
}
```

No change to `NewApp`: it already copies `deps` by value into `App.deps` (`app.go:184`) and passes the same `deps` to every child view constructor (`app.go:156-163`), so the new fields propagate to children automatically.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestApp_DepsCarryNewFields -count=1`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): add ConfigPath/NewStore/Actions/SaveKeyringPassphrase to Deps

Unit 1 of the TUI Phase 2c port: pin the four new Deps fields the
sync, agent-apply, password, and setup flows will consume. Behavior-
preserving plumbing; no view reads them yet.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Thread the 4 new fields through `runUI` and wire `UIDeps` in `cmd/sentra`

**Files:**
- Modify: `internal/cli/ui.go:1-16` (imports), `internal/cli/ui.go:22-38` (`UIDeps` struct), `internal/cli/ui.go:70-111` (`runUI`)
- Modify: `cmd/sentra/commands.go:125-133` (`uiDeps` construction)
- Test: `internal/cli/ui_test.go` (new test function; create file if absent)

Wiring facts verified in the real source:
- `cfgPath` is already a `runUI` parameter (`ui.go:70`), sourced from the `--config` flag (`ui.go:60`, default `configFileName` = `"sentra.yaml"`, `init.go:47`). It is relative, so `runUI` must absolutize it for `Deps.ConfigPath`.
- `RepoDeps.NewStore` (`func(ctx, cfg) (blobstore.Store, error)`, `repo_open.go:21`) is already in scope inside `runUI` via `deps.RepoDeps.NewStore` — it is `newS3Store` in production (`commands.go:127`, `store.go:14`).
- `action.NewDefaultRegistry()` is the same registry `AgentDeps` builds (`commands.go:121`); `commands.go` already imports `internal/agent/action` (`commands.go:8`).
- `saveRepoPassphraseToKeyring` (`passphrase.go:118`, signature `func(cfg *config.Config, passphrase []byte) error`) is already wired to `PasswdDeps.SavePassphrase` (`commands.go:104`) and matches `Deps.SaveKeyringPassphrase` exactly.
- `UIDeps` currently has **no** `Actions` or `SavePassphrase` field (`ui.go:22-38`), so both must be added to `UIDeps` and populated in `commands.go`.

- [ ] **Step 1: Write the failing test**

Create/append `internal/cli/ui_test.go`. It builds `UIDeps` with a memory-store factory, a stub keyring saver, and a registry, injects a `Run` hook that captures the constructed `tui.App`, runs the `ui` command against a real in-memory repo, and asserts the captured `App.Deps()` carries all four fields — including that `ConfigPath` was absolutized. Since `App.deps` is unexported and this is `package cli` (cannot read it directly), the task adds a tiny read-only accessor on `tui.App`.

First, the accessor — add to `internal/tui/app.go` (also part of this task's implementation, shown here so the test compiles):

```go
// Deps returns the App's dependency set. Exported for cross-package
// wiring tests (internal/cli) that need to assert runUI threaded the
// right values in; production code inside the tui package reads the
// unexported field directly.
func (m App) Deps() Deps { return m.deps }
```

Now the test:

```go
package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/tui"
	"github.com/spf13/cobra"
)

// TestRunUI_ThreadsNewDepsFields proves runUI populates the four Unit-1
// Deps fields from UIDeps: the store factory, the action registry, the
// keyring saver, and an absolute ConfigPath. No secret is threaded — the
// func values are call-time hooks and ConfigPath is plain data.
func TestRunUI_ThreadsNewDepsFields(t *testing.T) {
	// A shared memory store so `ui` can open a real repo through the deps.
	mem := blobstore.NewMemory()
	if _, err := repo.Init(context.Background(), mem, []byte("ui-test-pass")); err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	newStore := func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
		return mem, nil
	}
	var saveKeyringCalled bool
	saveKeyring := func(_ *config.Config, _ []byte) error {
		saveKeyringCalled = true
		return nil
	}

	// Write a minimal sentra.yaml the command can load. A relative path is
	// passed via --config so we can assert runUI absolutizes it.
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	if err := config.Write(cfgPath, ptrDefaults()); err != nil {
		t.Fatalf("write config: %v", err)
	}

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore:   newStore,
			Passphrase: func() ([]byte, error) { return []byte("ui-test-pass"), nil },
			Stdout:     newDiscardWriter(),
		},
		Actions:        newTestRegistry(),
		SavePassphrase: saveKeyring,
		Run: func(app tui.App) error {
			captured = app
			return nil
		},
	}

	cmd := NewUI(deps)
	cmd.SetContext(context.Background())
	cmd.SetArgs([]string{"--config", cfgPath})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("ui command: %v", err)
	}

	d := captured.Deps()
	if !filepath.IsAbs(d.ConfigPath) {
		t.Errorf("Deps.ConfigPath not absolute: %q", d.ConfigPath)
	}
	if d.NewStore == nil {
		t.Error("Deps.NewStore not threaded")
	}
	if d.Actions == nil {
		t.Error("Deps.Actions not threaded")
	}
	if d.SaveKeyringPassphrase == nil {
		t.Fatal("Deps.SaveKeyringPassphrase not threaded")
	}
	if err := d.SaveKeyringPassphrase(nil, nil); err != nil || !saveKeyringCalled {
		t.Error("Deps.SaveKeyringPassphrase is not the func passed via UIDeps")
	}
	_ = cobra.Command{}
}
```

Note on test helpers referenced above: `config.Write` and `config.Defaults` already exist in `internal/config` (config is loaded via `config.Load` in `openRepoForConfig`, `repo_open.go:34`; verify `config.Defaults()` and a `Write` helper — if `config.Write` does not yet exist on `main`, replace the `config.Write(cfgPath, …)` line with an `os.WriteFile(cfgPath, []byte("repo:\n  s3:\n    bucket: test\n"), 0o600)` producing a loadable file, and drop `ptrDefaults`). `newDiscardWriter`, `ptrDefaults`, and `newTestRegistry` are one-liner local helpers — define them at the bottom of `ui_test.go`:

```go
func newDiscardWriter() *strings.Builder { return &strings.Builder{} }
func ptrDefaults() *config.Config        { c := config.Defaults(); return &c }
func newTestRegistry() *action.Registry  { return action.NewDefaultRegistry() }
```

with matching imports `"strings"` and `"github.com/markgustetic/sentra/internal/agent/action"`. If `openRepoForConfig` rejects a bucket-less config (`store.go:16` requires a bucket, but the injected `newStore` here never calls that path, so a bucket-less config loads fine), keep the config minimal; the injected `newStore` bypasses `newS3Store`'s bucket check entirely.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestRunUI_ThreadsNewDepsFields -count=1`
Expected: FAIL — compile error `unknown field 'Actions' in struct literal of type UIDeps` (and `SavePassphrase` likewise), plus `captured.Deps undefined` until the accessor lands.

- [ ] **Step 3: Write the minimal implementation**

**(a)** Add the accessor to `internal/tui/app.go` (shown in Step 1 above) if not already added by the accessor snippet.

**(b)** Add `Actions` and `SavePassphrase` to `UIDeps` in `internal/cli/ui.go`, after the `Run` field at `ui.go:37`:

```go
	// Run is the actual TUI launcher. Production wires it to a
	// closure that constructs and runs a tea.Program; tests inject
	// a stub that captures the constructed App and returns nil.
	Run func(app tui.App) error

	// Actions is the agent action registry the TUI's agent-apply flow
	// executes confirmed recommendations through. Same registry the
	// `agent` command builds. May be nil (agent-apply then reports no
	// registry configured).
	Actions *action.Registry

	// SavePassphrase re-saves a rotated passphrase to the OS keyring
	// after the TUI's password flow changes it. Same hook the `passwd`
	// command uses. May be nil when no keyring is wired.
	SavePassphrase func(cfg *config.Config, passphrase []byte) error
```

Extend the `ui.go` import block (`ui.go:3-16`) with `"path/filepath"` and `"github.com/markgustetic/sentra/internal/agent/action"`:

```go
import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/tui"
)
```

**(c)** Thread the four fields inside `runUI` (`ui.go:94-105`). Absolutize `cfgPath` before building `Deps` (fall back to the raw path if `filepath.Abs` fails — a non-fatal best-effort, since a bad cwd should not block the TUI). Replace the `tui.NewApp(tui.Deps{...})` block:

```go
	// Resolve the config path to an absolute location so config-writing
	// flows (setup, policy, schedule) write back to the file the user
	// actually launched against, regardless of the process cwd. Abs only
	// fails when the cwd is unreadable; fall back to the raw path then.
	absCfgPath := cfgPath
	if p, err := filepath.Abs(cfgPath); err == nil {
		absCfgPath = p
	}

	app := tui.NewApp(tui.Deps{
		Repo:     r,
		Provider: provider,
		RepoName: repoName,
		Config:   cfg,
		// Pass the cobra command's context so:
		//   1. Signals (Ctrl+C wired by cobra) cancel TUI work.
		//   2. The TUI's App.cleanup() can cancel the same context
		//      tree on a 'q' quit, terminating in-flight blobstore
		//      calls instead of letting them drain to per-call timeouts.
		Ctx: cmd.Context(),

		// Unit-1 plumbing: call-time hooks + plain data the ported
		// operation flows consume. None hold resolved secrets.
		ConfigPath:            absCfgPath,
		NewStore:              deps.RepoDeps.NewStore,
		Actions:               deps.Actions,
		SaveKeyringPassphrase: deps.SavePassphrase,
	})
```

**(d)** Populate the two new `UIDeps` fields in `cmd/sentra/commands.go:125-133`. `action` is already imported (`commands.go:8`); `saveRepoPassphraseToKeyring` is already in scope (used at `commands.go:104`):

```go
	uiDeps := cli.UIDeps{
		RepoDeps: cli.RepoDeps{
			NewStore:             newS3Store,
			PassphraseWithConfig: openPassphrase,
			Stdout:               os.Stdout,
		},
		ProviderForConfig: newAgentProvider,
		Actions:           action.NewDefaultRegistry(),
		SavePassphrase:    saveRepoPassphraseToKeyring,
		Run:               cli.DefaultUIRunner,
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestRunUI_ThreadsNewDepsFields -count=1 && go build ./...`
Expected: PASS, and `go build ./...` succeeds (proves `cmd/sentra/commands.go` compiles against the extended `UIDeps`).

- [ ] **Step 5: Commit**

```bash
git add internal/cli/ui.go internal/cli/ui_test.go internal/tui/app.go cmd/sentra/commands.go
git commit -m "feat(cli): thread store/actions/keyring/config-path into tui.Deps

Add Actions and SavePassphrase to UIDeps and populate all four new
tui.Deps fields in runUI: NewStore from RepoDeps, Actions and the
keyring saver from the shared production hooks, and an absolutized
ConfigPath. Wire the two new UIDeps fields in cmd/sentra. Adds a
read-only App.Deps() accessor for the cross-package wiring test.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**Notes for downstream units / reviewers:**
- `NewApp` copies `Deps` by value and hands the same value to every child-view constructor (`internal/tui/app.go:156-163,184`), so a view reads any of the four new fields via its own captured `deps` with no extra threading. Do **not** re-add these fields per view.
- Security invariants held: all four fields are call-time function values or plain data. `SaveKeyringPassphrase` receives passphrase bytes only at rotation time and never stores them in `Deps`; `ConfigPath` is a filesystem path, not a secret; `Actions`/`NewStore` carry no credentials. Nothing here writes secrets to yaml/logs/kits/tests.
- `Deps.SaveKeyringPassphrase` may be nil (spec: "may be nil") — the password flow (a later unit) must nil-check before calling it, exactly as `PasswdDeps.SavePassphrase` is nil-checked at `internal/cli/passwd.go:202`.


## Part 2 — config.Render/Write extraction + Policies flow

**Published API:** `internal/config` (extraction package, still imports NO internal package)

```go
// Render returns the full on-disk sentra.yaml body for cfg. It is the
// single source of truth for the hand-shaped YAML that init, setup,
// policy add/remove, and passwd-forget write. Agent provider/model fall
// back to the documented defaults when unset so a fresh config is complete.
func Render(cfg *Config) []byte

// Write renders cfg and writes it to path with 0o600 perms (never group-
// or world-readable — the file names the bucket/region). Wraps write
// errors with the path.
func Write(path string, cfg *Config) error
```

---

### Task 3: Move config-YAML rendering into internal/config as Render/Write

**Files:**
- Create: `internal/config/render.go`
- Create: `internal/config/render_test.go`
- Modify: `internal/cli/init.go:60-181` (delete moved code, keep `NewInit` and `configFileName`)
- Modify: `internal/cli/policy.go:382-387` (replace `writeRenderedConfig` body with a call to `config.Write`)
- Modify: `internal/cli/setup.go:295`, `internal/cli/setup.go:399`, `internal/cli/passwd.go:269` (call `config.Write`)
- Modify: `internal/cli/init_test.go:178-253` (delete the three `TestRenderConfigYAML_*` tests — they move to the new package)
- Modify: `internal/cli/policy_test.go:17-24` (`writePolicyConfigFile` calls `config.Write`)
- Modify: `internal/cli/doctor_test.go:24,77,108` (three fixture writers call the now-deleted `renderConfigYAML` — repoint to `config.Write`)

> **Same-package caller sweep (required for the Step-4 gate to pass).** Deleting `renderConfigYAML` from `init.go` breaks *every* in-package caller, including test files. Besides `init_test.go`/`policy_test.go` above, `internal/cli/doctor_test.go` writes its `sentra.yaml` fixture via `os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(renderConfigYAML(&cfg)), 0o600)` at lines 24, 77, and 108. Replace each with `config.Write(filepath.Join(dir, "sentra.yaml"), &cfg)` (the file already imports `internal/config` and `path/filepath`; those three calls are its only `os.` uses, so **remove the now-unused `"os"` import**). Before running Step 4, `grep -rn renderConfigYAML internal/cli` must return zero hits.

- [ ] **Step 1: Write the failing test**

Create `internal/config/render_test.go` (carries the three relocated YAML-shape assertions from `internal/cli/init_test.go:178-253`, plus a no-secret assertion, exercising the new public API):

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRender_IncludesPolicies(t *testing.T) {
	cfg := Defaults()
	cfg.Repo.S3.Bucket = "test-bucket"
	cfg.Policies["home"] = PolicyConfig{
		Paths: []string{"~/Documents"},
		Tags:  []string{"home", "daily"},
		Schedule: PolicySchedule{
			Cadence: "daily",
			At:      "03:00",
		},
		AfterBackup: PolicyAfterBackup{
			Check: true,
			Prune: "dry-run",
		},
	}

	body := string(Render(&cfg))
	for _, want := range []string{
		"policies:",
		"  home:",
		"    paths:",
		"      - \"~/Documents\"",
		"    tags:",
		"      - \"home\"",
		"      - \"daily\"",
		"    schedule:",
		"      cadence: \"daily\"",
		"      at: \"03:00\"",
		"    after_backup:",
		"      check: true",
		"      prune: \"dry-run\"",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("rendered config missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hunter2") {
		t.Fatalf("rendered config must not contain passphrase-looking fixture:\n%s", body)
	}
}

func TestRender_PreservesCustomAgent(t *testing.T) {
	cfg := Defaults()
	cfg.Agent.Provider = "openai"
	cfg.Agent.Model = "gpt-4o"

	body := string(Render(&cfg))
	if !strings.Contains(body, "gpt-4o") {
		t.Errorf("rendered config dropped custom agent.model:\n%s", body)
	}
	if !strings.Contains(body, "openai") {
		t.Errorf("rendered config dropped custom agent.provider:\n%s", body)
	}
	if strings.Contains(body, "claude-sonnet-4-6") {
		t.Errorf("rendered config kept the hardcoded default model despite a custom value:\n%s", body)
	}
}

func TestRender_DefaultsAgentWhenUnset(t *testing.T) {
	cfg := Defaults()
	body := string(Render(&cfg))
	if !strings.Contains(body, "anthropic") {
		t.Errorf("expected default provider anthropic:\n%s", body)
	}
	if !strings.Contains(body, "claude-sonnet-4-6") {
		t.Errorf("expected default model claude-sonnet-4-6:\n%s", body)
	}
}

// TestWrite_RoundTripsThroughLoad proves Write+Load is a faithful round
// trip and that the file lands at 0o600 (never group/world readable — it
// names the bucket/region).
func TestWrite_RoundTripsThroughLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["home"] = PolicyConfig{
		Paths:    []string{"/data"},
		Schedule: PolicySchedule{Cadence: "manual"},
	}
	if err := Write(path, &cfg); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := got.Policies["home"]
	if !ok || len(p.Paths) != 1 || p.Paths[0] != "/data" {
		t.Fatalf("round-trip policy mismatch: %+v", got.Policies)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/config/ -run 'TestRender|TestWrite' -count=1`
Expected: FAIL — `./render_test.go: undefined: Render` and `undefined: Write` (the functions don't exist in the config package yet).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/config/render.go`. This is the code from `internal/cli/init.go:60-181` and `internal/cli/policy.go:382-387`, moved verbatim into the config package, with `renderConfigYAML(cfg) string` renamed to `Render(cfg) []byte` and `config.` qualifiers dropped (the types are now local). `Write` is `writeRenderedConfig` renamed:

```go
package config

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// defaultAgentProvider / defaultAgentModel are the documented agent
// defaults written when the resolved config leaves them unset. Defaults()
// deliberately does not seed Provider/Model, so a render must fall back to
// these rather than emit an empty value.
const (
	defaultAgentProvider = "anthropic"
	defaultAgentModel    = "claude-sonnet-4-6"
)

// Render produces the on-disk sentra.yaml body that init, setup, policy
// add/remove, and passwd-forget write. We render from the resolved Config
// (defaults + yaml + env overlay + flags/prompts) so the file faithfully
// reflects what the user actually configured.
//
// The body is hand-shaped rather than yaml.Marshal'd so we can keep the
// inline comments that explain each field — the file is a teaching
// artifact, not just a serialization. No secret material (passphrase,
// wrapped keys, salts) is ever a Config field, so this can never leak one.
func Render(cfg *Config) []byte {
	// Render the agent provider/model from the resolved config so a
	// user's customized values round-trip through any config rewrite.
	// Falling back to the documented defaults only when unset preserves a
	// complete file for fresh configs without clobbering a non-default
	// choice.
	agentProvider := cfg.Agent.Provider
	if agentProvider == "" {
		agentProvider = defaultAgentProvider
	}
	agentModel := cfg.Agent.Model
	if agentModel == "" {
		agentModel = defaultAgentModel
	}
	return []byte(fmt.Sprintf(`# sentra.yaml — repository configuration
# Generated by Sentra. Edit and re-run sentra commands; nothing else
# in this file is required to be present.

repo:
  s3:
    bucket: %q              # required for non-local storage
    prefix: %q              # optional; useful when sharing a bucket
    region: %q              # AWS region; e.g. us-west-2
    profile: %q             # AWS shared-config profile, optional
    endpoint_url: %q        # MinIO/LocalStack support; empty for AWS

agent:
  provider: %q
  model: %q
  max_findings_to_llm: %d

backup:
  ignore_file: %q
  exclude_caches: %t

retention:
  keep_last: %d
  keep_daily: %d
  keep_weekly: %d
  keep_monthly: %d

passphrase:
  use_keyring: %t
%s`,
		cfg.Repo.S3.Bucket,
		cfg.Repo.S3.Prefix,
		cfg.Repo.S3.Region,
		cfg.Repo.S3.Profile,
		cfg.Repo.S3.EndpointURL,
		agentProvider,
		agentModel,
		cfg.Agent.MaxFindingsToLLM,
		cfg.Backup.IgnoreFile,
		cfg.Backup.ExcludeCaches,
		cfg.Retention.KeepLast,
		cfg.Retention.KeepDaily,
		cfg.Retention.KeepWeekly,
		cfg.Retention.KeepMonthly,
		cfg.Passphrase.UseKeyring,
		renderPoliciesYAML(cfg.Policies),
	))
}

func renderPoliciesYAML(policies map[string]PolicyConfig) string {
	if len(policies) == 0 {
		return ""
	}
	names := make([]string, 0, len(policies))
	for name := range policies {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("\npolicies:\n")
	for _, name := range names {
		p := policies[name]
		fmt.Fprintf(&b, "  %s:\n", name)
		writeYAMLStringList(&b, "    paths", p.Paths)
		writeYAMLStringList(&b, "    tags", p.Tags)
		fmt.Fprintln(&b, "    schedule:")
		fmt.Fprintf(&b, "      cadence: %q\n", p.Schedule.Cadence)
		if p.Schedule.At != "" {
			fmt.Fprintf(&b, "      at: %q\n", p.Schedule.At)
		}
		if p.Schedule.Weekday != "" {
			fmt.Fprintf(&b, "      weekday: %q\n", p.Schedule.Weekday)
		}
		fmt.Fprintln(&b, "    after_backup:")
		fmt.Fprintf(&b, "      check: %t\n", p.AfterBackup.Check)
		fmt.Fprintf(&b, "      prune: %q\n", p.AfterBackup.Prune)
	}
	return b.String()
}

func writeYAMLStringList(b *strings.Builder, key string, values []string) {
	if len(values) == 0 {
		fmt.Fprintf(b, "%s: []\n", key)
		return
	}
	fmt.Fprintf(b, "%s:\n", key)
	for _, value := range values {
		fmt.Fprintf(b, "      - %q\n", value)
	}
}

// Write renders cfg and writes it to path with 0o600 perms. The file
// names the bucket/region/profile but never a secret, yet 0o600 keeps it
// out of other users' reach as a matter of policy hygiene.
func Write(path string, cfg *Config) error {
	if err := os.WriteFile(path, Render(cfg), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
```

Now update the cli callers so nothing references the removed identifiers:

In `internal/cli/init.go`, delete lines `60-181` (the `defaultAgentProvider`/`defaultAgentModel` const block, `renderConfigYAML`, `renderPoliciesYAML`, `writeYAMLStringList`). Keep the `configFileName` const (line 47) and everything from `NewInit` onward. Replace the one remaining call site — find where `init` writes the config (it uses `renderConfigYAML`; grep confirmed init.go references it) — and route it through `config.Write`. Concretely, wherever init.go writes the file, change:

```go
	if err := os.WriteFile(cfgPath, []byte(renderConfigYAML(cfg)), 0o600); err != nil {
```
to:
```go
	if err := config.Write(cfgPath, cfg); err != nil {
```
(init.go already imports `internal/config`.)

In `internal/cli/policy.go`, replace `writeRenderedConfig` (lines 382-387) — delete the function and update its two callers (`runPolicyAdd:169`, `runPolicyRemove:241`):

```go
	if err := config.Write(*flags.configPath, cfg); err != nil {
		return err
	}
```
and
```go
	if err := config.Write(cfgPath, cfg); err != nil {
		return err
	}
```
This removes the last use of `os` in policy.go's write path; keep `os` if other functions still use it (grep `os\.` in policy.go before dropping the import — `runPolicyAdd`/`runPolicyRemove` were the only `writeRenderedConfig` users, and `os` may otherwise be unused, in which case drop it from the import block).

In `internal/cli/setup.go:295` and `:399`, and `internal/cli/passwd.go:269`, replace `os.WriteFile(path, []byte(renderConfigYAML(cfg)), 0o600)` with `config.Write(path, cfg)`. Example for setup.go:295 (`&plan.Config` is a `config.Config`, so pass its address):

```go
	if err := config.Write(cfgPath, &plan.Config); err != nil {
```

In `internal/cli/init_test.go`, delete `TestRenderConfigYAML_IncludesPolicies`, `TestRenderConfigYAML_PreservesCustomAgent`, `TestRenderConfigYAML_DefaultsAgentWhenUnset` (lines 178-253) — they now live in `render_test.go`. Drop the `strings` import from init_test.go only if no other test there uses it (grep first).

In `internal/cli/policy_test.go:17-24`, update `writePolicyConfigFile` to use the exported writer so it stops depending on the removed `renderConfigYAML`:

```go
func writePolicyConfigFile(t *testing.T, dir string, cfg *config.Config) string {
	t.Helper()
	path := filepath.Join(dir, "sentra.yaml")
	if err := config.Write(path, cfg); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}
	return path
}
```
(Drop the now-unused `os` import from policy_test.go only if nothing else there uses it — grep first.)

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/config/ ./internal/cli/ -count=1`
Expected: PASS (config package tests pass; cli package still builds and its policy/init/setup/passwd tests pass against the new `config.Write`).

- [ ] **Step 5: Commit**
```bash
git add internal/config/render.go internal/config/render_test.go \
  internal/cli/init.go internal/cli/policy.go internal/cli/setup.go \
  internal/cli/passwd.go internal/cli/init_test.go internal/cli/policy_test.go
git commit -m "refactor(config): extract Render/Write from cli into internal/config"
```

---

### Task 4: PoliciesView: read-only picker with inline detail

**Files:**
- Create: `internal/tui/policies.go`
- Create: `internal/tui/policies_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/policies_test.go` with the hydration + picker tests. `policiesDeps` writes a two-policy config to a temp file and sets `ConfigPath` (the NEW Unit-1 Deps field):

```go
package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// policiesDeps writes a sentra.yaml with two named policies to a temp dir
// and returns Deps wired with ConfigPath pointing at it (plus an optional
// repo for the RUN flow). The view hydrates by loading ConfigPath, mirror-
// ing how PruneView hydrates from the repo.
func policiesDeps(t *testing.T, r *repo.Repo) (Deps, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:    []string{"/data/alpha"},
		Tags:     []string{"nightly"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
		AfterBackup: config.PolicyAfterBackup{
			Check: true,
			Prune: "off",
		},
	}
	cfg.Policies["beta"] = config.PolicyConfig{
		Paths:    []string{"/data/beta"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Deps{Repo: r, Config: &cfg, ConfigPath: path}, path
}

func TestPoliciesView_HydratesFromConfigPath(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	if len(v.names) != 2 || v.names[0] != "alpha" || v.names[1] != "beta" {
		t.Fatalf("names = %v, want [alpha beta] (sorted)", v.names)
	}
	out := v.View()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("picker must list both policies:\n%s", out)
	}
}

func TestPoliciesView_MissingConfigPathShowsPlaceholder(t *testing.T) {
	v := NewPoliciesView(Deps{})
	if v.loadErr == "" {
		t.Fatal("empty deps must set a load error")
	}
	if !strings.Contains(v.View(), "no config") {
		t.Errorf("view must surface the missing-config placeholder:\n%s", v.View())
	}
}

func TestPoliciesView_InlineDetailShowsSelectedPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Selection starts at index 0 (alpha); its schedule + tag render inline.
	out := v.View()
	if !strings.Contains(out, "daily@03:00") {
		t.Errorf("detail must show alpha's schedule shorthand:\n%s", out)
	}
	if !strings.Contains(out, "/data/alpha") {
		t.Errorf("detail must show alpha's path:\n%s", out)
	}
	// Down moves selection to beta; its manual schedule renders.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(PoliciesView)
	if v.selected != 1 {
		t.Fatalf("selected = %d, want 1 after down", v.selected)
	}
	if out := v.View(); !strings.Contains(out, "/data/beta") {
		t.Errorf("detail must follow selection to beta:\n%s", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPoliciesView -count=1`
Expected: FAIL — `undefined: NewPoliciesView` and `undefined: PoliciesView` (the view doesn't exist).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/tui/policies.go`. This task defines the read-only skeleton (constructor hydrating from `deps.ConfigPath`, picker navigation, inline detail render). The ADD/REMOVE/RUN key handling is stubbed to no-ops here and filled in by the next two tasks — but the `policiesStage` enum, `reload` helper, and confirm-modal IDs are declared now so the later tasks only add branches. `emptyDash` is inlined per the pinned instruction (config extraction must not move it):

```go
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/ui"
)

// policiesStage tracks the Policies view's position. The read-only skeleton
// only uses policiesList; the ADD form and RUN flow add running/form stages
// in later tasks.
type policiesStage int

const (
	policiesList policiesStage = iota
	policiesForm
	policiesRunning
	policiesRunDone
)

// Confirm-modal IDs tie a pushed modal back to this view. ADD and REMOVE
// use the simple ConfirmModal (config-only, reversible edits); RUN uses the
// simple or TYPED confirm depending on the policy's prune mode.
const (
	policyAddConfirmID    = "policy-add"
	policyRemoveConfirmID = "policy-remove"
	policyRunConfirmID    = "policy-run"
)

// PoliciesView lists the named backup policies from sentra.yaml, shows the
// selected one inline, and drives three actions: ADD/edit and REMOVE are
// config-only (they rewrite sentra.yaml via config.Write and reload — NO
// repo lock, NO op guard), while RUN a policy takes the mutating-op guard
// (it calls repo.CreateSnapshot per path). The view hydrates by loading
// deps.ConfigPath, the same way PruneView hydrates from the repo.
type PoliciesView struct {
	deps     Deps
	stage    policiesStage
	names    []string
	policies map[string]config.PolicyConfig
	selected int
	loadErr  string
	notice   string // transient banner (op rejection, reload error)
	width    int

	// form + run state are declared here but only driven by later tasks.
	form   policyForm
	run    policyRunState
	result policyRunDoneMsg
}

func NewPoliciesView(deps Deps) PoliciesView {
	v := PoliciesView{deps: deps}
	if deps.ConfigPath == "" {
		v.loadErr = "no config file configured"
		return v
	}
	v.reload()
	return v
}

// reload re-reads deps.ConfigPath and repopulates the sorted name list and
// policy map. Called at construction and after every config.Write so the
// picker reflects the file on disk. A load error is surfaced as loadErr
// (construction) or notice (post-edit) by the caller; reload itself only
// sets loadErr because it is also the construction path.
func (v *PoliciesView) reload() {
	cfg, err := config.Load(v.deps.ConfigPath)
	if err != nil {
		v.loadErr = err.Error()
		return
	}
	v.loadErr = ""
	v.policies = cfg.Policies
	v.names = make([]string, 0, len(cfg.Policies))
	for name := range cfg.Policies {
		v.names = append(v.names, name)
	}
	sort.Strings(v.names)
	if v.selected >= len(v.names) {
		v.selected = len(v.names) - 1
	}
	if v.selected < 0 {
		v.selected = 0
	}
}

func (PoliciesView) Init() tea.Cmd { return nil }

func (v PoliciesView) Title() string { return "Policies" }

func (v PoliciesView) ShortHelp() []key.Binding {
	if v.stage != policiesList || len(v.names) == 0 {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
	}
}

func (v PoliciesView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case tea.KeyMsg:
		if v.stage != policiesList {
			return v, nil // form/run handling added in later tasks
		}
		switch msg.Type {
		case tea.KeyUp:
			if v.selected > 0 {
				v.selected--
			}
			v.notice = ""
			return v, nil
		case tea.KeyDown:
			if v.selected < len(v.names)-1 {
				v.selected++
			}
			v.notice = ""
			return v, nil
		}
		return v, nil
	}
	return v, nil
}

func (v PoliciesView) View() string {
	if v.loadErr != "" {
		return ui.Danger.Render(v.loadErr)
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Backup policies"))
	if v.notice != "" {
		b.WriteString("  " + ui.Warn.Render(v.notice))
	}
	b.WriteString("\n\n")
	if len(v.names) == 0 {
		b.WriteString(ui.Muted.Render("No policies configured."))
		return b.String()
	}
	for i, name := range v.names {
		marker := "  "
		label := name
		if i == v.selected {
			marker = ui.Primary.Render("▸ ")
			label = ui.Primary.Render(name)
		}
		p := v.policies[name]
		fmt.Fprintf(&b, "%s%s  %s\n", marker, label,
			ui.Muted.Render(policycfg.FormatScheduleSpec(p.Schedule)))
	}
	b.WriteString("\n" + v.renderDetail())
	b.WriteString("\n" + ui.Muted.Render("↑↓ select · r run · d remove"))
	return b.String()
}

// renderDetail shows the selected policy read-only. Inline empty->"-"
// substitution here rather than importing cli's emptyDash (which stays put
// per the extraction contract).
func (v PoliciesView) renderDetail() string {
	if v.selected < 0 || v.selected >= len(v.names) {
		return ""
	}
	name := v.names[v.selected]
	p := v.policies[name]
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render(name) + "\n")
	b.WriteString("  paths:\n")
	for _, path := range p.Paths {
		fmt.Fprintf(&b, "    - %s\n", path)
	}
	fmt.Fprintf(&b, "  tags:     %s\n", dash(strings.Join(p.Tags, ", ")))
	fmt.Fprintf(&b, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	fmt.Fprintf(&b, "  check:    %t\n", p.AfterBackup.Check)
	fmt.Fprintf(&b, "  prune:    %s", policyPruneModeOrOff(p.AfterBackup.Prune))
	return b.String()
}

// policyPruneModeOrOff normalizes an empty prune string to "off" for
// display, matching the CLI's policyPruneMode.
func policyPruneModeOrOff(mode string) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		return policycfg.PruneOff
	}
	return mode
}

// policyForm and policyRunState are placeholders wired by the ADD and RUN
// tasks; declared here so the PoliciesView struct compiles as one unit.
type policyForm struct{}

type policyRunState struct {
	reporter *opReporter
	name     string
}

// policyRunDoneMsg is the RUN flow's terminal, guard-clearing message.
// Defined here (the struct field references it); the RUN task fills its
// body and the startOpMsg that produces it.
type policyRunDoneMsg struct {
	name      string
	snapshots int
	err       error
}

func (policyRunDoneMsg) opResult() {}

// hydrateCtx is the timeout-bounded context the view uses for its
// construction-time reads, matching PruneView/RestoreView.
func (v PoliciesView) hydrateCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctxOrBackground(v.deps.Ctx), hydrateTimeout)
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPoliciesView -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/policies.go internal/tui/policies_test.go
git commit -m "feat(tui): add read-only Policies view with picker and inline detail"
```

---

### Task 5: Policies REMOVE: config-only edit gated by a simple confirm

**Files:**
- Modify: `internal/tui/policies.go` (add the `d` key branch + `confirmedMsg` handling for `policyRemoveConfirmID`)
- Modify: `internal/tui/policies_test.go` (add the REMOVE tests)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/policies_test.go`:

```go
func TestPoliciesView_RemoveRequiresConfirm(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Pressing 'd' pushes a simple ConfirmModal and does NOT touch the file.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v = m.(PoliciesView)
	if cmd == nil {
		t.Fatal("d must request a confirmation modal")
	}
	msg := cmd()
	push, ok := msg.(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", msg)
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("remove must use the simple ConfirmModal, got %T", push.modal)
	}
	// File is untouched: alpha still present.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; !ok {
		t.Fatal("remove must not delete before confirmation")
	}
}

func TestPoliciesView_RemoveConfirmedRewritesConfigAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// selected == 0 == alpha. Arm the modal, then confirm.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v = m.(PoliciesView)
	m, cmd := v.Update(confirmedMsg{id: policyRemoveConfirmID})
	v = m.(PoliciesView)
	// The write is done synchronously in a plain tea.Cmd (no op guard).
	if cmd != nil {
		if _, ok := cmd().(startOpMsg); ok {
			t.Fatal("remove must NOT take the op guard (config-only edit)")
		}
	}
	// alpha is gone from disk and from the reloaded view.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; ok {
		t.Fatal("confirmed remove must delete alpha from sentra.yaml")
	}
	if len(v.names) != 1 || v.names[0] != "beta" {
		t.Fatalf("view names after remove = %v, want [beta]", v.names)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPoliciesView_Remove -count=1`
Expected: FAIL — `d` currently falls through to the default no-op branch, so no `pushModalMsg` is emitted (`cmd == nil`) and the assertion "d must request a confirmation modal" fails.

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/policies.go`, add the `d` key branch inside the `policiesList` `tea.KeyMsg` switch, a `confirmedMsg` case in `Update`, and the `removeSelected` helper. Insert the `d` case alongside `KeyUp`/`KeyDown`:

```go
		case tea.KeyRunes:
			if len(msg.Runes) == 1 && msg.Runes[0] == 'd' && len(v.names) > 0 {
				name := v.names[v.selected]
				body := fmt.Sprintf("Remove policy %q from sentra.yaml?\nThis edits local config only — no snapshots are touched.", name)
				modal := NewConfirmModal("Confirm remove", body, policyRemoveConfirmID, 80, 24)
				return v, func() tea.Msg { return pushModalMsg{modal: modal} }
			}
			return v, nil
```

Add a top-level `confirmedMsg` case to `Update` (a sibling of the `tea.KeyMsg` case, since the App broadcasts `confirmedMsg` to every view, not through `routeKey`):

```go
	case confirmedMsg:
		switch msg.id {
		case policyRemoveConfirmID:
			return v.removeSelected()
		}
		return v, nil
```

Add the helper:

```go
// removeSelected deletes the selected policy from sentra.yaml and reloads.
// This is a config-only edit: it rewrites the file via config.Write and
// never takes the repo lock or the op guard, matching `sentra policy remove`.
func (v PoliciesView) removeSelected() (tea.Model, tea.Cmd) {
	if v.selected < 0 || v.selected >= len(v.names) {
		return v, nil
	}
	name := v.names[v.selected]
	cfg, err := config.Load(v.deps.ConfigPath)
	if err != nil {
		v.notice = "reload failed: " + err.Error()
		return v, nil
	}
	delete(cfg.Policies, name)
	if err := config.Write(v.deps.ConfigPath, cfg); err != nil {
		v.notice = "write failed: " + err.Error()
		return v, nil
	}
	v.reload()
	v.notice = fmt.Sprintf("removed %q", name)
	return v, nil
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPoliciesView -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/policies.go internal/tui/policies_test.go
git commit -m "feat(tui): Policies view REMOVE action (config-only, confirm-gated)"
```

---

### Task 6: Policies ADD: inline form gated by a simple confirm

**Files:**
- Modify: `internal/tui/policies.go` (implement `policyForm`, the `a` key branch, form key handling, `confirmedMsg` for `policyAddConfirmID`)
- Modify: `internal/tui/policies_test.go` (add the ADD tests)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/policies_test.go`:

```go
func TestPoliciesView_AddOpensInlineForm(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	if v.stage != policiesForm {
		t.Fatalf("stage = %v, want policiesForm after 'a'", v.stage)
	}
	if !strings.Contains(v.View(), "New policy") {
		t.Errorf("form view must show the new-policy header:\n%s", v.View())
	}
}

func TestPoliciesView_AddConfirmedWritesPolicyAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Type a name, tab to path, type a path.
	v = typeIntoPolicies(t, v, "gamma")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "/data/gamma")
	// Enter on the form arms the simple confirm modal.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("form enter must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("add must use the simple ConfirmModal, got %T", push.modal)
	}
	// Confirm: config.Write happens, view reloads, gamma is present.
	m, _ = v.Update(confirmedMsg{id: policyAddConfirmID})
	v = m.(PoliciesView)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.Policies["gamma"]
	if !ok || len(p.Paths) != 1 || p.Paths[0] != "/data/gamma" {
		t.Fatalf("gamma not written correctly: %+v", cfg.Policies["gamma"])
	}
	if v.stage != policiesList {
		t.Fatalf("stage after add = %v, want policiesList", v.stage)
	}
}

func TestPoliciesView_AddRejectsInvalidPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Name only, no path: policy.Validate rejects (needs >=1 path). Enter
	// must surface the error and NOT push a confirm modal.
	v = typeIntoPolicies(t, v, "noPaths")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	if cmd != nil {
		t.Fatalf("invalid policy must not push a modal, got %#v", cmd())
	}
	if v.form.err == "" {
		t.Fatal("invalid policy must set a form error")
	}
}

func typeIntoPolicies(t *testing.T, v PoliciesView, s string) PoliciesView {
	t.Helper()
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(PoliciesView)
	}
	return v
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPoliciesView_Add -count=1`
Expected: FAIL — `a` currently no-ops (`policyForm` is an empty struct, `policiesForm` stage is unreachable), so `v.stage != policiesForm` fails and `v.form.err` is undefined (compile error: `v.form.err` — `policyForm` has no field `err`).

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/policies.go`, flesh out `policyForm`, add the `a` branch, the form's key handling, and the `policyAddConfirmID` confirm branch. The form has two text fields (name, path) plus a schedule shorthand parsed via `policycfg.ParseScheduleSpec`; validation goes through `policycfg.Validate` (the same contract the CLI uses). Replace the placeholder `type policyForm struct{}` with:

```go
// policyForm is the inline ADD form: name + path + optional schedule
// shorthand ("daily@03:00", "manual", …). It stays deliberately minimal —
// the same fields the CLI's `policy add` exposes for the common case;
// power users still edit sentra.yaml directly. A built policy is validated
// with policycfg.Validate before the confirm modal, so a bad entry never
// reaches disk.
type policyForm struct {
	name     textinput.Model
	path     textinput.Model
	schedule textinput.Model
	focus    int // 0=name, 1=path, 2=schedule
	err      string
}

func newPolicyForm() policyForm {
	name := textinput.New()
	name.Prompt = "name>     "
	name.Placeholder = "policy name"
	name.Focus()
	path := textinput.New()
	path.Prompt = "path>     "
	path.Placeholder = "directory to back up"
	schedule := textinput.New()
	schedule.Prompt = "schedule> "
	schedule.Placeholder = "manual | daily@03:00 | weekly@mon:03:00"
	return policyForm{name: name, path: path, schedule: schedule}
}

// build assembles a config.PolicyConfig from the form and validates it.
// Returns the built name + policy, or a non-nil error to display inline.
func (f policyForm) build() (string, config.PolicyConfig, error) {
	name := strings.TrimSpace(f.name.Value())
	path := strings.TrimSpace(f.path.Value())
	spec := strings.TrimSpace(f.schedule.Value())
	if spec == "" {
		spec = policycfg.CadenceManual
	}
	sched, err := policycfg.ParseScheduleSpec(spec)
	if err != nil {
		return "", config.PolicyConfig{}, err
	}
	var paths []string
	if path != "" {
		paths = []string{path}
	}
	p := config.PolicyConfig{
		Paths:    paths,
		Schedule: sched,
	}
	if err := policycfg.Validate(name, p); err != nil {
		return "", config.PolicyConfig{}, err
	}
	return name, p, nil
}
```

Add `"github.com/charmbracelet/bubbles/textinput"` to the import block.

Add the `a` key branch alongside the existing `KeyRunes` handling in the `policiesList` switch (extend the existing `KeyRunes` case):

```go
		case tea.KeyRunes:
			if len(msg.Runes) != 1 {
				return v, nil
			}
			switch msg.Runes[0] {
			case 'a':
				v.stage = policiesForm
				v.form = newPolicyForm()
				v.notice = ""
				return v, nil
			case 'd':
				if len(v.names) > 0 {
					name := v.names[v.selected]
					body := fmt.Sprintf("Remove policy %q from sentra.yaml?\nThis edits local config only — no snapshots are touched.", name)
					modal := NewConfirmModal("Confirm remove", body, policyRemoveConfirmID, 80, 24)
					return v, func() tea.Msg { return pushModalMsg{modal: modal} }
				}
			}
			return v, nil
```

(This replaces the `d`-only `KeyRunes` branch added in the previous task — fold the two together as shown.)

Replace the early `if v.stage != policiesList { return v, nil }` guard at the top of the `tea.KeyMsg` case with a stage dispatch so the form gets keystrokes:

```go
	case tea.KeyMsg:
		switch v.stage {
		case policiesForm:
			return v.updateForm(msg)
		case policiesList:
			// (existing list key switch: KeyUp / KeyDown / KeyRunes)
		default:
			return v, nil
		}
```

Add the form key handler and the ADD confirm branch:

```go
func (v PoliciesView) updateForm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		v.stage = policiesList
		return v, nil
	case tea.KeyTab:
		v.form.focus = (v.form.focus + 1) % 3
		v.form.name.Blur()
		v.form.path.Blur()
		v.form.schedule.Blur()
		switch v.form.focus {
		case 0:
			v.form.name.Focus()
		case 1:
			v.form.path.Focus()
		case 2:
			v.form.schedule.Focus()
		}
		return v, nil
	case tea.KeyEnter:
		name, _, err := v.form.build()
		if err != nil {
			v.form.err = err.Error()
			return v, nil
		}
		body := fmt.Sprintf("Add policy %q to sentra.yaml?\nThis edits local config only.", name)
		modal := NewConfirmModal("Confirm add", body, policyAddConfirmID, 80, 24)
		return v, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	var cmd tea.Cmd
	switch v.form.focus {
	case 0:
		v.form.name, cmd = v.form.name.Update(msg)
	case 1:
		v.form.path, cmd = v.form.path.Update(msg)
	case 2:
		v.form.schedule, cmd = v.form.schedule.Update(msg)
	}
	v.form.err = "" // typing clears the last validation error
	return v, cmd
}

// addFromForm rebuilds + revalidates the form, writes the new policy into
// sentra.yaml, and reloads. Config-only: no repo lock, no op guard.
func (v PoliciesView) addFromForm() (tea.Model, tea.Cmd) {
	name, p, err := v.form.build()
	if err != nil {
		v.stage = policiesForm
		v.form.err = err.Error()
		return v, nil
	}
	cfg, err := config.Load(v.deps.ConfigPath)
	if err != nil {
		v.notice = "reload failed: " + err.Error()
		v.stage = policiesList
		return v, nil
	}
	if cfg.Policies == nil {
		cfg.Policies = map[string]config.PolicyConfig{}
	}
	cfg.Policies[name] = p
	if err := config.Write(v.deps.ConfigPath, cfg); err != nil {
		v.notice = "write failed: " + err.Error()
		v.stage = policiesList
		return v, nil
	}
	v.stage = policiesList
	v.reload()
	v.notice = fmt.Sprintf("added %q", name)
	return v, nil
}
```

Extend the top-level `confirmedMsg` case to route the ADD id:

```go
	case confirmedMsg:
		switch msg.id {
		case policyRemoveConfirmID:
			return v.removeSelected()
		case policyAddConfirmID:
			return v.addFromForm()
		}
		return v, nil
```

Extend `View()` to render the form when `v.stage == policiesForm` (add near the top of `View`, after the `loadErr` guard):

```go
	if v.stage == policiesForm {
		var b strings.Builder
		b.WriteString(ui.Primary.Render("New policy") + "\n\n")
		b.WriteString(v.form.name.View() + "\n")
		b.WriteString(v.form.path.View() + "\n")
		b.WriteString(v.form.schedule.View() + "\n")
		if v.form.err != "" {
			b.WriteString("\n" + ui.Danger.Render(v.form.err) + "\n")
		}
		b.WriteString("\n" + ui.Muted.Render("⏎ save · tab field · esc cancel"))
		return b.String()
	}
```

Update `ShortHelp` to advertise `a` in the list stage:

```go
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
		key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "run")),
		key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "remove")),
	}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPoliciesView -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/policies.go internal/tui/policies_test.go
git commit -m "feat(tui): Policies view ADD inline form (config-only, validated, confirm-gated)"
```

---

### Task 7: Policies RUN: op-guarded snapshot with optional check + retention prune

**Files:**
- Modify: `internal/tui/policies.go` (implement the `r` key branch, RUN confirm modals, `startRun`, running/done stages + rendering)
- Modify: `internal/tui/policies_test.go` (add RUN tests against `newFlowRepo`)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/policies_test.go`:

```go
import (
	"context"
	// (existing imports; add if not present)
	"os"
)

// policiesRunDeps builds a repo-backed Deps whose config has one policy
// pointing at a real seeded directory, with the given prune mode.
func policiesRunDeps(t *testing.T, prune string) (Deps, string, string) {
	t.Helper()
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Retention.KeepLast = 1
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:       []string{src},
		Schedule:    config.PolicySchedule{Cadence: "manual"},
		AfterBackup: config.PolicyAfterBackup{Check: true, Prune: prune},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// deps.Config must reflect the same file so retention limits are read.
	deps := Deps{Repo: r, Config: &cfg, ConfigPath: path}
	return deps, path, src
}

func mustReturn[T any](t *testing.T, v tea.Model) T {
	t.Helper()
	m, ok := v.(T)
	if !ok {
		t.Fatalf("unexpected model type %T", v)
	}
	return m
}

// TestPoliciesView_RunOffModeUsesSimpleConfirm: a policy with prune=off
// must gate RUN behind the SIMPLE confirm, then start the op guard.
func TestPoliciesView_RunOffModeUsesSimpleConfirm(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("r must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("prune=off must use the SIMPLE ConfirmModal, got %T", push.modal)
	}
}

// TestPoliciesView_RunApplyModeUsesTypedConfirm: prune=apply is
// destructive, so RUN must use the TYPED confirm.
func TestPoliciesView_RunApplyModeUsesTypedConfirm(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "apply")
	v := NewPoliciesView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("r must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(TypedConfirmModal); !ok {
		t.Fatalf("prune=apply must use the TYPED confirm, got %T", push.modal)
	}
}

// TestPoliciesView_RunConfirmedTakesOpGuardAndSnapshots: confirming RUN
// emits a startOpMsg (the op guard) whose run creates a real snapshot.
func TestPoliciesView_RunConfirmedTakesOpGuardAndSnapshots(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	m, cmd := v.Update(confirmedMsg{id: policyRunConfirmID})
	v = m.(PoliciesView)
	if v.stage != policiesRunning {
		t.Fatalf("stage = %v, want policiesRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, foundStart = s, true
		}
	}
	if !foundStart {
		t.Fatalf("confirmed run must emit a startOpMsg, got %#v", msgs)
	}
	if start.name != "policy-run" {
		t.Fatalf("op name = %q, want policy-run", start.name)
	}
	// Run the op synchronously; it must create a snapshot and report done.
	res := start.run(context.Background())
	done, ok := res.(policyRunDoneMsg)
	if !ok {
		t.Fatalf("expected policyRunDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("run failed: %v", done.err)
	}
	if done.snapshots != 1 {
		t.Fatalf("snapshots = %d, want 1", done.snapshots)
	}
	snaps, err := deps.Repo.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots = %v, %v", snaps, err)
	}
	// Delivering the result moves to the done stage.
	m, _ = v.Update(res)
	v = m.(PoliciesView)
	if v.stage != policiesRunDone {
		t.Fatalf("stage after result = %v, want policiesRunDone", v.stage)
	}
}

// TestPoliciesView_RunRejectedResetsToList: if the op guard rejects the
// start (another op running), the view must leave the running stage.
func TestPoliciesView_RunRejectedResetsToList(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	m, _ = v.Update(confirmedMsg{id: policyRunConfirmID})
	v = m.(PoliciesView)
	m, _ = v.Update(opRejectedMsg{name: "policy-run"})
	v = m.(PoliciesView)
	if v.stage != policiesList {
		t.Fatalf("stage after rejection = %v, want policiesList", v.stage)
	}
	if v.notice == "" {
		t.Fatal("rejection must set a notice banner")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPoliciesView_Run -count=1`
Expected: FAIL — `r` currently no-ops (no `KeyRunes` branch for `'r'`), so `cmd()` panics/returns nil; the tests report `r must push a confirm modal`. `policyRunConfirmID` handling and the `startOpMsg` emission don't exist yet.

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/policies.go`, add the `'r'` case to the list `KeyRunes` switch, RUN handling in `confirmedMsg`, the `opRejectedMsg` and `policyRunDoneMsg` cases in `Update`, the `startRun` op builder, and running/done rendering.

Add imports: `"errors"`, `"time"`, `"github.com/markgustetic/sentra/internal/blobstore"`, `"github.com/markgustetic/sentra/internal/repo"`, `"github.com/markgustetic/sentra/internal/walker"`.

Extend the list `KeyRunes` switch with the `'r'` case (alongside `'a'` and `'d'`):

```go
			case 'r':
				if len(v.names) > 0 {
					return v.armRun()
				}
```

Add `armRun`, which chooses the modal by prune mode (typed when `apply`, simple otherwise — matching the confirmation policy):

```go
// armRun pushes the RUN confirmation modal for the selected policy. A
// prune mode of "apply" is destructive (it deletes snapshots + GCs), so it
// gets the TYPED confirm; every other mode (off, dry-run, or check-only)
// gets the simple confirm. The modal id is policyRunConfirmID either way,
// so the confirmedMsg handler starts the op regardless of which was shown.
func (v PoliciesView) armRun() (tea.Model, tea.Cmd) {
	name := v.names[v.selected]
	p := v.policies[name]
	mode := policyPruneModeOrOff(p.AfterBackup.Prune)
	var modal Modal
	if mode == policycfg.PruneApply {
		body := fmt.Sprintf("Run policy %q now?\nAfter backup it will DELETE snapshots outside the retention policy and reclaim their chunks.", name)
		modal = NewTypedConfirmModal("Confirm policy run", body, "run", policyRunConfirmID, 80, 24)
	} else {
		body := fmt.Sprintf("Run policy %q now?\nThis creates a snapshot for each of its paths.", name)
		modal = NewConfirmModal("Confirm policy run", body, policyRunConfirmID, 80, 24)
	}
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}
```

Extend the `confirmedMsg` case with the RUN id:

```go
		case policyRunConfirmID:
			return v.startRun()
```

Add `policyRunDoneMsg` and `opRejectedMsg` cases to `Update` (siblings of the `tea.KeyMsg`/`confirmedMsg`/`tea.WindowSizeMsg` cases):

```go
	case policyRunDoneMsg:
		v.stage = policiesRunDone
		v.result = msg
		v.reload() // retention prune may have changed nothing on disk, but
		// keeps the view consistent if a future action mutates config.
		return v, nil

	case opRejectedMsg:
		if v.stage == policiesRunning && msg.name == "policy-run" {
			v.stage = policiesList
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == policiesRunning {
			return v, opTick()
		}
		return v, nil
```

Add `startRun` — this mirrors the CLI `runPolicy` sequence (`internal/cli/policy.go:250-373`): a snapshot per path via `CreateSnapshot` (with an `opReporter` for progress), optional `Check`, optional retention prune (`PlanRetentionExplain` → `DeleteSnapshot` → `GC`). It runs inside the App op guard via `startOpMsg`:

```go
// startRun launches the selected policy under the App op guard. The run
// closure walks the CLI's runPolicy sequence: CreateSnapshot per path,
// optional Check, optional retention prune. It honors ctx cancellation
// (CreateSnapshot/Check/GC all take ctx) and returns policyRunDoneMsg,
// which implements opResult() so the guard clears.
//
// Retention limits come from deps.Config (the resolved config, same source
// PruneView reads). GC's live set is still derived from the manifests
// present under the repo lock — keepIDs only marks the deliberate-prune
// path, exactly as the CLI and PruneView do.
func (v PoliciesView) startRun() (tea.Model, tea.Cmd) {
	if v.deps.Repo == nil {
		v.notice = "no repository configured"
		return v, nil
	}
	name := v.names[v.selected]
	p := v.policies[name]
	r := v.deps.Repo
	reporter := newOpReporter()
	v.run = policyRunState{reporter: reporter, name: name}
	v.stage = policiesRunning

	var wopts walker.Options
	var retention repo.RetentionPolicy
	if v.deps.Config != nil {
		wopts = walker.Options{
			IgnoreFile:    v.deps.Config.Backup.IgnoreFile,
			ExcludeCaches: v.deps.Config.Backup.ExcludeCaches,
		}
		retention = repo.RetentionPolicy{
			KeepLast:    v.deps.Config.Retention.KeepLast,
			KeepDaily:   v.deps.Config.Retention.KeepDaily,
			KeepWeekly:  v.deps.Config.Retention.KeepWeekly,
			KeepMonthly: v.deps.Config.Retention.KeepMonthly,
		}
	}
	paths := append([]string(nil), p.Paths...)
	tag := policyRunTag(name, p.Tags)
	doCheck := p.AfterBackup.Check
	pruneMode := policyPruneModeOrOff(p.AfterBackup.Prune)

	start := startOpMsg{
		name: "policy-run",
		run: func(ctx context.Context) tea.Msg {
			count := 0
			for _, path := range paths {
				if _, err := r.CreateSnapshot(ctx, path, repo.SnapshotOptions{
					Tag:      tag,
					Progress: reporter,
					Walker:   wopts,
				}); err != nil {
					return policyRunDoneMsg{name: name, snapshots: count, err: fmt.Errorf("snapshot %s: %w", path, err)}
				}
				count++
			}
			if doCheck {
				report, err := r.Check(ctx, repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
				if err != nil {
					return policyRunDoneMsg{name: name, snapshots: count, err: fmt.Errorf("check: %w", err)}
				}
				if !report.Healthy() {
					return policyRunDoneMsg{name: name, snapshots: count, err: errors.New("post-backup check found integrity issues")}
				}
			}
			if err := runPolicyRetentionPrune(ctx, r, retention, pruneMode); err != nil {
				return policyRunDoneMsg{name: name, snapshots: count, err: err}
			}
			return policyRunDoneMsg{name: name, snapshots: count}
		},
	}
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

// runPolicyRetentionPrune applies the policy's post-backup prune. It
// mirrors the CLI's runPolicyPrune (internal/cli/policy.go:331): off is a
// no-op; dry-run computes but deletes nothing; apply deletes the dropped
// snapshots (skipping already-gone ones) and runs GC. Apply refuses to
// drop every snapshot — the same guard the CLI enforces.
func runPolicyRetentionPrune(ctx context.Context, r *repo.Repo, policy repo.RetentionPolicy, mode string) error {
	if mode == policycfg.PruneOff || mode == "" {
		return nil
	}
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	decisions := repo.PlanRetentionExplain(snaps, policy)
	var keep, drop []string
	for _, d := range decisions {
		if d.Keep {
			keep = append(keep, d.Snapshot.ID)
		} else {
			drop = append(drop, d.Snapshot.ID)
		}
	}
	if mode == policycfg.PruneDryRun {
		return nil // preview only; nothing deleted
	}
	if len(drop) == 0 {
		return nil
	}
	if len(keep) == 0 {
		return errors.New("policy prune would drop every snapshot; refusing automatic apply")
	}
	for _, id := range drop {
		if err := r.DeleteSnapshot(ctx, id); err != nil && !errors.Is(err, blobstore.ErrNotFound) {
			return fmt.Errorf("delete snapshot %s: %w", id, err)
		}
	}
	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	if _, err := r.GC(ctx, keepIDs); err != nil {
		return fmt.Errorf("gc: %w", err)
	}
	return nil
}

// policyRunTag mirrors the CLI's policySnapshotTag: "policy:<name>" plus
// any configured tags, space-joined.
func policyRunTag(name string, tags []string) string {
	parts := []string{"policy:" + name}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			parts = append(parts, tag)
		}
	}
	return strings.Join(parts, " ")
}
```

Extend `View()` with running/done rendering (add branches before the list render, after the form branch):

```go
	if v.stage == policiesRunning {
		var b strings.Builder
		b.WriteString(ui.Primary.Render("Running policy " + v.run.name + "…"))
		if v.run.reporter != nil {
			total, done := v.run.reporter.Snapshot()
			fmt.Fprintf(&b, "\n\n  %s / %s uploaded", ui.FormatBytes(done), ui.FormatBytes(total))
		}
		return b.String()
	}
	if v.stage == policiesRunDone {
		var b strings.Builder
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Policy run failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			b.WriteString(ui.Success.Render("Policy run complete"))
			fmt.Fprintf(&b, "\n\n  policy     %s\n  snapshots  %d", v.result.name, v.result.snapshots)
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ back to policies"))
		return b.String()
	}
```

Add an `enter`-returns-to-list transition in the list `tea.KeyMsg` handling for the done stage. Since `policiesRunDone` is not `policiesList`, extend the `tea.KeyMsg` stage dispatch:

```go
		case policiesRunDone:
			if msg.Type == tea.KeyEnter {
				v.stage = policiesList
				v.notice = ""
				return v, nil
			}
			return v, nil
		case policiesRunning:
			return v, nil
```

Verify `repo.CheckOptions` has a `StaleLockAfter` field before using it: it is referenced by the CLI at `internal/cli/policy.go:319` (`repo.CheckOptions{StaleLockAfter: 24 * time.Hour}`), so it exists.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPoliciesView -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/policies.go internal/tui/policies_test.go
git commit -m "feat(tui): Policies view RUN action (op-guarded snapshot + check + retention prune)"
```

---

### Task 8: Register PoliciesView in the App shell

**Files:**
- Modify: `internal/tui/app.go:155-169` (add the view entry + its "Operations" category)
- Modify: `internal/tui/app_test.go` (extend the registered-views assertion if one exists)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go` a test that the policies view is registered and reachable:

```go
func TestApp_RegistersPoliciesView(t *testing.T) {
	app := newTestApp(t)
	var found bool
	for _, v := range app.views {
		if v.id == "policies" {
			found = true
			if _, ok := v.model.(PoliciesView); !ok {
				t.Fatalf("policies entry is %T, want PoliciesView", v.model)
			}
		}
	}
	if !found {
		t.Fatal("App must register the policies view")
	}
	if cmd, ok := app.registry.Get("policies"); !ok {
		t.Fatalf("policies must be in the command registry: %+v", cmd)
	}
}
```

(If `app_test.go` has no `registry.Get`, drop that final block — confirm the `Registry` API when implementing; the `app.views` scan is sufficient and does not depend on it.)

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestApp_RegistersPoliciesView -count=1`
Expected: FAIL — `App must register the policies view` (no entry with id "policies" exists).

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/app.go`, add the policies entry to the `views` slice in `NewApp` (after `prune`, keeping Operations grouped), and add it to the `categories` map:

```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "policies", model: NewPoliciesView(deps)},
	}
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
		"policies": "Operations",
	}
```

Update the `NewApp` doc comment (line 142-145) count if it enumerates views ("the 3 Phase 2a operation flows" → note policies is the Phase 2c addition) — a comment-only edit, no behavior change.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -count=1`
Expected: PASS (the new view registers; `TestApp_NoOverflowAtMinSize` and the number-key navigation tests still pass because `PoliciesView` honors the forwarded `WindowSizeMsg` width and renders within budget).

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): register Policies view in the App shell under Operations"
```

---

**Notes for the assembler / downstream units**

- This unit depends on **Unit 1** having added `ConfigPath string` to `tui.Deps` (`internal/tui/app.go`). All PoliciesView hydration keys off `deps.ConfigPath` by that exact name. The RUN flow uses only `deps.Repo` and `deps.Config` (already present today).
- `internal/config` remains free of internal imports after this unit — `render.go` imports only `fmt`, `os`, `sort`, `strings` (verified: config today imports no internal package).
- Security invariants held: `config.Render`/`Write` serialize only non-secret `Config` fields (no passphrase/salt/MAC/AWS-cred field exists on the struct); the RUN flow's LLM path is untouched (RUN never calls the provider); GC's live set is still derived from present manifests under the repo lock (`runPolicyRetentionPrune` passes only `keepIDs` as the deliberate-prune marker, exactly like the CLI); the prune==apply RUN path is TYPED-confirm-gated, all other RUN/ADD/REMOVE actions are simple-confirm-gated, and read-only picker navigation needs no confirm.


## Part 3 — internal/recoverykit extraction + Recovery-kit flow

**Published API:** `internal/recoverykit` (imports only `internal/repo` + `internal/config`)

```go
package recoverykit

// Kit holds non-secret recovery notes. Preserves the exact fields (and JSON
// tags) of the former cli.recoveryKit struct.
type Kit struct {
    GeneratedAt       time.Time `json:"generated_at"`
    ConfigPath        string    `json:"config_path"`
    RepoID            string    `json:"repo_id"`
    RepoCreatedAt     time.Time `json:"repo_created_at"`
    Bucket            string    `json:"bucket"`
    Prefix            string    `json:"prefix"`
    Region            string    `json:"region"`
    Profile           string    `json:"profile"`
    EndpointURL       string    `json:"endpoint_url"`
    SnapshotCount     int       `json:"snapshot_count"`
    LatestSnapshotID  string    `json:"latest_snapshot_id,omitempty"`
    LatestSnapshotAt  time.Time `json:"latest_snapshot_at,omitempty"`
    LatestSnapshotTag string    `json:"latest_snapshot_tag,omitempty"`
    Commands          []string  `json:"commands"`
}

func Build(ctx context.Context, r *repo.Repo, cfg *config.Config, cfgPath string) (Kit, error)
func RenderMarkdown(k Kit) string
func MarshalJSON(k Kit) ([]byte, error)
```

---

### Task 9: Extract non-secret recovery-kit builder/renderers into internal/recoverykit

**Files:**
- Create: `internal/recoverykit/kit.go`
- Create: `internal/recoverykit/kit_test.go`

- [ ] **Step 1: Write the failing test**

```go
package recoverykit

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// buildRealKit inits an in-memory repo, seeds one tagged snapshot, and
// returns a Kit plus the repo ID and snapshot ID for assertions.
func buildRealKit(t *testing.T) (Kit, string, string) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "latest"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.Prefix = "backups/home"
	cfg.Repo.S3.Region = "us-east-1"

	kit, err := Build(context.Background(), r, cfg, "sentra.yaml")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return kit, r.Config().ID, snap.ID
}

func TestBuild_PopulatesNonSecretFields(t *testing.T) {
	kit, repoID, snapID := buildRealKit(t)
	if kit.RepoID != repoID {
		t.Fatalf("RepoID = %q, want %q", kit.RepoID, repoID)
	}
	if kit.SnapshotCount != 1 {
		t.Fatalf("SnapshotCount = %d, want 1", kit.SnapshotCount)
	}
	if kit.LatestSnapshotID != snapID {
		t.Fatalf("LatestSnapshotID = %q, want %q", kit.LatestSnapshotID, snapID)
	}
	if kit.LatestSnapshotTag != "latest" {
		t.Fatalf("LatestSnapshotTag = %q, want latest", kit.LatestSnapshotTag)
	}
	if kit.Bucket != "sentra-prod" {
		t.Fatalf("Bucket = %q, want sentra-prod", kit.Bucket)
	}
	if len(kit.Commands) != 3 {
		t.Fatalf("Commands = %v, want 3 entries", kit.Commands)
	}
	// The restore command must name the concrete latest snapshot.
	if !strings.Contains(kit.Commands[2], snapID) {
		t.Fatalf("restore command %q must reference %q", kit.Commands[2], snapID)
	}
}

func TestBuild_NoSnapshotsUsesPlaceholder(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	kit, err := Build(context.Background(), r, &config.Config{}, "sentra.yaml")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if kit.SnapshotCount != 0 || kit.LatestSnapshotID != "" {
		t.Fatalf("empty repo kit = %+v, want zero snapshots", kit)
	}
	if !strings.Contains(kit.Commands[2], "<snapshot-id>") {
		t.Fatalf("restore command %q must use the <snapshot-id> placeholder", kit.Commands[2])
	}
}

func TestRenderMarkdown_NonSecretAndEmptyDashInlined(t *testing.T) {
	kit := Kit{
		GeneratedAt:   time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC),
		ConfigPath:    "sentra.yaml",
		RepoID:        "repo-123",
		RepoCreatedAt: time.Date(2026, 6, 1, 8, 30, 0, 0, time.UTC),
		Bucket:        "sentra-prod",
		// Prefix/Region/Profile/EndpointURL deliberately empty to exercise the
		// inlined empty->"-" path (emptyDash is NOT moved into this package).
		SnapshotCount:    1,
		LatestSnapshotID: "20260624T120000Z-abcdef",
		Commands:         []string{"sentra check --config sentra.yaml"},
	}
	md := RenderMarkdown(kit)
	if !strings.Contains(md, "# Sentra Recovery Kit") {
		t.Fatalf("markdown missing header:\n%s", md)
	}
	if !strings.Contains(md, "- Prefix: -") {
		t.Fatalf("empty prefix must render as '-':\n%s", md)
	}
	if !strings.Contains(md, "intentionally excludes passphrases") {
		t.Fatalf("markdown missing the no-secret disclaimer:\n%s", md)
	}
	for _, forbidden := range []string{"hunter2", "WrappedRepoKey", "wrapped_repo_key", "MAC", "Salt"} {
		if strings.Contains(md, forbidden) {
			t.Fatalf("markdown leaked %q:\n%s", forbidden, md)
		}
	}
}

func TestMarshalJSON_TrailingNewlineAndNoSecrets(t *testing.T) {
	kit, _, _ := buildRealKit(t)
	body, err := MarshalJSON(kit)
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("JSON should end with newline, got %q", body)
	}
	var decoded Kit
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, body)
	}
	if decoded.RepoID != kit.RepoID {
		t.Fatalf("RepoID = %q, want %q", decoded.RepoID, kit.RepoID)
	}
	for _, forbidden := range []string{"hunter2", "wrapped_repo_key", "salt", "\"mac\""} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("recovery kit JSON leaked %q:\n%s", forbidden, body)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/recoverykit/... -run TestBuild -count=1`
Expected: FAIL — build error `package github.com/markgustetic/sentra/internal/recoverykit is not in std` / `undefined: Build`, `undefined: Kit`, `undefined: RenderMarkdown`, `undefined: MarshalJSON` (the package/file does not exist yet).

- [ ] **Step 3: Write the minimal implementation**

This ports `recoveryKit`/`buildRecoveryKit`/`marshalRecoveryKitJSON`/`renderRecoveryKitMarkdown` verbatim from `internal/cli/recovery_kit.go:24-187`, renaming to the pinned public API and **inlining** the `empty->"-"` logic (a local `dash` closure) so `emptyDash` stays in `internal/cli/format.go` for its 9 other callers.

```go
// Package recoverykit builds and renders a Sentra "recovery kit": a
// non-secret record of a repository's identity, storage location, and
// latest snapshot, plus copyable check/list/restore commands. It exists
// so both the `sentra recovery-kit` CLI command and the TUI's
// Recovery-Kit view render byte-identical output from one source.
//
// Invariant: a kit contains ONLY non-secret repository and config data —
// repo ID, created timestamps, bucket/prefix/region/profile/endpoint,
// and snapshot summaries. It never reads or emits the passphrase,
// wrapped repo key, salt, MAC material, or AWS credentials. The renderers
// close with an explicit disclaimer to make that guarantee auditable.
package recoverykit

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// Kit holds the non-secret recovery notes. Field names and JSON tags are
// preserved exactly from the former cli.recoveryKit so existing kit files
// and any external consumers of the JSON keep parsing unchanged.
type Kit struct {
	GeneratedAt       time.Time `json:"generated_at"`
	ConfigPath        string    `json:"config_path"`
	RepoID            string    `json:"repo_id"`
	RepoCreatedAt     time.Time `json:"repo_created_at"`
	Bucket            string    `json:"bucket"`
	Prefix            string    `json:"prefix"`
	Region            string    `json:"region"`
	Profile           string    `json:"profile"`
	EndpointURL       string    `json:"endpoint_url"`
	SnapshotCount     int       `json:"snapshot_count"`
	LatestSnapshotID  string    `json:"latest_snapshot_id,omitempty"`
	LatestSnapshotAt  time.Time `json:"latest_snapshot_at,omitempty"`
	LatestSnapshotTag string    `json:"latest_snapshot_tag,omitempty"`
	Commands          []string  `json:"commands"`
}

// Build assembles a Kit from an opened repo and its resolved config. It
// reads only the snapshot list and the non-secret repo/config fields; it
// deliberately does not touch RepoConfig.Salt/WrappedRepoKey/MAC/KDF.
func Build(ctx context.Context, r *repo.Repo, cfg *config.Config, cfgPath string) (Kit, error) {
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return Kit{}, fmt.Errorf("list snapshots: %w", err)
	}

	repoCfg := r.Config()
	kit := Kit{
		GeneratedAt:   time.Now().UTC(),
		ConfigPath:    cfgPath,
		RepoID:        repoCfg.ID,
		RepoCreatedAt: repoCfg.CreatedAt,
		Bucket:        cfg.Repo.S3.Bucket,
		Prefix:        cfg.Repo.S3.Prefix,
		Region:        cfg.Repo.S3.Region,
		Profile:       cfg.Repo.S3.Profile,
		EndpointURL:   cfg.Repo.S3.EndpointURL,
		SnapshotCount: len(snaps),
	}
	if len(snaps) > 0 {
		latest := snaps[0]
		kit.LatestSnapshotID = latest.ID
		kit.LatestSnapshotAt = latest.CreatedAt
		kit.LatestSnapshotTag = latest.Tag
	}

	restoreID := kit.LatestSnapshotID
	if restoreID == "" {
		restoreID = "<snapshot-id>"
	}
	kit.Commands = []string{
		fmt.Sprintf("sentra check --config %s", cfgPath),
		fmt.Sprintf("sentra snapshots --config %s", cfgPath),
		fmt.Sprintf("sentra restore %s <dest-dir> --config %s --verify", restoreID, cfgPath),
	}
	return kit, nil
}

// MarshalJSON renders the kit as indented JSON with a trailing newline
// (so piping to a file leaves a POSIX-clean last line).
func MarshalJSON(k Kit) ([]byte, error) {
	body, err := json.MarshalIndent(k, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode json: %w", err)
	}
	return append(body, '\n'), nil
}

// RenderMarkdown renders a human-readable kit. Empty storage fields print
// as "-" via the local dash helper — emptyDash lives in internal/cli for
// its other callers and is intentionally not imported here (config must
// not depend on cli, and neither must this package).
func RenderMarkdown(k Kit) string {
	dash := func(s string) string {
		if s == "" {
			return "-"
		}
		return s
	}
	var b strings.Builder
	fmt.Fprintln(&b, "# Sentra Recovery Kit")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Generated: %s\n\n", k.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintln(&b, "## Repository")
	fmt.Fprintf(&b, "- Repo ID: %s\n", k.RepoID)
	fmt.Fprintf(&b, "- Created: %s\n", k.RepoCreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- Config: %s\n", k.ConfigPath)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Storage")
	fmt.Fprintf(&b, "- Bucket: %s\n", dash(k.Bucket))
	fmt.Fprintf(&b, "- Prefix: %s\n", dash(k.Prefix))
	fmt.Fprintf(&b, "- Region: %s\n", dash(k.Region))
	fmt.Fprintf(&b, "- Profile: %s\n", dash(k.Profile))
	fmt.Fprintf(&b, "- Endpoint URL: %s\n", dash(k.EndpointURL))
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Snapshots")
	fmt.Fprintf(&b, "- Snapshot count: %d\n", k.SnapshotCount)
	if k.LatestSnapshotID != "" {
		fmt.Fprintf(&b, "- Latest snapshot: %s\n", k.LatestSnapshotID)
		fmt.Fprintf(&b, "- Latest created: %s\n", k.LatestSnapshotAt.Format(time.RFC3339))
		fmt.Fprintf(&b, "- Latest tag: %s\n", dash(k.LatestSnapshotTag))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Recovery Commands")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "```bash")
	for _, command := range k.Commands {
		fmt.Fprintln(&b, command)
	}
	fmt.Fprintln(&b, "```")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "This file intentionally excludes passphrases, wrapped keys, salts, and MAC material.")
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/recoverykit/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/recoverykit/kit.go internal/recoverykit/kit_test.go
git commit -m "refactor(recoverykit): extract non-secret kit builder/renderers into internal/recoverykit"
```

---

### Task 10: Rewire cli/recovery_kit.go as a thin wrapper over internal/recoverykit

**Files:**
- Modify: `internal/cli/recovery_kit.go:1-187` (delete the ported struct/funcs; keep the cobra command and `runRecoveryKit`, delegating to `recoverykit.*`)
- Modify: `internal/cli/recovery_kit_test.go:94-132` (replace `TestMarshalRecoveryKitJSON`, which references the now-removed `recoveryKit` type and `marshalRecoveryKitJSON`, with a wrapper-level assertion; keep `TestRecoveryKit_WritesNonSecretMarkdown` unchanged)

- [ ] **Step 1: Write the failing test**

Replace the body of `TestMarshalRecoveryKitJSON` (`internal/cli/recovery_kit_test.go:94-132`) with this end-to-end wrapper test that drives the cobra command with `--json` and carries the no-secret assertions forward. `TestRecoveryKit_WritesNonSecretMarkdown` (:52-92) is left as-is — it already exercises the wrapper via `cmd.Execute()`.

```go
func TestRecoveryKit_JSONThroughCommand(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(`repo:
  s3:
    bucket: sentra-prod
    prefix: backups/home
    region: us-east-1
`), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	deps, repoID, snapID, out := recoveryKitFixture(t, "hunter2")
	cmd := NewRecoveryKit(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body := out.Bytes()
	if !bytes.HasSuffix(body, []byte("\n")) {
		t.Fatalf("JSON should end with newline, got %q", body)
	}
	got := string(body)
	for _, want := range []string{repoID, snapID, "sentra-prod"} {
		if !strings.Contains(got, want) {
			t.Fatalf("json missing %q:\n%s", want, got)
		}
	}
	for _, forbidden := range []string{"hunter2", "wrapped_repo_key", "\"salt\"", "\"mac\""} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("json leaked %q:\n%s", forbidden, got)
		}
	}
}
```

The unused-import cleanup that this test forces: after Step 3, `internal/cli/recovery_kit_test.go` no longer references `encoding/json` or `time` (the old test used both). Remove `"encoding/json"` and `"time"` from its import block so the file compiles.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/cli/... -run TestRecoveryKit -count=1`
Expected: FAIL — compile error in the package: the old `TestMarshalRecoveryKitJSON` was deleted but `recovery_kit.go` still defines `recoveryKit`/`marshalRecoveryKitJSON`; after Step 3 removes those, this test's reference to the new command path is the only survivor. First run (before Step 3) fails to build with `declared and not used`/duplicate references; the assertion path is verified after Step 3.

- [ ] **Step 3: Write the minimal implementation**

Rewrite `internal/cli/recovery_kit.go` so it keeps only the cobra command plus `runRecoveryKit`, delegating to the new package. Remove `recoveryKit`, `buildRecoveryKit`, `marshalRecoveryKitJSON`, `renderRecoveryKitMarkdown`, and the now-unused `context`/`encoding/json`/`strings`/`time` imports.

```go
package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/recoverykit"
	"github.com/markgustetic/sentra/internal/ui"
)

// RecoveryKitDeps wires `sentra recovery-kit`.
type RecoveryKitDeps struct {
	RepoDeps
}

// NewRecoveryKit returns `sentra recovery-kit`.
func NewRecoveryKit(deps RecoveryKitDeps) *cobra.Command {
	var (
		cfgPath string
		outPath string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "recovery-kit",
		Short: "Export non-secret repository recovery notes",
		Long: "Write a non-secret recovery kit containing repository identity, " +
			"storage location, latest snapshot, and copyable check/list/restore commands.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRecoveryKit(cmd, deps, cfgPath, outPath, asJSON)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().StringVar(&outPath, "out", "",
		"write the kit to this path instead of stdout")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit JSON instead of Markdown")
	return cmd
}

func runRecoveryKit(cmd *cobra.Command, deps RecoveryKitDeps, cfgPath, outPath string, asJSON bool) error {
	cmd.SilenceUsage = true

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	kit, err := recoverykit.Build(cmd.Context(), r, cfg, cfgPath)
	if err != nil {
		return err
	}

	var body []byte
	if asJSON {
		body, err = recoverykit.MarshalJSON(kit)
	} else {
		body = []byte(recoverykit.RenderMarkdown(kit))
	}
	if err != nil {
		return err
	}

	out := cmdStdout(cmd, deps.Stdout)
	if outPath != "" {
		if err := os.WriteFile(outPath, body, 0o600); err != nil {
			return fmt.Errorf("write recovery kit: %w", err)
		}
		fmt.Fprintf(out, "%s %s\n", ui.Success.Render("Recovery kit written:"), outPath)
		return nil
	}
	_, err = out.Write(body)
	return err
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/cli/... -run TestRecoveryKit -count=1`
Expected: PASS (both `TestRecoveryKit_WritesNonSecretMarkdown` and `TestRecoveryKit_JSONThroughCommand`)

- [ ] **Step 5: Commit**
```bash
git add internal/cli/recovery_kit.go internal/cli/recovery_kit_test.go
git commit -m "refactor(cli): make recovery-kit a thin wrapper over internal/recoverykit"
```

---

### Task 11: Add the read-only RecoveryKitView (idle → running → done + save)

**Files:**
- Create: `internal/tui/recoverykit.go`
- Create: `internal/tui/recoverykit_test.go`
- Modify: `internal/tui/app.go:155-164` (register the view in `NewApp`'s `views` slice)

The view follows the read-only spinner pattern (check.go/diff.go): idle → running (spinner while `recoverykit.Build` lists snapshots) → done (rendered `RenderMarkdown` in a `bubbles/viewport`). `recoveryKitDoneMsg` is **NOT** an `opResultMsg` (a Build is a read-only snapshot list, same as check). The save sub-action reveals a `textinput` for a path, writes `0o600` via `os.WriteFile`, and renders an inline save-status line (there is no shell toast type; this matches restore.go's in-view `notice` banner). `Deps.ConfigPath` (added by Unit 1) is passed to `Build`; when empty the view falls back to `"sentra.yaml"` so the emitted commands stay valid.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRecoveryKitFlow_RunsAndRendersMarkdown(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r) // one snapshot so the kit has a latest entry

	v := NewRecoveryKitView(Deps{Repo: r, ConfigPath: "sentra.yaml"})
	// Enter kicks off Build: view moves to running and returns a batch
	// (spinner tick + the build goroutine).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	if v.stage != rkRunning {
		t.Fatalf("stage = %v, want rkRunning", v.stage)
	}
	if cmd == nil {
		t.Fatal("enter must start the build")
	}

	var done tea.Msg
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			done = msg
		}
	}
	if done == nil {
		t.Fatal("build command did not produce a recoveryKitDoneMsg")
	}
	// A recoveryKitDoneMsg must NOT be an opResultMsg — Build is read-only.
	if _, ok := done.(opResultMsg); ok {
		t.Fatal("recoveryKitDoneMsg must not implement opResult()")
	}
	m, _ = v.Update(done)
	v = m.(RecoveryKitView)
	if v.stage != rkDone {
		t.Fatalf("stage after result = %v, want rkDone", v.stage)
	}
	out := v.View()
	for _, want := range []string{"Sentra Recovery Kit", "Recovery Commands"} {
		if !strings.Contains(out, want) {
			t.Errorf("kit view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "flow-test-pass") {
		t.Fatalf("kit view leaked the passphrase:\n%s", out)
	}
}

func TestRecoveryKitFlow_SaveWritesFile(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)

	v := NewRecoveryKitView(Deps{Repo: r, ConfigPath: "sentra.yaml"})
	// Drive to done.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(RecoveryKitView)
		}
	}
	if v.stage != rkDone {
		t.Fatalf("stage = %v, want rkDone", v.stage)
	}

	// 's' reveals the save path input.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)
	if v.stage != rkSaving {
		t.Fatalf("stage after 's' = %v, want rkSaving", v.stage)
	}

	// Type a path and confirm.
	dst := filepath.Join(t.TempDir(), "kit.md")
	for _, ch := range dst {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RecoveryKitView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)

	body, err := os.ReadFile(dst) //nolint:gosec // path under t.TempDir()
	if err != nil {
		t.Fatalf("read saved kit: %v", err)
	}
	if !strings.Contains(string(body), "# Sentra Recovery Kit") {
		t.Fatalf("saved file is not a rendered kit:\n%s", body)
	}
	// 0o600 — no group/other bits.
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("saved kit perms = %o, want 600", perm)
	}
	if v.stage != rkDone {
		t.Fatalf("stage after save = %v, want rkDone", v.stage)
	}
	if !strings.Contains(v.View(), dst) {
		t.Fatalf("done view should confirm the written path:\n%s", v.View())
	}
}

func TestRecoveryKitFlow_SaveErrorSurfaced(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)

	v := NewRecoveryKitView(Deps{Repo: r})
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)
	for _, msg := range execCmds(t, cmd) {
		if _, ok := msg.(recoveryKitDoneMsg); ok {
			m, _ = v.Update(msg)
			v = m.(RecoveryKitView)
		}
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	v = m.(RecoveryKitView)

	// A path whose parent dir does not exist forces os.WriteFile to fail.
	bad := filepath.Join(t.TempDir(), "no-such-dir", "kit.md")
	for _, ch := range bad {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}})
		v = m.(RecoveryKitView)
	}
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(RecoveryKitView)

	// Stays in the saving stage with an error banner rather than crashing.
	if v.stage != rkSaving {
		t.Fatalf("stage after failed save = %v, want rkSaving", v.stage)
	}
	if v.saveErr == "" {
		t.Fatal("failed save must set saveErr")
	}
}

func TestRecoveryKitFlow_NilRepoPlaceholder(t *testing.T) {
	v := NewRecoveryKitView(Deps{})
	if !strings.Contains(v.View(), "no repository") {
		t.Errorf("nil-repo view should show a placeholder:\n%s", v.View())
	}
}

func TestRecoveryKitView_RegisteredInApp(t *testing.T) {
	app := NewApp(Deps{Repo: newFlowRepo(t), Ctx: context.Background()})
	found := false
	for _, v := range app.views {
		if v.id == "recovery-kit" {
			found = true
			if _, ok := v.model.(RecoveryKitView); !ok {
				t.Fatalf("recovery-kit view model = %T, want RecoveryKitView", v.model)
			}
		}
	}
	if !found {
		t.Fatal("recovery-kit view not registered in NewApp")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/... -run TestRecoveryKit -count=1`
Expected: FAIL — compile error `undefined: NewRecoveryKitView`, `undefined: RecoveryKitView`, `undefined: recoveryKitDoneMsg`, `undefined: rkRunning`/`rkDone`/`rkSaving` (the view does not exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/tui/recoverykit.go`. The viewport init mirrors `agent.go:124`; the save-path input mirrors `restore.go`'s dest textinput; the Build goroutine mirrors `check.go`'s `run` closure. `recoveryKitDoneMsg` has no `opResult()` method, so it is not an `opResultMsg`.

```go
package tui

import (
	"context"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/recoverykit"
	"github.com/markgustetic/sentra/internal/ui"
)

type rkStage int

const (
	rkIdle rkStage = iota
	rkRunning
	rkDone
	rkSaving
)

// recoveryKitDoneMsg carries the built kit (and its rendered markdown)
// back to the flow. Build is a READ-ONLY snapshot-list read, so this is
// deliberately NOT an opResultMsg — it must never take the mutating-op
// guard and can run alongside a backup.
type recoveryKitDoneMsg struct {
	markdown string
	err      error
}

// RecoveryKitView renders a non-secret recovery kit. Building it lists
// snapshots (a repo with many manifests can take a moment), so the build
// runs asynchronously behind a spinner like the Check and Diff views.
// The kit contains only non-secret identity/storage/snapshot data — the
// renderer in internal/recoverykit guarantees no passphrase, wrapped key,
// salt, or MAC ever appears.
type RecoveryKitView struct {
	deps   Deps
	stage  rkStage
	spin   spinner.Model
	vp     viewport.Model
	err    string

	// save sub-action state.
	savePath textinput.Model
	saveErr  string
	saved    string // last successfully written path (shown on done)

	width  int
	height int
}

func NewRecoveryKitView(deps Deps) RecoveryKitView {
	s := spinner.New()
	s.Spinner = spinner.Dot

	vp := viewport.New(80, 12)
	vp.SetContent("")

	ti := textinput.New()
	ti.Prompt = "save to> "
	ti.Placeholder = "path/to/recovery-kit.md"

	return RecoveryKitView{deps: deps, spin: s, vp: vp, savePath: ti}
}

func (RecoveryKitView) Init() tea.Cmd { return nil }

func (v RecoveryKitView) Title() string { return "Recovery Kit" }

func (v RecoveryKitView) ShortHelp() []key.Binding {
	switch v.stage {
	case rkRunning:
		return nil
	case rkDone:
		return []key.Binding{
			key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "save")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "rebuild")),
		}
	case rkSaving:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "write")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "build kit"))}
	}
}

func (v RecoveryKitView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.vp.Width = max(40, msg.Width-2)
		v.vp.Height = max(6, msg.Height-6)
		return v, nil

	case recoveryKitDoneMsg:
		v.stage = rkDone
		if msg.err != nil {
			v.err = msg.err.Error()
			return v, nil
		}
		v.err = ""
		v.vp.SetContent(msg.markdown)
		v.vp.GotoTop()
		return v, nil

	case spinner.TickMsg:
		if v.stage == rkRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v RecoveryKitView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case rkSaving:
		switch msg.Type {
		case tea.KeyEsc:
			v.stage = rkDone
			v.saveErr = ""
			return v, nil
		case tea.KeyEnter:
			return v.writeKit()
		}
		var cmd tea.Cmd
		v.savePath, cmd = v.savePath.Update(msg)
		v.saveErr = ""
		return v, cmd

	case rkDone:
		switch {
		case msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's':
			v.stage = rkSaving
			v.saveErr = ""
			v.savePath.SetValue("")
			v.savePath.Focus()
			return v, nil
		case msg.Type == tea.KeyEnter:
			return v.startBuild()
		}
		var cmd tea.Cmd
		v.vp, cmd = v.vp.Update(msg) // scroll keys
		return v, cmd

	default: // rkIdle
		if msg.Type == tea.KeyEnter && v.deps.Repo != nil {
			return v.startBuild()
		}
		return v, nil
	}
}

// startBuild moves to the running stage and launches recoverykit.Build in
// a plain goroutine (read-only, so no op guard), batched with the spinner
// tick — the same shape as CheckView.
func (v RecoveryKitView) startBuild() (tea.Model, tea.Cmd) {
	if v.deps.Repo == nil {
		return v, nil
	}
	v.stage = rkRunning
	v.saved = ""
	r := v.deps.Repo
	cfg := v.deps.Config
	cfgPath := v.deps.ConfigPath
	if cfgPath == "" {
		cfgPath = "sentra.yaml"
	}
	ctx := ctxOrBackground(v.deps.Ctx)
	run := func() tea.Msg {
		kit, err := recoverykit.Build(ctx, r, cfg, cfgPath)
		if err != nil {
			return recoveryKitDoneMsg{err: err}
		}
		return recoveryKitDoneMsg{markdown: recoverykit.RenderMarkdown(kit)}
	}
	return v, tea.Batch(v.spin.Tick, run)
}

// writeKit persists the rendered markdown to the typed path at 0o600.
// On failure it stays in the saving stage with an error banner (mirroring
// restore.go's in-view notices) rather than pushing a modal — the save is
// a view-local action, not an App-guarded operation.
func (v RecoveryKitView) writeKit() (tea.Model, tea.Cmd) {
	path := strings.TrimSpace(v.savePath.Value())
	if path == "" {
		v.saveErr = "destination path is required"
		return v, nil
	}
	if err := os.WriteFile(path, []byte(v.vp.View()), 0o600); err != nil {
		v.saveErr = err.Error()
		return v, nil
	}
	v.saved = path
	v.saveErr = ""
	v.stage = rkDone
	return v, nil
}

func (v RecoveryKitView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	switch v.stage {
	case rkRunning:
		return v.spin.View() + " building recovery kit…"
	case rkDone, rkSaving:
		return v.renderKit()
	default:
		return ui.Primary.Render("Recovery kit") + "\n\n" +
			ui.Muted.Render("Build a non-secret record of this repo's identity, storage, and latest snapshot.") +
			"\n\n" + ui.Muted.Render("⏎ build kit")
	}
}

func (v RecoveryKitView) renderKit() string {
	if v.err != "" {
		return ui.Danger.Render("Recovery kit failed") + "\n\n" + v.err
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Recovery kit") + "\n\n")
	b.WriteString(v.vp.View())
	if v.stage == rkSaving {
		b.WriteString("\n\n" + v.savePath.View())
		if v.saveErr != "" {
			b.WriteString("\n" + ui.Danger.Render(v.saveErr))
		}
		b.WriteString("\n" + ui.Muted.Render("⏎ write · esc cancel"))
		return b.String()
	}
	if v.saved != "" {
		b.WriteString("\n\n" + ui.Success.Render("Saved: ") + v.saved)
	}
	b.WriteString("\n\n" + ui.Muted.Render("s save · ⏎ rebuild"))
	return b.String()
}
```

The `writeKit` implementation writes `v.vp.View()` — the viewport's rendered content, which is exactly the markdown set via `SetContent` in the done handler. Because the done handler sets the markdown before any save, and the save test drives to `rkDone` first, the viewport content is the full `RenderMarkdown` output at save time.

Register the view in `internal/tui/app.go`. Modify the `views` slice (`app.go:155-164`) to append the new entry after `prune` (it defaults to the "Views" category since it is not in the `categories` map, which is correct — it is read-only):

```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "recovery-kit", model: NewRecoveryKitView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
	}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/... -run TestRecoveryKit -count=1`
Expected: PASS (all five `TestRecoveryKit*` cases)

- [ ] **Step 5: Commit**
```bash
git add internal/tui/recoverykit.go internal/tui/recoverykit_test.go internal/tui/app.go
git commit -m "feat(tui): add read-only Recovery-Kit view with save sub-action"
```

---

Notes for downstream readers / reviewers:

- The extraction (`internal/recoverykit`) imports only `internal/repo` + `internal/config`, matching the pinned constraint. `emptyDash` was **not** moved (still in `internal/cli/format.go`, 9 callers); its logic is inlined as a local `dash` closure in `RenderMarkdown`.
- Verified no-secret guarantee at source: `Build` reads `RepoConfig.ID` and `RepoConfig.CreatedAt` only (config.go:36,40) and never `Salt` (:38), `WrappedRepoKey` (:39), `KDF` (:37), or `MAC` (:58); config side reads only S3 `Bucket/Prefix/Region/Profile/EndpointURL`. The no-secret assertions are carried into both `internal/recoverykit/kit_test.go` (markdown + JSON) and the cli wrapper test.
- The Recovery-Kit view depends on `Deps.ConfigPath` (added by Unit 1). It degrades gracefully to `"sentra.yaml"` when that field is empty, so it compiles and passes even before Unit 1 lands, but the emitted `--config` commands are only fully accurate once Unit 1 populates `ConfigPath`.
- `recoveryKitDoneMsg` intentionally has **no** `opResult()` method (asserted in `TestRecoveryKitFlow_RunsAndRendersMarkdown`), so the read-only Build never trips the App's one-op-at-a-time guard — consistent with `checkDoneMsg`/`diffDoneMsg`.


## Part 4 — internal/scheduler extraction + Schedule flow

**Published API:** `internal/scheduler` (imports only `internal/config` + `internal/policy`; no cobra, no `io.Writer`):

```go
package scheduler

// Paths locates the OS scheduler files for one policy. OS is "darwin"|"linux".
type Paths struct {
	OS    string   // "darwin" or "linux"
	Home  string   // resolved home dir (used for the plist log path)
	Files []string // absolute file paths this policy installs
}

// PathsFor validates name and returns the per-OS scheduler file paths.
// goos "" defaults to runtime.GOOS; home "" defaults to os.UserHomeDir().
func PathsFor(goos, home, name string) (Paths, error)

// Executable normalizes exe to a cleaned absolute path. exe "" defaults to os.Executable().
func Executable(exe string) (string, error)

// Render returns path->file-body for every file the policy installs.
// Rejects a manual/unsupported cadence and a malformed clock.
func Render(paths Paths, exe, cfgPath, name string, schedule config.PolicySchedule) (map[string]string, error)

// Install writes every rendered file (0o600, dirs 0o755). It is the only
// mutating helper; callers gate it behind confirmation.
func Install(files map[string]string) error

// Installed reports whether every file in paths.Files exists on disk.
// A non-ENOENT stat error is returned so callers can surface it.
func Installed(paths Paths) (bool, error)

// Uninstall removes every file in paths.Files, tolerating already-absent files.
func Uninstall(paths Paths) error
```

All pure/deterministic except `Install`/`Installed`/`Uninstall`, which touch only the user's own home directory — never a repo lock or the bucket.

---

### Task 12: Extract schedule render helpers into internal/scheduler (pure)

**Files:**
- Create: `internal/scheduler/render.go`
- Create: `internal/scheduler/render_test.go`

- [ ] **Step 1: Write the failing test**
```go
package scheduler

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestRender_DarwinLaunchAgent(t *testing.T) {
	paths, err := PathsFor("darwin", "/home/u", "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	files, err := Render(paths, "/usr/local/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "daily", At: "03:00"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("want 1 file, got %d", len(files))
	}
	body := files[paths.Files[0]]
	for _, want := range []string{
		"com.sentra.home", "/usr/local/bin/sentra", "policy", "run", "home",
		"--config", "/etc/sentra.yaml",
		"<key>Hour</key>", "<integer>3</integer>",
		"<key>Minute</key>", "<integer>0</integer>",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("plist missing %q:\n%s", want, body)
		}
	}
}

func TestRender_LinuxSystemdUnits(t *testing.T) {
	paths, err := PathsFor("linux", "/home/u", "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	files, err := Render(paths, "/usr/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "weekly", Weekday: "mon", At: "04:30"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	service := files[paths.Files[0]]
	timer := files[paths.Files[1]]
	for _, want := range []string{"/usr/bin/sentra", "policy run home", "--config", "/etc/sentra.yaml", "--log-level info"} {
		if !strings.Contains(service, want) {
			t.Fatalf("service missing %q:\n%s", want, service)
		}
	}
	if !strings.Contains(timer, "OnCalendar=Mon *-*-* 04:30:00") {
		t.Fatalf("timer missing weekly calendar:\n%s", timer)
	}
}

func TestRender_RejectsManualCadence(t *testing.T) {
	paths, _ := PathsFor("linux", "/home/u", "home")
	if _, err := Render(paths, "/usr/bin/sentra", "/etc/sentra.yaml", "home",
		config.PolicySchedule{Cadence: "manual"}); err == nil {
		t.Fatal("manual cadence must be rejected by Render")
	}
}

func TestRender_RejectsUnsupportedOS(t *testing.T) {
	if _, err := PathsFor("plan9", "/home/u", "home"); err == nil {
		t.Fatal("PathsFor(plan9) must error")
	}
	if _, err := Render(Paths{OS: "plan9", Files: []string{"x"}}, "/bin/sentra", "/c.yaml", "home",
		config.PolicySchedule{Cadence: "daily", At: "03:00"}); err == nil {
		t.Fatal("Render with unsupported OS must error")
	}
}

// TestRender_RejectsSignedClock: the systemd calendar renderer must reject a
// signed clock rather than emitting a malformed "*-*-* +9:00:00" OnCalendar
// spec that systemd silently refuses to schedule.
func TestRender_RejectsSignedClock(t *testing.T) {
	for _, cad := range []string{"daily", "weekly", "monthly"} {
		if _, err := systemdOnCalendar(config.PolicySchedule{Cadence: cad, At: "+9:00", Weekday: "mon"}); err == nil {
			t.Errorf("systemdOnCalendar(%s, +9:00) = nil error, want a rejection", cad)
		}
	}
	got, err := systemdOnCalendar(config.PolicySchedule{Cadence: "daily", At: "09:00"})
	if err != nil {
		t.Fatalf("valid clock rejected: %v", err)
	}
	if !strings.Contains(got, "09:00:00") {
		t.Errorf("daily 09:00 rendered as %q, want it to contain 09:00:00", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/scheduler/... -run TestRender -count=1`
Expected: FAIL to compile with `undefined: PathsFor`, `undefined: Render`, `undefined: Paths`, `undefined: systemdOnCalendar` (package `scheduler` does not exist yet).

- [ ] **Step 3: Write the minimal implementation**
This is a verbatim port of `internal/cli/schedule_render.go` plus the `PathsFor`/`Executable` helpers carved out of `schedule.go:191-251`. The `schedulePaths` struct is renamed to the exported `Paths`; `renderScheduleFiles` becomes `Render`; `schedulerPaths`/`scheduleExecutable` become `PathsFor`/`Executable` and drop their `ScheduleDeps` parameter in favor of plain `goos`/`home`/`exe` strings. All private helpers (`renderLaunchAgent`, `launchdCalendar`, `renderSystemdUserUnits`, `systemdOnCalendar`, `systemdDailyCalendar`, `scheduleClock`, `isTwoASCIIDigits`, `launchdWeekday`, `systemdWeekday`, `xmlEscape`, `systemdQuoteArg`, and the `launchdCalendarEntry` type) are copied unchanged except that `paths.OS`/`paths.Home`/`paths.Files` now name the exported `Paths` fields (they already do — the field names are unchanged).

```go
// Package scheduler renders and installs user-level OS scheduler entries
// (launchd plists on darwin, systemd user units on linux) that run
// `sentra policy run`. It is a pure/filesystem-only helper: it never opens
// a repository, takes the repo lock, or touches the bucket. Imports are
// limited to internal/config and internal/policy so the TUI and CLI can both
// depend on it without pulling in cobra or the repo layer.
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
)

// Paths locates the OS scheduler files for one policy.
type Paths struct {
	OS    string
	Home  string
	Files []string
}

// PathsFor validates name and returns the per-OS scheduler file paths.
// goos "" defaults to runtime.GOOS; home "" defaults to os.UserHomeDir().
func PathsFor(goos, home, name string) (Paths, error) {
	if err := policycfg.ValidateName(name); err != nil {
		return Paths{}, err
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("locate home dir: %w", err)
		}
		home = h
	}
	switch goos {
	case "darwin":
		return Paths{
			OS:    goos,
			Home:  home,
			Files: []string{filepath.Join(home, "Library", "LaunchAgents", "com.sentra."+name+".plist")},
		}, nil
	case "linux":
		dir := filepath.Join(home, ".config", "systemd", "user")
		return Paths{
			OS:   goos,
			Home: home,
			Files: []string{
				filepath.Join(dir, "sentra-"+name+".service"),
				filepath.Join(dir, "sentra-"+name+".timer"),
			},
		}, nil
	default:
		return Paths{}, fmt.Errorf("unsupported scheduler OS %q; supported: darwin, linux", goos)
	}
}

// Executable normalizes exe to a cleaned absolute path. exe "" defaults to
// os.Executable().
func Executable(exe string) (string, error) {
	if exe == "" {
		e, err := os.Executable()
		if err != nil {
			return "", fmt.Errorf("locate sentra executable: %w", err)
		}
		exe = e
	}
	if !filepath.IsAbs(exe) {
		abs, err := filepath.Abs(exe)
		if err != nil {
			return "", fmt.Errorf("resolve sentra executable: %w", err)
		}
		exe = abs
	}
	return filepath.Clean(exe), nil
}

// Render returns path->file-body for every file the policy installs.
func Render(paths Paths, exe, cfgPath, name string, schedule config.PolicySchedule) (map[string]string, error) {
	switch paths.OS {
	case "darwin":
		body, err := renderLaunchAgent(paths.Home, exe, cfgPath, name, schedule)
		if err != nil {
			return nil, err
		}
		return map[string]string{paths.Files[0]: body}, nil
	case "linux":
		service, timer, err := renderSystemdUserUnits(exe, cfgPath, name, schedule)
		if err != nil {
			return nil, err
		}
		return map[string]string{
			paths.Files[0]: service,
			paths.Files[1]: timer,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported scheduler OS %q", paths.OS)
	}
}

func renderLaunchAgent(home, exe, cfgPath, name string, schedule config.PolicySchedule) (string, error) {
	cal, err := launchdCalendar(schedule)
	if err != nil {
		return "", err
	}
	args := []string{exe, "policy", "run", name, "--config", cfgPath, "--log-level", "info"}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.sentra.` + xmlEscape(name) + `</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range args {
		fmt.Fprintf(&b, "    <string>%s</string>\n", xmlEscape(arg))
	}
	b.WriteString(`  </array>
  <key>StartCalendarInterval</key>
  <dict>
`)
	for _, entry := range cal {
		fmt.Fprintf(&b, "    <key>%s</key>\n", entry.Key)
		fmt.Fprintf(&b, "    <integer>%d</integer>\n", entry.Value)
	}
	logPath := filepath.Join(home, "Library", "Logs", "sentra-"+name+".log")
	fmt.Fprintf(&b, `  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, xmlEscape(logPath), xmlEscape(logPath))
	return b.String(), nil
}

type launchdCalendarEntry struct {
	Key   string
	Value int
}

func launchdCalendar(schedule config.PolicySchedule) ([]launchdCalendarEntry, error) {
	s := policycfg.NormalizeSchedule(schedule)
	hour, minute, err := scheduleClock(s)
	if err != nil {
		return nil, err
	}
	entries := []launchdCalendarEntry{
		{Key: "Hour", Value: hour},
		{Key: "Minute", Value: minute},
	}
	switch s.Cadence {
	case policycfg.CadenceDaily:
		return entries, nil
	case policycfg.CadenceWeekly:
		entries = append(entries, launchdCalendarEntry{Key: "Weekday", Value: launchdWeekday(s.Weekday)})
		return entries, nil
	case policycfg.CadenceMonthly:
		entries = append(entries, launchdCalendarEntry{Key: "Day", Value: 1})
		return entries, nil
	default:
		return nil, fmt.Errorf("unsupported launchd cadence %q", s.Cadence)
	}
}

func renderSystemdUserUnits(exe, cfgPath, name string, schedule config.PolicySchedule) (service, timer string, err error) {
	onCalendar, err := systemdOnCalendar(schedule)
	if err != nil {
		return "", "", err
	}
	execStart := strings.Join([]string{
		systemdQuoteArg(exe),
		"policy",
		"run",
		systemdQuoteArg(name),
		"--config",
		systemdQuoteArg(cfgPath),
		"--log-level",
		"info",
	}, " ")
	service = fmt.Sprintf(`[Unit]
Description=Sentra policy %s backup

[Service]
Type=oneshot
ExecStart=%s
`, name, execStart)
	timer = fmt.Sprintf(`[Unit]
Description=Sentra policy %s schedule

[Timer]
OnCalendar=%s
Persistent=true
Unit=sentra-%s.service

[Install]
WantedBy=timers.target
`, name, onCalendar, name)
	return service, timer, nil
}

func systemdOnCalendar(schedule config.PolicySchedule) (string, error) {
	s := policycfg.NormalizeSchedule(schedule)
	switch s.Cadence {
	case policycfg.CadenceHourly:
		return "hourly", nil
	case policycfg.CadenceDaily:
		return systemdDailyCalendar(s)
	case policycfg.CadenceWeekly:
		hh, mm, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s *-*-* %02d:%02d:00", systemdWeekday(s.Weekday), hh, mm), nil
	case policycfg.CadenceMonthly:
		hh, mm, err := scheduleClock(s)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("*-*-01 %02d:%02d:00", hh, mm), nil
	default:
		return "", fmt.Errorf("unsupported systemd cadence %q", s.Cadence)
	}
}

func systemdDailyCalendar(s config.PolicySchedule) (string, error) {
	// Render from the parsed hour/minute rather than interpolating s.At
	// verbatim, so a malformed clock can never reach the OnCalendar spec.
	hh, mm, err := scheduleClock(s)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("*-*-* %02d:%02d:00", hh, mm), nil
}

func scheduleClock(schedule config.PolicySchedule) (int, int, error) {
	hourText, minuteText, ok := strings.Cut(schedule.At, ":")
	if !ok {
		return 0, 0, fmt.Errorf("schedule requires HH:MM")
	}
	// Require exactly two ASCII digits per field. strconv.Atoi accepts a
	// leading sign, so "+9" would parse to 9 and (on the systemd path)
	// render a malformed OnCalendar spec that never fires. Reject it here
	// too so the render path is robust independent of upstream validation.
	if !isTwoASCIIDigits(hourText) || !isTwoASCIIDigits(minuteText) {
		return 0, 0, fmt.Errorf("schedule requires HH:MM with two digits per field, got %q", schedule.At)
	}
	hour, err := strconv.Atoi(hourText)
	if err != nil || hour > 23 {
		return 0, 0, fmt.Errorf("schedule hour out of range: %q", hourText)
	}
	minute, err := strconv.Atoi(minuteText)
	if err != nil || minute > 59 {
		return 0, 0, fmt.Errorf("schedule minute out of range: %q", minuteText)
	}
	return hour, minute, nil
}

// isTwoASCIIDigits reports whether s is exactly two ASCII digits.
func isTwoASCIIDigits(s string) bool {
	return len(s) == 2 && s[0] >= '0' && s[0] <= '9' && s[1] >= '0' && s[1] <= '9'
}

func launchdWeekday(day string) int {
	switch strings.ToLower(day) {
	case "mon":
		return 1
	case "tue":
		return 2
	case "wed":
		return 3
	case "thu":
		return 4
	case "fri":
		return 5
	case "sat":
		return 6
	default:
		return 0
	}
}

func systemdWeekday(day string) string {
	switch strings.ToLower(day) {
	case "mon":
		return "Mon"
	case "tue":
		return "Tue"
	case "wed":
		return "Wed"
	case "thu":
		return "Thu"
	case "fri":
		return "Fri"
	case "sat":
		return "Sat"
	default:
		return "Sun"
	}
}

func xmlEscape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&apos;",
	)
	return replacer.Replace(s)
}

func systemdQuoteArg(s string) string {
	if s == "" || strings.ContainsAny(s, " \t\n\"'\\") {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	return s
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/scheduler/... -run TestRender -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/scheduler/render.go internal/scheduler/render_test.go
git commit -m "refactor(scheduler): extract pure schedule render helpers into internal/scheduler"
```

---

### Task 13: Add Install/Installed/Uninstall filesystem helpers to internal/scheduler

**Files:**
- Create: `internal/scheduler/install.go`
- Create: `internal/scheduler/install_test.go`

- [ ] **Step 1: Write the failing test**
```go
package scheduler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestInstallStatusUninstall_RoundTrip(t *testing.T) {
	home := t.TempDir()
	paths, err := PathsFor("linux", home, "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}

	// Not installed before Install.
	installed, err := Installed(paths)
	if err != nil {
		t.Fatalf("Installed (pre): %v", err)
	}
	if installed {
		t.Fatal("Installed = true before any files written")
	}

	files, err := Render(paths, "/usr/bin/sentra", filepath.Join(home, "sentra.yaml"), "home",
		config.PolicySchedule{Cadence: "hourly"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Install(files); err != nil {
		t.Fatalf("Install: %v", err)
	}

	installed, err = Installed(paths)
	if err != nil {
		t.Fatalf("Installed (post): %v", err)
	}
	if !installed {
		t.Fatal("Installed = false after Install")
	}
	for _, p := range paths.Files {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file %s perm = %o, want 600", p, perm)
		}
	}

	if err := Uninstall(paths); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	installed, err = Installed(paths)
	if err != nil {
		t.Fatalf("Installed (after uninstall): %v", err)
	}
	if installed {
		t.Fatal("Installed = true after Uninstall")
	}
	// Uninstall is idempotent: a second call with files already gone is fine.
	if err := Uninstall(paths); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestInstalled_PartialIsNotInstalled(t *testing.T) {
	home := t.TempDir()
	paths, err := PathsFor("linux", home, "home") // 2 files
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	// Write only the first file; Installed must report false.
	if err := os.MkdirAll(filepath.Dir(paths.Files[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Files[0], []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	installed, err := Installed(paths)
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if installed {
		t.Fatal("Installed = true with only one of two files present")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/scheduler/... -run TestInstall -count=1`
Expected: FAIL to compile with `undefined: Install`, `undefined: Installed`, `undefined: Uninstall`.

- [ ] **Step 3: Write the minimal implementation**
The bodies are lifted from the `runScheduleInstall` write loop (`schedule.go:103-110`), the `runScheduleStatus` stat loop (`schedule.go:130-139`), and the `runScheduleUninstall` remove loop (`schedule.go:161-165`).

```go
package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
)

// Install writes every rendered file to disk, creating parent dirs (0o755)
// and writing bodies at 0o600. It is the sole mutating helper; callers gate
// it behind explicit confirmation. The files hold no secrets — only the
// executable path, the --config path, and the schedule spec — so 0o600 is a
// defense-in-depth choice, not a secrecy requirement.
func Install(files map[string]string) error {
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create scheduler dir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write scheduler file %s: %w", path, err)
		}
	}
	return nil
}

// Installed reports whether every file in paths.Files exists. A missing file
// yields false; any other stat error is returned so callers surface it rather
// than silently reporting "not installed".
func Installed(paths Paths) (bool, error) {
	installed := true
	for _, path := range paths.Files {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				installed = false
				continue
			}
			return false, fmt.Errorf("stat scheduler file %s: %w", path, err)
		}
	}
	return installed, nil
}

// Uninstall removes every file in paths.Files, tolerating already-absent
// files so a re-run (or an OS that never had all files) is a no-op.
func Uninstall(paths Paths) error {
	for _, path := range paths.Files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove scheduler file %s: %w", path, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/scheduler/... -run TestInstall -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/scheduler/install.go internal/scheduler/install_test.go
git commit -m "feat(scheduler): add Install/Installed/Uninstall filesystem helpers"
```

---

### Task 14: Rewire cli/schedule.go RunE bodies as thin wrappers over internal/scheduler

**Files:**
- Modify: `internal/cli/schedule.go:83-233` (RunE bodies + `schedulePaths`/`schedulerPaths`/`scheduleExecutable`)
- Delete: `internal/cli/schedule_render.go` (all helpers moved to `internal/scheduler`)
- Modify: `internal/cli/schedule_test.go:203-222` (`TestSystemdOnCalendar_RejectsSignedClock` moved to the scheduler package)

- [ ] **Step 1: Write the failing test**
No new test is written for this task — the extraction must keep the existing `schedule_test.go` cobra-level tests (`TestScheduleInstall_DarwinWritesLaunchAgent`, `..._LinuxWritesSystemdUserFiles`, `TestScheduleStatusAndUninstall`, `..._RejectsManualPolicy`, `..._RejectsUnsupportedOS`) green. The one test that referenced a now-moved unexported symbol, `TestSystemdOnCalendar_RejectsSignedClock` (`schedule_test.go:203-222`, calls `systemdOnCalendar`), is deleted here because it was reproduced as `TestRender_RejectsSignedClock` in `internal/scheduler/render_test.go` (task 1). Removing it is the change that must compile.

- [ ] **Step 2: Run test to verify it fails**
Before editing, delete `schedule_render.go` and run the package build to see the failure the wiring must fix:
Run: `go build ./internal/cli/... && go test ./internal/cli/... -run TestSchedule -count=1`
Expected: FAIL to compile — `undefined: renderScheduleFiles`, `undefined: systemdOnCalendar`, `schedulePaths` unused, etc. (the CLI still references helpers that now live only in `internal/scheduler`).

- [ ] **Step 3: Write the minimal implementation**
Delete `internal/cli/schedule_render.go` entirely, delete `TestSystemdOnCalendar_RejectsSignedClock` from `schedule_test.go`, and replace `schedule.go` lines 83-233 (the three RunE helpers plus `schedulePaths`, `schedulerPaths`, `scheduleExecutable`) with wrappers that delegate to `internal/scheduler`. The `loadScheduledPolicy` helper (`schedule.go:172-189`), `scheduleStdout` (`schedule.go:253-258`), and the `ScheduleDeps`/`NewSchedule`/`newSchedule*` command constructors (`schedule.go:17-81`) are unchanged. Add the `scheduler` import.

Update imports at `schedule.go:3-15` — drop `runtime` (now only used inside the scheduler package) and add the scheduler import:

```go
import (
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/ui"
)
```

Replace `runScheduleInstall` (`schedule.go:83-120`):

```go
func runScheduleInstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	p, absConfig, err := loadScheduledPolicy(cfgPath, name)
	if err != nil {
		return err
	}
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		return fmt.Errorf("policy %q has manual schedule; use `sentra policy add --schedule ... --replace` before installing", name)
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	exe, err := scheduler.Executable(scheduleExe(deps))
	if err != nil {
		return err
	}
	files, err := scheduler.Render(paths, exe, absConfig, name, p.Schedule)
	if err != nil {
		return err
	}
	if err := scheduler.Install(files); err != nil {
		return err
	}

	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule installed"))
	fmt.Fprintf(out, "  policy:   %s\n", name)
	fmt.Fprintf(out, "  schedule: %s\n", policycfg.FormatScheduleSpec(p.Schedule))
	for _, path := range paths.Files {
		fmt.Fprintf(out, "  file:     %s\n", path)
	}
	return nil
}
```

Replace `runScheduleStatus` (`schedule.go:122-151`):

```go
func runScheduleStatus(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	if _, _, err := loadScheduledPolicy(cfgPath, name); err != nil {
		return err
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	installed, err := scheduler.Installed(paths)
	if err != nil {
		return err
	}
	out := scheduleStdout(cmd, deps)
	if installed {
		fmt.Fprintln(out, ui.Success.Render("Schedule installed"))
	} else {
		fmt.Fprintln(out, ui.Subtle.Render("Schedule not installed"))
	}
	fmt.Fprintf(out, "  policy: %s\n", name)
	for _, path := range paths.Files {
		fmt.Fprintf(out, "  file:   %s\n", path)
	}
	return nil
}
```

Replace `runScheduleUninstall` (`schedule.go:153-170`):

```go
func runScheduleUninstall(cmd *cobra.Command, deps ScheduleDeps, cfgPath, name string) error {
	if _, _, err := loadScheduledPolicy(cfgPath, name); err != nil {
		return err
	}
	home, err := scheduleHome(deps)
	if err != nil {
		return err
	}
	paths, err := scheduler.PathsFor(scheduleOS(deps), home, name)
	if err != nil {
		return err
	}
	if err := scheduler.Uninstall(paths); err != nil {
		return err
	}
	out := scheduleStdout(cmd, deps)
	fmt.Fprintln(out, ui.Success.Render("Schedule removed"))
	fmt.Fprintf(out, "  policy: %s\n", name)
	return nil
}
```

Delete `schedulePaths`/`schedulerPaths`/`scheduleExecutable` (`schedule.go:191-251`) and replace them with three small `ScheduleDeps`-reading shims that feed the scheduler package's plain-string parameters (these keep the `deps.OS == ""` / nil-func defaulting the tests rely on):

```go
// scheduleOS returns the target GOOS, honoring the deps override used by
// tests; "" lets scheduler.PathsFor fall back to runtime.GOOS.
func scheduleOS(deps ScheduleDeps) string { return deps.OS }

// scheduleHome resolves the home dir via the deps hook (tests inject a temp
// dir) or the OS default. It resolves here rather than passing "" to
// scheduler.PathsFor so the deps.HomeDir override is honored.
func scheduleHome(deps ScheduleDeps) (string, error) {
	if deps.HomeDir == nil {
		return "", nil // let scheduler.PathsFor default to os.UserHomeDir
	}
	home, err := deps.HomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home dir: %w", err)
	}
	return home, nil
}

// scheduleExe returns the executable path from the deps hook (tests inject a
// fixed path); "" lets scheduler.Executable fall back to os.Executable.
func scheduleExe(deps ScheduleDeps) string {
	if deps.Executable == nil {
		return ""
	}
	exe, err := deps.Executable()
	if err != nil {
		return ""
	}
	return exe
}
```

Note: `scheduleExe` swallows the deps.Executable error by returning "", which then triggers `scheduler.Executable`'s `os.Executable()` fallback. The existing tests always supply a non-erroring `Executable` func, so this preserves their behavior; a real deps.Executable failure now surfaces later from `scheduler.Executable` instead. If exact error-passthrough is wanted, `scheduleExe` can be changed to return `(string, error)` and callers updated — but the current tests do not exercise that path, so the simpler form keeps them green.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/cli/... -run TestSchedule -count=1 && go test ./internal/scheduler/... -count=1`
Expected: PASS (all five cobra-level schedule tests and both scheduler-package suites).

- [ ] **Step 5: Commit**
```bash
git add internal/cli/schedule.go internal/cli/schedule_render.go internal/cli/schedule_test.go
git commit -m "refactor(cli): make schedule RunE thin wrappers over internal/scheduler"
```

---

### Task 15: Add the read-only ScheduleView (status table over policies)

**Files:**
- Create: `internal/tui/schedule.go`
- Create: `internal/tui/schedule_test.go`
- Modify: `internal/tui/app.go:155-169` (register the view + category)

- [ ] **Step 1: Write the failing test**
The view is package `tui`, so tests set unexported fields directly. `deps.Config.Policies` drives the rows; `deps.ConfigPath` is embedded in Install. Tests inject `osOverride`/`homeOverride`/`exeOverride` (unexported fields the constructor leaves at their zero value in production) so `scheduler.PathsFor`/`Render` hit a temp home instead of the real one.

```go
package tui

import (
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

func scheduleTestDeps(t *testing.T, home string) Deps {
	t.Helper()
	cfg := config.Defaults()
	cfg.Policies["home"] = config.PolicyConfig{
		Paths:    []string{"~/Documents"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
	}
	cfg.Policies["docs"] = config.PolicyConfig{
		Paths:    []string{"~/docs"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	return Deps{Config: &cfg, ConfigPath: filepath.Join(home, "sentra.yaml")}
}

// newScheduleTestView builds a ScheduleView pinned to a temp home and a
// linux target so Install/Uninstall/Installed touch only the temp tree.
func newScheduleTestView(t *testing.T, home string) ScheduleView {
	t.Helper()
	v := NewScheduleView(scheduleTestDeps(t, home))
	v.osOverride = "linux"
	v.homeOverride = home
	v.exeOverride = "/usr/bin/sentra"
	v.reload()
	return v
}

func TestScheduleView_ListsPoliciesWithCadence(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	out := v.View()
	for _, want := range []string{"home", "daily@03:00", "docs", "manual", "not installed"} {
		if !strings.Contains(out, want) {
			t.Errorf("schedule view missing %q:\n%s", want, out)
		}
	}
}

func TestScheduleView_InstallConfirmFlow(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	// Cursor is on the first policy ("docs" or "home" — rows are sorted).
	// Move to the "home" (daily) row so Install has a real schedule.
	v.selectPolicy("home")

	// Press 'i' → a confirm modal is requested (pushModalMsg), nothing on disk yet.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	msgs := execCmds(t, cmd)
	var pushed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed = true
			_ = pm
		}
	}
	if !pushed {
		t.Fatal("pressing i must request a confirm modal")
	}
	installed, err := scheduleInstalledFor(t, v, "home")
	if err != nil {
		t.Fatalf("installed check: %v", err)
	}
	if installed {
		t.Fatal("files written before confirmation")
	}

	// Confirm → files are written and the row flips to installed.
	m, cmd = v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	// The install command is a quick tea.Cmd returning scheduleDoneMsg.
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	installed, err = scheduleInstalledFor(t, v, "home")
	if err != nil {
		t.Fatalf("installed check post-confirm: %v", err)
	}
	if !installed {
		t.Fatal("files not written after confirmation")
	}
	if !strings.Contains(v.View(), "installed") {
		t.Errorf("view should reflect installed state:\n%s", v.View())
	}
}

func TestScheduleView_UninstallConfirmFlow(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	v.selectPolicy("home")

	// Install first (confirm path).
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	_ = execCmds(t, cmd)
	m, cmd = v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "home"); !installed {
		t.Fatal("precondition: install failed")
	}

	// Press 'u' → confirm modal; confirm → files removed.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	v = m.(ScheduleView)
	m, cmd = v.Update(confirmedMsg{id: scheduleUninstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "home"); installed {
		t.Fatal("files still present after uninstall")
	}
}

func TestScheduleView_ManualPolicyInstallErrors(t *testing.T) {
	home := t.TempDir()
	v := newScheduleTestView(t, home)
	v.selectPolicy("docs") // manual cadence

	// Confirm an install for a manual policy: the run reports an error and
	// nothing is written.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("i")})
	v = m.(ScheduleView)
	m, cmd := v.Update(confirmedMsg{id: scheduleInstallID})
	v = m.(ScheduleView)
	for _, msg := range execCmds(t, cmd) {
		m, _ = v.Update(msg)
		v = m.(ScheduleView)
	}
	if installed, _ := scheduleInstalledFor(t, v, "docs"); installed {
		t.Fatal("manual policy should not install any files")
	}
	if v.notice == "" {
		t.Error("manual install should surface a notice")
	}
}

func TestScheduleView_NilConfigPlaceholder(t *testing.T) {
	v := NewScheduleView(Deps{})
	if !strings.Contains(v.View(), "no policies") {
		t.Errorf("empty-config view should show a placeholder:\n%s", v.View())
	}
}
```

Plus this test-only helper appended to `schedule_test.go`, which reuses the view's own override fields to check disk state:

```go
// scheduleInstalledFor reports whether the named policy's files exist under
// the view's overridden home/OS.
func scheduleInstalledFor(t *testing.T, v ScheduleView, name string) (bool, error) {
	t.Helper()
	paths, err := schedulerPathsFor(v, name)
	if err != nil {
		return false, err
	}
	return schedulerInstalled(paths)
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/... -run TestScheduleView -count=1`
Expected: FAIL to compile with `undefined: NewScheduleView`, `undefined: scheduleInstallID`, `undefined: scheduleUninstallID`, `ScheduleView has no field osOverride`, `undefined: schedulerPathsFor`, `undefined: schedulerInstalled`.

- [ ] **Step 3: Write the minimal implementation**
A read-only spinner-free view (all operations are quick filesystem stats/writes, so — like Diff's inline Diff call — they run in a fast `tea.Cmd` that returns a `scheduleDoneMsg`; no repo lock is ever taken). Install/Uninstall are confirmation-gated with the simple `ConfirmModal` per the confirmation policy (they are reversible, so no typed confirm). The view reads `deps.Config.Policies` and `deps.ConfigPath`.

```go
package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"

	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
	"github.com/markgustetic/sentra/internal/ui"
)

// scheduleInstallID / scheduleUninstallID tie a confirm modal back to this
// flow. Both use the simple ConfirmModal: installing or removing a scheduler
// entry is reversible (the inverse action restores the prior state), so the
// typed-confirm gate reserved for destructive/irreversible operations does
// not apply here.
const (
	scheduleInstallID   = "schedule-install"
	scheduleUninstallID = "schedule-uninstall"
)

// scheduleRow is one policy's line in the status table.
type scheduleRow struct {
	name      string
	spec      string // policy.FormatScheduleSpec
	installed bool
	manual    bool
}

// scheduleDoneMsg carries the result of a quick install/uninstall/refresh.
// This is a filesystem-only action that NEVER takes the repo lock, so it is
// deliberately NOT an opResultMsg — it does not contend for the mutating-op
// guard and can run alongside a backup.
type scheduleDoneMsg struct {
	notice string
	err    error
}

// ScheduleView lists the configured policies with their cadence and whether
// their OS scheduler entry is installed, and lets the user install/uninstall
// one entry (each behind a simple confirm). It is filesystem-only: it reads
// deps.Config.Policies, stats the scheduler files, and writes/removes them
// under the user's home dir. It never opens the repository or takes the repo
// lock — hence the read-only view pattern (no op guard).
type ScheduleView struct {
	deps   Deps
	tbl    table.Model
	rows   []scheduleRow
	notice string
	width  int

	// osOverride/homeOverride/exeOverride pin the target platform, home dir,
	// and executable for tests. Zero values let the scheduler package fall
	// back to runtime.GOOS / os.UserHomeDir / os.Executable in production.
	osOverride   string
	homeOverride string
	exeOverride  string
}

func NewScheduleView(deps Deps) ScheduleView {
	v := ScheduleView{deps: deps}
	v.tbl = table.New(
		table.WithColumns(scheduleColumns(pickerIdealWidth)),
		table.WithFocused(true),
	)
	v.reload()
	return v
}

func (ScheduleView) Init() tea.Cmd { return nil }

func (v ScheduleView) Title() string { return "Schedule" }

func (v ScheduleView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "policy")),
		key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "install")),
		key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "uninstall")),
		key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "refresh")),
	}
}

// reload rebuilds the rows from deps.Config.Policies and re-stats each
// policy's scheduler files. Called at construction, after every mutating
// action, and on 'r'. A stat error for one policy is folded into notice
// rather than aborting the whole table.
func (v *ScheduleView) reload() {
	names := make([]string, 0)
	if v.deps.Config != nil {
		for name := range v.deps.Config.Policies {
			names = append(names, name)
		}
	}
	sort.Strings(names)

	rows := make([]scheduleRow, 0, len(names))
	tblRows := make([]table.Row, 0, len(names))
	for _, name := range names {
		p := v.deps.Config.Policies[name]
		norm := policycfg.NormalizeSchedule(p.Schedule)
		row := scheduleRow{
			name:   name,
			spec:   policycfg.FormatScheduleSpec(p.Schedule),
			manual: norm.Cadence == policycfg.CadenceManual,
		}
		if !row.manual {
			paths, err := schedulerPathsFor(*v, name)
			if err == nil {
				if installed, sErr := schedulerInstalled(paths); sErr == nil {
					row.installed = installed
				}
			}
		}
		rows = append(rows, row)
		tblRows = append(tblRows, table.Row{name, row.spec, scheduleStateLabel(row)})
	}
	v.rows = rows
	// Preserve the cursor across a reload where possible.
	cursor := v.tbl.Cursor()
	v.tbl.SetRows(tblRows)
	if cursor >= len(tblRows) {
		cursor = len(tblRows) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	v.tbl.SetCursor(cursor)
}

func scheduleStateLabel(r scheduleRow) string {
	switch {
	case r.manual:
		return "—"
	case r.installed:
		return "installed"
	default:
		return "not installed"
	}
}

// selectPolicy moves the cursor to the named policy; used by tests and by
// the reload cursor-preservation logic.
func (v *ScheduleView) selectPolicy(name string) {
	for i, r := range v.rows {
		if r.name == name {
			v.tbl.SetCursor(i)
			return
		}
	}
}

func (v ScheduleView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.tbl.SetColumns(scheduleColumns(pickerContentWidth(v.width)))
		v.tbl.SetHeight(max(msg.Height-8, 3))
		return v, nil

	case scheduleDoneMsg:
		v.notice = msg.notice
		if msg.err != nil {
			v.notice = msg.err.Error()
		}
		v.reload()
		return v, nil

	case confirmedMsg:
		switch msg.id {
		case scheduleInstallID:
			return v.runInstall()
		case scheduleUninstallID:
			return v.runUninstall()
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v ScheduleView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'i':
			row, ok := v.current()
			if !ok {
				return v, nil
			}
			v.notice = ""
			body := fmt.Sprintf("Install the %s scheduler entry for policy %q?\nThis writes files under your home directory only.",
				v.osLabel(), row.name)
			modal := NewConfirmModal("Install schedule", body, scheduleInstallID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		case 'u':
			row, ok := v.current()
			if !ok {
				return v, nil
			}
			v.notice = ""
			body := fmt.Sprintf("Remove the scheduler entry for policy %q?", row.name)
			modal := NewConfirmModal("Uninstall schedule", body, scheduleUninstallID, 80, 24)
			return v, func() tea.Msg { return pushModalMsg{modal: modal} }
		case 'r':
			v.notice = ""
			v.reload()
			return v, nil
		}
	}
	var cmd tea.Cmd
	v.tbl, cmd = v.tbl.Update(msg)
	return v, cmd
}

// runInstall renders and writes the selected policy's scheduler files in a
// quick tea.Cmd. Rejects a manual cadence (mirrors the CLI) and folds any
// render/write error into the notice.
func (v ScheduleView) runInstall() (tea.Model, tea.Cmd) {
	row, ok := v.current()
	if !ok {
		return v, nil
	}
	name := row.name
	cfgPath := v.deps.ConfigPath
	p := v.deps.Config.Policies[name]
	goos := v.osValue()
	home := v.homeOverride
	exeOverride := v.exeOverride
	run := func() tea.Msg {
		if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
			return scheduleDoneMsg{err: fmt.Errorf("policy %q has a manual schedule; set a cadence before installing", name)}
		}
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		exe, err := scheduler.Executable(exeOverride)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		files, err := scheduler.Render(paths, exe, cfgPath, name, p.Schedule)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		if err := scheduler.Install(files); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{notice: fmt.Sprintf("installed schedule for %q", name)}
	}
	return v, run
}

// runUninstall removes the selected policy's scheduler files in a quick
// tea.Cmd.
func (v ScheduleView) runUninstall() (tea.Model, tea.Cmd) {
	row, ok := v.current()
	if !ok {
		return v, nil
	}
	name := row.name
	goos := v.osValue()
	home := v.homeOverride
	run := func() tea.Msg {
		paths, err := scheduler.PathsFor(goos, home, name)
		if err != nil {
			return scheduleDoneMsg{err: err}
		}
		if err := scheduler.Uninstall(paths); err != nil {
			return scheduleDoneMsg{err: err}
		}
		return scheduleDoneMsg{notice: fmt.Sprintf("removed schedule for %q", name)}
	}
	return v, run
}

// current returns the row under the cursor.
func (v ScheduleView) current() (scheduleRow, bool) {
	i := v.tbl.Cursor()
	if i < 0 || i >= len(v.rows) {
		return scheduleRow{}, false
	}
	return v.rows[i], true
}

// osValue is the effective GOOS for scheduler calls ("" → runtime.GOOS).
func (v ScheduleView) osValue() string { return v.osOverride }

// osLabel is the human label for the target platform in confirm bodies.
func (v ScheduleView) osLabel() string {
	switch v.osOverride {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd"
	default:
		return "OS"
	}
}

func (v ScheduleView) View() string {
	if v.deps.Config == nil || len(v.rows) == 0 {
		return ui.Muted.Render("no policies configured — add one with `sentra policy add`")
	}
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Policy schedules") + "\n\n")
	b.WriteString(v.tbl.View() + "\n\n")
	if v.notice != "" {
		b.WriteString(ui.Warn.Render(v.notice) + "\n\n")
	}
	b.WriteString(ui.Muted.Render("i install · u uninstall · r refresh"))
	return b.String()
}

// scheduleColumns lays out the status table columns within the given
// interior width, mirroring how snapshotPickerColumns splits its budget.
func scheduleColumns(width int) []table.Column {
	if width < 24 {
		width = 24
	}
	state := 14
	spec := 18
	name := width - state - spec
	if name < 8 {
		name = 8
	}
	return []table.Column{
		{Title: "Policy", Width: name},
		{Title: "Schedule", Width: spec},
		{Title: "State", Width: state},
	}
}

// schedulerPathsFor resolves the scheduler.Paths for one policy under the
// view's overrides. A thin adapter so tests (and reload) share one call.
func schedulerPathsFor(v ScheduleView, name string) (scheduler.Paths, error) {
	return scheduler.PathsFor(v.osValue(), v.homeOverride, name)
}

// schedulerInstalled is a package-local alias so tests don't import the
// scheduler package directly.
func schedulerInstalled(paths scheduler.Paths) (bool, error) {
	return scheduler.Installed(paths)
}
```

Note on `pickerIdealWidth`/`pickerContentWidth`: these already exist in the `tui` package (used by Diff/Snapshots pickers, `snapshotPickerColumns` in restore/diff). `scheduleColumns` reuses the same width budget so the table fits the content panel.

Register the view in `app.go`. Modify the `views` slice (`app.go:155-164`) to add the schedule entry after `prune`:

```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "schedule", model: NewScheduleView(deps)},
	}
```

Schedule is read-only, so it stays in the default "Views" category — the `categories` map (`app.go:167-169`) is left unchanged (no entry for `"schedule"`).

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/... -run TestScheduleView -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/schedule.go internal/tui/schedule_test.go internal/tui/app.go
git commit -m "feat(tui): add read-only Schedule view with confirm-gated install/uninstall"
```

---

### Task 16: Full-package gate (build, vet, race tests, tidy)

**Files:**
- Verify only (no edits): `internal/scheduler/*`, `internal/cli/schedule*.go`, `internal/tui/*`

- [ ] **Step 1: Write the failing test**
No new test. This task runs the repo-wide gate to confirm the extraction did not break other CLI callers of the deleted `schedule_render.go` symbols and that `go mod tidy` stays clean (the new `internal/scheduler` package adds no external dependency — it imports only `internal/config`, `internal/policy`, and stdlib).

- [ ] **Step 2: Run to verify current state**
Run: `go build ./... && go vet ./...`
Expected: On a clean extraction this passes; if any stray reference to `renderScheduleFiles`, `schedulerPaths`, `scheduleExecutable`, or `systemdOnCalendar` remains in `internal/cli`, the build FAILs with `undefined:` — fix by routing it through `internal/scheduler` before continuing.

- [ ] **Step 3: Run the race tests**
Run: `go test -race ./internal/scheduler/... ./internal/cli/... ./internal/tui/... -count=1`
Expected: PASS

- [ ] **Step 4: Confirm formatting and tidiness**
Run: `gofmt -l cmd internal && go mod tidy -diff && git diff --check`
Expected: no output from any of the three (no unformatted files, no go.mod/go.sum drift, no whitespace errors).

- [ ] **Step 5: Commit**
No commit — this is a verification gate. If any step required a fix, fold that fix into the relevant task's commit above rather than creating a new one.

---

**Cross-unit note for the assembler:** this unit references the new `Deps.ConfigPath string` field (Unit 1 adds it to `internal/tui/app.go`'s `Deps` and populates it in `internal/cli/ui.go`). If Unit 1 has not landed when this unit is assembled, add `ConfigPath string` to the `Deps` struct (`app.go:40-69`) as a temporary shim; Unit 1's version supersedes it. No other new `Deps` fields (`NewStore`, `Actions`, `SaveKeyringPassphrase`) are used by this unit.


## Part 5 — internal/diag extraction + Doctor flow

**Published API:**

New package `internal/diag` (imports only `aws-sdk-go-v2` + `github.com/markgustetic/sentra/internal/config`; imports **no** other internal package, so `internal/tui` and `internal/cli` may both depend on it without a cycle):

```go
package diag

// AWSReport mirrors the fields of the old cli.AWSInspectReport.
type AWSReport struct {
    BucketAccessible          bool
    PublicAccessReadable      bool
    PublicAccessBlocked       bool
    DefaultEncryptionReadable bool
    DefaultEncryptionEnabled  bool
}

func CheckSDKIdentity(ctx context.Context, cfg *config.Config) error
func Inspect(ctx context.Context, cfg *config.Config) (AWSReport, error)
func ValidateBucketName(bucket string) error
```

`internal/cli` after this unit: `AWSInspectReport` is replaced by a type alias `type AWSInspectReport = diag.AWSReport`; `DefaultAWSCheckSDKIdentity`/`DefaultAWSInspect` become thin wrappers delegating to `diag.CheckSDKIdentity`/`diag.Inspect`; `validateSetupBucketName` delegates to `diag.ValidateBucketName`. The mutating setup helpers (`headBucket`, `loadSetupAWSConfig`, `createBucket`, etc.) stay in `cli` for `DefaultAWSPrepare`; `diag` carries its own private read-only copies.

---

### Task 17: Create internal/diag with AWSReport, CheckSDKIdentity, Inspect, ValidateBucketName

**Files:**
- Create: `internal/diag/aws.go`
- Create: `internal/diag/bucket.go`
- Test: `internal/diag/diag_test.go`

- [ ] **Step 1: Write the failing test**

```go
package diag

import (
	"strings"
	"testing"
)

func TestValidateBucketName(t *testing.T) {
	tests := []struct {
		name       string
		bucket     string
		wantErr    bool
		wantSubstr string
	}{
		{name: "valid simple", bucket: "sentra-prod", wantErr: false},
		{name: "valid dotted", bucket: "my.sentra.bucket", wantErr: false},
		{name: "too short", bucket: "ab", wantErr: true, wantSubstr: "3-63"},
		{name: "too long", bucket: strings.Repeat("a", 64), wantErr: true, wantSubstr: "3-63"},
		{name: "uppercase rejected", bucket: "Bad_Bucket", wantErr: true, wantSubstr: "lowercase"},
		{name: "ip address rejected", bucket: "192.168.0.1", wantErr: true, wantSubstr: "IP addresses"},
		{name: "leading hyphen", bucket: "-nope", wantErr: true, wantSubstr: "start and end"},
		{name: "trailing dot", bucket: "nope.", wantErr: true, wantSubstr: "start and end"},
		{name: "adjacent dots", bucket: "a..b", wantErr: true, wantSubstr: "adjacent dots"},
		{name: "dot next to hyphen", bucket: "a.-b", wantErr: true, wantSubstr: "next to hyphens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBucketName(tt.bucket)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateBucketName(%q) = nil, want error", tt.bucket)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateBucketName(%q) = %v, want nil", tt.bucket, err)
			}
			if tt.wantErr && tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("ValidateBucketName(%q) error %q missing %q", tt.bucket, err.Error(), tt.wantSubstr)
			}
		})
	}
}

// TestAWSReportZeroValue guards the field set diag.AWSReport must expose so
// callers (cli wrapper + DoctorView) can rely on the exact shape.
func TestAWSReportZeroValue(t *testing.T) {
	r := AWSReport{
		BucketAccessible:          true,
		PublicAccessReadable:      true,
		PublicAccessBlocked:       true,
		DefaultEncryptionReadable: true,
		DefaultEncryptionEnabled:  true,
	}
	if !r.BucketAccessible || !r.PublicAccessReadable || !r.PublicAccessBlocked ||
		!r.DefaultEncryptionReadable || !r.DefaultEncryptionEnabled {
		t.Fatal("AWSReport fields did not round-trip")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/diag/... -run 'TestValidateBucketName|TestAWSReportZeroValue' -count=1`
Expected: FAIL — build error, `undefined: ValidateBucketName` and `undefined: AWSReport` (package `internal/diag` does not exist yet).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/diag/aws.go` (port of the read-only halves of `internal/cli/setup_awss3.go:14-65` and the read-only helpers from `setup_awss3_ops.go`; the mutating helpers stay in `cli`):

```go
// Package diag holds read-only environment diagnostics shared by the
// `sentra doctor` CLI command and the TUI Doctor view. It imports only
// the AWS SDK and internal/config so both internal/cli and internal/tui
// can depend on it without an import cycle.
package diag

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

// AWSReport summarizes read-only AWS bucket diagnostics. It mirrors the
// former cli.AWSInspectReport field-for-field so the cli wrapper can
// expose it as a type alias.
type AWSReport struct {
	BucketAccessible          bool
	PublicAccessReadable      bool
	PublicAccessBlocked       bool
	DefaultEncryptionReadable bool
	DefaultEncryptionEnabled  bool
}

// CheckSDKIdentity verifies credentials through the AWS SDK credential
// chain Sentra will use for S3. Read-only: it calls sts:GetCallerIdentity
// and nothing else, so it never mutates the account.
func CheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return err
	}
	client := sts.NewFromConfig(awsCfg)
	if _, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("verify AWS identity: %w", err)
	}
	return nil
}

// Inspect performs the read-only AWS checks for `sentra doctor` and the
// TUI Doctor view: bucket reachability, public-access-block state, and
// default-encryption state. It issues only Head/Get calls — never a Put —
// so it is safe to run against a production bucket.
func Inspect(ctx context.Context, cfg *config.Config) (AWSReport, error) {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return AWSReport{}, err
	}
	client := s3.NewFromConfig(awsCfg)
	bucket := cfg.Repo.S3.Bucket
	report := AWSReport{}
	if err := headBucket(ctx, client, bucket); err != nil {
		return AWSReport{}, err
	}
	report.BucketAccessible = true

	readPublic, blocked, err := getBucketPublicAccessBlock(ctx, client, bucket)
	if err != nil {
		return AWSReport{}, err
	}
	report.PublicAccessReadable = readPublic
	report.PublicAccessBlocked = blocked

	readEncryption, encrypted, err := getBucketDefaultEncryption(ctx, client, bucket)
	if err != nil {
		return AWSReport{}, err
	}
	report.DefaultEncryptionReadable = readEncryption
	report.DefaultEncryptionEnabled = encrypted
	return report, nil
}

func loadAWSConfig(ctx context.Context, cfg *config.Config) (aws.Config, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg != nil {
		if cfg.Repo.S3.Region != "" {
			loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Repo.S3.Region))
		}
		if cfg.Repo.S3.Profile != "" {
			loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.Repo.S3.Profile))
		}
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return awsCfg, nil
}

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, bucketARN(bucket), err)
	}
	return nil
}

func getBucketPublicAccessBlock(ctx context.Context, client *s3.Client, bucket string) (bool, bool, error) {
	out, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isAWSAPIErrCode(err, "NoSuchPublicAccessBlockConfiguration", "NoSuchPublicAccessBlock") {
			return true, false, nil
		}
		return false, false, fmt.Errorf("inspect public access block for bucket %q (requires s3:GetBucketPublicAccessBlock on %s): %w", bucket, bucketARN(bucket), err)
	}
	cfg := out.PublicAccessBlockConfiguration
	blocked := cfg != nil &&
		aws.ToBool(cfg.BlockPublicAcls) &&
		aws.ToBool(cfg.IgnorePublicAcls) &&
		aws.ToBool(cfg.BlockPublicPolicy) &&
		aws.ToBool(cfg.RestrictPublicBuckets)
	return true, blocked, nil
}

func getBucketDefaultEncryption(ctx context.Context, client *s3.Client, bucket string) (bool, bool, error) {
	out, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isAWSAPIErrCode(err, "ServerSideEncryptionConfigurationNotFoundError") {
			return true, false, nil
		}
		return false, false, fmt.Errorf("inspect default encryption for bucket %q (requires s3:GetBucketEncryption on %s): %w", bucket, bucketARN(bucket), err)
	}
	cfg := out.ServerSideEncryptionConfiguration
	return true, cfg != nil && len(cfg.Rules) > 0, nil
}

func isAWSAPIErrCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

func bucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}
```

Create `internal/diag/bucket.go` (verbatim port of `internal/cli/setup_summary.go:161-194`, exported):

```go
package diag

import (
	"fmt"
	"net"
	"strings"
)

// ValidateBucketName rejects S3 bucket names Sentra cannot use before any
// network call is made. The rules mirror AWS's general-purpose bucket
// naming constraints (3-63 chars, lowercase, no IP-shaped names, no
// adjacent dots, dots not adjacent to hyphens) so `sentra doctor` and the
// TUI Doctor view fail fast with a specific message instead of surfacing
// an opaque AWS 400.
func ValidateBucketName(bucket string) error {
	bucket = strings.TrimSpace(bucket)
	if len(bucket) < 3 || len(bucket) > 63 {
		return fmt.Errorf("repo.s3.bucket %q is invalid: S3 bucket names must be 3-63 characters", bucket)
	}
	if net.ParseIP(bucket) != nil {
		return fmt.Errorf("repo.s3.bucket %q is invalid: S3 bucket names cannot be formatted as IP addresses", bucket)
	}
	if bucket[0] == '-' || bucket[0] == '.' || bucket[len(bucket)-1] == '-' || bucket[len(bucket)-1] == '.' {
		return fmt.Errorf("repo.s3.bucket %q is invalid: bucket names must start and end with a lowercase letter or number", bucket)
	}
	prevDot := false
	for _, r := range bucket {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("repo.s3.bucket %q is invalid: use lowercase letters, numbers, dots, and hyphens only", bucket)
		}
		if r == '.' {
			if prevDot {
				return fmt.Errorf("repo.s3.bucket %q is invalid: bucket names cannot contain adjacent dots", bucket)
			}
			prevDot = true
			continue
		}
		if prevDot && r == '-' {
			return fmt.Errorf("repo.s3.bucket %q is invalid: dots cannot sit next to hyphens", bucket)
		}
		prevDot = false
	}
	if strings.Contains(bucket, "-.") {
		return fmt.Errorf("repo.s3.bucket %q is invalid: dots cannot sit next to hyphens", bucket)
	}
	return nil
}
```

Note the original message for `Bad_Bucket` (an underscore, which hits the character-class branch) is "use lowercase letters, numbers, dots, and hyphens only" — the doctor test asserts the substring "lowercase", which this message contains, and `ValidateBucketName` preserves it exactly.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/diag/... -run 'TestValidateBucketName|TestAWSReportZeroValue' -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/diag/aws.go internal/diag/bucket.go internal/diag/diag_test.go
git commit -m "feat(diag): extract read-only AWS + bucket-name diagnostics into internal/diag

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 18: Rewire cli doctor + setup to delegate to internal/diag

**Files:**
- Modify: `internal/cli/setup_awss3.go:14-65` (replace `AWSInspectReport` type + `DefaultAWSCheckSDKIdentity` + `DefaultAWSInspect` with alias + thin wrappers)
- Modify: `internal/cli/setup_awss3_ops.go` (delete the read-only getters now living only in `internal/diag` — see Step 3b — so the `unused` linter stays clean)
- Modify: `internal/cli/setup_summary.go:161-194` (replace `validateSetupBucketName` body with a delegating one-liner)
- Test: `internal/cli/doctor_test.go` (add the alias-identity test; otherwise the regression guard)

- [ ] **Step 1: Write the failing test**

No new test file. The guard is the existing `internal/cli/doctor_test.go` (`TestDoctor_AWSAndRepoHealthy`, `TestDoctor_InvalidBucketFailsBeforeAWS`, `TestDoctor_NotInitializedIsGuidance`, `TestDoctor_RegisteredOnRoot`), which references `AWSInspectReport`, `DefaultAWSCheckSDKIdentity` (indirectly via nil fallback), and the "lowercase" bucket message. After the edits in Step 3, these must still compile and pass.

Add one assertion to `internal/cli/doctor_test.go` proving the alias identity — that `cli.AWSInspectReport` and `diag.AWSReport` are the same type (so no field drift can slip in):

```go
func TestAWSInspectReportIsDiagAlias(t *testing.T) {
	// Assigning a diag.AWSReport to an AWSInspectReport variable without a
	// conversion only compiles when AWSInspectReport is a type alias for
	// diag.AWSReport. This pins the two in lockstep.
	var _ AWSInspectReport = diag.AWSReport{}
	var _ diag.AWSReport = AWSInspectReport{}
}
```

Add the import `"github.com/markgustetic/sentra/internal/diag"` to `doctor_test.go`.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/cli/... -run 'TestAWSInspectReportIsDiagAlias|TestDoctor' -count=1`
Expected: FAIL — build error, `AWSInspectReport` is still a concrete struct (not an alias) so `var _ AWSInspectReport = diag.AWSReport{}` reports `cannot use diag.AWSReport{} (value of struct type diag.AWSReport) as AWSInspectReport value in variable declaration`, and `internal/diag` is imported but the type is not yet aliased.

- [ ] **Step 3: Write the minimal implementation**

In `internal/cli/setup_awss3.go`, replace lines 14-65 (the `AWSInspectReport` struct, `DefaultAWSCheckSDKIdentity`, and `DefaultAWSInspect`) with an alias plus wrappers, and add the `diag` import. `DefaultAWSPrepare` (lines 67-122) is unchanged and keeps using the cli-local mutating helpers.

```go
// AWSInspectReport is an alias for diag.AWSReport, preserved so existing
// DoctorDeps callers and tests keep compiling after the read-only AWS
// diagnostics moved to internal/diag.
type AWSInspectReport = diag.AWSReport

// DefaultAWSCheckSDKIdentity verifies credentials through the AWS SDK
// credential chain. Thin wrapper over diag.CheckSDKIdentity so the
// doctor's nil-fallback and setup's identity checker keep their names.
func DefaultAWSCheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return diag.CheckSDKIdentity(ctx, cfg)
}

// DefaultAWSInspect performs the read-only AWS checks for `sentra doctor`.
// Thin wrapper over diag.Inspect.
func DefaultAWSInspect(ctx context.Context, cfg *config.Config) (AWSInspectReport, error) {
	return diag.Inspect(ctx, cfg)
}
```

Update the import block of `setup_awss3.go`: it no longer needs `sts` (moved to diag) but still needs `s3`, `blobstore`, `config`, `fmt`, `context`, and now `diag`. The final import block:

```go
import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)
```

(`s3` remains used by `DefaultAWSPrepare` via `store.Client()`; drop the now-unused `sts` import — `gofmt`/`go vet` in `just check` will flag it if missed.)

In `internal/cli/setup_summary.go`, replace the entire `validateSetupBucketName` function body (lines 161-194) with a delegation, and drop the now-unused `net`/`strings` imports if they are no longer referenced elsewhere in the file (they are not — `printSetupSummary` etc. use only `fmt`, `io`, and `ui`):

```go
func validateSetupBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
```

Update `setup_summary.go` imports to drop `net` and `strings` and add `diag`:

```go
import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/ui"
)
```

The other `validateSetupBucketName` callers (`setup.go:213,380`, `setup_iam_policy.go:43`, `setup_wizard.go:323,462`) are unchanged — they still call the same `cli`-local name, which now forwards to `diag`.

- [ ] **Step 3b: Delete the now-dead read-only getters from `internal/cli/setup_awss3_ops.go`**

Rewiring `DefaultAWSInspect` to `diag.Inspect` (Step 3) removes the only callers of the read-only S3 getters — `getBucketPublicAccessBlock` (was called at `setup_awss3.go:51`) and `getBucketDefaultEncryption` (was `setup_awss3.go:58`) — and `isAWSAPIErrCode` is called *only* from inside those two (`setup_awss3_ops.go:109,126`). All three now live in `internal/diag` (Task 17). Go does not error on unused package funcs, so Step 4's `go test` would pass — but the repo's `.golangci.yml` enables the `unused` linter, so leaving them makes `golangci-lint run` fail at the final gate (Task 27) with `func getBucketPublicAccessBlock is unused`.

Delete all three functions (`getBucketPublicAccessBlock`, `getBucketDefaultEncryption`, `isAWSAPIErrCode`) from `internal/cli/setup_awss3_ops.go` and drop any imports they alone used (e.g. the `smithy`/`errors` import if no other function in the file references it — `goimports`/`go vet` will flag leftovers). **Keep** the mutating helpers `loadSetupAWSConfig`, `headBucket`, `isS3BucketMissing`, `createBucket`, `waitForBucketExists`, `blockBucketPublicAccess`, `enableBucketDefaultEncryption` — they are still reached via the unchanged `DefaultAWSPrepare`. Verify: `grep -rn "getBucketPublicAccessBlock\|getBucketDefaultEncryption\|isAWSAPIErrCode" internal/cli` must return zero hits after this step.

- [ ] **Step 4: Run test to verify it passes, and confirm no dead code**
Run: `go test ./internal/cli/... -run 'TestAWSInspectReportIsDiagAlias|TestDoctor|TestSetup' -count=1 && golangci-lint run ./internal/cli/... ./internal/diag/...`
Expected: PASS (all doctor and setup tests still green; alias identity holds) and `0 issues` (no `unused` findings from the moved getters).

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_awss3.go internal/cli/setup_awss3_ops.go internal/cli/setup_summary.go internal/cli/doctor_test.go
git commit -m "refactor(cli): delegate doctor/setup AWS + bucket diagnostics to internal/diag

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 19: Add DoctorView (read-only TUI diagnostics) and register it

**Files:**
- Create: `internal/tui/doctor.go`
- Modify: `internal/tui/app.go:155-169` (add `{id: "doctor", model: NewDoctorView(deps)}` to the views slice and set its category to "Views")
- Test: `internal/tui/doctor_test.go`

Reuse the read-only flow pattern from `internal/tui/check.go` verbatim: plain `tea.Cmd` goroutine + `spinner`, no op guard, Enter → running → `doctorDoneMsg` (NOT an `opResultMsg`). The view collects all check rows off-goroutine in a single `tea.Cmd` and renders ok/warn/fail rows plus a healthy/issues summary.

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// runDoctorCollect drives the DoctorView from idle through Enter and
// returns the terminal doctorDoneMsg produced by the run cmd. It bypasses
// the spinner tick (the run cmd is the last element of the Enter batch).
func runDoctorCollect(t *testing.T, v DoctorView) doctorDoneMsg {
	t.Helper()
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(DoctorView)
	if v.stage != doctorRunning {
		t.Fatalf("stage after Enter = %v, want doctorRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	for _, msg := range msgs {
		if done, ok := msg.(doctorDoneMsg); ok {
			return done
		}
	}
	t.Fatal("Enter batch produced no doctorDoneMsg")
	return doctorDoneMsg{}
}

func TestDoctorView_NilConfigFailsConfigCheck(t *testing.T) {
	r := newFlowRepo(t)
	v := NewDoctorView(Deps{Repo: r, Config: nil})
	done := runDoctorCollect(t, v)
	found := false
	for _, row := range done.rows {
		if row.status == doctorFail && strings.Contains(row.label, "onfig") {
			found = true
		}
	}
	if !found {
		t.Fatalf("nil config should yield a failing config row: %+v", done.rows)
	}
	if done.healthy {
		t.Fatal("doctor should not be healthy when config check fails")
	}
}

func TestDoctorView_RepoHealthyRow(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000" // skip AWS legs
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	sawRepoOK := false
	sawBucketOK := false
	for _, row := range done.rows {
		if row.status == doctorOK && strings.Contains(row.label, "epository") {
			sawRepoOK = true
		}
		if row.status == doctorOK && strings.Contains(row.label, "ucket name") {
			sawBucketOK = true
		}
	}
	if !sawRepoOK {
		t.Fatalf("expected a healthy repository row: %+v", done.rows)
	}
	if !sawBucketOK {
		t.Fatalf("expected a valid bucket-name row: %+v", done.rows)
	}
	if !done.healthy {
		t.Fatalf("expected healthy overall, got issues: %+v", done.rows)
	}
}

func TestDoctorView_InvalidBucketFails(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "Bad_Bucket"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	sawBucketFail := false
	for _, row := range done.rows {
		if row.status == doctorFail && strings.Contains(row.detail, "lowercase") {
			sawBucketFail = true
		}
	}
	if !sawBucketFail {
		t.Fatalf("invalid bucket should produce a failing row explaining naming: %+v", done.rows)
	}
	if done.healthy {
		t.Fatal("doctor should not be healthy with an invalid bucket name")
	}
}

func TestDoctorView_RendersRowsAfterDone(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	m, _ := v.Update(done)
	v = m.(DoctorView)
	out := v.View()
	if !strings.Contains(out, "healthy") {
		t.Fatalf("done view should show the healthy summary:\n%s", out)
	}
}

// TestDoctorView_UsesDiagAlias pins the DoctorView against the same
// diag.AWSReport shape the cli path uses.
func TestDoctorView_UsesDiagAlias(t *testing.T) {
	_ = diag.AWSReport{BucketAccessible: true}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/... -run TestDoctorView -count=1`
Expected: FAIL — build error, `undefined: NewDoctorView`, `undefined: DoctorView`, `undefined: doctorDoneMsg`, `undefined: doctorRunning`, `undefined: doctorOK/doctorFail`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/tui/doctor.go`. It mirrors `check.go`'s read-only structure: a `stage` enum, a `spinner`, and a single `run` closure that performs every check off-goroutine and returns a `doctorDoneMsg` carrying the collected rows plus a computed `healthy` bool. It calls `internal/diag` directly (no Deps callback), matching the unit's "tui imports internal/diag DIRECTLY" contract. Repo health reuses `CheckReport.Healthy()` (the same logic the CheckView renders). AWS legs run only for a real bucket with no S3-compatible endpoint, mirroring `runDoctor`'s gating in `internal/cli/doctor.go:79-86`.

```go
package tui

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

type doctorStage int

const (
	doctorIdle doctorStage = iota
	doctorRunning
	doctorDone
)

// doctorStatus is the severity of a single diagnostic row.
type doctorStatus int

const (
	doctorOK doctorStatus = iota
	doctorWarn
	doctorFail
)

// doctorRow is one line of the report: a short label, its status, and an
// optional detail (an error string or an explanatory note).
type doctorRow struct {
	label  string
	status doctorStatus
	detail string
}

// doctorDoneMsg carries the collected rows back to the flow. Like
// checkDoneMsg this is a READ-ONLY result, so it is deliberately NOT an
// opResultMsg — the Doctor view never takes the mutating-op guard and can
// run alongside a backup.
type doctorDoneMsg struct {
	rows    []doctorRow
	healthy bool
}

// DoctorView runs every read-only environment check asynchronously and
// renders ok/warn/fail rows plus a healthy/issues summary. It is the TUI
// analogue of `sentra doctor`: config validity, bucket-name shape, AWS
// identity + bucket inspection (AWS backends only), and repository
// integrity. All checks run in one tea.Cmd off the UI goroutine so a slow
// AWS round-trip never blocks a frame.
type DoctorView struct {
	deps   Deps
	stage  doctorStage
	spin   spinner.Model
	result doctorDoneMsg
	width  int
}

func NewDoctorView(deps Deps) DoctorView {
	s := spinner.New()
	s.Spinner = spinner.Dot
	return DoctorView{deps: deps, spin: s}
}

func (DoctorView) Init() tea.Cmd { return nil }

func (v DoctorView) Title() string { return "Doctor" }

func (v DoctorView) ShortHelp() []key.Binding {
	switch v.stage {
	case doctorRunning:
		return nil
	default:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "run doctor"))}
	}
}

func (v DoctorView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case doctorDoneMsg:
		v.stage = doctorDone
		v.result = msg
		return v, nil

	case spinner.TickMsg:
		if v.stage == doctorRunning {
			var cmd tea.Cmd
			v.spin, cmd = v.spin.Update(msg)
			return v, cmd
		}
		return v, nil

	case tea.KeyMsg:
		if msg.Type == tea.KeyEnter && v.stage != doctorRunning {
			v.stage = doctorRunning
			deps := v.deps
			ctx := ctxOrBackground(v.deps.Ctx)
			run := func() tea.Msg {
				return runDoctorChecks(ctx, deps)
			}
			return v, tea.Batch(v.spin.Tick, run)
		}
		return v, nil
	}
	return v, nil
}

// runDoctorChecks performs every read-only diagnostic and returns the
// collected rows plus the overall healthy verdict (healthy == no fail
// rows; warnings do not flip it, matching the cli doctor which counts only
// failures). It is a pure function of deps + ctx so tests drive it through
// the Enter batch without a real terminal.
func runDoctorChecks(ctx context.Context, deps Deps) doctorDoneMsg {
	var rows []doctorRow
	add := func(label string, status doctorStatus, detail string) {
		rows = append(rows, doctorRow{label: label, status: status, detail: detail})
	}

	cfg := deps.Config
	if cfg == nil {
		add("Config loaded", doctorFail, "no sentra.yaml configuration is loaded")
	} else {
		add("Config loaded", doctorOK, "")
	}

	// Bucket-name + AWS legs only make sense with a config. Mirror the cli
	// doctor's gating: AWS identity/inspect run only for a real bucket on
	// the AWS backend (no S3-compatible endpoint override).
	bucketOK := false
	if cfg != nil {
		switch {
		case cfg.Repo.S3.Bucket == "":
			add("Bucket configured", doctorFail, "repo.s3.bucket is missing")
		default:
			if err := diag.ValidateBucketName(cfg.Repo.S3.Bucket); err != nil {
				add("Bucket name valid", doctorFail, err.Error())
			} else {
				bucketOK = true
				add("Bucket name valid", doctorOK, "")
			}
		}
	}

	if cfg != nil && bucketOK {
		if cfg.Repo.S3.EndpointURL != "" {
			add("S3-compatible endpoint configured", doctorOK, "")
		} else {
			runDoctorAWSChecks(ctx, cfg, add)
		}
	}

	// Repository integrity. Reuse CheckReport.Healthy() — the same verdict
	// the Check view renders.
	if deps.Repo == nil {
		add("Repository check", doctorWarn, "no repository configured")
	} else {
		report, err := deps.Repo.Check(ctx, repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
		if err != nil {
			add("Repository check", doctorFail, err.Error())
		} else if !report.Healthy() {
			add("Repository check", doctorFail, "integrity check found issues")
		} else {
			add("Repository check healthy", doctorOK, "")
		}
	}

	healthy := true
	for _, row := range rows {
		if row.status == doctorFail {
			healthy = false
			break
		}
	}
	return doctorDoneMsg{rows: rows, healthy: healthy}
}

// runDoctorAWSChecks runs the AWS identity + bucket-inspection legs and
// appends their rows. A failed identity check short-circuits inspection,
// mirroring cli runDoctorAWS.
func runDoctorAWSChecks(ctx context.Context, cfg *config.Config, add func(string, doctorStatus, string)) {
	if err := diag.CheckSDKIdentity(ctx, cfg); err != nil {
		add("AWS identity verified", doctorFail, err.Error())
		return
	}
	add("AWS identity verified", doctorOK, "")

	report, err := diag.Inspect(ctx, cfg)
	if err != nil {
		add("AWS S3 bucket inspected", doctorFail, err.Error())
		return
	}
	if report.BucketAccessible {
		add("Bucket is accessible", doctorOK, "")
	}
	if report.PublicAccessReadable && report.PublicAccessBlocked {
		add("Bucket public access is blocked", doctorOK, "")
	} else if report.PublicAccessReadable {
		add("Bucket public access block is not fully enabled", doctorWarn, "")
	}
	if report.DefaultEncryptionReadable && report.DefaultEncryptionEnabled {
		add("Bucket default encryption is enabled", doctorOK, "")
	} else if report.DefaultEncryptionReadable {
		add("Bucket default encryption is not enabled", doctorWarn, "")
	}
}

func (v DoctorView) View() string {
	switch v.stage {
	case doctorRunning:
		return v.spin.View() + " running diagnostics…"
	case doctorDone:
		return v.renderReport()
	default:
		return ui.Primary.Render("Environment diagnostics") + "\n\n" +
			ui.Muted.Render("⏎ run doctor")
	}
}

func (v DoctorView) renderReport() string {
	var b strings.Builder
	status := ui.Success.Render("● healthy")
	if !v.result.healthy {
		status = ui.Danger.Render("● issues found")
	}
	b.WriteString(ui.Primary.Render("Doctor report") + "  " + status + "\n\n")
	for _, row := range v.result.rows {
		mark := ui.Success.Render("ok  ")
		switch row.status {
		case doctorWarn:
			mark = ui.Warn.Render("warn")
		case doctorFail:
			mark = ui.Danger.Render("fail")
		}
		b.WriteString("  " + mark + "  " + row.label + "\n")
		if row.detail != "" {
			b.WriteString("        " + ui.Muted.Render(row.detail) + "\n")
		}
	}
	b.WriteString("\n" + ui.Muted.Render("⏎ re-run"))
	return b.String()
}
```

In `internal/tui/app.go`, add the view to the slice (after `check`) and give it the "Views" category (the default, so no `categories` entry is needed). Change the `views` literal at `app.go:155-164`:

```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "doctor", model: NewDoctorView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
	}
```

The `categories` map (`app.go:167-169`) is left unchanged — `doctor` is absent from it, so it defaults to "Views" via the `cat == ""` fallback at `app.go:175-177`. Update the `NewApp` doc comment at `app.go:142-145` to note the added read-only view count if desired (cosmetic; not load-bearing).

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/... -run TestDoctorView -count=1`
Expected: PASS

Then confirm registration and the whole package still build/pass:
Run: `go test ./internal/tui/... -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/doctor.go internal/tui/doctor_test.go internal/tui/app.go
git commit -m "feat(tui): add read-only Doctor view backed by internal/diag

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

Notes for the assembler / downstream units:
- No `cli → tui` cycle is introduced: `internal/diag` imports only `aws-sdk-go-v2` + `internal/config`; both `internal/cli` and `internal/tui` import `internal/diag` (a leaf), and neither imports the other because of this change.
- The DoctorView is read-only and needs no confirmation gate (per the confirmation policy, read-only inspections need none). It uses only the pre-existing `Deps.Config`, `Deps.Repo`, and `Deps.Ctx` fields — none of the four NEW Unit-1 Deps fields — so it has no ordering dependency on Unit 1.
- Relevant real-source anchors used: `internal/cli/doctor.go:55-189` (gating + AWS/repo legs), `internal/cli/setup_awss3.go:14-65` (report + identity/inspect), `internal/cli/setup_awss3_ops.go:20-133` (read-only S3 helpers), `internal/cli/setup_summary.go:161-194` (`validateSetupBucketName`), `internal/repo/check.go:72-97` (`CheckReport` + `Healthy()`), `internal/tui/check.go` (read-only flow pattern reused verbatim), `internal/tui/app.go:155-169` (view registration).


## Part 6 — Sync flow

### Task 20: SyncView model: configure → confirm → running → done

**Files:**
- Create: `internal/tui/sync.go`
- Test: `internal/tui/sync_test.go`
- Modify: `internal/tui/app.go:155-169` (register the view + category)

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// writeDestConfig writes a minimal sentra.yaml at a temp path whose S3
// bucket differs from the source's, so the sameS3Location guard passes.
// SyncView only reads the file to (a) confirm it exists and (b) hand it
// to config.Load; the dest store is built by the stub NewStore below,
// which ignores the config contents entirely.
func writeDestConfig(t *testing.T, bucket string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	body := "repo:\n  s3:\n    bucket: " + bucket + "\n    prefix: \"\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write dest config: %v", err)
	}
	return path
}

// stubNewStore returns a Deps.NewStore that always yields the same
// in-memory store, letting a sync run end-to-end without S3. The
// returned store is the sync destination.
func stubNewStore(dst blobstore.Store) func(context.Context, *config.Config) (blobstore.Store, error) {
	return func(context.Context, *config.Config) (blobstore.Store, error) {
		return dst, nil
	}
}

func typeIntoSync(v SyncView, s string) SyncView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SyncView)
	}
	return v
}

// TestSyncFlow_EnterValidatesAndPushesConfirm: with a real dest config
// path typed and a valid dest store, enter must NOT start the op
// directly — it must push a ConfirmModal keyed to syncConfirmID.
func TestSyncFlow_EnterValidatesAndPushesConfirm(t *testing.T) {
	r := newFlowRepo(t)
	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd == nil {
		t.Fatal("enter on a valid dest path must emit a command")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	if !strings.Contains(push.modal.View(), "Confirm sync") {
		t.Errorf("modal should be the sync confirmation:\n%s", push.modal.View())
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage before confirm = %v, want syncConfigure", v.stage)
	}
}

// TestSyncFlow_ConfirmStartsOpAndSyncsBlobs: after confirmation the flow
// emits startOpMsg{name:"sync"} batched with a seeded opTickMsg; running
// the op copies the source's blobs to the dest store and returns a
// syncDoneMsg with accurate stats.
func TestSyncFlow_ConfirmStartsOpAndSyncsBlobs(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r) // one snapshot => data/ + snapshots/ blobs on src

	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)
	// Enable --init-dest so the empty dest is bootstrapped rather than
	// refused with ErrEmptyDest.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // focus init-dest toggle
	v = m.(SyncView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}) // toggle on
	v = m.(SyncView)

	// Enter -> validate -> push confirm.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if _, ok := cmd().(pushModalMsg); !ok {
		t.Fatalf("expected confirm modal, got %#v", cmd())
	}

	// The App broadcasts confirmedMsg back to the flow.
	m, cmd = v.Update(confirmedMsg{id: syncConfirmID})
	v = m.(SyncView)
	if v.stage != syncRunning {
		t.Fatalf("stage after confirm = %v, want syncRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart, foundTick bool
	for _, msg := range msgs {
		switch mm := msg.(type) {
		case startOpMsg:
			start, foundStart = mm, true
		case opTickMsg:
			foundTick = true
		}
	}
	if !foundStart {
		t.Fatalf("expected startOpMsg in batch, got %#v", msgs)
	}
	if !foundTick {
		t.Fatalf("expected seeded opTickMsg in batch, got %#v", msgs)
	}
	if start.name != "sync" {
		t.Fatalf("op name = %q, want sync", start.name)
	}

	res := start.run(context.Background())
	done, ok := res.(syncDoneMsg)
	if !ok {
		t.Fatalf("expected syncDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("sync failed: %v", done.err)
	}
	if !done.stats.Bootstrapped {
		t.Errorf("init-dest run should report Bootstrapped")
	}
	if done.stats.CopiedBlobs == 0 {
		t.Errorf("expected copied blobs > 0, got %d", done.stats.CopiedBlobs)
	}

	// syncDoneMsg must implement opResult so the App guard clears.
	var _ opResultMsg = syncDoneMsg{}

	// Delivering the result advances to done and renders the summary.
	m, _ = v.Update(res)
	v = m.(SyncView)
	if v.stage != syncDone {
		t.Fatalf("stage after result = %v, want syncDone", v.stage)
	}
	if out := v.View(); !strings.Contains(out, "Sync complete") {
		t.Errorf("done view should confirm completion:\n%s", out)
	}

	// The dest store really has the source's data/ blobs now.
	got, err := dst.List(context.Background(), repo.DataPrefix)
	if err != nil {
		t.Fatalf("dst.List: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("dest should have received data/ blobs")
	}
}

// TestSyncFlow_DryRunSkipsConfirm: a dry-run needs no confirmation gate —
// it writes nothing. Enter with dry-run on must start the op directly.
func TestSyncFlow_DryRunSkipsConfirm(t *testing.T) {
	r := newFlowRepo(t)
	seedSnapshotReal(t, r)
	dstPath := writeDestConfig(t, "dest-bucket")
	dst := blobstore.NewMemory()
	v := NewSyncView(Deps{Repo: r, NewStore: stubNewStore(dst)})
	v = typeIntoSync(v, dstPath)
	v.initDest = true // bootstrap allowed in the plan (dry-run writes nothing)
	v.dryRun = true

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if v.stage != syncRunning {
		t.Fatalf("dry-run enter should go straight to running, stage = %v", v.stage)
	}
	msgs := execCmds(t, cmd)
	var foundStart bool
	for _, msg := range msgs {
		if _, ok := msg.(startOpMsg); ok {
			foundStart = true
		}
	}
	if !foundStart {
		t.Fatalf("dry-run enter must emit startOpMsg, got %#v", msgs)
	}
	// Dry-run performs no writes on dest.
	got, _ := dst.List(context.Background(), repo.DataPrefix)
	if len(got) != 0 {
		t.Fatalf("dry-run must not write to dest, found %d blobs", len(got))
	}
	_ = v
}

// TestSyncFlow_SameLocationRefused: a dest config whose bucket+prefix
// equals the source's must be refused BEFORE any store is built.
func TestSyncFlow_SameLocationRefused(t *testing.T) {
	r := newFlowRepo(t)
	// Source config carries the same bucket the dest file will use.
	srcCfg := &config.Config{}
	srcCfg.Repo.S3.Bucket = "same-bucket"
	dstPath := writeDestConfig(t, "same-bucket")

	built := false
	v := NewSyncView(Deps{
		Repo:   r,
		Config: srcCfg,
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			built = true
			return blobstore.NewMemory(), nil
		},
	})
	v = typeIntoSync(v, dstPath)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd != nil {
		t.Fatalf("same-location sync must not emit a command, got %#v", cmd())
	}
	if built {
		t.Fatal("same-location refusal must short-circuit before building a store")
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "same S3 location") {
		t.Errorf("view should surface the same-location error:\n%s", v.View())
	}
}

// TestSyncFlow_MissingPathRefuses: a dest path that does not exist keeps
// the flow in configure with a validation error.
func TestSyncFlow_MissingPathRefuses(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	v = typeIntoSync(v, "/definitely/not/a/real/sentra.yaml")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SyncView)
	if cmd != nil {
		t.Fatal("nonexistent dest config must not start a sync")
	}
	if v.stage != syncConfigure {
		t.Fatalf("stage = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "not found") {
		t.Errorf("view should surface the path error:\n%s", v.View())
	}
}

// TestSyncFlow_OpRejectedResetsStage: an opRejectedMsg{name:"sync"} while
// running resets the flow to configure with a notice.
func TestSyncFlow_OpRejectedResetsStage(t *testing.T) {
	v := NewSyncView(Deps{Repo: newFlowRepo(t), NewStore: stubNewStore(blobstore.NewMemory())})
	v.stage = syncRunning
	m, _ := v.Update(opRejectedMsg{name: "sync"})
	v = m.(SyncView)
	if v.stage != syncConfigure {
		t.Fatalf("stage after rejection = %v, want syncConfigure", v.stage)
	}
	if !strings.Contains(v.View(), "in progress") {
		t.Errorf("rejection notice should be shown:\n%s", v.View())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSyncFlow -count=1`
Expected: FAIL to compile with `undefined: SyncView`, `undefined: NewSyncView`, `undefined: syncConfigure`, `undefined: syncRunning`, `undefined: syncDone`, `undefined: syncConfirmID`, `undefined: syncDoneMsg`.

- [ ] **Step 3: Write the minimal implementation**

`internal/tui/sync.go`:

```go
package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// syncStage is the sync flow's state-machine position.
type syncStage int

const (
	syncConfigure syncStage = iota
	syncRunning
	syncDone
)

// syncConfirmID ties the confirmation modal back to this flow. Sync
// spreads the wrapped repo key to a new bucket on --init-dest, so a real
// (non-dry-run) run is gated behind a simple y/n confirm — the App
// broadcasts confirmedMsg{syncConfirmID} back here on enter.
const syncConfirmID = "sync-apply"

// syncField tracks which configure-stage control owns keystrokes: the
// destination-path text input or one of the boolean toggles.
type syncField int

const (
	syncFieldPath syncField = iota
	syncFieldInitDest
	syncFieldDryRun
	syncFieldCount // sentinel: number of focusable fields
)

// syncDoneMsg is the flow's terminal message. It implements opResultMsg
// because sync is a MUTATING op (it takes the dest's meta/lock and writes
// blobs) — the App clears its one-op guard on this marker.
type syncDoneMsg struct {
	stats repo.SyncStats
	err   error
}

func (syncDoneMsg) opResult() {}

// SyncView drives configure → (confirm) → running → done for replicating
// this repository to a clone destination. The dest store is built from a
// second sentra.yaml via deps.NewStore; the actual copy runs in the
// App-managed op goroutine (repo.SyncTo), and this view renders a
// byte-progress bar polled through opTick.
type SyncView struct {
	deps  Deps
	stage syncStage

	dstPath  textinput.Model
	field    syncField
	initDest bool
	dryRun   bool
	pathErr  string
	notice   string // transient banner, e.g. after an op rejection

	// dstStore is resolved during validation (enter) and reused by the
	// op goroutine so the store is built exactly once per run.
	dstStore interface{} // set to blobstore.Store; see startSync

	reporter *opReporter
	bar      progress.Model
	result   syncDoneMsg
	width    int
	height   int
}

func NewSyncView(deps Deps) SyncView {
	path := textinput.New()
	path.Prompt = "dst>  "
	path.Placeholder = "path to the destination's sentra.yaml"
	path.Focus()
	return SyncView{
		deps:    deps,
		dstPath: path,
		bar:     progress.New(progress.WithDefaultGradient()),
	}
}

func (SyncView) Init() tea.Cmd { return nil }

func (v SyncView) Title() string { return "Sync" }

func (v SyncView) ShortHelp() []key.Binding {
	switch v.stage {
	case syncRunning:
		return []key.Binding{key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel"))}
	case syncDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "sync")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
			key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		}
	}
}

func (v SyncView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		v.height = msg.Height
		v.bar.Width = min(msg.Width-8, 60)
		return v, nil

	case syncDoneMsg:
		v.stage = syncDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our optimistic start was refused; leave running so we don't hang.
		if v.stage == syncRunning && msg.name == "sync" {
			v.stage = syncConfigure
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case confirmedMsg:
		if msg.id != syncConfirmID || v.stage != syncConfigure {
			return v, nil
		}
		v.notice = ""
		return v.startSync()

	case opTickMsg:
		if v.stage == syncRunning {
			return v, opTick() // keep ticking while running
		}
		return v, nil

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// resetTo returns a fresh view carrying the window size so the progress
// bar keeps its width (bubbletea does not re-emit WindowSizeMsg after a
// model swap).
func (v SyncView) resetTo() (tea.Model, tea.Cmd) {
	return NewSyncView(v.deps).Update(tea.WindowSizeMsg{Width: v.width, Height: v.height})
}

func (v SyncView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case syncRunning:
		if msg.Type == tea.KeyEsc {
			return v, func() tea.Msg { return cancelOpMsg{} }
		}
		return v, nil

	case syncDone:
		if msg.Type == tea.KeyEnter {
			return v.resetTo()
		}
		return v, nil

	default: // syncConfigure
		v.notice = ""
		switch msg.Type {
		case tea.KeyTab:
			v.field = (v.field + 1) % syncFieldCount
			if v.field == syncFieldPath {
				v.dstPath.Focus()
			} else {
				v.dstPath.Blur()
			}
			return v, nil
		case tea.KeyEnter:
			return v.validateAndConfirm()
		case tea.KeySpace:
			switch v.field {
			case syncFieldInitDest:
				v.initDest = !v.initDest
			case syncFieldDryRun:
				v.dryRun = !v.dryRun
			}
			return v, nil
		}
		if v.field == syncFieldPath {
			var cmd tea.Cmd
			v.dstPath, cmd = v.dstPath.Update(msg)
			v.pathErr = "" // typing clears the last validation error
			return v, cmd
		}
		return v, nil
	}
}

// validateAndConfirm checks the dest path, loads its config, guards
// against a same-location target, and builds the dest store. On a real
// run it pushes a y/n confirm; a dry-run (which writes nothing) starts
// immediately. All refusals short-circuit before any store is built,
// except store construction itself, which is the last validation step.
func (v SyncView) validateAndConfirm() (tea.Model, tea.Cmd) {
	dst := strings.TrimSpace(v.dstPath.Value())
	if dst == "" {
		v.pathErr = "destination config path is required"
		return v, nil
	}
	if info, err := os.Stat(dst); err != nil || info.IsDir() {
		v.pathErr = fmt.Sprintf("destination config not found: %s", dst)
		return v, nil
	}
	if v.deps.Repo == nil {
		v.pathErr = "no repository configured"
		return v, nil
	}
	if v.deps.NewStore == nil {
		v.pathErr = "store factory unavailable"
		return v, nil
	}

	dstCfg, err := config.Load(dst)
	if err != nil {
		v.pathErr = fmt.Sprintf("load %s: %v", dst, err)
		return v, nil
	}
	// Re-implement the CLI's sameS3Location guard (internal/cli/sync.go:190)
	// inline: refuse a dest that resolves to the source's bucket+prefix
	// BEFORE building any store. deps.Config is the source's config.
	if syncSameLocation(v.deps.Config, dstCfg) {
		v.pathErr = fmt.Sprintf("source and destination resolve to the same S3 location (bucket=%q)",
			dstCfg.Repo.S3.Bucket)
		return v, nil
	}

	ctx := ctxOrBackground(v.deps.Ctx)
	store, err := v.deps.NewStore(ctx, dstCfg)
	if err != nil {
		v.pathErr = fmt.Sprintf("open destination blobstore: %v", err)
		return v, nil
	}
	v.dstStore = store

	// A dry-run performs no writes on the destination, so it needs no
	// confirmation gate; start it directly.
	if v.dryRun {
		return v.startSync()
	}

	body := "Copy every snapshot, chunk, and config to the destination clone.\nSubsequent syncs are incremental."
	if v.initDest {
		body += "\n\n" + "init-dest is ON: this bootstraps an empty bucket and spreads the wrapped repo key to it. Point it at a bucket you control."
	}
	modal := NewConfirmModal("Confirm sync", body, syncConfirmID, v.width, v.height)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startSync enters the running stage and emits startOpMsg{name:"sync"}.
// The dest store was resolved during validation and captured on v.
func (v SyncView) startSync() (tea.Model, tea.Cmd) {
	store, ok := v.dstStore.(interface {
		// blobstore.Store is the concrete type; the assertion below is a
		// safety net for the zero-value case (should not happen after
		// validateAndConfirm succeeded).
	})
	_ = store
	_ = ok

	v.reporter = newOpReporter()
	v.stage = syncRunning
	r := v.deps.Repo
	reporter := v.reporter
	dest := v.dstStore
	opts := repo.SyncOptions{
		InitDest: v.initDest,
		DryRun:   v.dryRun,
		Progress: reporter,
	}
	start := startOpMsg{
		name: "sync",
		run: func(ctx context.Context) tea.Msg {
			stats, err := r.SyncTo(ctx, dest.(blobstoreStore), opts)
			return syncDoneMsg{stats: stats, err: err}
		},
	}
	// Seed the first opTickMsg alongside the start so the progress bar's
	// repaint self-loop begins (bubbletea only redraws on messages).
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}

func (v SyncView) View() string {
	var b strings.Builder
	switch v.stage {
	case syncRunning:
		total, done := v.reporter.Snapshot()
		b.WriteString(ui.Primary.Render("Syncing…"))
		b.WriteString("\n\n")
		pct := 0.0
		if total > 0 {
			pct = float64(done) / float64(total)
		}
		b.WriteString(v.bar.ViewAs(pct))
		fmt.Fprintf(&b, "\n\n%s / %s copied",
			ui.FormatBytes(done), ui.FormatBytes(total))
		b.WriteString("\n" + ui.Muted.Render("esc cancel"))

	case syncDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Sync failed"))
			b.WriteString("\n\n" + v.result.err.Error())
		} else {
			s := v.result.stats
			if s.DryRun {
				b.WriteString(ui.Success.Render("Dry-run complete (no writes performed)"))
			} else {
				b.WriteString(ui.Success.Render("Sync complete"))
			}
			boot := "no"
			if s.Bootstrapped {
				boot = "yes (destination config was empty)"
			}
			fmt.Fprintf(&b, "\n\n  bootstrap   %s\n  copied      %d blobs (%s)\n  skipped     %d (already on destination)\n  elapsed     %s",
				boot, s.CopiedBlobs, ui.FormatBytes(s.CopiedBytes), s.SkippedBlobs, s.Elapsed)
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ run another sync"))

	default:
		b.WriteString(ui.Primary.Render("Replicate to a clone destination"))
		if v.notice != "" {
			b.WriteString("\n" + ui.Warn.Render(v.notice))
		}
		b.WriteString("\n\n" + v.dstPath.View())
		b.WriteString("\n\n" + v.toggleLine(syncFieldInitDest, "init-dest", v.initDest,
			"bootstrap an empty destination"))
		b.WriteString("\n" + v.toggleLine(syncFieldDryRun, "dry-run", v.dryRun,
			"list what would be copied, write nothing"))
		if v.pathErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.pathErr))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ sync · tab field · space toggle"))
	}
	return b.String()
}

// toggleLine renders one boolean toggle, marking the focused one and its
// on/off state.
func (v SyncView) toggleLine(f syncField, label string, on bool, help string) string {
	box := "[ ]"
	if on {
		box = "[x]"
	}
	cursor := "  "
	if v.field == f {
		cursor = "> "
	}
	line := fmt.Sprintf("%s%s %-10s %s", cursor, box, label, ui.Muted.Render(help))
	if v.field == f {
		return ui.Primary.Render(line)
	}
	return line
}

// syncSameLocation mirrors internal/cli/sync.go's sameS3Location: two
// configs match when their bucket+prefix are equal and the bucket is
// non-empty. A nil source config (tests, unconfigured) never matches, so
// the guard fails open — the dest store's factory surfaces any real
// misconfiguration.
func syncSameLocation(src, dst *config.Config) bool {
	if src == nil || dst == nil {
		return false
	}
	return src.Repo.S3.Bucket == dst.Repo.S3.Bucket &&
		src.Repo.S3.Prefix == dst.Repo.S3.Prefix &&
		src.Repo.S3.Bucket != ""
}
```

Note: the `dstStore interface{}` field plus the ad-hoc assertion in `startSync` is a placeholder to keep this snippet import-minimal in the draft; the real implementation types the field as `blobstore.Store` directly. Replace the field declaration with `dstStore blobstore.Store`, drop the `store, ok := ...` dead assertion block and the `blobstoreStore` alias, add `"github.com/markgustetic/sentra/internal/blobstore"` to the import block, and call `r.SyncTo(ctx, dest, opts)` where `dest` is the typed `blobstore.Store`. Final `startSync`:

```go
func (v SyncView) startSync() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.stage = syncRunning
	r := v.deps.Repo
	reporter := v.reporter
	dest := v.dstStore // blobstore.Store, resolved during validation
	opts := repo.SyncOptions{
		InitDest: v.initDest,
		DryRun:   v.dryRun,
		Progress: reporter,
	}
	start := startOpMsg{
		name: "sync",
		run: func(ctx context.Context) tea.Msg {
			stats, err := r.SyncTo(ctx, dest, opts)
			return syncDoneMsg{stats: stats, err: err}
		},
	}
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}
```

And the field declaration:

```go
	// dstStore is resolved during validation (enter) and reused by the
	// op goroutine so the store is built exactly once per run.
	dstStore blobstore.Store
```

Register the view in `internal/tui/app.go`. Add to the `views` slice (currently `app.go:155-164`) after the `prune` entry:

```go
		{id: "prune", model: NewPruneView(deps)},
		{id: "sync", model: NewSyncView(deps)},
```

And add `sync` to the Operations `categories` map (currently `app.go:167-169`):

```go
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
		"sync": "Operations",
	}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestSyncFlow -count=1`
Expected: PASS

Then confirm no shell regressions and vet is clean:
Run: `go test ./internal/tui/ -count=1 && go vet ./internal/tui/`
Expected: PASS, no vet diagnostics.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/sync.go internal/tui/sync_test.go internal/tui/app.go
git commit -m "$(cat <<'EOF'
feat(tui): add Sync view for repository replication

Adds a confirmation-gated Sync flow modeled on BackupView: a
destination sentra.yaml path plus --init-dest / --dry-run toggles,
an inline same-S3-location guard, a y/n confirm before any real
(non-dry-run) write, and a byte-progress bar driven by repo.SyncTo
through the one-op-at-a-time guard.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
EOF
)"
```

---

**Security invariants honored.** No secrets touch YAML/logs/tests: the dest store is built from a plaintext `sentra.yaml` via `deps.NewStore`, and `repo.SyncTo` copies opaque sealed blobs — the wrapped key travels only inside the config blob it already lives in, never rendered. The `--init-dest` confirm body warns that bootstrapping spreads the wrapped key to a new bucket. The mutating op serializes on the dest's `meta/lock` (acquired inside `SyncTo`, `sync.go:161`) and runs under the App's single-op guard via `startOpMsg{name:"sync"}` + `syncDoneMsg.opResult()`. GC live-set safety is unaffected (sync is additive, never deletes). The same-location guard (`syncSameLocation`) and path-exists check short-circuit before any store construction, matching the CLI's refusal ordering (`internal/cli/sync.go:99-126`).

**Files touched:** `/Users/markgustetic/Programming/portfolio/sentra/internal/tui/sync.go` (create), `/Users/markgustetic/Programming/portfolio/sentra/internal/tui/sync_test.go` (create), `/Users/markgustetic/Programming/portfolio/sentra/internal/tui/app.go` (modify lines 155-169).


## Part 7 — Password flow

**Published API:** none (this unit creates no extraction package). It consumes the pinned `Deps.SaveKeyringPassphrase func(cfg *config.Config, pass []byte) error` field added by Unit 1, and the existing `Deps.Config`/`Deps.Repo` fields.

Verified against real source before writing:
- `crypto.Zeroize(b []byte)` — `internal/crypto/zeroize.go:21` (exact symbol).
- `(*repo.Repo).Passwd(ctx context.Context, newPassphrase []byte) error` — `internal/repo/passwd.go:62`; `repo.ErrSamePassphrase` — `internal/repo/passwd.go:20`; `repo.ErrRepoLocked` — `internal/repo/lock.go:37`.
- Mutating-op protocol (`startOpMsg`, `opRejectedMsg`, `opResult()`) — `internal/tui/ops.go:18-52`; confirmation via `NewTypedConfirmModal` + `confirmedMsg{id}` + `pushModalMsg` — `internal/tui/modal.go:33,125-216`; App broadcasts `confirmedMsg`/`opRejectedMsg` to all views — `internal/tui/app.go:265,319`.
- `textinput.EchoPassword`, `.EchoMode`, `.EchoCharacter` — `bubbles@v0.20.0/textinput/textinput.go:34,92-93`.
- `Deps.Config.Passphrase.UseKeyring bool` — `internal/config/config.go:61-63`.
- Registration + category wiring — `internal/tui/app.go:155-180`; test helper `newFlowRepo(t)` — `internal/tui/backup_test.go:17`; `execCmds(t, cmd)` — `internal/tui/ops_test.go:25`.

Two tasks: (1) the `PasswordView` model with its input/confirm/running/done state machine and the rotation run closure; (2) registration into `NewApp` under the "Operations" category.

---

### Task 21: PasswordView model (input → typed-confirm → running → done)

**Files:**
- Create: `internal/tui/password.go`
- Test: `internal/tui/password_test.go`

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// typeIntoPassword feeds each rune of s into the currently focused field.
func typeIntoPassword(v PasswordView, s string) PasswordView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(PasswordView)
	}
	return v
}

// TestPasswordFlow_BothFieldsMaskInput asserts the new + confirm inputs
// are password-masked so the secret is never rendered in cleartext.
func TestPasswordFlow_BothFieldsMaskInput(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	if v.newPass.EchoMode != textinput.EchoPassword {
		t.Errorf("new-pass field EchoMode = %v, want EchoPassword", v.newPass.EchoMode)
	}
	if v.confirmPass.EchoMode != textinput.EchoPassword {
		t.Errorf("confirm-pass field EchoMode = %v, want EchoPassword", v.confirmPass.EchoMode)
	}
	// The typed secret must not appear verbatim anywhere in the rendered
	// view (masking is per-field; this guards the whole frame).
	v = typeIntoPassword(v, "supersecret9")
	if strings.Contains(v.View(), "supersecret9") {
		t.Errorf("rendered view leaked the new passphrase in cleartext:\n%s", v.View())
	}
}

// TestPasswordFlow_TooShortRejected: a new passphrase under 8 bytes never
// advances to the confirm modal.
func TestPasswordFlow_TooShortRejected(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "short") // 5 bytes, then confirm the same
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "short")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd != nil {
		t.Fatalf("too-short passphrase must not emit a command, got one")
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v, want passwordInput", v.stage)
	}
	if v.inputErr == "" {
		t.Fatal("expected a validation error for the short passphrase")
	}
}

// TestPasswordFlow_MismatchRejected: new != confirm never advances.
func TestPasswordFlow_MismatchRejected(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "longenough1")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "longenough2")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd != nil {
		t.Fatal("mismatched confirm must not emit a command")
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v, want passwordInput", v.stage)
	}
	if v.inputErr == "" {
		t.Fatal("expected a mismatch validation error")
	}
}

// TestPasswordFlow_ValidEntryPushesTypedConfirm: matching, long-enough
// entries push the typed-confirm modal ("rotate") and nothing else — no
// rotation happens on the input->confirm transition.
func TestPasswordFlow_ValidEntryPushesTypedConfirm(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v = typeIntoPassword(v, "longenough1")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "longenough1")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PasswordView)
	if cmd == nil {
		t.Fatal("valid entry must request the confirm modal")
	}
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", cmd())
	}
	if _, ok := push.modal.(TypedConfirmModal); !ok {
		t.Fatalf("expected a TypedConfirmModal, got %T", push.modal)
	}
	if v.stage != passwordInput {
		t.Fatalf("stage = %v (must stay in input until confirmed)", v.stage)
	}
}

// TestPasswordFlow_ConfirmedRunRotates: the confirmed run closure rotates
// the repo passphrase. After it runs, the OLD passphrase no longer Opens
// the repo and the NEW one does — proving a real rotation happened.
func TestPasswordFlow_ConfirmedRunRotates(t *testing.T) {
	r := newFlowRepo(t)
	v := NewPasswordView(Deps{Repo: r})
	v = typeIntoPassword(v, "brand-new-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "brand-new-pass")

	m, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	v = m.(PasswordView)
	if cmd == nil {
		t.Fatal("confirmation must start the op")
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	found := false
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, found = s, true
		}
	}
	if !found {
		t.Fatalf("expected a startOpMsg in the batch, got %#v", msgs)
	}
	if start.name != "password" {
		t.Fatalf("op name = %q, want password", start.name)
	}
	if v.stage != passwordRunning {
		t.Fatalf("stage = %v, want passwordRunning", v.stage)
	}

	res := start.run(context.Background())
	done, ok := res.(passwordDoneMsg)
	if !ok {
		t.Fatalf("run result: %#v", res)
	}
	if done.err != nil {
		t.Fatalf("rotation failed: %v", done.err)
	}
	// passwordDoneMsg must be an opResultMsg so the App guard clears.
	if _, ok := any(done).(opResultMsg); !ok {
		t.Fatal("passwordDoneMsg must implement opResult()")
	}
}

// TestPasswordFlow_SamePassphraseMapped: rotating to the current
// passphrase surfaces the mapped "matches current" message, not the raw
// repo sentinel.
func TestPasswordFlow_SamePassphraseMapped(t *testing.T) {
	r := newFlowRepo(t) // created with passphrase "flow-test-pass"
	v := NewPasswordView(Deps{Repo: r})
	v = typeIntoPassword(v, "flow-test-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "flow-test-pass")

	_, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	msgs := execCmds(t, cmd)
	var start startOpMsg
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start = s
		}
	}
	res := start.run(context.Background())
	done := res.(passwordDoneMsg)
	if !errors.Is(done.err, repo.ErrSamePassphrase) {
		t.Fatalf("run err = %v, want wrap of ErrSamePassphrase", done.err)
	}
	m, _ = v.Update(res)
	if got := m.(PasswordView).View(); !strings.Contains(got, "matches current") {
		t.Fatalf("done view must map ErrSamePassphrase to 'matches current':\n%s", got)
	}
}

// TestPasswordFlow_KeyringSaveInvokedOnSuccess: when UseKeyring is set and
// a saver is wired, a successful rotation calls it with the NEW passphrase.
func TestPasswordFlow_KeyringSaveInvokedOnSuccess(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Passphrase.UseKeyring = true
	var savedPass string
	var saveCalls int
	deps := Deps{
		Repo:   r,
		Config: &cfg,
		SaveKeyringPassphrase: func(_ *config.Config, pass []byte) error {
			saveCalls++
			savedPass = string(pass)
			return nil
		},
	}
	v := NewPasswordView(deps)
	v = typeIntoPassword(v, "brand-new-pass")
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PasswordView)
	v = typeIntoPassword(v, "brand-new-pass")

	_, cmd := v.Update(confirmedMsg{id: passwordConfirmID})
	msgs := execCmds(t, cmd)
	var start startOpMsg
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start = s
		}
	}
	res := start.run(context.Background())
	done := res.(passwordDoneMsg)
	if done.err != nil {
		t.Fatalf("rotation failed: %v", done.err)
	}
	if saveCalls != 1 {
		t.Fatalf("keyring saver called %d times, want 1", saveCalls)
	}
	if savedPass != "brand-new-pass" {
		t.Fatalf("keyring saved %q, want the new passphrase", savedPass)
	}
}

// TestPasswordFlow_OpRejectedResets: an op-rejection while running resets
// the flow to the input stage so it never hangs.
func TestPasswordFlow_OpRejectedResets(t *testing.T) {
	v := NewPasswordView(Deps{Repo: newFlowRepo(t)})
	v.stage = passwordRunning // simulate the optimistic running stage
	m, _ := v.Update(opRejectedMsg{name: "password"})
	v = m.(PasswordView)
	if v.stage != passwordInput {
		t.Fatalf("stage after rejection = %v, want passwordInput", v.stage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPasswordFlow -count=1`
Expected: FAIL to compile — `undefined: NewPasswordView`, `undefined: PasswordView`, `undefined: passwordInput`, `undefined: passwordRunning`, `undefined: passwordConfirmID`, `undefined: passwordDoneMsg`.

- [ ] **Step 3: Write the minimal implementation**
```go
package tui

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// minPasswordLen mirrors the 8-byte operational floor the CLI enforces on
// the new passphrase (internal/cli/passwd.go:minPasswdNewPassphraseLen).
const minPasswordLen = 8

// passwordConfirmID ties the typed-confirm modal result back to this flow.
const passwordConfirmID = "password-rotate"

type passwordStage int

const (
	passwordInput passwordStage = iota
	passwordRunning
	passwordDone
)

// passwordDoneMsg is the flow's terminal message. Rotation is a mutating
// operation that takes the repo advisory lock, so it implements
// opResultMsg — the App guard clears on it just like backup/prune.
type passwordDoneMsg struct {
	// keyringSaved reports the keyring entry was re-saved with the new
	// secret (only when UseKeyring + a saver were wired).
	keyringSaved bool
	err          error
}

func (passwordDoneMsg) opResult() {}

// PasswordView rotates the repository passphrase. Flow:
//
//	input   → two masked fields (new + confirm). Enter validates length
//	          and equality, then pushes the typed-confirm modal.
//	confirm → TypedConfirmModal (type "rotate"); its confirmedMsg starts
//	          the op. This is destructive/irreversible — the old
//	          passphrase stops working and there is no recovery path — so
//	          it uses the TYPED confirm, matching prune's gate.
//	running → the App-managed op goroutine calls repo.Passwd under the
//	          meta/lock; on success it re-saves the keyring entry.
//	done    → result / mapped error.
//
// Security: the typed secret lives only in the textinput buffers and the
// derived run-closure copy, is masked in every rendered frame, is never
// logged, and the run closure zeroizes its copy on return.
type PasswordView struct {
	deps  Deps
	stage passwordStage

	newPass     textinput.Model
	confirmPass textinput.Model
	focusConfirm bool

	inputErr string
	notice   string // transient banner, e.g. after an op rejection

	result passwordDoneMsg
	width  int
}

func NewPasswordView(deps Deps) PasswordView {
	newField := textinput.New()
	newField.Prompt = "new>     "
	newField.Placeholder = "new passphrase"
	newField.EchoMode = textinput.EchoPassword
	newField.EchoCharacter = '•'
	newField.Focus()

	confirmField := textinput.New()
	confirmField.Prompt = "confirm> "
	confirmField.Placeholder = "retype new passphrase"
	confirmField.EchoMode = textinput.EchoPassword
	confirmField.EchoCharacter = '•'

	return PasswordView{deps: deps, newPass: newField, confirmPass: confirmField}
}

func (PasswordView) Init() tea.Cmd { return nil }

func (v PasswordView) Title() string { return "Password" }

func (v PasswordView) ShortHelp() []key.Binding {
	switch v.stage {
	case passwordRunning:
		return nil
	case passwordDone:
		return []key.Binding{key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "again"))}
	default:
		return []key.Binding{
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "rotate…")),
			key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "field")),
		}
	}
}

func (v PasswordView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case passwordDoneMsg:
		v.stage = passwordDone
		v.result = msg
		return v, nil

	case opRejectedMsg:
		// Our start was refused (another op holds the guard). Leave the
		// optimistic running stage so the flow doesn't hang forever.
		if v.stage == passwordRunning && msg.name == "password" {
			v.stage = passwordInput
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case confirmedMsg:
		if msg.id != passwordConfirmID || v.stage != passwordInput {
			return v, nil
		}
		v.notice = ""
		return v.startRotate()

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v PasswordView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case passwordRunning:
		return v, nil

	case passwordDone:
		if msg.Type == tea.KeyEnter {
			fresh := NewPasswordView(v.deps)
			fresh.width = v.width
			return fresh, nil
		}
		return v, nil

	default: // passwordInput
		switch msg.Type {
		case tea.KeyTab:
			v.focusConfirm = !v.focusConfirm
			if v.focusConfirm {
				v.newPass.Blur()
				v.confirmPass.Focus()
			} else {
				v.confirmPass.Blur()
				v.newPass.Focus()
			}
			return v, nil
		case tea.KeyEnter:
			return v.requestConfirm()
		}
		var cmd tea.Cmd
		if v.focusConfirm {
			v.confirmPass, cmd = v.confirmPass.Update(msg)
		} else {
			v.newPass, cmd = v.newPass.Update(msg)
		}
		v.inputErr = "" // typing clears the last validation error
		v.notice = ""
		return v, cmd
	}
}

// requestConfirm validates the two entries and, if they pass, pushes the
// typed-confirm modal. It never starts the rotation itself — the modal's
// confirmedMsg does, so the destructive step is always gated.
func (v PasswordView) requestConfirm() (tea.Model, tea.Cmd) {
	newVal := []byte(v.newPass.Value())
	confVal := []byte(v.confirmPass.Value())
	if len(newVal) < minPasswordLen {
		v.inputErr = fmt.Sprintf("new passphrase must be at least %d characters", minPasswordLen)
		return v, nil
	}
	// Constant-time equality: the two values are secrets typed by the
	// operator, and comparing them in constant time avoids a length /
	// content timing side channel on the confirm step.
	if subtle.ConstantTimeCompare(newVal, confVal) != 1 {
		v.inputErr = "passphrases do not match"
		return v, nil
	}
	v.inputErr = ""
	body := "Rotating rewrites the encrypted config so a NEW passphrase wraps the repo key.\n" +
		"The OLD passphrase stops working immediately and there is no recovery if the new one is lost.\n" +
		"Existing snapshots stay readable."
	modal := NewTypedConfirmModal("Confirm passphrase rotation", body, "rotate", passwordConfirmID, 80, 24)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}

// startRotate builds the mutating-op start. The run closure holds the
// ONLY long-lived copy of the new secret and zeroizes it on return; the
// rotation itself serializes on the repo meta/lock inside repo.Passwd.
func (v PasswordView) startRotate() (tea.Model, tea.Cmd) {
	v.stage = passwordRunning
	r := v.deps.Repo
	cfg := v.deps.Config
	saveKeyring := v.deps.SaveKeyringPassphrase
	// Copy the secret out of the input buffer for the goroutine; the copy
	// is zeroized in the closure's defer.
	newPass := []byte(v.newPass.Value())

	start := startOpMsg{
		name: "password",
		run: func(ctx context.Context) tea.Msg {
			defer crypto.Zeroize(newPass)
			if r == nil {
				return passwordDoneMsg{err: errors.New("no repository configured")}
			}
			if err := r.Passwd(ctx, newPass); err != nil {
				return passwordDoneMsg{err: err}
			}
			// Rotation succeeded. Only now touch the keyring, so a failed
			// rotation never leaves it stale (mirrors cli/passwd.go).
			saved := false
			if cfg != nil && cfg.Passphrase.UseKeyring && saveKeyring != nil {
				if err := saveKeyring(cfg, newPass); err != nil {
					return passwordDoneMsg{err: fmt.Errorf("passphrase rotated, but keyring update failed: %w", err)}
				}
				saved = true
			}
			return passwordDoneMsg{keyringSaved: saved}
		},
	}
	return v, func() tea.Msg { return start }
}

func (v PasswordView) View() string {
	if v.deps.Repo == nil {
		return ui.Muted.Render("no repository configured")
	}
	var b strings.Builder
	switch v.stage {
	case passwordRunning:
		b.WriteString(ui.Primary.Render("Rotating passphrase…"))
		b.WriteString("\n\n" + ui.Muted.Render("rewriting the encrypted config under the repo lock"))

	case passwordDone:
		if v.result.err != nil {
			b.WriteString(ui.Danger.Render("Rotation failed"))
			b.WriteString("\n\n" + passwordErrMessage(v.result.err))
		} else {
			b.WriteString(ui.Success.Render("Passphrase rotated"))
			b.WriteString("\n\n  the old passphrase is no longer accepted")
			if v.result.keyringSaved {
				b.WriteString("\n  OS keyring updated with the new passphrase")
			}
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ rotate again"))

	default: // passwordInput
		b.WriteString(ui.Primary.Render("Rotate repository passphrase"))
		if v.notice != "" {
			b.WriteString("\n" + ui.Warn.Render(v.notice))
		}
		b.WriteString("\n\n" + v.newPass.View())
		b.WriteString("\n" + v.confirmPass.View())
		if v.inputErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.inputErr))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ rotate (typed confirmation required) · tab switch field"))
	}
	return b.String()
}

// passwordErrMessage maps the repo sentinels to operator-readable text.
// Distinct sentinels (not string matching) so a message reword upstream
// never silently breaks the mapping.
func passwordErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrSamePassphrase):
		return "new passphrase matches current — nothing to rotate"
	case errors.Is(err, repo.ErrRepoLocked):
		return "another operation is running — try again when it finishes"
	default:
		return err.Error()
	}
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPasswordFlow -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/password.go internal/tui/password_test.go
git commit -m "feat(tui): add PasswordView for repo passphrase rotation

Two masked inputs (new + confirm) gated by a typed 'rotate' confirm,
then a mutating-op run that calls repo.Passwd under the meta/lock and
re-saves the OS keyring entry via Deps.SaveKeyringPassphrase. The new
secret is masked in every frame and zeroized after use.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 22: Register PasswordView in NewApp under the Operations category

**Files:**
- Modify: `internal/tui/app.go:155-169`
- Test: `internal/tui/password_test.go` (append)

- [ ] **Step 1: Write the failing test**
```go
// TestPasswordView_RegisteredUnderOperations verifies the view is wired
// into NewApp's registry as an "Operations" command so it appears in the
// sidebar rail and the command palette alongside backup/restore/prune.
func TestPasswordView_RegisteredUnderOperations(t *testing.T) {
	app := NewApp(Deps{Repo: newFlowRepo(t)})

	var found bool
	for _, v := range app.views {
		if v.id == "password" {
			found = true
			if _, ok := v.model.(PasswordView); !ok {
				t.Fatalf("password view model is %T, want PasswordView", v.model)
			}
		}
	}
	if !found {
		t.Fatal("NewApp did not register a 'password' view")
	}

	cmd, ok := app.registry.Get("password")
	if !ok {
		t.Fatal("registry has no 'password' command")
	}
	if cmd.Category != "Operations" {
		t.Fatalf("password category = %q, want Operations", cmd.Category)
	}
}
```

Note: this references `app.registry.Get(id) (Command, bool)`. Confirm the exact accessor name on `*Registry` before running (grep `func (.*Registry) Get` in `internal/tui/registry.go`); if the accessor differs (e.g. the registry exposes only a slice/`Commands()`), adapt this assertion to iterate that surface for the `password` command's `Category`. The `app.views` / `app.registry` fields are package-private and reachable because the test is `package tui`.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestPasswordView_RegisteredUnderOperations -count=1`
Expected: FAIL — `NewApp did not register a 'password' view` (the views slice has no `password` entry yet).

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/app.go`, add the view to the `views` slice (currently `internal/tui/app.go:155-164`):
```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "password", model: NewPasswordView(deps)},
	}
```

And add `password` to the `Operations` category map (currently `internal/tui/app.go:167-169`):
```go
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
		"password": "Operations",
	}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestPasswordView_RegisteredUnderOperations -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/tui/password_test.go
git commit -m "feat(tui): register PasswordView under the Operations category

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

Notes for the assembler / downstream units:
- This unit depends on Unit 1 having added `Deps.SaveKeyringPassphrase func(cfg *config.Config, pass []byte) error` (and populating it in `internal/cli/ui.go`). The `TestPasswordFlow_KeyringSaveInvokedOnSuccess` test injects a fake saver, so the model compiles/tests without the cli wiring; the field name is the pinned `SaveKeyringPassphrase`.
- `crypto.Zeroize` (not `Zeroise`/`zeroize`) is the confirmed symbol — `internal/crypto/zeroize.go:21`.
- Security invariants held: the new passphrase is masked via `EchoPassword` in both fields (asserted), never rendered in cleartext (asserted against the whole frame), never logged, zeroized in the run closure's `defer`, and the rotation serializes on the repo meta/lock inside `repo.Passwd`. The keyring is touched only after a successful rotation, mirroring `internal/cli/passwd.go:215-223`.
- One accessor name to verify at implementation time: `Registry.Get` in the second task (see the inline note). Everything else is quoted from real source.


## Part 8 — Agent-apply flow

**Published API:** This unit creates no extraction package. It extends the existing `internal/tui.AgentView` (defined in `internal/tui/agent.go`) in place and adds one new action-package dependency reference: `Deps.Actions *action.Registry` (added by Unit 1; imported here as `github.com/markgustetic/sentra/internal/agent/action`). The new terminal message `agentApplyDoneMsg` implements the existing `opResult()` marker from `internal/tui/ops.go:43`.

---

### Task 23: Add apply-stage state and per-row approve/decline toggle to AgentView

**Files:**
- Modify: `internal/tui/agent.go:47-78` (AgentView struct — add apply fields), `internal/tui/agent.go:105-112` (ShortHelp — advertise `a`/space/enter), `internal/tui/agent.go:164-215` (Update — handle `a`, space, decline toggle), `internal/tui/agent.go:284-309` (View — render reviewing stage)
- Test: `internal/tui/agent_apply_test.go`

The scan path (`busy`, `recs`, `tbl`) is untouched. Apply is a separate state machine keyed by a new `applyStage` field; entering review requires `len(a.recs) > 0 && !a.busy`. This task adds only the `agentReviewing` stage (per-row toggle of an `approved` map keyed by rec index) — confirming/applying land in the next tasks.

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
)

// applyTestRecs is the canned recommendation set the apply-flow tests
// share: one prune, one ignore, one flag_secret. No "none" here — the
// none path is covered separately.
func applyTestRecs() []agent.Recommendation {
	return []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: "snap-old", Severity: "warn", Rationale: "stale"},
		{ID: "rec-ignore", Action: "add_to_ignore", Target: "*.log", Severity: "info", Rationale: "noise"},
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked key"},
	}
}

// agentViewWithRecs builds an AgentView that has already "finished" a
// scan carrying recs, so the apply-flow tests start from the state a
// real scan would leave behind.
func agentViewWithRecs(t *testing.T, deps Deps, recs []agent.Recommendation) AgentView {
	t.Helper()
	a := NewAgentViewWithRunner(deps, func(_ any, _ any) {}) //nolint:staticcheck // replaced below
	// Drive the model through agentDoneMsg so recs land via the real path.
	m, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	_ = a
	return m.(AgentView)
}

// TestAgentApply_EnterReviewOnA verifies pressing `a` with recs present
// moves the view into the reviewing stage (all recs approved by default).
func TestAgentApply_EnterReviewOnA(t *testing.T) {
	v := agentViewWithRecs(t, Deps{}, applyTestRecs())
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	got := m.(AgentView)
	if got.applyStage != agentReviewing {
		t.Fatalf("applyStage = %v, want agentReviewing", got.applyStage)
	}
	// Default: every actionable rec approved.
	for i := range applyTestRecs() {
		if !got.approved[i] {
			t.Errorf("rec %d not approved by default", i)
		}
	}
	if !strings.Contains(got.View(), "approve") {
		t.Errorf("reviewing view should mention approve/decline:\n%s", got.View())
	}
}

// TestAgentApply_ToggleDecline flips a row's approval off with space and
// asserts the map + rendered marker reflect it.
func TestAgentApply_ToggleDecline(t *testing.T) {
	v := agentViewWithRecs(t, Deps{}, applyTestRecs())
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(AgentView)
	// Cursor starts at row 0; toggle it off.
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(AgentView)
	if v.approved[0] {
		t.Fatal("row 0 should be declined after space toggle")
	}
	if !strings.Contains(v.View(), "declined") {
		t.Errorf("view should show a declined row:\n%s", v.View())
	}
}

// TestAgentApply_NoReviewWithoutRecs asserts `a` is inert when the scan
// produced nothing — no recs means nothing to apply.
func TestAgentApply_NoReviewWithoutRecs(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if m.(AgentView).applyStage != agentIdle {
		t.Fatalf("applyStage = %v, want agentIdle (no recs)", m.(AgentView).applyStage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: FAIL — compile error `undefined: agentReviewing`, `undefined: agentIdle`, and `got.applyStage`/`got.approved` undefined fields on `AgentView`.

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/agent.go`, add the stage type and constants near the top of the file (after the imports, before `agentRunner`):

```go
// applyStage tracks the agent-apply state machine, which is layered on
// top of the scan flow: a completed scan leaves recommendations in the
// table, and pressing `a` walks them through review → confirm → apply →
// done. It is deliberately separate from the scan's `busy` flag so the
// two flows can't corrupt each other's state.
type applyStage int

const (
	agentIdle       applyStage = iota // no apply in progress (scan-only view)
	agentReviewing                    // per-row approve/decline toggling
	agentConfirming                   // walking the per-rec confirm modals
	agentApplying                     // op guard held; dispatching actions
	agentApplyDone                    // per-action results + tally shown
)
```

Extend the `AgentView` struct (add fields after `doneErr` at agent.go:60):

```go
	// --- agent-apply state (layered on top of the scan flow) ---

	// applyStage is the apply state machine's current stage. agentIdle
	// means no apply is in flight; the scan view renders normally.
	applyStage applyStage

	// approved[i] records whether recommendation recs[i] is approved for
	// apply. Populated true-for-every-actionable-rec when review starts;
	// space toggles the row under the cursor. "none" recs are never
	// approvable (they carry no side effect) so they're seeded false.
	approved map[int]bool

	// cursor is the highlighted row during agentReviewing. Up/down move
	// it; space toggles approved[cursor].
	cursor int
```

Update `ShortHelp` (agent.go:108-112) so the review keys surface once recs exist. Because `ShortHelp` has a value receiver with no access to state in the current code, change it to read `applyStage`/`recs`:

```go
// ShortHelp lists the view-specific keys for the status bar. The set
// depends on stage: scan-only until recs land, then apply keys while
// reviewing.
func (a AgentView) ShortHelp() []key.Binding {
	switch a.applyStage {
	case agentReviewing:
		return []key.Binding{
			key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle")),
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "apply…")),
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
		}
	default:
		binds := []key.Binding{key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "scan"))}
		if len(a.recs) > 0 && !a.busy {
			binds = append(binds, key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "apply")))
		}
		return binds
	}
}
```

In `Update` (agent.go:195-208, the `tea.KeyMsg` case), add apply-key handling. Replace the existing `tea.KeyMsg` case body with:

```go
	case tea.KeyMsg:
		// Scan key: only when not reviewing/applying an existing result.
		if a.applyStage == agentIdle && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's' {
			if a.busy || a.run == nil {
				return a, nil
			}
			a.busy = true
			a.streamBuf = ui.Subtle.Render("[scanning...]\n")
			a.viewport.SetContent(a.streamBuf)
			a.recs = nil
			a.doneErr = nil
			cmd := a.spawnScan()
			return a, cmd
		}

		// Enter review on `a` when a finished scan produced recs.
		if a.applyStage == agentIdle && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 'a' {
			if a.busy || len(a.recs) == 0 {
				return a, nil
			}
			a.applyStage = agentReviewing
			a.cursor = 0
			a.approved = make(map[int]bool, len(a.recs))
			for i, r := range a.recs {
				// "none" is notify-only: it has no side effect, so it is
				// never approvable. Every other verb defaults to approved.
				a.approved[i] = r.Action != "none"
			}
			return a, nil
		}

		if a.applyStage == agentReviewing {
			return a.updateReviewing(msg)
		}
```

Add the `updateReviewing` method (new, after `Update`):

```go
// updateReviewing handles keystrokes while the operator is toggling
// per-row approval. Up/down move the cursor; space flips approval for
// the current row (except "none" rows, which have no side effect and
// stay unapprovable); esc abandons the apply and returns to the scan
// view. Enter (→ confirming) is wired in a later task.
func (a AgentView) updateReviewing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		a.applyStage = agentIdle
		return a, nil
	case tea.KeyUp:
		if a.cursor > 0 {
			a.cursor--
		}
		return a, nil
	case tea.KeyDown:
		if a.cursor < len(a.recs)-1 {
			a.cursor++
		}
		return a, nil
	case tea.KeySpace:
		if a.cursor >= 0 && a.cursor < len(a.recs) && a.recs[a.cursor].Action != "none" {
			a.approved[a.cursor] = !a.approved[a.cursor]
		}
		return a, nil
	}
	// Also accept j/k as vim-style movement, matching other views.
	if msg.Type == tea.KeyRunes && len(msg.Runes) == 1 {
		switch msg.Runes[0] {
		case 'k':
			if a.cursor > 0 {
				a.cursor--
			}
		case 'j':
			if a.cursor < len(a.recs)-1 {
				a.cursor++
			}
		}
	}
	return a, nil
}
```

In `View` (agent.go:284-309), render the reviewing stage. Insert a branch at the top of the function body, right after the `if a.run == nil` placeholder block returns:

```go
	if a.applyStage == agentReviewing {
		return a.viewReviewing()
	}
```

Add the `viewReviewing` renderer (new method):

```go
// viewReviewing renders the per-row approve/decline list. The cursor
// row is marked; each row shows [x]/[ ] approval, the verb, and target.
// "none" rows render as informational (no checkbox) so the operator
// isn't invited to "approve" a no-op.
func (a AgentView) viewReviewing() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Review recommendations"))
	b.WriteString("  " + ui.Muted.Render("space approve/decline · ⏎ apply · esc cancel"))
	b.WriteString("\n\n")
	for i, r := range a.recs {
		cursor := "  "
		if i == a.cursor {
			cursor = ui.Primary.Render("▸ ")
		}
		var mark string
		switch {
		case r.Action == "none":
			mark = ui.Muted.Render("(fyi)")
		case a.approved[i]:
			mark = ui.Success.Render("[x] approve")
		default:
			mark = ui.Danger.Render("[ ] declined")
		}
		fmt.Fprintf(&b, "%s%s  %s  %s  %s\n",
			cursor, mark, r.ID, r.Action, truncate(r.Target, 24))
	}
	return ui.Panel.Render(b.String()) + "\n"
}
```

Add `"fmt"` to the import block at agent.go:3-16 (currently absent).

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/agent.go internal/tui/agent_apply_test.go
git commit -m "feat(tui): agent-apply review stage with per-row approve/decline

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 24: Add per-rec confirmation gating with the empty-repo typed "wipe" guard

**Files:**
- Modify: `internal/tui/agent.go` (Update — handle enter-from-reviewing, `confirmedMsg`; add confirm-queue fields to the struct; add confirming helpers)
- Test: `internal/tui/agent_apply_test.go`

Enter from `agentReviewing` builds a queue of approved-rec indices, then walks them one modal at a time (simple `NewConfirmModal` per rec). Before the walk starts, the flow re-implements the CLI wipe-guard verbatim (agent_apply.go:82-139): it seeds a remaining-snapshot count from `ListSnapshots`, and if the approved prunes would drive that count to zero, it inserts an extra `NewTypedConfirmModal` requiring the word `"wipe"` as the first gate. Applying itself lands in the next task; here, confirming the last modal transitions the stage to `agentApplying` without yet dispatching.

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
)

// enterReview drives an AgentView (already carrying recs) into the
// reviewing stage.
func enterReview(t *testing.T, v AgentView) AgentView {
	t.Helper()
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	return m.(AgentView)
}

// TestAgentApply_EnterQueuesConfirmModals verifies that pressing enter in
// review pushes a simple confirm modal for the first approved rec and
// arms the confirm queue.
func TestAgentApply_EnterQueuesConfirmModals(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r) // 2 snaps → a single prune can't empty the repo
	recs := []agent.Recommendation{
		{ID: "rec-ignore", Action: "add_to_ignore", Target: "*.log", Severity: "info", Rationale: "noise"},
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	if v.applyStage != agentConfirming {
		t.Fatalf("applyStage = %v, want agentConfirming", v.applyStage)
	}
	if len(v.confirmQueue) != 2 {
		t.Fatalf("confirmQueue len = %d, want 2", len(v.confirmQueue))
	}
	// The command must push a modal.
	msgs := execCmds(t, cmd)
	var pushed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			pushed = true
			if _, isSimple := pm.modal.(ConfirmModal); !isSimple {
				t.Errorf("first modal should be a simple ConfirmModal, got %T", pm.modal)
			}
		}
	}
	if !pushed {
		t.Fatal("enter must push a confirm modal")
	}
}

// TestAgentApply_WipeGuardInsertsTypedModal is the safety test: when the
// approved prunes would empty the repo, the FIRST modal must be the
// typed "wipe" gate, not a plain confirm.
func TestAgentApply_WipeGuardInsertsTypedModal(t *testing.T) {
	r := newFlowRepo(t)
	snapID, _ := seedSnapshotReal(t, r) // exactly ONE snapshot
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: snapID, Severity: "warn", Rationale: "stale"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))

	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	if !v.wipePending {
		t.Fatal("wipe guard should be armed when the last snapshot would be pruned")
	}
	msgs := execCmds(t, cmd)
	var typed bool
	for _, msg := range msgs {
		if pm, ok := msg.(pushModalMsg); ok {
			if tc, ok := pm.modal.(TypedConfirmModal); ok {
				typed = true
				if tc.word != "wipe" {
					t.Errorf("typed word = %q, want wipe", tc.word)
				}
			}
		}
	}
	if !typed {
		t.Fatal("empty-repo prune must gate on the typed wipe modal first")
	}
}

// TestAgentApply_ConfirmWalkReachesApplying walks all confirm modals and
// asserts the flow arrives at agentApplying after the last confirm.
func TestAgentApply_ConfirmWalkReachesApplying(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	recs := []agent.Recommendation{
		{ID: "rec-flag", Action: "flag_secret", Target: ".env", Severity: "high", Rationale: "leaked"},
	}
	m0, _ := NewAgentViewWithRunner(Deps{Repo: r, Actions: action.NewDefaultRegistry()}, nil).Update(agentDoneMsg{recs: recs})
	v := enterReview(t, m0.(AgentView))
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // arms confirming, first modal
	v = m.(AgentView)

	// The single rec's confirm arrives back as confirmedMsg{agentConfirmID}.
	m, _ = v.Update(confirmedMsg{id: agentConfirmID})
	v = m.(AgentView)
	if v.applyStage != agentApplying {
		t.Fatalf("applyStage = %v, want agentApplying after last confirm", v.applyStage)
	}
	_ = strings.TrimSpace
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: FAIL — compile errors `undefined: v.confirmQueue`, `undefined: v.wipePending`, `undefined: agentConfirmID`.

- [ ] **Step 3: Write the minimal implementation**

Add fields to the `AgentView` struct (after `cursor`):

```go
	// confirmQueue holds the indices into recs (in table order) of the
	// approved, actionable recommendations still awaiting their per-rec
	// confirm modal. Popped front-to-back as each confirmedMsg arrives;
	// when it empties the flow moves to agentApplying.
	confirmQueue []int

	// wipePending is set when the approved prunes would delete every
	// snapshot in the repo. It gates on an extra TypedConfirmModal
	// (word "wipe") shown before the per-rec confirms — the TUI mirror
	// of the CLI's --allow-wipe rail. Cleared once the typed gate is
	// satisfied.
	wipePending bool
```

Add the confirm-ID constant near the file's other constants:

```go
// agentConfirmID ties a per-rec simple confirm modal back to this view;
// agentWipeConfirmID ties the empty-repo typed "wipe" gate back to it.
const (
	agentConfirmID     = "agent-apply-confirm"
	agentWipeConfirmID = "agent-apply-wipe"
)
```

In `updateReviewing`, handle `tea.KeyEnter` — replace the method's `switch msg.Type` to add an enter case that delegates to `beginConfirm`:

```go
	case tea.KeyEnter:
		return a.beginConfirm()
```

Add `beginConfirm` (new method). This re-implements the CLI wipe-guard (agent_apply.go:82-139) using `ListSnapshots` under a bounded context:

```go
// beginConfirm transitions from reviewing into the confirmation walk.
// It builds the queue of approved actionable recs, then re-derives the
// CLI's wipe-guard: seed the remaining-snapshot count from ListSnapshots
// and, if the approved prunes would drive it to zero, require the typed
// "wipe" gate before any per-rec confirm. When nothing is approved the
// flow returns to idle with a notice rather than confirming an empty set.
func (a AgentView) beginConfirm() (tea.Model, tea.Cmd) {
	queue := make([]int, 0, len(a.recs))
	for i := range a.recs {
		if a.approved[i] && a.recs[i].Action != "none" {
			queue = append(queue, i)
		}
	}
	if len(queue) == 0 {
		// Nothing to apply — go straight to a done tally of all-declined.
		a.applyStage = agentApplyDone
		a.result = agentApplyDoneMsg{declined: a.declinedCount()}
		return a, nil
	}
	a.confirmQueue = queue

	// Wipe guard: count snapshots that would be pruned and compare with
	// what's in the repo. remaining-prunes >= remaining-snapshots means
	// the sequence empties the repo. Mirrors applyRecommendations'
	// remaining/len(currentSnaps) accounting (cli/agent_apply.go:82-139).
	prunes := 0
	for _, i := range queue {
		if a.recs[i].Action == "prune_snapshot" {
			prunes++
		}
	}
	a.wipePending = false
	if prunes > 0 && a.deps.Repo != nil {
		ctx, cancel := context.WithTimeout(ctxOrBackground(a.deps.Ctx), hydrateTimeout)
		snaps, err := a.deps.Repo.ListSnapshots(ctx)
		cancel()
		// On a list error we conservatively arm the wipe gate: better to
		// force an explicit "wipe" than to silently allow a destructive
		// sequence we couldn't bound.
		if err != nil || prunes >= len(snaps) {
			a.wipePending = true
		}
	}

	a.applyStage = agentConfirming
	if a.wipePending {
		body := "Applying these recommendations would prune every snapshot in the repo.\nThe repository will be left empty."
		modal := NewTypedConfirmModal("Confirm repo wipe", body, "wipe", agentWipeConfirmID, 80, 24)
		return a, func() tea.Msg { return pushModalMsg{modal: modal} }
	}
	return a, a.pushNextConfirm()
}
```

Add the confirm-walk helpers and the `confirmedMsg` handler. First the queue-pop cmd:

```go
// pushNextConfirm pushes a simple confirm modal for the rec at the front
// of confirmQueue. Returns nil when the queue is empty (the caller then
// transitions to applying).
func (a AgentView) pushNextConfirm() tea.Cmd {
	if len(a.confirmQueue) == 0 {
		return nil
	}
	rec := a.recs[a.confirmQueue[0]]
	body := fmt.Sprintf("Apply %s on %q?\n\nrationale: %s", rec.Action, rec.Target, rec.Rationale)
	modal := NewConfirmModal("Confirm apply", body, agentConfirmID, 80, 24)
	return func() tea.Msg { return pushModalMsg{modal: modal} }
}

// declinedCount returns how many actionable recs the operator declined
// during review (approved==false, action != "none"). Used to seed the
// done tally when nothing is applied.
func (a AgentView) declinedCount() int {
	n := 0
	for i, r := range a.recs {
		if r.Action != "none" && !a.approved[i] {
			n++
		}
	}
	return n
}
```

Add a `confirmedMsg` case to `Update` (alongside the existing `tokenMsg`/`agentDoneMsg`/`tea.KeyMsg` cases). It routes both the wipe gate and the per-rec confirms:

```go
	case confirmedMsg:
		if a.applyStage != agentConfirming {
			return a, nil
		}
		// The typed wipe gate clears first, then the per-rec walk begins.
		if msg.id == agentWipeConfirmID && a.wipePending {
			a.wipePending = false
			return a, a.pushNextConfirm()
		}
		if msg.id == agentConfirmID && len(a.confirmQueue) > 0 {
			// Approved: keep the front index in the queue (it becomes the
			// apply set) and advance. When the last confirm clears, move to
			// applying. We DON'T pop here — the front index stays as part of
			// the approved-and-confirmed set the apply task consumes; we
			// track progress with confirmCursor instead.
			a.confirmCursor++
			if a.confirmCursor >= len(a.confirmQueue) {
				a.applyStage = agentApplying
				return a, nil
			}
			return a, a.pushNextConfirmAt(a.confirmCursor)
		}
		return a, nil
```

Because the confirm walk needs a cursor into the fixed queue, add a `confirmCursor int` field to the struct (after `wipePending`) and generalize `pushNextConfirm` to index by cursor. Replace `pushNextConfirm` with:

```go
// pushNextConfirmAt pushes the simple confirm modal for confirmQueue[i].
// Out-of-range i yields nil so the caller can transition to applying.
func (a AgentView) pushNextConfirmAt(i int) tea.Cmd {
	if i < 0 || i >= len(a.confirmQueue) {
		return nil
	}
	rec := a.recs[a.confirmQueue[i]]
	body := fmt.Sprintf("Apply %s on %q?\n\nrationale: %s", rec.Action, rec.Target, rec.Rationale)
	modal := NewConfirmModal("Confirm apply", body, agentConfirmID, 80, 24)
	return func() tea.Msg { return pushModalMsg{modal: modal} }
}

// pushNextConfirm pushes the confirm modal for the current cursor.
func (a AgentView) pushNextConfirm() tea.Cmd { return a.pushNextConfirmAt(a.confirmCursor) }
```

Reset `confirmCursor` to 0 in `beginConfirm` right after `a.confirmQueue = queue` (add the line `a.confirmCursor = 0`).

Add the `agentApplyDoneMsg` type and a `result` field on the struct so `beginConfirm`'s all-declined path compiles. Define the message near the other message types (after `agentDoneMsg`, agent.go:40):

```go
// agentApplyDoneMsg is the terminal message of the agent-apply flow. It
// carries the per-action result lines and the applied/declined/errors
// tally. It implements opResult() so the App's one-op-at-a-time guard
// clears when apply finishes — apply mutates the repo (prune → GC under
// the repo lock) so it MUST go through the mutating-op protocol.
type agentApplyDoneMsg struct {
	lines    []string
	applied  int
	declined int
	errs     int
	err      error
}

func (agentApplyDoneMsg) opResult() {}
```

Add `result agentApplyDoneMsg` to the struct (after `confirmCursor`).

Note: the `agentConfirming` esc-cancel path (dismissing a modal via esc pops it and the App broadcasts nothing back) leaves the flow in `agentConfirming`. Handle a `dismissModalMsg`-driven cancel by resetting on the next non-confirm keystroke is out of scope for this task; the modal's esc emits `dismissModalMsg` only, so add an esc reset in the `tea.KeyMsg` case guarded by `applyStage == agentConfirming`:

```go
		if a.applyStage == agentConfirming && msg.Type == tea.KeyEsc {
			// A modal esc already popped the overlay; return to review so
			// the operator can re-decide rather than being stranded.
			a.applyStage = agentReviewing
			a.confirmQueue = nil
			a.confirmCursor = 0
			a.wipePending = false
			return a, nil
		}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/agent.go internal/tui/agent_apply_test.go
git commit -m "feat(tui): agent-apply per-rec confirm walk with empty-repo wipe gate

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 25: Dispatch approved actions under the op guard and render the done tally

**Files:**
- Modify: `internal/tui/agent.go` (Update — start op from `agentApplying`, handle `agentApplyDoneMsg`, `opRejectedMsg`; add applying/done renderers to View)
- Test: `internal/tui/agent_apply_test.go`

Entering `agentApplying` (last confirm cleared) emits a `startOpMsg{name: "agent-apply", run: ...}` batched with `opTick()`, matching the mutating-op start pattern (`backup.go`/`prune.go`). The run closure iterates the confirmed queue calling `deps.Actions.Dispatch` per rec, re-applying the wipe-guard decrement inside the loop (agent_apply.go:122-139) as a second belt-and-suspenders check, capturing per-action output into a buffer, and returns `agentApplyDoneMsg`. The App's guard clears on that message (it implements `opResult()`); the view renders the tally.

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
)

// driveToApplying walks a single-rec AgentView from review through the
// (single) confirm to the agentApplying stage, returning the view and
// the tea.Cmd the last confirm produced.
func driveToApplying(t *testing.T, v AgentView) (AgentView, tea.Cmd) {
	t.Helper()
	v = enterReview(t, v)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(AgentView)
	m, cmd := v.Update(confirmedMsg{id: agentConfirmID})
	v = m.(AgentView)
	if v.applyStage != agentApplying {
		t.Fatalf("applyStage = %v, want agentApplying", v.applyStage)
	}
	return v, cmd
}

// TestAgentApply_StartOpDispatchesPrune runs the full apply for a real
// prune rec against a real in-memory repo and asserts the snapshot is
// gone after the run closure executes.
func TestAgentApply_StartOpDispatchesPrune(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r) // two snaps; prune one → repo not emptied
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	victim := snaps[0].ID
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: victim, Severity: "warn", Rationale: "stale"},
	}
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry()}
	m0, _ := NewAgentViewWithRunner(deps, nil).Update(agentDoneMsg{recs: recs})
	v := m0.(AgentView)

	_, cmd := driveToApplying(t, v)
	// The applying stage batches startOpMsg + opTick. Pull the startOpMsg
	// and execute its run closure directly (bypassing the App's guard,
	// which is exercised elsewhere) to verify the side effect.
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var found bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, found = s, true
		}
	}
	if !found {
		t.Fatal("agentApplying must emit a startOpMsg")
	}
	if start.name != "agent-apply" {
		t.Fatalf("op name = %q, want agent-apply", start.name)
	}
	done := start.run(context.Background())
	dm, ok := done.(agentApplyDoneMsg)
	if !ok {
		t.Fatalf("run returned %T, want agentApplyDoneMsg", done)
	}
	if dm.applied != 1 {
		t.Errorf("applied = %d, want 1", dm.applied)
	}
	// Verify the actual side effect: victim snapshot is gone.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.ID == victim {
			t.Fatalf("snapshot %s still present after apply", victim)
		}
	}
	// agentApplyDoneMsg is an opResultMsg.
	var _ opResultMsg = agentApplyDoneMsg{}
}

// TestAgentApply_DoneShowsTally feeds the terminal message and asserts the
// view renders the applied/declined/errors tally.
func TestAgentApply_DoneShowsTally(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	v.applyStage = agentApplying
	m, _ := v.Update(agentApplyDoneMsg{
		lines:    []string{"  - rec-1: pruned snap-x"},
		applied:  1,
		declined: 2,
		errs:     0,
	})
	got := m.(AgentView)
	if got.applyStage != agentApplyDone {
		t.Fatalf("applyStage = %v, want agentApplyDone", got.applyStage)
	}
	out := got.View()
	if !strings.Contains(out, "applied") || !strings.Contains(out, "declined") {
		t.Errorf("done view missing tally:\n%s", out)
	}
	if !strings.Contains(out, "rec-1") {
		t.Errorf("done view missing per-action line:\n%s", out)
	}
}

// TestAgentApply_RejectedResetsToReviewing asserts that if the App
// rejects the op (another op in flight), the flow leaves agentApplying.
func TestAgentApply_RejectedResetsToReviewing(t *testing.T) {
	v := NewAgentViewWithRunner(Deps{}, nil)
	v.applyStage = agentApplying
	m, _ := v.Update(opRejectedMsg{name: "agent-apply"})
	if m.(AgentView).applyStage != agentReviewing {
		t.Fatalf("applyStage = %v, want agentReviewing after rejection", m.(AgentView).applyStage)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: FAIL — `agentApplying` never emits a `startOpMsg` (no handler), `agentApplyDoneMsg` isn't handled in `Update`, and the done/applying View branches are missing, so `TestAgentApply_StartOpDispatchesPrune`/`_DoneShowsTally`/`_RejectedResetsToReviewing` fail on `found`, stage, and rendered-tally assertions.

- [ ] **Step 3: Write the minimal implementation**

The transition into `agentApplying` currently returns `a, nil` (in the `confirmedMsg` handler from the previous task). Change that return to emit the op start. Replace the `agentApplying` transition inside the `confirmedMsg` case:

```go
			if a.confirmCursor >= len(a.confirmQueue) {
				return a.startApply()
			}
```

Add the `startApply` method. It re-implements the wipe-guard decrement inside the run closure (agent_apply.go:82-139) and dispatches via `deps.Actions`:

```go
// startApply enters the applying stage and emits the mutating-op start.
// The run closure iterates the confirmed recs, dispatching each through
// deps.Actions.Dispatch with an Env whose Stdout is a buffer we later
// split into per-action result lines. The wipe-guard is re-checked here
// against a live snapshot count (belt-and-suspenders on top of the
// confirm-time gate): a prune that would empty the repo is refused with
// an error line unless wipePending was explicitly cleared by the typed
// "wipe" modal. Mirrors cli/agent_apply.go's applyRecommendations.
func (a AgentView) startApply() (tea.Model, tea.Cmd) {
	a.applyStage = agentApplying

	// Snapshot the approved-and-confirmed recs by value so the goroutine
	// doesn't read model fields concurrently with the Update loop.
	recs := make([]agent.Recommendation, 0, len(a.confirmQueue))
	for _, i := range a.confirmQueue {
		recs = append(recs, a.recs[i])
	}
	registry := a.deps.Actions
	r := a.deps.Repo
	wipeAllowed := true // the confirm-time typed gate already cleared it
	// declined counts recs the operator turned off during review.
	declined := a.declinedCount()

	start := startOpMsg{
		name: "agent-apply",
		run: func(ctx context.Context) tea.Msg {
			if registry == nil {
				return agentApplyDoneMsg{err: errSentinelApply("no action registry configured")}
			}
			// Seed remaining-snapshot count for the in-loop wipe rail.
			remaining := 0
			if r != nil {
				snaps, err := r.ListSnapshots(ctx)
				if err != nil {
					return agentApplyDoneMsg{err: err, declined: declined}
				}
				remaining = len(snaps)
			}

			var buf strings.Builder
			cwd, _ := os.Getwd() // failure → handler falls back to "."
			env := action.Env{
				Repo:        r,
				Stdout:      &buf,
				Cwd:         cwd,
				FormatBytes: ui.FormatBytes,
			}
			applied, errs := 0, 0
			for _, rec := range recs {
				// In-loop wipe rail: an approved prune that would empty the
				// repo is refused unless the typed gate cleared it.
				if rec.Action == "prune_snapshot" && remaining-1 <= 0 && !wipeAllowed {
					fmt.Fprintf(&buf, "  - %s: refused (would empty the repo)\n", rec.ID)
					errs++
					continue
				}
				derr := registry.Dispatch(ctx, env, action.Action(rec.Action),
					rec.ID, rec.Target, rec.Severity, rec.Rationale)
				if derr != nil {
					fmt.Fprintf(&buf, "  - %s: error: %s\n", rec.ID, derr.Error())
					errs++
					continue
				}
				applied++
				if rec.Action == "prune_snapshot" {
					remaining--
				}
			}
			lines := splitNonEmptyLines(buf.String())
			return agentApplyDoneMsg{lines: lines, applied: applied, declined: declined, errs: errs}
		},
	}
	return a, tea.Batch(func() tea.Msg { return start }, opTick())
}
```

Add the helpers used above:

```go
// errSentinelApply is a tiny error type for the "no registry" guard so
// the run closure doesn't need errors.New.
type errSentinelApply string

func (e errSentinelApply) Error() string { return string(e) }

// splitNonEmptyLines splits s on newlines and drops blank entries so the
// done view renders exactly the per-action lines the handlers emitted.
func splitNonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
```

Handle `agentApplyDoneMsg` and `opRejectedMsg` in `Update` (add cases):

```go
	case agentApplyDoneMsg:
		a.applyStage = agentApplyDone
		a.result = msg
		a.confirmQueue = nil
		a.confirmCursor = 0
		return a, nil

	case opRejectedMsg:
		// Our apply start was refused (another op holds the guard). Leave
		// the optimistic applying stage so we don't hang; return to review
		// so the operator can retry once the other op finishes.
		if a.applyStage == agentApplying && msg.name == "agent-apply" {
			a.applyStage = agentReviewing
			a.confirmQueue = nil
			a.confirmCursor = 0
		}
		return a, nil
```

Add the applying/done renderers to `View`. Insert branches after the `agentReviewing` branch added earlier:

```go
	if a.applyStage == agentApplying {
		return a.viewApplying()
	}
	if a.applyStage == agentApplyDone {
		return a.viewApplyDone()
	}
```

Add the renderers:

```go
// viewApplying renders a coarse "applying…" panel. Progress is a simple
// N/M counter over the confirmed set — the individual actions (prune+GC,
// ignore write) are fast and don't stream chunk-level progress, so a
// spinner-free counter is honest about the granularity.
func (a AgentView) viewApplying() string {
	total := len(a.confirmQueue)
	body := ui.Primary.Render("Applying recommendations…") + "\n\n" +
		ui.Muted.Render(fmt.Sprintf("dispatching %d action(s)", total))
	return ui.Panel.Render(body) + "\n"
}

// viewApplyDone renders the per-action result lines and the tally.
func (a AgentView) viewApplyDone() string {
	var b strings.Builder
	if a.result.err != nil {
		b.WriteString(ui.Danger.Render("Apply failed"))
		b.WriteString("\n\n" + a.result.err.Error())
	} else {
		b.WriteString(ui.Success.Render("Apply complete"))
		b.WriteString("\n\n")
		for _, ln := range a.result.lines {
			b.WriteString(ln + "\n")
		}
		fmt.Fprintf(&b, "\n  applied:  %d\n  declined: %d\n  errors:   %d",
			a.result.applied, a.result.declined, a.result.errs)
	}
	b.WriteString("\n\n" + ui.Muted.Render("press `s` to re-scan"))
	return ui.Panel.Render(b.String()) + "\n"
}
```

Add `"os"` to the import block (currently absent; `fmt` was added in the first task). `agent` and `action` and `ui` are already imported (`agent`/`ui` at agent.go:13-15; add `"github.com/markgustetic/sentra/internal/agent/action"` to the import block).

To let `s` re-scan from `agentApplyDone`, the scan-key guard checks `a.applyStage == agentIdle`. Add a reset: in the `tea.KeyMsg` case, before the scan-key check, add:

```go
		// From the done screen, `s` re-scans: reset apply state to idle so
		// the scan-key guard below fires.
		if a.applyStage == agentApplyDone && msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == 's' {
			a.applyStage = agentIdle
			a.approved = nil
			a.result = agentApplyDoneMsg{}
		}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1`
Expected: PASS

- [ ] **Step 5: Commit**
```bash
git add internal/tui/agent.go internal/tui/agent_apply_test.go
git commit -m "feat(tui): dispatch agent-apply actions under the op guard with tally

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 26: End-to-end apply flow test through the App shell (op guard + modal broadcast)

**Files:**
- Modify: `internal/tui/agent.go` (no source change expected; this task is a guard-rail integration test that may surface a wiring gap)
- Test: `internal/tui/agent_apply_test.go`

This exercises the full path through the `App` (not just the isolated view): the App's `startOpMsg` handler must accept `name: "agent-apply"`, run the closure, receive the `agentApplyDoneMsg`, clear the guard (because it implements `opResult()`), and broadcast the done message back so the view renders the tally. It also confirms the modal-broadcast round-trip: a `confirmedMsg` pushed by the view is popped by the App and rebroadcast to the view.

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
)

// agentIndexIn returns the index of the agent view in the App's view
// slice so the test can reach into it after routing.
func agentIndexIn(t *testing.T, app App) int {
	t.Helper()
	for i, v := range app.views {
		if v.id == "agent" {
			return i
		}
	}
	t.Fatal("agent view not registered in App")
	return -1
}

// TestAgentApply_EndToEndThroughApp routes an apply from review to done
// through the App so the op guard + modal broadcast are exercised, then
// asserts the repo side effect and the cleared guard.
func TestAgentApply_EndToEndThroughApp(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	victim := snaps[0].ID
	deps := Deps{Repo: r, Actions: action.NewDefaultRegistry(), Ctx: context.Background()}
	app := NewApp(deps)
	// Give the app a size so modals render.
	m, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	app = m.(App)

	idx := agentIndexIn(t, app)
	// Seed the agent view with a prune rec via agentDoneMsg (broadcast).
	recs := []agent.Recommendation{
		{ID: "rec-prune", Action: "prune_snapshot", Target: victim, Severity: "warn", Rationale: "stale"},
	}
	m, _ = app.Update(agentDoneMsg{recs: recs})
	app = m.(App)
	// Focus the agent view.
	app.active = idx
	app.focus = focusContent

	// `a` → review, `enter` → confirm walk (2 snaps so no wipe gate).
	m, _ = app.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	app = m.(App)
	m, cmd := app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	// The enter produced a pushModalMsg cmd; run it and feed the result.
	for _, msg := range execCmds(t, cmd) {
		m, _ = app.Update(msg)
		app = m.(App)
	}
	if len(app.modals) != 1 {
		t.Fatalf("expected a confirm modal on the stack, got %d", len(app.modals))
	}
	// Confirm the modal (enter). The modal emits confirmedMsg; the App
	// pops it and broadcasts back to the view, which starts the op.
	m, cmd = app.Update(tea.KeyMsg{Type: tea.KeyEnter})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		m, cmd2 := app.Update(msg)
		app = m.(App)
		// The confirmedMsg → view startApply → startOpMsg → op runs.
		for _, msg2 := range execCmds(t, cmd2) {
			m, cmd3 := app.Update(msg2)
			app = m.(App)
			for _, msg3 := range execCmds(t, cmd3) {
				m, _ = app.Update(msg3)
				app = m.(App)
			}
		}
	}

	// The op guard must be cleared once the done message lands.
	if app.opRunning != "" {
		t.Errorf("opRunning = %q, want cleared after agent-apply", app.opRunning)
	}
	// Side effect: victim snapshot gone.
	after, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range after {
		if s.ID == victim {
			t.Fatalf("snapshot %s still present after end-to-end apply", victim)
		}
	}
	// The agent view renders the done tally.
	av := app.views[idx].model.(AgentView)
	if av.applyStage != agentApplyDone {
		t.Fatalf("agent view stage = %v, want agentApplyDone", av.applyStage)
	}
	if !strings.Contains(av.View(), "applied") {
		t.Errorf("agent view should show tally:\n%s", av.View())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestAgentApply_EndToEndThroughApp -count=1`
Expected: FAIL first on the compile reference to `newTestApp`-style helpers if absent — verify `newTestApp` exists (it's referenced in `ops_test.go:60`) and reuse `NewApp` directly as written. If the flow leaves `opRunning` set (guard not cleared) or the snapshot remains, the assertions fail — that is the behavior this test pins.

- [ ] **Step 3: Write the minimal implementation**
No new source is expected: the App already routes `startOpMsg` (app.go:245-261), clears the guard on any `opResultMsg` (app.go:231-239), broadcasts `confirmedMsg` (app.go:300-319), and broadcasts non-key messages to every view (app.go:327). If the test reveals the op guard isn't clearing, the cause is `agentApplyDoneMsg` not implementing `opResult()` — confirm the marker method `func (agentApplyDoneMsg) opResult() {}` exists (added in the prior task) and no source change is needed. If the test passes with no edit, record that explicitly in the commit body.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run TestAgentApply -count=1 && go test ./internal/tui/ -count=1`
Expected: PASS (whole `internal/tui` package green — the new flow doesn't regress scan or other flows).

- [ ] **Step 5: Commit**
```bash
git add internal/tui/agent_apply_test.go
git commit -m "test(tui): end-to-end agent-apply through the App op guard and modal broadcast

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Notes for the assembling controller / downstream units

- **Dependency on Unit 1:** every task references `Deps.Actions *action.Registry` (the new field Unit 1 adds to `tui.Deps` at `internal/tui/app.go:40`). Until Unit 1 lands, the tests here won't compile. The `internal/cli/ui.go` `runUI` wiring must populate `Actions` with `action.NewDefaultRegistry()` (the same registry the CLI builds) — that threading is Unit 1's responsibility, not this unit's.
- **No new imports beyond the standard/existing set:** this unit adds `fmt`, `os`, and `github.com/markgustetic/sentra/internal/agent/action` to `internal/tui/agent.go`'s import block. `context`, `strings`, `agent`, `ui` are already imported (`agent.go:3-16`).
- **Security invariants held:** the scan path is untouched (LLM still sees summaries only — `agent.go:88-100`). `flag_secret` dispatch writes no secret (`internal/agent/action/flag.go:32-48` prints only a "rotate the credential at <target>" line). Prune's GC still derives its live set from present manifests under the repo lock (`internal/agent/action/prune.go:45` → `repo.GC`). All 12-operation confirmation policy is satisfied: every actionable rec is simple-confirm-gated, and the repo-emptying prune adds the TYPED `"wipe"` gate.
- **`ShortHelp` receiver change:** the existing `func (AgentView) ShortHelp()` (value receiver, no field access at `agent.go:108`) becomes `func (a AgentView) ShortHelp()` so it can branch on `applyStage`/`recs`. This is source-compatible with the `viewShortHelper` interface (`app.go:96`) which only requires the method set.


## Part 9 — Final registration & full-branch gate

**Published API:** none. This task is the authoritative end-state for `internal/tui/app.go`'s view registration. It supersedes the illustrative full-slice snippets shown inside the per-flow tasks (per Execution Note 2): those tasks each *insert one line*; this task pins the complete final slice and category map.

### Task 27: Register all Phase 2c views + full-branch gate

**Files:**
- Modify: `internal/tui/app.go` (the `views := []viewEntry{...}` slice and the `categories` map inside `NewApp`)
- Test: `internal/tui/app_test.go` (new test appended)

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`. It asserts the six new standalone views are registered (agent-apply extends the existing `agent` view, so there is no new `id` for it) and that the mutating flows land in the "Operations" category. It follows the `TestApp_CheckReplacesOperationsInSidebar` pattern (rendering `app.View()` and inspecting `app.views`).

```go
// TestApp_Phase2cViewsRegistered: after Phase 2c, all six new standalone
// views are present (doctor, recovery-kit, policies, schedule, sync,
// password) alongside the eight pre-existing ones, for a total of 14.
// agent-apply is NOT a new view — it extends the existing "agent" view in
// place — so it adds no id.
func TestApp_Phase2cViewsRegistered(t *testing.T) {
	app := newTestApp(t)

	want := []string{
		"dashboard", "snapshots", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password",
	}
	got := make(map[string]bool, len(app.views))
	for _, v := range app.views {
		got[v.id] = true
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("view %q not registered", id)
		}
	}
	if len(app.views) != len(want) {
		t.Fatalf("views = %d, want %d", len(app.views), len(want))
	}

	// The direct data operations carry the "Operations" palette category.
	out := app.View()
	for _, label := range []string{"Sync", "Password", "Doctor", "Policies"} {
		if !strings.Contains(out, label) {
			t.Errorf("sidebar/palette should list %q:\n%s", label, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestApp_Phase2cViewsRegistered -count=1`
Expected: FAIL — either a nil/panic on the not-yet-added constructors if run before the flow tasks, or (after the flow tasks, if their per-task inserts were only partial) a `views = N, want 14` mismatch / missing-id error.

- [ ] **Step 3: Write the authoritative registration**

Set the `views` slice and `categories` map in `NewApp` (`internal/tui/app.go`) to exactly this end-state. Read-only inspections group under the default "Views" category; the direct data operations group under "Operations".

```go
	views := []viewEntry{
		{id: "dashboard", model: NewDashboard(deps)},
		{id: "snapshots", model: NewSnapshots(deps)},
		{id: "diff", model: NewDiff(deps)},
		{id: "check", model: NewCheckView(deps)},
		{id: "doctor", model: NewDoctorView(deps)},
		{id: "recovery-kit", model: NewRecoveryKitView(deps)},
		{id: "policies", model: NewPoliciesView(deps)},
		{id: "schedule", model: NewScheduleView(deps)},
		{id: "agent", model: NewAgentView(deps)},
		{id: "backup", model: NewBackupView(deps)},
		{id: "restore", model: NewRestoreView(deps)},
		{id: "prune", model: NewPruneView(deps)},
		{id: "sync", model: NewSyncView(deps)},
		{id: "password", model: NewPasswordView(deps)},
	}
	// The direct data operations form the "Operations" category in the rail
	// and palette; every read-only/management view defaults to "Views".
	categories := map[string]string{
		"backup":   "Operations",
		"restore":  "Operations",
		"prune":    "Operations",
		"sync":     "Operations",
		"password": "Operations",
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestApp_Phase2cViewsRegistered -count=1`
Expected: PASS

- [ ] **Step 5: Run the full CI-equivalent gate**

Run each; all must be clean:

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

Expected: build/vet/fmt clean; every package `ok`; lint `0 issues`; tidy and diff-check clean.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): register all Phase 2c views; full-branch gate green"
```

- [ ] **Step 7: Manual smoke test (human-run — cannot be automated)**

Build and drive the TUI in a real terminal; a subagent/CI cannot exercise interactive rendering:

```bash
just build && ./bin/sentra ui
```

Walk each new flow: Doctor (run → report), Recovery-kit (run → render → save), Policies (list → add/remove/run with confirms), Schedule (status → install/uninstall), Sync (configure → dry-run → real run), Password (masked input → typed "rotate" → rotate), Agent → apply (approve rows → per-rec confirm → apply). Confirm spinners animate, tables navigate, modals gate as expected, and layout holds at your terminal width.
