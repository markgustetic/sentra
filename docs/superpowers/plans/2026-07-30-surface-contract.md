# Surface Contract Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the two-way TUI/CLI parity obligation with a one-way surface contract, and delete the duplicated `huh` CLI setup wizard by making `sentra setup` a thin launcher for the TUI wizard.

**Architecture:** `runUI` gains a `forceSetup bool` that lets a caller land the TUI on the setup wizard regardless of whether a config exists. `sentra setup` becomes a ~45-line cobra command that delegates to it. Everything the `huh` wizard needed — its forms, spinner, summary printers, and the 18-field `SetupDeps` struct — is deleted. The engine (`internal/setup`) is untouched; it already holds the logic and 66 tests.

**Tech Stack:** Go 1.25, cobra, bubbletea/bubbles/lipgloss, `internal/setup` engine, `huh` (retained only for `internal/cli/confirm.go`).

**Spec:** `docs/superpowers/specs/2026-07-30-surface-contract-design.md`

## Global Constraints

- Go 1.25, module `github.com/markgustetic/sentra`.
- `internal/tui` must NEVER import `internal/cli`. The reverse is allowed and used here.
- Selection and state are glyphs/text, never color alone — tests run under lipgloss's Ascii profile, which emits no ANSI.
- Never wrap an already-styled string: `outer.Render(s)` where `s` holds styled fragments embeds a reset that kills the outer style mid-line. Style plain text, then append styled fragments.
- No secrets in artifacts: never write passphrases, wrapped keys, salts, or AWS credentials into configs, drafts, logs, tests, or fixtures.
- `huh` cannot run inside a live `tea.Program`. Nothing in this plan may introduce a `huh` call reachable from the TUI.
- Every commit must build in isolation. Before claiming a series is good:
  ```sh
  git worktree add -q --detach /tmp/chk <ref> && (cd /tmp/chk && go build ./...)
  git worktree remove --force /tmp/chk
  ```
- Full gate is `just check` (build, `test -race`, vet, lint, vuln, `go mod tidy -diff`, `gofmt`, `git diff --check`).
- While iterating, scope `-race` to changed packages; run the full `-race ./...` once before pushing.
- `command tail` / `command head` — `tail`/`head`/`cat` are aliased to `bat` in this shell.

---

### Task 1: Close the prepare-failure ordering gap

The CLI's `TestSetup_PreparesAWSBeforeWritingConfig` is the only test asserting that a failed `PrepareAWS` prevents the config write. It dies in Task 4, so its replacement must exist first. `setup.Engine` has no pipeline method — each caller sequences the steps — so the replacement belongs at the wizard level, which is the surviving sequencer (`internal/tui/setup_wizard.go:597-655`).

This test passes against current code: the wizard already orders correctly. It is a characterization test, so Step 2 proves it can fail by breaking the order on purpose.

**Files:**
- Test: `internal/tui/setup_wizard_test.go` (append)

**Interfaces:**
- Consumes: `stubEffects{prepareErr error}` (already defined at `internal/tui/setup_wizard_test.go:491`), `setupTypeField`, `setupTypePass`, `execCmds`, `setupReviewConfirmID`, `startOpMsg`, `setupDoneMsg`, `confirmedMsg` — all existing test helpers in this package.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write the test**

Append to `internal/tui/setup_wizard_test.go`:

```go
// TestSetupWizard_PrepareFailureWritesNoConfig pins the pipeline ordering
// invariant: PrepareAWS runs BEFORE WriteConfig, so a failed bucket prep must
// leave no sentra.yaml behind. A config written first would record a bucket
// that does not exist, and the operator's next command would fail against it.
// This replaces the CLI wizard's TestSetup_PreparesAWSBeforeWritingConfig,
// whose runSetup sequencer is deleted; the wizard is now the only sequencer.
func TestSetupWizard_PrepareFailureWritesNoConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	deps := Deps{
		Config:       &config.Config{},
		ConfigPath:   cfgPath,
		SetupEffects: stubEffects{prepareErr: errors.New("AccessDenied: s3:CreateBucket")},
	}
	v := NewSetupWizardView(deps)
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)

	// Drive to review: backend → details → actions → passphrase → review.
	v.backendCursor = 0
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	v = setupTypeField(v, "my-sentra-bucket")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	v = setupTypePass(v, "correcthorse", "correcthorse")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}) // push confirm modal
	v = m.(SetupWizardView)
	m, cmd := v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView)

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
	if done.err == nil {
		t.Fatal("PrepareAWS failure must surface as setupDoneMsg.err")
	}
	if _, statErr := os.Stat(cfgPath); !os.IsNotExist(statErr) {
		t.Fatalf("PrepareAWS failed, so no config may be written; stat err = %v", statErr)
	}
}
```

