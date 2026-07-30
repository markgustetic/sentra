# Surface contract: one-way parity, TUI-only wizard

Date: 2026-07-30

## Problem

`AGENTS.md` currently states a two-way parity obligation: "every operational CLI
capability has a TUI affordance," followed by an eleven-line list of grandfathered
CLI-only escapes and the instruction *"Do not let new CLI capabilities ship without
either a TUI affordance or an entry in this list."*

That contract taxes every feature twice and grows a list that never shrinks. It also
mis-describes what the two surfaces are for. The CLI is not a lesser mirror of the
TUI — it is the only surface that unattended and disaster-recovery use can reach:

- `internal/scheduler/render.go:182` emits systemd/cron units whose `ExecStart`
  invokes this binary. Cron cannot press a key.
- `internal/recoverykit/kit.go:79` prints `sentra check --config <path>` into the
  recovery kit — instructions for a machine that no longer exists.
- The test suite drives the CLI. TUI coverage is structurally weaker; a real-TTY
  smoke test is still outstanding.

Meanwhile the setup wizard genuinely *is* duplicated. `internal/cli/setup_wizard.go`
(491 lines of `huh` forms) and `internal/tui/setup_wizard.go` (1,335 lines) both drive
the same `setup.Engine` pipeline. That is the one place where two-way parity produced
real duplication rather than useful coverage.

## Decisions

1. **`sentra setup` becomes a thin launcher for the TUI wizard.** `internal/cli`
   already imports `internal/tui`, so this is architecturally free.
2. **The exception list is replaced by a TUI capability floor.** CLI-at-will becomes
   the default; the floor names the jobs a human must be able to finish in the TUI.
3. **Scope is the wizard, its now-dead support code, and the contract.** CLI flag
   trimming is explicitly out.
4. **Reconfiguring is a supported flow, not a `--force` escape hatch.** The wizard
   opens pre-filled from the existing config and confirms at its review stage.

## The new contract

Replaces `AGENTS.md:144-154` in full:

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

Notes on the wording:

- The useful content of the old list survives as one clause (per-run knobs come from
  config). What dies is the bookkeeping obligation.
- "Run a named policy" is in the floor because it is already true (the policies view
  landed in `cb73516`) and because `AGENTS.md:141-143` already requires hooks to fire
  identically from the TUI's policy run. The floor makes that rule's premise explicit.
- A gap *inside* the floor is a bug. Outside it, silence is the correct amount of
  documentation.

### Knock-on documentation edit

`CLAUDE.md` claims "Every CLI capability is also operable from the TUI (20 views)."
That becomes false. Replace with wording that points at the contract, e.g. "The TUI
covers every job a human does; the CLI is the machine and recovery surface (see the
surface contract in AGENTS.md)."

## Code design

### `sentra setup` as launcher

`NewSetup` changes its dependency type from `SetupDeps` to `UIDeps` and delegates:

```go
func NewSetup(deps UIDeps) *cobra.Command {
    var cfgPath string
    cmd := &cobra.Command{
        Use:   "setup",
        Short: "Run the guided Sentra setup wizard",
        RunE: func(cmd *cobra.Command, _ []string) error {
            return runUI(cmd, deps, cfgPath, true /* forceSetup */)
        },
        Args: cobra.NoArgs, SilenceUsage: true,
    }
    cmd.Flags().StringVar(&cfgPath, "config", configFileName,
        "path to sentra.yaml (defaults to ./sentra.yaml)")
    cmd.AddCommand(newSetupIAMPolicy(deps.Stdout))
    return cmd
}
```

`UIDeps` embeds `RepoDeps`, which carries `Stdout`, so `setup iam-policy` survives the
deps swap unchanged.

`--force` is removed. Under decision 4 the review stage is the confirmation gate, so a
flag whose only job was to unlock a flow that gate already protects has none left. This
is the change's one user-visible removal.

### Routing

`runUI` gains a trailing `forceSetup bool` parameter —
`runUI(cmd *cobra.Command, deps UIDeps, cfgPath string, forceSetup bool) error` — and
`NewUI` passes `false`. The launch branch at `internal/cli/ui.go:155` becomes:

```go
if forceSetup || !st.ConfigExists || !st.PassphraseAvailable {
    initial := "setup"
    if st.ConfigExists && !forceSetup {
        initial = "unlock"
    }
```