- [ ] **Step 2: Run it, then prove it can fail**

Run: `go test ./internal/tui/ -run TestSetupWizard_PrepareFailureWritesNoConfig -v`
Expected: PASS (the wizard already orders correctly).

Now prove the assertion has teeth. In `internal/tui/setup_wizard.go`, temporarily move the `eng.WriteConfig(...)` call in the provisioning op (around line 630-650) to run *before* the `if plan.PrepareAWS` block.

Run: `go test ./internal/tui/ -run TestSetupWizard_PrepareFailureWritesNoConfig -v`
Expected: FAIL with `PrepareAWS failed, so no config may be written; stat err = <nil>`

Revert the reordering. Re-run: PASS.

- [ ] **Step 3: Verify the package is clean**

Run: `go test -race ./internal/tui/`
Expected: PASS (no other test regressed).

- [ ] **Step 4: Commit**

```bash
git add internal/tui/setup_wizard_test.go
git commit -m "test(tui): pin prepare-failure ordering before deleting the CLI wizard

The CLI wizard's TestSetup_PreparesAWSBeforeWritingConfig is the only test
asserting a failed PrepareAWS leaves no config on disk, and it dies with
runSetup. setup.Engine has no pipeline method — callers sequence the steps —
so the invariant's home is the wizard, now the only sequencer."
```

---

### Task 2: Add `forceSetup` routing and `tui.Deps.Reconfigure`

The behavioral core. `runUI` learns to force the wizard; `tui.Deps` learns to tell the wizard it is overwriting.

**Files:**
- Modify: `internal/cli/ui.go:112` (NewUI RunE), `:122` (runUI signature), `:155-168` (routing + seed guard), `:68-77` (SetupSeedConfig doc comment)
- Modify: `internal/cli/local.go:88` (call-site update)
- Modify: `internal/tui/app.go:44` (Deps struct — add `Reconfigure`)
- Test: `internal/cli/ui_test.go` (append)

**Interfaces:**
- Consumes: `launchState{ConfigExists, PassphraseAvailable bool; Config *config.Config}` from `internal/cli/repo_open.go:61`; `writeBackupConfigFile(t, dir)` and `chDir(t, dir)` test helpers.
- Produces:
  - `runUI(cmd *cobra.Command, deps UIDeps, cfgPath string, forceSetup bool) error`
  - `tui.Deps.Reconfigure bool` — true only when the wizard is opening over a config that already exists on disk. Task 3 and Task 4 both depend on these exact names.

- [ ] **Step 1: Write the failing routing-matrix test**

Append to `internal/cli/ui_test.go`. Six rows cover every distinct routing behavior; the two `!ConfigExists && PassphraseAvailable` combinations collapse into rows 1 and 2, because `!ConfigExists` decides the branch on its own.

```go
// TestRunUI_SetupRoutingMatrix drives the full cross product of
// (ConfigExists x PassphraseAvailable x forceSetup) rather than only the new
// forceSetup cases. The same class of bug — one launch condition silently
// stealing another's route — has shipped here before, so the regression rows
// (forceSetup=false) are as load-bearing as the new ones.
func TestRunUI_SetupRoutingMatrix(t *testing.T) {
	const passphrase = "hunter2"

	tests := []struct {
		name            string
		configExists    bool
		passphraseAvail bool
		forceSetup      bool
		wantInitialView string
		wantReconfigure bool
	}{
		{"first run", false, false, false, "setup", false},
		{"first run, forced", false, false, true, "setup", false},
		{"configured and locked", true, false, false, "unlock", false},
		{"configured and locked, forced", true, false, true, "setup", true},
		{"configured and unlocked", true, true, false, "", false},
		{"configured and unlocked, forced", true, true, true, "setup", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			chDir(t, dir)

			var passFile string
			if tc.configExists {
				writeBackupConfigFile(t, ".") // keyring off, no env source
			}
			if tc.passphraseAvail {
				passFile = filepath.Join(dir, "pass.txt")
				if err := os.WriteFile(passFile, []byte(passphrase+"\n"), 0o600); err != nil {
					t.Fatalf("write passphrase file: %v", err)
				}
			}

			// Initialize whenever a passphrase source exists, not only for the
			// dashboard row. Only that row opens the repo, but the launch probe
			// resolves the passphrase on every configured row, and an
			// uninitialized store is a needless way for an unrelated row to fail.
			store := blobstore.NewMemory()
			if tc.passphraseAvail {
				r, err := repo.Init(context.Background(), store, []byte(passphrase))
				if err != nil {
					t.Fatalf("repo init: %v", err)
				}
				if err := r.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
			}

			var captured tui.App
			deps := UIDeps{
				RepoDeps: RepoDeps{
					NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
						return store, nil
					},
					PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
						t.Fatal("interactive passphrase resolver must not run on the launch path")
						return nil, nil
					},
				},
				Run:            func(app tui.App) error { captured = app; return nil },
				PassphraseFile: func() string { return passFile },
			}

			cmd := NewUI(deps)
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{})
			// NewUI always passes forceSetup=false; exercise the forced path
			// through runUI directly, which is what `sentra setup` will call.
			var err error
			if tc.forceSetup {
				err = runUI(cmd, deps, configFileName, true)
			} else {
				err = cmd.Execute()
			}
			if err != nil {
				t.Fatalf("launch: %v", err)
			}

			d := captured.Deps()
			if d.InitialView != tc.wantInitialView {
				t.Errorf("InitialView = %q, want %q", d.InitialView, tc.wantInitialView)
			}
			if d.Reconfigure != tc.wantReconfigure {
				t.Errorf("Reconfigure = %v, want %v", d.Reconfigure, tc.wantReconfigure)
			}
		})
	}
}

// TestRunUI_ForcedSetupPrefersOnDiskConfigOverSeed guards the seed condition.
// forceSetup makes initial=="setup" reachable WITH a config present, so the
// SetupSeedConfig override must additionally require !ConfigExists — otherwise
// a seeded caller (sentra local's MinIO coordinates) would silently outrank the
// operator's real config on a forced reconfigure.
func TestRunUI_ForcedSetupPrefersOnDiskConfigOverSeed(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")

	// config.Config.Repo and .Repo.S3 are ANONYMOUS nested structs, so there is
	// no config.RepoConfig/config.S3Config to compose a literal from. Build the
	// seed by field assignment.
	seed := &config.Config{}
	seed.Repo.S3.Bucket = "seeded-not-wanted"

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
			},
		},
		Run:             func(app tui.App) error { captured = app; return nil },
		SetupSeedConfig: seed,
	}
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if err := runUI(cmd, deps, configFileName, true); err != nil {
		t.Fatalf("launch: %v", err)
	}
	d := captured.Deps()
	if d.Config.Repo.S3.Bucket == "seeded-not-wanted" {
		t.Error("forced setup over an existing config must use the on-disk config, not SetupSeedConfig")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/cli/ -run 'TestRunUI_SetupRoutingMatrix|TestRunUI_ForcedSetupPrefersOnDiskConfigOverSeed'`
Expected: FAIL to compile — `too many arguments in call to runUI` and `d.Reconfigure undefined`.

- [ ] **Step 3: Add `Reconfigure` to `tui.Deps`**

In `internal/tui/app.go`, inside the `Deps` struct (starts at line 44), next to `InitialView`:

```go
	// Reconfigure tells the setup wizard it is opening over a sentra.yaml that
	// already exists, so its review stage can warn that completing the wizard
	// overwrites that file. Set by runUI only on the forced-setup path
	// (`sentra setup`); the first-run path leaves it false because there is
	// nothing to overwrite.
	Reconfigure bool
```

- [ ] **Step 4: Thread `forceSetup` through `runUI`**

In `internal/cli/ui.go`, change the signature at line 122:

```go
func runUI(cmd *cobra.Command, deps UIDeps, cfgPath string, forceSetup bool) error {
```

Update `NewUI`'s RunE at line 112:

```go
			return runUI(cmd, deps, cfgPath, false)
```

Update `internal/cli/local.go:88`:

```go
	return runUI(cmd, ui, localConfigFileName, false)
```

- [ ] **Step 5: Update the routing branch**

In `internal/cli/ui.go`, replace the branch head at line 155-159:

```go
	// First run (no config), configured-but-locked, and an explicit
	// `sentra setup` all launch the TUI WITHOUT opening a repo — the wizard /
	// unlock view own the interactive path so huh never fires here. Repo is
	// nil; the unlock view swaps a live repo in via repoReadyMsg once the user
	// provides the passphrase. forceSetup outranks the lock gate: reconfiguring
	// must not demand the passphrase for a repo the operator may be replacing.
	if forceSetup || !st.ConfigExists || !st.PassphraseAvailable {
		initial := "setup"
		if st.ConfigExists && !forceSetup {
			initial = "unlock"
		}
```

Then tighten the seed guard (currently `if initial == "setup" && deps.SetupSeedConfig != nil`):