`tui.Deps` gains `Reconfigure`, set to `forceSetup && st.ConfigExists`.

The forced path keeps the existing "launch without opening the repo" behavior. That is
correct here: reconfiguring must not require the passphrase.

**Pre-fill is free; the seed guard is not.** `launchCfg := st.Config` already holds the
on-disk config and the wizard reads its start state from `deps.Config`, so
reconfiguring pre-fills with no new code. But the `SetupSeedConfig` override currently
fires on `initial == "setup" && deps.SetupSeedConfig != nil`, and `initial == "setup"`
can now be true with a config present. The condition must gain `&& !st.ConfigExists`
so a real on-disk config always beats a caller-supplied seed. Without it, a future
force-setup path could silently prefer `sentra local`'s MinIO coordinates over the
operator's actual config. The comment at `ui.go:68-77` needs updating to match.

### TUI addition

One change in `internal/tui/setup_wizard.go`: the review stage renders an
"overwrites `<path>`" line when `Reconfigure` is set.

**No error-advice work is needed.** An earlier draft of this spec claimed the TUI never
surfaced `setup.ErrorAdvice` and that wiring it in was a required regression guard. That
was wrong. `internal/tui/setup_wizard.go:1158` already renders it in the `stageError`
view and `:1004` builds it into the error modal, covered by
`TestSetupWizard_DoneMsgErrorRendersAdvice`. Deleting the CLI wizard's
`printSetupErrorDetail` loses nothing.

## Deletion inventory

### Delete wholesale — 675 lines

| File | Lines | Why |
|---|---|---|
| `internal/cli/setup_wizard.go` | 491 | the four `huh` prompts |
| `internal/cli/setup_spinner.go` | 97 | `startSetupProgress`, wizard-only caller |
| `internal/cli/setup_init.go` | 42 | `runSetupInit`, only `runSetup` called it |
| `internal/cli/setup_errors.go` | 45 | CLI presentation/test wrappers; logic is in `internal/setup` |

### Trim — about 305 lines

- `internal/cli/setup.go`, 297 → ~45. `runSetup` and its full helper chain go
  (`loadSetupConfigForWizard`, `confirmSetupReviewIfNeeded`,
  `promptSetupAWSRepairIfNeeded`, `continueSetupAfterAWSRepair`, the draft wrappers,
  and the `normalizeSetupConfig` / `applySetup*` / `resolveSetup*` aliases), along with
  the entire 18-field `SetupDeps` struct.
- `internal/cli/setup_summary.go`, 78 → 0, but only ~53 lines are a net removal: three
  survivors relocate into `internal/cli/format.go` rather than dying. They are
  `printSetupStep`, `printSetupOK` (used by `doctor.go` and `setup_auth.go`), and
  `validateSetupBucketName` (used by `doctor.go`, `setup_iam_policy.go`, and
  **`internal/tui/setup_wizard.go`**). What dies is `printSetupSummary`,
  `printSetupApplyHeader`, `printSetupRepairContinue`, and `setupAWSPreparedLabel`.
  "Summary" no longer describes what remains, and `format.go` is already the home for
  CLI output helpers.

### Stays untouched

`setup_effects.go`, `setup_awss3.go`, `setup_auth.go`, `setup_aliases.go`,
`setup_iam_policy.go` — all reachable from the TUI launch path via
`internal/cli/ui.go:245`. One wrinkle: `setup_auth.go` defines
`printSetupAuthProgress`, whose only caller was `runSetup`; that function dies, the
file stays.

### Call-site update

`cmd/sentra/commands.go:25` — `cli.NewSetup(cli.SetupDeps{...})` collapses to pass the
same `UIDeps` value `NewUI` already receives.

Net: roughly 980 non-test lines, plus the wizard-driving portions of `setup_test.go`
(1,863 lines / 45 test functions), `setup_helpers_test.go`, `setup_init_alias_test.go`,
and `setup_transform_alias_test.go`.

## Testing

### Coverage audit precedes deletion

**No CLI setup test is deleted until a below-CLI equivalent is confirmed to exist.**
`internal/setup` already carries 66 test functions, and the CLI's 45 setup tests are
largely duplicate coverage expressed through the `huh` wizard — leftovers from the
extraction that created `internal/setup`. Spot-checked and confirmed covered:

| Behavior | Below-CLI coverage |
|---|---|
| S3-compatible never inherits an AWS profile | `TestDefaultPlanS3CompatibleDoesNotInheritDiscoveredProfile`, `…IgnoresAWSProfileEnv`, `…KeepsExplicitProfile` |
| `endpoint_url` incompatible with AWS prep | `setup.ValidatePlan` (`transform.go:213`), `TestValidatePlan` |
| Engine stages | `TestEnginePrepareAWS*`, `TestEngineInitRepo*`, `TestEngineWriteConfig`, `TestEngineWriteAndRemoveDraft` |
| Error classification and advice | `TestErrorAdvice`, `TestWrapAWSPrepareErrorClassifiesByMethod`, `TestWrapAWSLoginAndSSOFlowErrors` |
| Review and summary text | `TestReviewText*`, `TestSummaryLines*` |
| AWS CLI install, SSO probes | `TestDefaultEnsureAWSCLI_*`, `TestDefaultAWSSSOConfigured_*` |

**One genuine coverage gap, and it must be closed before the CLI test dies.**
`TestSetup_PreparesAWSBeforeWritingConfig` asserts the pipeline *ordering* (WriteDraft →
PrepareAWS → WriteConfig → InitRepo). That ordering is a real invariant: a config
written before a failed bucket prep records a bucket that does not exist.

The replacement belongs in `internal/tui`, not `internal/setup`. `setup.Engine` has no
pipeline method — it exposes discrete steps and each caller sequences them
(`internal/tui/setup_wizard.go:597-655` is the surviving sequencer). So there is nothing
at the engine level to assert an order against.

The existing `TestSetupWizard_ProvisionOpRunsEngineEndToEnd` covers only the success
path. No test asserts that a *failed* `PrepareAWS` leaves no config file on disk, which
is the half of the invariant that matters. That test is Task 1 of the plan.

### New tests

1. **Routing matrix** (`internal/cli`, stub `Run` captures the constructed `tui.App`) —
   table over all eight combinations of `ConfigExists × PassphraseAvailable ×
   forceSetup`, asserting `InitialView` and `Reconfigure`. The full matrix, not just
   the new case; include the regression row where config + locked + `forceSetup=false`
   still routes to `unlock`.
2. **Seed guard** — `ConfigExists && forceSetup && SetupSeedConfig != nil` launches
   with the on-disk config, not the seed.
3. **Review warning** (tui view) — `Reconfigure` true renders the config path, false
   does not. Text assertion, safe under lipgloss's Ascii profile.
4. **Prepare-failure ordering** (tui view) — drive the wizard to a provisioning run
   whose `PrepareAWS` fails, then assert no file exists at `ConfigPath`. This replaces
   `TestSetup_PreparesAWSBeforeWritingConfig` and must be written and passing *before*
   that CLI test is deleted.
5. **`sentra setup --force`** returns an unknown-flag error, so the flag cannot quietly
   return.
6. **`setup iam-policy`** is still registered under the launcher.

Each test is written failing-first. Test 4 is the one that must be proved capable of
failing: temporarily reordering the wizard's op so `WriteConfig` precedes `PrepareAWS`
must break it.

### Gate

`just check` (build, `test -race`, vet, lint, vuln, tidy, fmt), plus per-commit
isolation builds using the worktree recipe in `CLAUDE.md`. This series deletes across a
dozen files, and a green check on a dirty tree proves nothing about the individual
commits.

## Out of scope

- **CLI flag trimming.** `prune`'s 15 flags, `agent`'s 10, `backup`'s 9. Each removal
  is an independent judgment about who depends on it, and mixing behavior changes into
  a deletion refactor obscures both.
- **Rebuilding any deleted CLI capability elsewhere.** Nothing here is a capability
  loss: the engine, transforms, and error classification all live in `internal/setup`
  and keep their tests.

## Known pre-existing issue, deliberately not fixed

Seeding the wizard from `config.Load` yields the *resolved* config (file plus
`SENTRA_*` overlay), so a transient env override can be baked in when the wizard
writes. This hazard exists in `sentra setup` today — `internal/cli/setup.go:216`
already loads resolved — and `CLAUDE.md` explicitly blesses `config.Write` for `init`
and `setup`, which must record the bucket they just provisioned against. `config`
exports no file-only load, so fixing it means new API. Out of scope; recorded so the
next reader knows it was seen and judged, not missed.