```go
		// On the true first-run path (no config file), an optional seed config
		// pre-fills the wizard's S3 fields. The !ConfigExists guard is load
		// bearing now that forceSetup can reach initial=="setup" WITH a config
		// present: a real on-disk config must always outrank a caller-supplied
		// seed. Nothing is written to disk here — the wizard persists on
		// completion.
		launchCfg := st.Config
		if initial == "setup" && !st.ConfigExists && deps.SetupSeedConfig != nil {
			launchCfg = deps.SetupSeedConfig
		}
```

And pass `Reconfigure` into the App's `tui.Deps` literal (alongside `InitialView: initial`):

```go
			Reconfigure:             forceSetup && st.ConfigExists,
```

- [ ] **Step 6: Update the `SetupSeedConfig` doc comment**

In `internal/cli/ui.go`, the comment at lines 68-77 says the seed applies "When non-nil AND the launch lands on the first-run path (no config file present)". That is now enforced by an explicit `!st.ConfigExists` term rather than implied by the branch. Append one sentence:

```go
	// `sentra setup` forces the wizard with a config present, so the seed's
	// first-run precondition is now an explicit !ConfigExists term in runUI
	// rather than a property of the branch.
```

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/cli/ -run 'TestRunUI' -v`
Expected: PASS — the six matrix rows, the seed guard, and every pre-existing `TestRunUI_*` test.

Run: `go test -race ./internal/cli/ ./internal/tui/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/ui.go internal/cli/local.go internal/cli/ui_test.go internal/tui/app.go
git commit -m "feat(cli,tui): let a caller force the TUI setup wizard

runUI gains forceSetup, which lands the App on the wizard regardless of
whether sentra.yaml exists, and tui.Deps gains Reconfigure so the wizard can
warn that finishing overwrites an existing config. forceSetup outranks the
lock gate: reconfiguring must not demand the passphrase for a repo the
operator may be replacing.

The seed override now requires !ConfigExists explicitly. That was previously
implied by the branch — forceSetup breaks the implication, and without the
guard a seeded caller would outrank the operator's real config."
```

---

### Task 3: Review-stage overwrite warning

**Files:**
- Modify: `internal/tui/setup_wizard.go:1133-1139` (the `stageReview` render arm)
- Test: `internal/tui/setup_wizard_test.go` (append)

**Interfaces:**
- Consumes: `tui.Deps.Reconfigure` from Task 2; `v.deps` (the view's `Deps`), `ui.Warn`, `setup.ReviewText`.
- Produces: nothing consumed by later tasks.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/setup_wizard_test.go`:

```go
// TestSetupWizard_ReviewWarnsOnReconfigure: when the wizard was opened over an
// existing config (`sentra setup` on a configured repo), the review stage is
// the only confirmation gate before the file is rewritten, so it must name the
// path it will overwrite. Asserted as text, not color — tests run under
// lipgloss's Ascii profile, which emits no ANSI at all.
func TestSetupWizard_ReviewWarnsOnReconfigure(t *testing.T) {
	tests := []struct {
		name        string
		reconfigure bool
		wantWarning bool
	}{
		{"reconfigure warns", true, true},
		{"first run stays quiet", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "sentra.yaml")
			v := NewSetupWizardView(Deps{
				Config:      &config.Config{},
				ConfigPath:  cfgPath,
				Reconfigure: tc.reconfigure,
			})
			m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			v = m.(SetupWizardView)
			v.stage = stageReview

			out := v.View()
			got := strings.Contains(out, "overwrites")
			if got != tc.wantWarning {
				t.Errorf("review stage overwrite warning present = %v, want %v; got:\n%s",
					got, tc.wantWarning, out)
			}
			if tc.wantWarning && !strings.Contains(out, cfgPath) {
				t.Errorf("overwrite warning must name the config path %q; got:\n%s", cfgPath, out)
			}
		})
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/tui/ -run TestSetupWizard_ReviewWarnsOnReconfigure -v`
Expected: FAIL on the `reconfigure warns` subtest — `overwrite warning present = false, want true`.

- [ ] **Step 3: Render the warning**

In `internal/tui/setup_wizard.go`, in the `case stageReview:` arm (line 1133), insert after the `setup.ReviewText` write and before the `v.notice` block:

```go
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run 'TestSetupWizard' -v`
Expected: PASS, including the pre-existing `TestSetupWizard_ReviewRendersPlanAndPushesConfirm`.

Run: `go test -race ./internal/tui/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): warn at review when setup will overwrite a config

sentra setup can now open the wizard over an existing sentra.yaml, and the
review stage is the only gate before that file is rewritten, so it names the
path. Replaces the deleted CLI wizard's separate overwrite prompt."
```

---

### Task 4: Make `sentra setup` a launcher and delete the CLI wizard

The large one. It cannot be split further: the moment `NewSetup` stops calling `HuhSetupPrompt`, every wizard-only symbol becomes unused and `golangci-lint`'s `unused` check fails. Deleting them in the same commit is what keeps each commit gate-clean.

**Files:**
- Rewrite: `internal/cli/setup.go` (297 → ~45 lines)
- Delete: `internal/cli/setup_wizard.go`, `internal/cli/setup_spinner.go`, `internal/cli/setup_init.go`, `internal/cli/setup_errors.go`
- Delete: `internal/cli/setup_wizard_test.go` if present, plus the wizard-driving tests in `internal/cli/setup_test.go`, `internal/cli/setup_helpers_test.go`, `internal/cli/setup_init_alias_test.go`, `internal/cli/setup_transform_alias_test.go`
- Modify: `internal/cli/setup_auth.go` (drop `printSetupAuthProgress`)
- Modify: `cmd/sentra/commands.go:17` and `:25-30`, `cmd/sentra/passphrase.go:68`
- Test: `internal/cli/setup_test.go` (the surviving file, with new launcher tests)

**Interfaces:**
- Consumes: `runUI(cmd, deps, cfgPath, forceSetup)` from Task 2; `UIDeps` (embeds `RepoDeps`, which carries `Stdout`); `newSetupIAMPolicy(out io.Writer) *cobra.Command` from `internal/cli/setup_iam_policy.go`; `configFileName`.
- Produces: `NewSetup(deps UIDeps) *cobra.Command`. `SetupDeps` and every `Huh*Setup*` symbol cease to exist.

- [ ] **Step 1: Audit the CLI setup tests before deleting any**

Do not skip this. For each of the 45 test functions in `internal/cli/setup_test.go`, classify it:

- **Duplicate** — an equivalent assertion exists in `internal/setup` or `internal/tui`. Delete.
- **Launcher** — it tests command registration or the `--config` flag. Rewrite against the new launcher (Step 4).
- **Orphan** — it only tested deleted presentation code (`TestSetupProgressFallsBackToPlainOutput`, `TestSetupPlanReviewMentionsPassphraseSourceForInit`). Delete.
- **Uncovered** — no equivalent anywhere. **Stop and port it below the CLI before continuing.**

Run this to produce the checklist:

```bash
grep "^func Test" internal/cli/setup_test.go | sed 's/func \(Test[A-Za-z0-9_]*\).*/\1/' | sort
grep -h "^func Test" internal/setup/*_test.go internal/tui/setup_wizard_test.go | sed 's/func \(Test[A-Za-z0-9_]*\).*/\1/' | sort
```

The spec records the already-confirmed mappings (plan derivation, `endpoint_url` guard, engine stages, error advice, review/summary text, AWS CLI/SSO probes). `TestSetup_PreparesAWSBeforeWritingConfig` is covered by Task 1. Expect zero **Uncovered**; if you find one, it is a real gap and porting it is part of this task.

- [ ] **Step 2: Rewrite `internal/cli/setup.go`**

Replace the entire file with:

```go
package cli

import (
	"github.com/spf13/cobra"
)

// NewSetup returns the cobra command for `sentra setup`. It is a thin launcher
// for the TUI setup wizard: the wizard drives setup.Engine directly, so a
// second huh-based wizard here would be a duplicate of the same flow against
// the same engine.
//
// The command forces the wizard even when sentra.yaml already exists, which
// makes reconfiguring a normal supported flow. The wizard's review stage is
// the confirmation gate for the overwrite, so there is no --force flag.
func NewSetup(deps UIDeps) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run the guided Sentra setup wizard",
		Long: "Open the guided terminal wizard for configuring Sentra. The wizard " +
			"can sign in with AWS CLI browser login, run AWS SSO profile setup when " +
			"selected, verify credentials, prepare an AWS S3 bucket, write " +
			"sentra.yaml, and initialize the encrypted repository in one flow. " +
			"Re-running it over an existing config reconfigures in place.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, deps, cfgPath, true)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.AddCommand(newSetupIAMPolicy(deps.Stdout))
	return cmd
}
```

- [ ] **Step 3: Delete the orphaned files**

```bash
git rm internal/cli/setup_wizard.go internal/cli/setup_spinner.go \
       internal/cli/setup_init.go internal/cli/setup_errors.go
```

Then delete, from `internal/cli/setup_auth.go`, the `printSetupAuthProgress` function — `runSetup` was its only caller.

Delete the test files that exist solely to drive deleted symbols, and the **Duplicate** / **Orphan** tests identified in Step 1:

```bash
git rm internal/cli/setup_helpers_test.go internal/cli/setup_init_alias_test.go \
       internal/cli/setup_transform_alias_test.go
```

Keep `internal/cli/setup_aliases_test.go`, `internal/cli/setup_iam_alias_test.go`, and `internal/cli/setup_effects_test.go` — they cover surviving shared code.

- [ ] **Step 4: Write the launcher tests**

Replace the contents of `internal/cli/setup_test.go` with (keeping any **Launcher**-class tests you rewrote in Step 1):

```go
package cli

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/tui"
)

func launcherDeps(t *testing.T, captured *tui.App) UIDeps {
	t.Helper()
	return UIDeps{
		RepoDeps: RepoDeps{
			Stdout: io.Discard,
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return blobstore.NewMemory(), nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
			},
		},
		Run: func(app tui.App) error { *captured = app; return nil },
	}
}

// TestSetup_LaunchesWizardOnFirstRun: the launcher's whole job is landing the
// TUI on the wizard. No config present is the plain case.
func TestSetup_LaunchesWizardOnFirstRun(t *testing.T) {
	chDir(t, t.TempDir())
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := captured.Deps().InitialView; got != "setup" {
		t.Errorf("InitialView = %q, want setup", got)
	}
}

// TestSetup_RejectsForceFlag: --force was removed when the wizard's review
// stage became the overwrite gate. Pinned so it cannot quietly return; a
// silently-accepted --force would read as "confirmed" to a scripted caller
// while the TUI still waits at the review prompt.
func TestSetup_RejectsForceFlag(t *testing.T) {
	chDir(t, t.TempDir())
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--force"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("setup --force must fail: the flag no longer exists")
	}
	if !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("want an unknown-flag error, got: %v", err)
	}
}

// TestSetup_IAMPolicySubcommandStillRegistered: `setup iam-policy` prints a
// least-privilege policy for an arbitrary bucket and is deliberately
// CLI-only. It must survive the launcher rewrite.
func TestSetup_IAMPolicySubcommandStillRegistered(t *testing.T) {
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	for _, sub := range cmd.Commands() {
		if sub.Name() == "iam-policy" {
			return
		}
	}
	t.Fatal("setup iam-policy must stay registered under the launcher")
}

// TestSetup_CustomConfigPath: --config must reach runUI, so the wizard writes
// back to the file the operator named.
func TestSetup_CustomConfigPath(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	var captured tui.App
	cmd := NewSetup(launcherDeps(t, &captured))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--config", "custom.yaml"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := captured.Deps().ConfigPath; !strings.HasSuffix(got, "custom.yaml") {
		t.Errorf("ConfigPath = %q, want it to end in custom.yaml", got)
	}
}
```

- [ ] **Step 5: Rewire `cmd/sentra`**

In `cmd/sentra/commands.go`, delete the `cli.NewSetup(cli.SetupDeps{...})` block at lines 25-30 and the now-unused local at line 17 (`setupPassphrase := promptSetupPassphrase(rootFlags)` — an unused local is a Go **compile error**, not a lint warning).

Add the launcher next to the other UI-dependent commands, after `uiDeps` is constructed (line 166):

```go
	root.AddCommand(cli.NewUI(uiDeps))
	cli.SetUIAsDefault(root, uiDeps)
	// `sentra setup` is a launcher for the same TUI wizard, so it takes the
	// very same deps — it differs only in forcing the wizard route.
	root.AddCommand(cli.NewSetup(uiDeps))
```

Then delete `promptSetupPassphrase` from `cmd/sentra/passphrase.go:68` — `setupPassphrase` was its only caller, and `golangci-lint`'s `unused` check will fail otherwise.

- [ ] **Step 6: Build and run the full suite**

Run: `go build ./...`
Expected: success. If anything still references a deleted symbol, the compiler names it.

Run: `go test -race ./internal/cli/ ./internal/tui/ ./internal/setup/`
Expected: PASS.

Run: `just lint`
Expected: clean. Any `unused` finding here is a symbol whose deletion was missed.

- [ ] **Step 7: Commit**

```bash
git add -A internal/cli cmd/sentra
git commit -m "refactor(cli): make sentra setup a launcher, delete the huh wizard

internal/cli/setup_wizard.go duplicated internal/tui/setup_wizard.go: both
drove the same setup.Engine through the same stages. sentra setup now forces
the TUI wizard via runUI, so there is one wizard.

Deletes the wizard, its spinner and error printers, runSetupInit, and the
18-field SetupDeps struct — all reachable only from runSetup. The engine,
transforms, and error classification are untouched in internal/setup and keep
their 66 tests; the CLI's setup tests were duplicate coverage of them
expressed through huh. --force is gone: the wizard's review stage is the
overwrite gate.

Shared code stays: setup_effects.go, setup_awss3.go, setup_auth.go,
setup_aliases.go, and setup_iam_policy.go all serve the TUI launch path."
```

---

### Task 5: Relocate the survivors, delete `setup_summary.go`

Pure refactor: `setup_summary.go`'s remaining three functions have nothing to do with a setup summary, and `format.go` is already the home for CLI output helpers.

**Files:**
- Modify: `internal/cli/format.go` (receive three functions)
- Delete: `internal/cli/setup_summary.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `printSetupStep`, `printSetupOK`, `validateSetupBucketName` — same signatures, new home. Callers (`doctor.go`, `setup_auth.go`, `setup_iam_policy.go`, `internal/tui/setup_wizard.go`) are unaffected: same package for the CLI ones, and the TUI reaches bucket validation through `internal/setup`.

- [ ] **Step 1: Confirm exactly which functions survive**

Run:

```bash
cd /Users/markgustetic/Programming/portfolio/sentra
for sym in printSetupStep printSetupOK validateSetupBucketName \
           printSetupSummary printSetupApplyHeader printSetupRepairContinue \
           setupAWSPreparedLabel setupBackendLabel setupAWSAuthMethodLabel; do
  printf "%-28s %s\n" "$sym" "$(grep -rln "$sym" internal/cli/*.go internal/tui/*.go | grep -v setup_summary.go | tr '\n' ' ')"
done
```

Expected after Task 4: `printSetupStep`, `printSetupOK`, and `validateSetupBucketName` have callers; the other **six** have none. `setupBackendLabel` and `setupAWSAuthMethodLabel` are thin aliases to `setup.BackendLabel` / `setup.AWSAuthMethodLabel` whose only callers were the dying wizard printers — the `internal/setup` originals stay. If any of the six still has a caller, Task 4 missed a deletion; fix that first.

- [ ] **Step 2: Move the three survivors into `format.go`**

Cut these three from `internal/cli/setup_summary.go` and append to `internal/cli/format.go` exactly as written here (the bodies are verbatim from `setup_summary.go:56`, `:60`, and `:76`):

```go
// printSetupStep and printSetupOK are the CLI's step/success line printers.
// They outlived `sentra setup`'s huh wizard — doctor and the AWS auth helpers
// still use them for progress output — so they live here with the other output
// helpers rather than in a setup-specific file.
func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

// validateSetupBucketName is shared by doctor, `setup iam-policy`, and the TUI
// wizard's details stage, so it is not setup-command-specific.
func validateSetupBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
```

`format.go` needs `fmt`, `io`, `github.com/markgustetic/sentra/internal/ui`, and `github.com/markgustetic/sentra/internal/diag` in scope — add whichever it does not already import, and drop any import that `setup_summary.go`'s removal left unused elsewhere. `gofmt` and `goimports` will not add these for you; `go build` names what is missing.

- [ ] **Step 3: Delete the file**

```bash
git rm internal/cli/setup_summary.go
```

- [ ] **Step 4: Verify**

Run: `go build ./... && go test -race ./internal/cli/`
Expected: PASS. This is a move with no behavior change, so no new test is needed — the existing `doctor` and `iam-policy` tests are the regression guard.

Run: `just fmt && just vet && just lint`
Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/cli/format.go internal/cli/setup_summary.go
git commit -m "refactor(cli): move the setup-summary survivors into format.go

After the wizard deletion, setup_summary.go held only printSetupStep,
printSetupOK, and validateSetupBucketName — used by doctor, the AWS auth
helpers, setup iam-policy, and the TUI wizard. None is a setup summary, and
format.go is already where CLI output helpers live. Pure move."
```

---

### Task 6: Rewrite the surface contract in the docs

Last, so the contract describes a codebase that already satisfies it.

**Files:**
- Modify: `AGENTS.md:144-154`
- Modify: `CLAUDE.md` (the "What this is" paragraph)

**Interfaces:** none — documentation only.

- [ ] **Step 1: Replace the parity bullet in `AGENTS.md`**

Delete the bullet at `AGENTS.md:144-154` (starts `- TUI parity: every operational CLI capability...`) and put this in its place, verbatim:

```markdown
- Surface contract — the obligation between the two surfaces runs ONE WAY.
  The CLI is the machine and recovery surface: every capability lands in the
  core layer plus a CLI verb, always. Three consumers depend on that and none
  of them can press a key — `internal/scheduler` emits systemd/cron units whose
  `ExecStart` invokes this binary, the recovery kit prints commands to type when
  the machine is gone, and the test suite drives the CLI. A mutating capability
  with no CLI verb is a defect, not a style choice.
  The TUI is the human surface and the default one, and it owes the CLI no
  flag-for-flag coverage. It owes a floor instead: setup/reconfigure, unlock,
  backup, run a named policy, restore, browse snapshots, check, prune, and
  recovery kit must each be completable start to finish without leaving the
  TUI. A gap inside the floor is a bug; anything outside it is CLI-at-will,
  needing no TUI affordance and no entry in any list. Per-run knobs
  (`prune --keep-*`, `--concurrency`, `--stale-lock-after`, agent
  `--root`/`--categories`/`--local-only`/`--max-tool-calls`) come from config
  in the TUI by design.
```

- [ ] **Step 2: Fix the now-false claim in `CLAUDE.md`**

`CLAUDE.md`'s "What this is" section says "Every CLI capability is also operable from the TUI (20 views)." That is false under the new contract. Replace that sentence with:

```markdown
The TUI covers every job a human does; the CLI is the machine and recovery
surface (see the surface contract in AGENTS.md).
```

- [ ] **Step 3: Check for other stale parity claims**

Run:

```bash
grep -rn "parity\|every CLI capability\|also operable" AGENTS.md CLAUDE.md README.md docs/*.md 2>/dev/null
```

Fix any other statement of the two-way obligation. The hooks rule at `AGENTS.md:138-143` ("MUST run identically from `sentra policy run` and the TUI's policy run") is a **correctness** invariant, not parity bookkeeping — leave it exactly as is.

- [ ] **Step 4: Verify `sentra setup --help` matches the docs**

Run: `just build && ./bin/sentra setup --help`
Expected: the long description mentions reconfiguring in place, and **no `--force` flag** is listed. If `--force` appears, Task 4 Step 2 was not applied.

- [ ] **Step 5: Commit**

```bash
git add AGENTS.md CLAUDE.md
git commit -m "docs: replace two-way TUI parity with the one-way surface contract

The old rule taxed every feature twice and grew an exception list that never
shrank. It also mis-described the CLI, which is the only surface cron, the
recovery kit, and the tests can reach. The replacement makes a CLI verb
mandatory and names a TUI capability floor instead of demanding
flag-for-flag coverage.

Also fixes CLAUDE.md's claim that every CLI capability is operable from the
TUI, which the new contract deliberately stops promising."
```

---

## Final verification

- [ ] **Full gate**

```bash
just check
```

Expected: build, `test -race`, vet, lint, vuln, `go mod tidy -diff`, `gofmt`, and `git diff --check` all clean.

- [ ] **Per-commit isolation build**

A gate run tests the working tree, not the commits. This series deletes across a dozen files, so verify each commit compiles on its own:

```bash
for ref in $(git rev-list --reverse origin/main..HEAD); do
  git worktree add -q --detach /tmp/chk "$ref" || exit 1
  (cd /tmp/chk && go build ./...) || { echo "FAILED at $ref"; git worktree remove --force /tmp/chk; exit 1; }
  git worktree remove --force /tmp/chk
  echo "ok $ref"
done
```

- [ ] **Manual smoke test (needs a real TTY — the agent cannot do this)**

Automated tests are headless; key routing and rail focus only show up in a real terminal.

```bash
just local-reset && just local
```

Verify: lands on the first-run wizard; complete it against MinIO. Then:

```bash
./bin/sentra setup --config .sentra-local.yaml
```

Verify: the wizard opens **pre-filled** with the MinIO coordinates rather than the dashboard, and the review stage shows the `completing setup overwrites …` line. Press `esc` to abandon and confirm `.sentra-local.yaml` is unchanged.

---

## Notes for the implementer

**Do not "fix" the resolved-config seeding.** The wizard seeds from `config.Load`, which returns file + `SENTRA_*` overlay, so a transient env override can be persisted when the wizard writes. This predates the change, `CLAUDE.md` explicitly blesses `config.Write` for `init` and `setup`, and `config` exports no file-only load. Out of scope by decision, not by oversight.

**Do not add TUI affordances for CLI-only surfaces.** The whole point of Task 6 is that this is no longer owed. If you notice `backup plan`/`apply` or `--json` has no TUI equivalent, that is now correct by contract.

**`huh` stays in the module.** `internal/cli/confirm.go` uses it for the `backup apply` / `prune --apply` / `agent apply` human gates, which are CLI-native and not TUI duplicates. Do not remove the dependency; `go mod tidy -diff` will fail if you do.
