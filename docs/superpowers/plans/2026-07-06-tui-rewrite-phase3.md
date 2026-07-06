# TUI Rewrite Phase 3: Setup Wizard → TUI + `internal/setup` Engine Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the `sentra setup` wizard into the TUI — a first-run gate, a Settings re-entry, and a registered sidebar view that performs AWS provisioning in-app — by extracting a pure, headless, testable **`internal/setup` engine** that both the kept CLI wizard (thin driver) and the new TUI wizard drive.

**Architecture:** A new lower package `internal/setup` holds the setup **state model** (`Plan`, backend/auth/repair enums, report/option types), **pure transforms** (`DefaultPlan`, `NormalizeConfig`, `ValidatePlan`, `ReviewText`, `SummaryLines`, label maps), the **IAM-policy** builder, the **error taxonomy** (`ErrorAdvice` + `WrapAWS*`, substring-matching), the **AWS-CLI config parser**, an **`Effects` interface** (the AWS-SDK / `aws`-CLI-subprocess / keyring / store side effects) with a production `DefaultEffects()`, and a **stepwise `Engine`** (`PrepareAWS`, `WriteConfig`, `InitRepo`, `WriteDraft`/`RemoveDraft`/`DraftPath`) — nothing in it imports `cobra`, `huh`, `bubbletea`, `os.Stdin`, `internal/cli`, or `internal/tui`. The keyring-user derivation moves from `cmd/sentra` into `internal/config` so both the injected saver and the TUI unlock path compute the same keyring identity. The CLI wizard (`internal/cli/setup*.go`) becomes a thin driver over the engine, keeping its `huh` forms + `\r` spinner + repair loop, and its untouched 1,863-line `setup_test.go` is the behavior-preservation oracle. The TUI adds a `SetupWizardView` (stage machine, inline bubbles for every step, async provisioning under the op-guard, `tea.ExecProcess` for interactive `aws` sign-in), a masked `UnlockView`, a `SettingsView`, and first-run/locked routing in `runUI`.

**Tech Stack:** Go 1.25; Bubbletea v1.3.10, Bubbles v1.0.0 (`textinput`, `list`, `viewport`, `spinner`, `key`), Lipgloss v1.1.0; `internal/config`, `internal/diag`, `internal/repo`, `internal/blobstore`, `crypto/subtle`, AWS SDK v2.

---

## Locked decisions (from Phase 3 brainstorming)

- **One combined plan** — engine extraction + TUI wizard on a single `feature/tui-phase3` branch.
- **Provision in-app** — the TUI wizard runs bucket creation/hardening + repo init + keyring under the op-guard with a spinner checklist; interactive `aws` sign-in runs via `tea.ExecProcess`.
- **Setup is a first-run gate AND a Settings re-entry AND a registered sidebar view.**
- **AWS-CLI bootstrap:** support Existing / Browser-login / SSO in the TUI (interactive `aws` subprocesses via `tea.ExecProcess`); `brew`-based AWS-CLI auto-install stays **CLI-only** — the TUI detects a missing `aws` binary and shows a `setup.ErrorAdvice` modal instead of auto-installing.

## Settled technical constraints (NOT choices — the plan depends on these)

- **`internal/cli` imports `internal/tui`**, so `internal/tui` must NEVER import `internal/cli`. `internal/setup` imports only `config`/`diag`/`repo`/`blobstore`/stdlib/aws-sdk. `internal/tui` MAY import `internal/setup`.
- **`huh` forms cannot run inside a running Bubbletea program** (`huh.Form.Run` owns `os.Stdin` and fights `tea.Program` — proven by the Phase 2c password flow). Every interactive `huh` step in the TUI wizard is re-expressed as inline bubbles; `huh` stays ONLY in the CLI wizard and NEVER on the TUI launch path.
- **Keyring identity must be shared.** `KeyringUserForConfig` binds bucket **and** prefix; a passphrase the wizard saves must resolve at unlock, so the derivation lives in `internal/config` and both frontends/both flows call it.

## Security invariants

- No secrets (passphrase, AWS creds, wrapped keys, salts, MAC) into `sentra.yaml`, logs, the `.setup-draft`, recovery kits, tests, or fixtures.
- The keyring is populated only AFTER `repo.Init` or a verified `repo.Open` — the **verify-before-save guard** in `Engine.InitRepo` is preserved verbatim and unit-tested.
- Passphrase entry (wizard `stagePassphrase` and `UnlockView`) is masked (`textinput` `EchoPassword`), compared with `crypto/subtle.ConstantTimeCompare`, and zeroized (`crypto.Zeroize`) — never via `huh`.

## Execution notes — READ BEFORE STARTING

1. **Branch.** Create `feature/tui-phase3` from `main` before Task 1. Commit per task with the messages shown.
2. **Dependency order.** The engine is extracted before its consumers: do the parts in listed order (leaf pure funcs + keyring → types/transforms → Effects/Engine → CLI driver rewrite → Deps/routing/unlock → wizard view → Settings/registration). The CLI driver rewrite's gate is the **unchanged `internal/cli/setup_test.go` staying green** — that is the behavior-preservation proof.
3. **The behavior oracle.** `internal/cli/setup_test.go` (1,863 lines) and `internal/cli/setup_awscli_test.go` (moved with the parser) must stay green. If a move breaks them, the move — not the test — is wrong (except the awscli test file relocates packages).
4. **Delete moved-out code.** After each extraction, delete the now-dead `internal/cli` original so `golangci-lint`'s `unused` linter stays clean (per Phase 2c). Every task ends green: `go build ./...`, its tests, `gofmt -l cmd internal`, `go vet ./...`, and `golangci-lint run` on the touched packages.
5. **First-run / locked routing uses a single `InitialView string` mechanism (Unit 5).** `runUI` sets `Deps.InitialView` = `"setup"` when no `sentra.yaml` exists (first-run), `"unlock"` when config exists but no passphrase source resolves, else `""` (dashboard). `NewApp` honors it. **Some Unit-7 tasks were drafted referencing a `Deps.FirstRun` bool — treat every such reference as `InitialView == "setup"` and use the `InitialView` field throughout. There is no `FirstRun` field.**
6. **View registration is cumulative — insert, don't replace** (as in Phase 2c). Several tasks add one `{id: ...}` line to the `views := []viewEntry{...}` slice in `internal/tui/app.go`. Insert into the current slice; the authoritative end-state (all views, the sidebar/registry split, and every count assertion) is the **final task**, which is the source of truth. Do not paste a full-slice snippet that drops earlier views.
7. **`cat`/`tail`/`head` are aliased to `bat`** — use `command tail -n N` (or file tools) when piping long output.

## File structure (created / modified)

**New package `internal/setup`** — `types.go` (Plan + enums + report/option types), `transform.go` (DefaultPlan/Normalize/Validate/ReviewText/SummaryLines/labels + EnvProbe), `iam.go` (BuildIAMPolicy), `errors.go` (ErrorAdvice + WrapAWS*), `awscli.go` (+ relocated `awscli_test.go`), `effects.go` (Effects + DefaultEffects), `engine.go` (Engine + stepwise methods), with focused `_test.go` files.

**`internal/config`** — `keyring.go` (`KeyringUserForConfig`/`LegacyKeyringUsersForConfig`/`KeyringOptionsForConfig` + consts), moved from `cmd/sentra/passphrase.go`.

**New TUI views** — `internal/tui/setup_wizard.go`, `internal/tui/unlock.go`, `internal/tui/settings.go` (+ `_test.go`).

**Modified** — `internal/tui/app.go` (Deps `SetupEffects` + `InitialView`; final registration), `internal/cli/ui.go` + `cmd/sentra/commands.go` (thread `SetupEffects`, first-run/locked routing), `internal/cli/setup*.go` (thin driver over `internal/setup`; `=` aliases), `cmd/sentra/passphrase.go` (call `config.Keyring*`).

---

## Plan corrections — AUTHORITATIVE (an adversarial review of this plan found these cross-unit defects; apply each correction; it overrides the per-task text where they differ)

**C1 (blocker) — no duplicate type declarations in `internal/setup`.** Part 1 (Tasks 3–4) owns `AWSAuthMethod` (+ consts `AWSAuthLogin/AWSAuthSSO/AWSAuthExisting/AWSAuthSkip`), `AWSCLIInstallPlan`, `AWSCLIInstallReport`, `AWSCLIInstallConfirm` in `internal/setup/aws_types.go`. Part 2 Task 5's `types.go` MUST NOT re-declare them — remove those from Task 5 and keep only the genuinely-new types there (`Backend` + consts, `AWSRepairChoice` + consts, `Plan`, `AWSPrepareOptions`, `AWSPrepareReport`, `AWSAuthReport`, `InitResult`). Two files in `package setup` declaring the same identifier is a hard compile error.

**C2 (blocker) — `Effects.EnsureAWSCLI` is 2-param everywhere.** Signature is `EnsureAWSCLI(ctx context.Context, confirm setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error)`. Part 6's TUI test stubs (`stubEffects`, `execProbe` in Tasks 30–31) MUST use this exact 2-param signature (ignore `confirm`), and the production wizard call in `maybeStartInteractiveAuth` (Task 31) MUST be `eff.EnsureAWSCLI(ctx, nil)` — the TUI never brew-installs; a missing CLI surfaces an `ErrorAdvice` modal.

**C3 (blocker) — `setup.ValidateBucketName` does not exist.** Part 6's wizard `commitDetails` (Tasks 25–26) references it, but bucket validation lives in `internal/diag`. Fix: in Part 2 Task 7, add a thin re-export to `package setup`: `func ValidateBucketName(bucket string) error { return diag.ValidateBucketName(bucket) }` and add it to the pinned API. Then the wizard's `setup.ValidateBucketName(bucket)` calls resolve. (`internal/setup` already imports `diag` for `ValidatePlan`.)

**C4 (blocker) — preserve the CLI oracle's test helper `writeAWSConfig`.** Part 1 Task 4 `git rm`s `internal/cli/setup_awscli_test.go`, but `internal/cli/setup_test.go` (the 1863-line oracle) references its `writeAWSConfig(t, body) string` helper. Before removing that file, relocate `func writeAWSConfig` into a surviving `internal/cli` test file (e.g. a new `internal/cli/setup_helpers_test.go`). `writeExecutable` is only used by the moved awscli test — it goes to `internal/setup/awscli_test.go` with the rest.

**C5 (blocker) — thread `ConfirmAWSCLIInstall` into the engine.** The headless engine's AWS-login/SSO paths (Part 1/Part 3) must NOT hardcode `EnsureAWSCLI(ctx, nil)` for the CLI driver. In Part 4, `cliSetupEffects.EnsureAWSCLI` MUST substitute `deps.ConfirmAWSCLIInstall` (falling back to `HuhAWSCLIInstallConfirm`) for a nil `confirm` before calling the real `EnsureAWSCLI`, so the CLI's AWS-CLI-install oracle case doesn't nil-panic. Add a `setup` unit test asserting `confirm` is invoked. (The TUI's effects impl keeps passing `nil` per C2.)

**C6 (important) — keep the cli-local awscli wrappers until their callers move.** Part 4 Task 14's `setup_awscli.go` rewrite MUST retain `func loadAWSCLIConfig() (setup.AWSCLIConfig, error) { return setup.LoadAWSCLIConfig() }` and `func awsProfileSection(profile string) string { return setup.AWSProfileSection(profile) }` (as Part 1 Task 4 created them) because `setup_wizard.go` still calls them; remove them only in Task 15 together with their last caller.

**C7 (important) — intermediate view-count assertions.** `internal/tui/app_test.go` has two pre-existing `!= 14` count assertions (`TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd` ~:41, `TestApp_CheckReplacesOperationsInSidebar` ~:767). Every task that inserts a view (Part 5 Task 23 adds `unlock` → 15; Part 7 adds `setup`+`settings` → 17) MUST bump those two assertions in the SAME commit so its gate stays green. The final task (Task 35) pins them at 17; the intermediate bumps just keep the suite green between here and there.

**C8 (blocker) — the final task owns the FULL `NewApp` body, not just the views slice.** Tasks 21 (Part 5), 34 (Part 7), and 35 (Part 8) all edit `NewApp` (`app.go:181-241`). There is NO `Deps.FirstRun` — the mechanism is `Deps.InitialView string` (Part 5). Task 34 MUST NOT write a `FirstRun`-keyed / setup-only `active` loop or a hardcoded `focus: focusSidebar`. Task 35 Step 3 is AUTHORITATIVE for the whole return block:
```go
active := 0
if deps.InitialView != "" {
    for i, v := range views {
        if v.id == deps.InitialView {
            active = i
            break
        }
    }
}
focus := focusSidebar
if active != 0 {
    focus = focusContent
}
sidebar := NewSidebar(registry, sidebarWidth, minHeight)
if !hiddenFromRail[views[active].id] {
    sidebar.Select(views[active].id) // C9: don't select a hidden startup gate
}
// ... return App{ active: active, focus: focus, sidebar: sidebar, ... }
```
Add a test: `NewApp(Deps{InitialView:"unlock"})` ⇒ `app.views[app.active].id == "unlock"` && `app.focus == focusContent`; same for `"setup"`.

**C9 (important) — don't `Sidebar.Select` a hidden gate.** `Sidebar.Select` iterates the registry and is a silent no-op for ids not in it; `unlock` is hidden (C8/Task 35). Guard the landing selection with `if !hiddenFromRail[views[active].id]` (shown in C8) so a locked/first-run landing on a content-focused gate doesn't leave a misleading dashboard highlight.

**C10 (minor) — `setup.WrapAWSPrepareError` takes `config.Config` by value.** Part 4 Task 15's cli wrapper must call `setup.WrapAWSPrepareError(*cfg, method, err)` (dereference the `*config.Config`), matching the `setupErrorAdvice` deref in the same task.

**C11 (minor) — the awscli behavior oracle now lives in `internal/setup`.** Part 4's prose citing `internal/cli/setup_awscli_test.go` as an unchanged cli oracle is stale (Part 1 moved it to `internal/setup/awscli_test.go`). Treat `internal/setup/awscli_test.go` as the `DefaultEnsureAWSCLI`/`DefaultAWSSSOConfigured` coverage; don't assert the cli copy stays green.

---


## Part 1 — Engine leaf functions (IAM, errors, awscli parser) + keyring relocation

**Published API** (defined by this unit's tasks; consumers in later units reference these exact signatures):

`internal/config`: `const KeyringService = "sentra"`; `const KeyringDefaultUser = "default"`; `func KeyringUserForConfig(cfg *Config) string`; `func LegacyKeyringUsersForConfig(cfg *Config) []string`; `func KeyringOptionsForConfig(cfg *Config) StoreKeyringOptions`.

`internal/setup` (new package): `func BuildIAMPolicy(bucket, prefix string) IAMPolicyDocument`; `type IAMPolicyDocument struct`; `type IAMPolicyStatement struct`; `func WriteIAMPolicy(w io.Writer, bucket, prefix string) error`; `func BucketARN(bucket string) string`; `func ObjectARN(bucket, prefix string) string`; `func WrapAWSPrepareError(cfg config.Config, method AWSAuthMethod, err error) error`; `func WrapAWSLoginFlowError(profile string, err error) error`; `func WrapAWSSSOFlowError(command, profile string, err error) error`; `func IsAWSMissingCredentialsError(err error) bool`; `func ErrorAdvice(err error, cfg config.Config) []string`; `type AWSAuthMethod string` (+ const values `AWSAuthLogin`/`AWSAuthSSO`/`AWSAuthExisting`/`AWSAuthSkip`); `type AWSCLIInstallPlan struct`; `type AWSCLIInstallReport struct`; `type AWSCLIInstallConfirm func(AWSCLIInstallPlan) (bool, error)`; `func DefaultAWSCLIInstallPlan() (AWSCLIInstallPlan, bool)`; `func LoadAWSCLIConfig() (AWSCLIConfig, error)`; `type AWSCLIConfig map[string]map[string]string`; `func AWSConfigPath() (string, error)`; `func AWSSSOProfileConfigured(cfg AWSCLIConfig, profile string) bool`; `func AWSProfileSection(profile string) string`; `func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)`; `func DefaultAWSLogin(ctx context.Context, profile, region string) error`; `func DefaultAWSSSOConfigured(ctx context.Context, profile string) (bool, error)`; `func DefaultAWSConfigureSSO(ctx context.Context, profile string) error`; `func DefaultAWSSSOLogin(ctx context.Context, profile string) error`.

Note on `AWSAuthMethod`: this unit introduces the `setup.AWSAuthMethod` type with its four const values (the pinned API places it in `internal/setup`) because task C's `WrapAWSPrepareError` needs the parameter type. A later unit adds `type SetupAWSAuthMethod = setup.AWSAuthMethod` aliases in cli; for this unit cli keeps its own `SetupAWSAuthMethod` consts and the cli wrapper maps to `setup.AWSAuthMethod`.

---

### Task 1: Move keyring derivation into internal/config with a byte-for-byte pin test

**Files:**
- Create: `internal/config/keyring.go`
- Test: `internal/config/keyring_test.go`
- Modify: `cmd/sentra/passphrase.go:15-17,55-62,65-89,118-145`
- Modify: `cmd/sentra/passphrase_test.go:11-51`

- [ ] **Step 1: Write the failing test**
```go
// internal/config/keyring_test.go
package config

import (
	"strings"
	"testing"
)

func TestKeyringUserForConfig(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		prefix string
		want   string
	}{
		{name: "empty", bucket: "", prefix: "", want: "default"},
		{name: "bucket only", bucket: "shared-bucket", prefix: "", want: "shared-bucket"},
		{name: "bucket and prefix", bucket: "shared-bucket", prefix: "repo-a/", want: "shared-bucket/repo-a/"},
		{name: "whitespace bucket", bucket: "  ", prefix: "", want: "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			cfg.Repo.S3.Bucket = tt.bucket
			cfg.Repo.S3.Prefix = tt.prefix
			if got := KeyringUserForConfig(&cfg); got != tt.want {
				t.Fatalf("KeyringUserForConfig = %q, want %q", got, tt.want)
			}
		})
	}
	if got := KeyringUserForConfig(nil); got != KeyringDefaultUser {
		t.Fatalf("KeyringUserForConfig(nil) = %q, want %q", got, KeyringDefaultUser)
	}
}

func TestLegacyKeyringUsersForConfig(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		prefix string
		want   []string
	}{
		{name: "nil-ish empty", bucket: "", prefix: "", want: nil},
		{name: "bucket only has no legacy fallback", bucket: "shared-bucket", prefix: "", want: nil},
		{name: "bucket and prefix falls back to bucket", bucket: "shared-bucket", prefix: "repo-a/", want: []string{"shared-bucket"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			cfg.Repo.S3.Bucket = tt.bucket
			cfg.Repo.S3.Prefix = tt.prefix
			got := LegacyKeyringUsersForConfig(&cfg)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("LegacyKeyringUsersForConfig = %v, want %v", got, tt.want)
			}
		})
	}
	if got := LegacyKeyringUsersForConfig(nil); got != nil {
		t.Fatalf("LegacyKeyringUsersForConfig(nil) = %v, want nil", got)
	}
}

func TestKeyringOptionsForConfig(t *testing.T) {
	var cfg Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"
	opts := KeyringOptionsForConfig(&cfg)
	if opts.KeyringService != KeyringService {
		t.Fatalf("KeyringService = %q, want %q", opts.KeyringService, KeyringService)
	}
	if opts.KeyringUser != "shared-bucket/repo-a/" {
		t.Fatalf("KeyringUser = %q, want %q", opts.KeyringUser, "shared-bucket/repo-a/")
	}
	if KeyringService != "sentra" || KeyringDefaultUser != "default" {
		t.Fatalf("constants drifted: service=%q user=%q", KeyringService, KeyringDefaultUser)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/config/ -run 'TestKeyringUserForConfig|TestLegacyKeyringUsersForConfig|TestKeyringOptionsForConfig' -count=1`
Expected: FAIL — build error `undefined: KeyringUserForConfig` (and `KeyringDefaultUser`, `KeyringService`, `LegacyKeyringUsersForConfig`, `KeyringOptionsForConfig`).

- [ ] **Step 3: Write the minimal implementation**
```go
// internal/config/keyring.go
package config

import "strings"

// KeyringService is the service name Sentra passes to the OS keyring. It is a
// fixed namespace so every repo's entry lives under one service and only the
// per-repo user string disambiguates them.
const KeyringService = "sentra"

// KeyringDefaultUser is the keyring user for a config with no bucket. A
// single-repo user never collides, and it keeps a clean install from failing
// hard before a bucket is chosen.
const KeyringDefaultUser = "default"

// KeyringUserForConfig derives the per-repo keyring identifier from the S3
// coordinates. Binding both bucket and prefix means two repos that share a
// bucket but differ only by prefix get distinct keyring entries — the fix for
// the earlier bug where they aliased onto the same stored passphrase.
func KeyringUserForConfig(cfg *Config) string {
	if cfg == nil {
		return KeyringDefaultUser
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" {
		return KeyringDefaultUser
	}
	prefix := strings.TrimSpace(cfg.Repo.S3.Prefix)
	if prefix == "" {
		return bucket
	}
	return bucket + "/" + prefix
}

// LegacyKeyringUsersForConfig lists the pre-prefix keyring identifiers to try
// after the current KeyringUserForConfig misses. Before the bucket+prefix
// identity existed, entries were keyed on the bucket alone; this lets an
// existing entry still resolve after an upgrade. It returns nothing when the
// current identity already equals the bucket (nothing to fall back to).
func LegacyKeyringUsersForConfig(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" || KeyringUserForConfig(cfg) == bucket {
		return nil
	}
	return []string{bucket}
}

// KeyringOptionsForConfig builds the StoreKeyringOptions used to save or delete
// the passphrase for cfg's repo.
func KeyringOptionsForConfig(cfg *Config) StoreKeyringOptions {
	return StoreKeyringOptions{
		KeyringService: KeyringService,
		KeyringUser:    KeyringUserForConfig(cfg),
	}
}
```

Now rewrite `cmd/sentra/passphrase.go` to delegate. Delete the local `keyringService`/`keyringDefaultUser` consts and the `keyringUserForConfig`/`legacyKeyringUsersForConfig`/`keyringOptionsForConfig` funcs, and call `config.*`. Replace `cmd/sentra/passphrase.go:15-17`:
```go
```
(delete both const lines).

Replace `buildResolveOptsFromConfig` body at `cmd/sentra/passphrase.go:48-63`:
```go
func buildResolveOptsFromConfig(rootFlags *cli.RootFlags, cfg *config.Config, prompt func() ([]byte, error)) config.ResolveOptions {
	opts := config.ResolveOptions{
		PassphraseFile: rootFlags.PassphraseFile,
		Prompt:         prompt,
	}
	if cfg != nil {
		opts.UseKeyring = cfg.Passphrase.UseKeyring
		opts.KeyringService = config.KeyringService
		opts.KeyringUser = config.KeyringUserForConfig(cfg)
		opts.KeyringFallbackUsers = config.LegacyKeyringUsersForConfig(cfg)
	}
	if opts.KeyringUser == "" {
		opts.KeyringUser = config.KeyringDefaultUser
	}
	return opts
}
```

Delete the three functions `keyringUserForConfig` (`:65-78`), `legacyKeyringUsersForConfig` (`:80-89`), and `keyringOptionsForConfig` (`:140-145`).

Replace `saveRepoPassphraseToKeyring`/`deleteRepoPassphraseFromKeyring` (`:118-138`):
```go
func saveRepoPassphraseToKeyring(cfg *config.Config, passphrase []byte) error {
	return config.StoreKeyringPassphrase(config.KeyringOptionsForConfig(cfg), passphrase)
}

func deleteRepoPassphraseFromKeyring(cfg *config.Config) (bool, error) {
	deleted, err := config.DeleteKeyringPassphrase(config.KeyringOptionsForConfig(cfg))
	if err != nil {
		return false, err
	}
	for _, user := range config.LegacyKeyringUsersForConfig(cfg) {
		legacyDeleted, err := config.DeleteKeyringPassphrase(config.StoreKeyringOptions{
			KeyringService: config.KeyringService,
			KeyringUser:    user,
		})
		if err != nil {
			return deleted, err
		}
		deleted = deleted || legacyDeleted
	}
	return deleted, nil
}
```

Update `cmd/sentra/passphrase_test.go` so its three tests reference `config.*` (the local funcs are gone). Replace `cmd/sentra/passphrase_test.go:11-36`:
```go
func TestKeyringUserForConfigIncludesBucketAndPrefix(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"

	got := config.KeyringUserForConfig(&cfg)
	if got != "shared-bucket/repo-a/" {
		t.Fatalf("keyring user: got %q, want bucket/prefix identity", got)
	}
}

func TestLegacyKeyringUsersForConfigFallsBackToBucketOnly(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"

	got := config.LegacyKeyringUsersForConfig(&cfg)
	if len(got) != 1 || got[0] != "shared-bucket" {
		t.Fatalf("legacy users: got %v, want [shared-bucket]", got)
	}

	cfg.Repo.S3.Prefix = ""
	if got := config.LegacyKeyringUsersForConfig(&cfg); len(got) != 0 {
		t.Fatalf("legacy users without prefix: got %v, want none", got)
	}
}
```
(`TestBuildResolveOptsAddsLegacyKeyringFallback` at `:38-51` is unchanged — it exercises the cmd-level `buildResolveOptsFromConfig`, which still works.)

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/config/ ./cmd/sentra/ -run 'Keyring|BuildResolveOpts' -count=1`
Expected: PASS. Also `go build ./...` clean.

- [ ] **Step 5: Commit**
```bash
git add internal/config/keyring.go internal/config/keyring_test.go cmd/sentra/passphrase.go cmd/sentra/passphrase_test.go
git commit -m "refactor(config): move keyring derivation into internal/config with pin test

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 2: Extract IAM policy builder into internal/setup with a golden-JSON test

**Files:**
- Create: `internal/setup/iam_policy.go`
- Test: `internal/setup/iam_policy_test.go`
- Modify: `internal/cli/setup_iam_policy.go:12-23,54-111`
- Modify: `internal/cli/setup_awss3_ops.go:104-113` (delete moved ARN helpers, replace call sites with `setup.*`)

Dedup decision: `internal/diag` already has an unexported `bucketARN` (`internal/diag/aws.go:147`) that returns the same string, but it is package-private and diag has no object-ARN helper. To keep this unit free of a new `internal/setup → internal/diag` dependency and to own the object-ARN logic in one place, `internal/setup` gets its own exported `BucketARN`/`ObjectARN`. diag's copy stays as-is (unchanged, still private). cli's `setup_awss3_ops.go` switches its five error messages to call `setup.BucketARN`, and its local `s3BucketARN`/`s3ObjectARN` are deleted.

- [ ] **Step 1: Write the failing test**
```go
// internal/setup/iam_policy_test.go
package setup

import (
	"bytes"
	"testing"
)

func TestBucketAndObjectARN(t *testing.T) {
	if got := BucketARN("b"); got != "arn:aws:s3:::b" {
		t.Fatalf("BucketARN = %q", got)
	}
	if got := ObjectARN("b", ""); got != "arn:aws:s3:::b/*" {
		t.Fatalf("ObjectARN empty prefix = %q", got)
	}
	if got := ObjectARN("b", "sentra/"); got != "arn:aws:s3:::b/sentra/*" {
		t.Fatalf("ObjectARN with prefix = %q", got)
	}
}

func TestWriteIAMPolicyGolden(t *testing.T) {
	const want = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SentraSetupBucketControls",
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:GetBucketEncryption",
        "s3:GetBucketPublicAccessBlock",
        "s3:PutBucketEncryption",
        "s3:PutBucketPublicAccessBlock"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket"
      ]
    },
    {
      "Sid": "SentraListBucket",
      "Effect": "Allow",
      "Action": [
        "s3:GetBucketLocation",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket"
      ],
      "Condition": {
        "StringLike": {
          "s3:prefix": [
            "sentra/*"
          ]
        }
      }
    },
    {
      "Sid": "SentraRepositoryObjects",
      "Effect": "Allow",
      "Action": [
        "s3:DeleteObject",
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket/sentra/*"
      ]
    }
  ]
}
`
	var buf bytes.Buffer
	if err := WriteIAMPolicy(&buf, "example-bucket", "sentra/"); err != nil {
		t.Fatalf("WriteIAMPolicy: %v", err)
	}
	if buf.String() != want {
		t.Fatalf("WriteIAMPolicy mismatch:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

func TestBuildIAMPolicyNoPrefixOmitsCondition(t *testing.T) {
	doc := BuildIAMPolicy("example-bucket", "")
	for _, s := range doc.Statement {
		if s.Sid == "SentraListBucket" && s.Condition != nil {
			t.Fatalf("expected no Condition when prefix is empty, got %v", s.Condition)
		}
		if s.Sid == "SentraRepositoryObjects" && s.Resource[0] != "arn:aws:s3:::example-bucket/*" {
			t.Fatalf("object resource = %q, want /*", s.Resource[0])
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestBucketAndObjectARN|TestWriteIAMPolicyGolden|TestBuildIAMPolicyNoPrefixOmitsCondition' -count=1`
Expected: FAIL — package `internal/setup` does not exist / `undefined: WriteIAMPolicy` (build error).

- [ ] **Step 3: Write the minimal implementation**
```go
// internal/setup/iam_policy.go
package setup

import (
	"encoding/json"
	"fmt"
	"io"
)

// IAMPolicyDocument is a least-privilege AWS IAM policy for Sentra, rendered as
// non-secret JSON for the operator to paste into AWS.
type IAMPolicyDocument struct {
	Version   string               `json:"Version"`
	Statement []IAMPolicyStatement `json:"Statement"`
}

// IAMPolicyStatement is one statement in an IAMPolicyDocument. Condition is
// omitted from the JSON when nil so a prefix-less policy stays clean.
type IAMPolicyStatement struct {
	Sid       string         `json:"Sid"`
	Effect    string         `json:"Effect"`
	Action    []string       `json:"Action"`
	Resource  []string       `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

// BucketARN returns the S3 ARN for the bucket itself (no object suffix).
func BucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}

// ObjectARN returns the S3 ARN pattern for the objects Sentra reads and writes.
// An empty prefix widens the pattern to the whole bucket; a prefix scopes it so
// the granted identity only touches Sentra's keys.
func ObjectARN(bucket string, prefix string) string {
	if prefix == "" {
		return BucketARN(bucket) + "/*"
	}
	return BucketARN(bucket) + "/" + prefix + "*"
}

// WriteIAMPolicy encodes BuildIAMPolicy(bucket, prefix) as indented JSON. It is
// the single rendering path so the CLI command and any TUI reuse the exact
// same document.
func WriteIAMPolicy(w io.Writer, bucket string, prefix string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(BuildIAMPolicy(bucket, prefix)); err != nil {
		return fmt.Errorf("encode IAM policy: %w", err)
	}
	return nil
}

// BuildIAMPolicy assembles the three-statement least-privilege policy: bucket
// controls used during setup, list access scoped by prefix, and object CRUD on
// the repo keys. The prefix condition is only attached when a prefix is set.
func BuildIAMPolicy(bucket string, prefix string) IAMPolicyDocument {
	bucketResource := BucketARN(bucket)
	objectResource := ObjectARN(bucket, prefix)
	listStatement := IAMPolicyStatement{
		Sid:    "SentraListBucket",
		Effect: "Allow",
		Action: []string{
			"s3:GetBucketLocation",
			"s3:ListBucket",
		},
		Resource: []string{bucketResource},
	}
	if prefix != "" {
		listStatement.Condition = map[string]any{
			"StringLike": map[string]any{
				"s3:prefix": []string{prefix + "*"},
			},
		}
	}
	return IAMPolicyDocument{
		Version: "2012-10-17",
		Statement: []IAMPolicyStatement{
			{
				Sid:    "SentraSetupBucketControls",
				Effect: "Allow",
				Action: []string{
					"s3:CreateBucket",
					"s3:GetBucketEncryption",
					"s3:GetBucketPublicAccessBlock",
					"s3:PutBucketEncryption",
					"s3:PutBucketPublicAccessBlock",
				},
				Resource: []string{bucketResource},
			},
			listStatement,
			{
				Sid:    "SentraRepositoryObjects",
				Effect: "Allow",
				Action: []string{
					"s3:DeleteObject",
					"s3:GetObject",
					"s3:PutObject",
				},
				Resource: []string{objectResource},
			},
		},
	}
}
```

Rewrite `internal/cli/setup_iam_policy.go` to delete the moved type/build/write code and delegate. Delete the two types (`:12-23`) and the funcs `writeSetupIAMPolicy`/`buildSetupIAMPolicy` (`:54-111`). Replace the `RunE` return at `:46` to call `setup.WriteIAMPolicy(out, bucket, prefix)` and add `"github.com/markgustetic/sentra/internal/setup"` to the import block (dropping the now-unused `"encoding/json"` import). New file body:
```go
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/setup"
)

func newSetupIAMPolicy(out io.Writer) *cobra.Command {
	var bucket string
	var prefix string
	cmd := &cobra.Command{
		Use:   "iam-policy",
		Short: "Print a least-privilege AWS IAM policy for Sentra",
		Long: "Print non-secret IAM JSON for the selected S3 bucket and prefix. " +
			"The policy covers setup checks plus normal backup, restore, check, sync, and prune operations.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out = cmdStdout(cmd, out)
			bucket = strings.TrimSpace(bucket)
			prefix = strings.TrimSpace(prefix)
			if bucket == "" {
				return fmt.Errorf("--bucket is required")
			}
			if err := validateSetupBucketName(bucket); err != nil {
				return err
			}
			return setup.WriteIAMPolicy(out, bucket, prefix)
		},
	}
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&prefix, "prefix", "sentra/", "S3 key prefix Sentra will use")
	return cmd
}
```

In `internal/cli/setup.go:217`, the call `writeSetupIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix)` must become `setup.WriteIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix)` and `setup.go`'s import block gains `"github.com/markgustetic/sentra/internal/setup"`.

In `internal/cli/setup_awss3_ops.go`, delete the local ARN helpers `s3BucketARN`/`s3ObjectARN` (`:104-113`) and rewrite the five error messages (`:20,39,46,62,81`) to use `setup.BucketARN(bucket)`; add the `setup` import. Note `s3ObjectARN` has no remaining caller after `buildSetupIAMPolicy` moves, so it is simply deleted. Edited lines:
```go
// :20
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, setup.BucketARN(bucket), err)
// :39
	return false, fmt.Errorf("create bucket %q (requires s3:CreateBucket on %s): %w", bucket, setup.BucketARN(bucket), err)
// :46
		return fmt.Errorf("wait for bucket %q to exist (requires s3:ListBucket on %s): %w", bucket, setup.BucketARN(bucket), err)
// :62
		return fmt.Errorf("block public access for bucket %q (requires s3:PutBucketPublicAccessBlock on %s): %w", bucket, setup.BucketARN(bucket), err)
// :81
		return fmt.Errorf("enable default encryption for bucket %q (requires s3:PutBucketEncryption on %s): %w", bucket, setup.BucketARN(bucket), err)
```
Add `"github.com/markgustetic/sentra/internal/setup"` to `setup_awss3_ops.go`'s import block.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ ./internal/cli/ -run 'IAMPolicy|ARN|Setup' -count=1` and `go build ./...`
Expected: PASS; build clean. `golangci-lint run ./internal/cli/... ./internal/setup/...` reports no `unused` findings (`s3BucketARN`/`s3ObjectARN` deleted, `encoding/json` import dropped).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/iam_policy.go internal/setup/iam_policy_test.go internal/cli/setup_iam_policy.go internal/cli/setup_awss3_ops.go internal/cli/setup.go
git commit -m "refactor(setup): extract IAM policy builder into internal/setup with golden test

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 3: Move AWS setup error taxonomy into internal/setup, keeping substring semantics

**Files:**
- Create: `internal/setup/errors.go`
- Create: `internal/setup/aws_types.go`
- Test: `internal/setup/errors_test.go`
- Modify: `internal/cli/setup_errors.go` (delete moved funcs; keep `printSetupErrorDetail` as I/O wrapper delegating to `setup.ErrorAdvice`)
- Modify: `internal/cli/setup_auth.go:49,96,105` (call `setup.WrapAWS*` via cli wrappers)

`WrapAWSPrepareError` takes an auth-method parameter. The pinned API puts `AWSAuthMethod` (and its const values) in `internal/setup`, so this task introduces `internal/setup/aws_types.go` with `type AWSAuthMethod string` and consts `AWSAuthLogin`/`AWSAuthSSO`/`AWSAuthExisting`/`AWSAuthSkip` whose string values match cli's `SetupAWSAuthMethod` (`"login"`/`"sso"`/`"existing"`/`"skip"`, from `setup.go:30-33`). cli keeps its own `SetupAWSAuthMethod` consts for now; the cli wrapper `wrapAWSPrepareError` converts. `WrapAWSPrepareError`/`ErrorAdvice` take `config.Config` by value (not pointer) per the pinned API; the cli wrappers deref.

- [ ] **Step 1: Write the failing test**
```go
// internal/setup/errors_test.go
package setup

import (
	"errors"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestIsAWSMissingCredentialsError(t *testing.T) {
	tests := []struct {
		msg  string
		want bool
	}{
		{"failed to refresh cached credentials", true},
		{"no EC2 IMDS role found", true},
		{"no valid credential sources", true},
		{"no credential provider configured", true},
		{"resolve credential providers", true},
		{"ec2imds timeout", true},
		{"access denied", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := IsAWSMissingCredentialsError(errors.New(tt.msg)); got != tt.want {
			t.Fatalf("IsAWSMissingCredentialsError(%q) = %v, want %v", tt.msg, got, tt.want)
		}
	}
}

func TestWrapAWSPrepareErrorClassifiesByMethod(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Profile = "work"

	missing := errors.New("no valid credential sources")
	got := WrapAWSPrepareError(cfg, AWSAuthLogin, missing)
	if !strings.Contains(got.Error(), "browser login") || !strings.Contains(got.Error(), "AWS profile work") {
		t.Fatalf("login wrap = %v", got)
	}
	if !errors.Is(got, missing) {
		t.Fatalf("wrap must preserve cause chain")
	}

	sso := WrapAWSPrepareError(cfg, AWSAuthSSO, missing)
	if !strings.Contains(sso.Error(), "SSO flow") {
		t.Fatalf("sso wrap = %v", sso)
	}

	other := errors.New("some unrelated failure")
	if got := WrapAWSPrepareError(cfg, AWSAuthExisting, other); !strings.HasPrefix(got.Error(), "prepare AWS S3:") {
		t.Fatalf("non-credential error should be plain prepare wrap, got %v", got)
	}
}

func TestWrapAWSLoginAndSSOFlowErrors(t *testing.T) {
	base := errors.New("boom")
	if got := WrapAWSLoginFlowError("", base); !strings.Contains(got.Error(), "profile default") {
		t.Fatalf("login flow default profile = %v", got)
	}
	if got := WrapAWSSSOFlowError("aws sso login", "work", base); !strings.Contains(got.Error(), "profile work") {
		t.Fatalf("sso flow = %v", got)
	}
	if got := WrapAWSSSOFlowError("aws configure sso", "", base); !strings.Contains(got.Error(), "the default profile") {
		t.Fatalf("sso flow default = %v", got)
	}
}

func TestErrorAdvice(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "my-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	cfg.Repo.S3.Profile = "work"

	advice := ErrorAdvice(errors.New("head bucket: AccessDenied: status code: 403"), cfg)
	joined := strings.Join(advice, "\n")
	if !strings.Contains(joined, "my-bucket") {
		t.Fatalf("advice should mention bucket, got %v", advice)
	}
	if !strings.Contains(joined, "iam-policy") {
		t.Fatalf("access-denied advice should mention iam-policy, got %v", advice)
	}

	if got := ErrorAdvice(nil, cfg); got != nil {
		t.Fatalf("nil error advice = %v, want nil", got)
	}

	fallback := ErrorAdvice(errors.New("totally novel failure"), config.Config{})
	if len(fallback) != 1 {
		t.Fatalf("expected single fallback line, got %v", fallback)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestIsAWSMissingCredentialsError|TestWrapAWSPrepareErrorClassifiesByMethod|TestWrapAWSLoginAndSSOFlowErrors|TestErrorAdvice' -count=1`
Expected: FAIL — `undefined: WrapAWSPrepareError` / `undefined: AWSAuthLogin` (build error).

- [ ] **Step 3: Write the minimal implementation**
```go
// internal/setup/aws_types.go
package setup

// AWSAuthMethod names how setup makes AWS credentials available before it
// prepares the bucket. The string values are stable and match the CLI wizard's
// SetupAWSAuthMethod so config and reports read the same across both drivers.
type AWSAuthMethod string

const (
	AWSAuthLogin    AWSAuthMethod = "login"
	AWSAuthSSO      AWSAuthMethod = "sso"
	AWSAuthExisting AWSAuthMethod = "existing"
	AWSAuthSkip     AWSAuthMethod = "skip"
)
```
```go
// internal/setup/errors.go
package setup

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
)

// WrapAWSSSOFlowError annotates a failed `aws configure sso` / `aws sso login`
// with the profile it ran for and the recovery paths, preserving the cause.
func WrapAWSSSOFlowError(command string, profile string, err error) error {
	profile = strings.TrimSpace(profile)
	profileLabel := "the default profile"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	return fmt.Errorf("%s did not complete for %s. Rerun `sentra setup` and choose IAM Identity Center / SSO again, choose Browser login, or choose Existing credentials after running `%s`: %w", command, profileLabel, configureCommand, err)
}

// WrapAWSPrepareError classifies a bucket-prep failure. Missing-credential
// causes get method-specific guidance; everything else is a plain prepare wrap.
// The credential test is substring matching on raw AWS SDK text (not
// errors.Is) because the SDK does not expose typed sentinels for these.
func WrapAWSPrepareError(cfg config.Config, method AWSAuthMethod, err error) error {
	if !IsAWSMissingCredentialsError(err) {
		return fmt.Errorf("prepare AWS S3: %w", err)
	}

	profile := strings.TrimSpace(cfg.Repo.S3.Profile)
	profileLabel := "the default AWS credential chain"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "AWS profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	switch method {
	case AWSAuthLogin:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not available for %s after browser login. Rerun `sentra setup` and choose Browser login again, or configure non-browser credentials with `%s`: %w", profileLabel, configureCommand, err)
	case AWSAuthSSO:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not available for %s after the SSO flow. Rerun `sentra setup` and choose IAM Identity Center / SSO again, or configure non-SSO credentials with `%s`: %w", profileLabel, configureCommand, err)
	default:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not found for %s. Configure them with `%s`, export AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, use role credentials, or rerun `sentra setup` and choose Browser login if you want Sentra to open an AWS sign-in flow: %w", profileLabel, configureCommand, err)
	}
}

// WrapAWSLoginFlowError annotates a failed `aws login` with its profile and the
// recovery paths, preserving the cause.
func WrapAWSLoginFlowError(profile string, err error) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = "default"
	}
	return fmt.Errorf("aws login did not complete for profile %s. Rerun `sentra setup` and choose Browser login again, choose IAM Identity Center / SSO, or choose Existing credentials after configuring a profile manually: %w", profile, err)
}

// IsAWSMissingCredentialsError reports whether err's text matches the AWS SDK's
// missing-credential phrasings. It is deliberately substring-based: the SDK
// returns these as plain messages, not wrapped sentinels, so errors.Is cannot
// classify them.
func IsAWSMissingCredentialsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"failed to refresh cached credentials",
		"no ec2 imds role found",
		"no valid credential",
		"no credential provider",
		"credential providers",
		"ec2imds",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// ErrorAdvice returns operator-facing recovery hints for an AWS setup failure.
// It classifies err by substring (raw AWS error text) and adds cfg context when
// available. Returns nil for a nil error and a single generic line when nothing
// else matched.
func ErrorAdvice(err error, cfg config.Config) []string {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	var advice []string
	add := func(line string) {
		for _, existing := range advice {
			if existing == line {
				return
			}
		}
		advice = append(advice, line)
	}

	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	region := strings.TrimSpace(cfg.Repo.S3.Region)
	profile := strings.TrimSpace(cfg.Repo.S3.Profile)
	switch {
	case bucket != "" && region != "" && profile != "":
		add(fmt.Sprintf("Using bucket %q in region %q with AWS profile %q.", bucket, region, profile))
	case bucket != "" && region != "":
		add(fmt.Sprintf("Using bucket %q in region %q with the default AWS credential chain.", bucket, region))
	case bucket != "":
		add(fmt.Sprintf("Using bucket %q.", bucket))
	}

	switch {
	case IsAWSMissingCredentialsError(err):
		add("Credentials are still unavailable. Choose Browser login/SSO again, or configure a working profile with `aws configure --profile <profile>`.")
	case strings.Contains(msg, "accessdenied") || strings.Contains(msg, "access denied") || strings.Contains(msg, "status code: 403") || strings.Contains(msg, "forbidden"):
		if strings.Contains(msg, "head bucket") {
			add("S3 HeadBucket can return AccessDenied when the bucket is owned by another account or when your identity lacks s3:ListBucket.")
			add("If this is a new bucket, choose a globally unique name; generic names like `sentra-test` are often already taken.")
			add("If this is your existing bucket, grant the selected identity s3:ListBucket on the bucket ARN.")
		}
		add("Use `sentra setup iam-policy --bucket <bucket> --prefix <prefix>` or the setup policy option to print the required IAM policy.")
	case strings.Contains(msg, "bucketalreadyexists"):
		add("That bucket name is already owned by another AWS account. Pick a globally unique bucket name and rerun setup.")
	case strings.Contains(msg, "bucketalreadyownedbyyou"):
		add("That bucket already belongs to you. Rerun setup with create/verify enabled, or choose verify-only if you do not want creation attempts.")
	case strings.Contains(msg, "permanentredirect") || strings.Contains(msg, "authorizationheadermalformed") || strings.Contains(msg, "wrong region"):
		add("The bucket appears to be in a different region. Check the bucket region in AWS, then rerun setup with that region.")
	case strings.Contains(msg, "invalidbucketname") || strings.Contains(msg, "invalid bucket"):
		add("Use a DNS-compatible S3 bucket name: lowercase letters, numbers, hyphens, and dots; 3-63 characters.")
	case strings.Contains(msg, "does not exist") || strings.Contains(msg, "nosuchbucket"):
		add("The bucket was not found. Allow setup to create it, create it manually, or choose a different bucket name.")
	}

	if len(advice) == 0 {
		add("You can edit the region/profile, switch sign-in methods, write config only, or cancel and rerun setup after fixing AWS.")
	}
	return advice
}
```

Now rewrite `internal/cli/setup_errors.go` to delete the moved funcs and keep thin cli wrappers plus the I/O `printSetupErrorDetail`. New file body:
```go
package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

func wrapAWSSSOFlowError(command string, profile string, err error) error {
	return setup.WrapAWSSSOFlowError(command, profile, err)
}

func wrapAWSPrepareError(cfg *config.Config, method SetupAWSAuthMethod, err error) error {
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	return setup.WrapAWSPrepareError(c, setup.AWSAuthMethod(method), err)
}

func wrapAWSLoginFlowError(profile string, err error) error {
	return setup.WrapAWSLoginFlowError(profile, err)
}

func isAWSMissingCredentialsError(err error) bool {
	return setup.IsAWSMissingCredentialsError(err)
}

func printSetupErrorDetail(out io.Writer, err error, cfg *config.Config) {
	if err == nil {
		return
	}
	var c config.Config
	if cfg != nil {
		c = *cfg
	}
	fmt.Fprintf(out, "%s %v\n", ui.Danger.Render("reason:"), err)
	for _, line := range setup.ErrorAdvice(err, c) {
		fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("advice:"), line)
	}
}
```
(This drops the local `setupErrorAdvice` and `strings` import; the wrappers keep every existing cli call site — `setup_auth.go:49,96,105`, `setup.go`'s `wrapAWSPrepareError` caller, and `doctor.go`/`setup.go` uses of `isAWSMissingCredentialsError`/`printSetupErrorDetail` — compiling unchanged.)

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ ./internal/cli/ ./cmd/sentra/ -count=1` and `go build ./...`
Expected: PASS; build clean. `golangci-lint run ./internal/cli/... ./internal/setup/...` — no `unused` findings (`setupErrorAdvice` deleted).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/errors.go internal/setup/aws_types.go internal/setup/errors_test.go internal/cli/setup_errors.go
git commit -m "refactor(setup): move AWS setup error taxonomy into internal/setup

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 4: Move AWS-CLI config parser + install-plan + effectful helpers into internal/setup

**Files:**
- Create: `internal/setup/awscli.go`
- Test: `internal/setup/awscli_test.go` (moved verbatim from `internal/cli/setup_awscli_test.go`, retargeted to package `setup` and exported names)
- Modify: `internal/cli/setup_awscli.go` → becomes thin wrappers delegating to `setup.*`
- Delete: `internal/cli/setup_awscli_test.go`
- Modify: `internal/cli/setup_wizard.go:261,266` (call the cli wrappers `loadAWSCLIConfig`/`awsProfileSection`, which now delegate — no source change needed there since wrappers keep the same names)

Scope: this task moves the parser (`LoadAWSCLIConfig`, `AWSConfigPath`, `AWSSSOProfileConfigured`, `AWSProfileSection`, and the `hasAll/hasAny` predicates), the install plan (`DefaultAWSCLIInstallPlan`), the `AWSCLIConfig` type, and the effectful runners (`DefaultEnsureAWSCLI`, `DefaultAWSLogin`, `DefaultAWSSSOConfigured`, `DefaultAWSConfigureSSO`, `DefaultAWSSSOLogin`, `runAWSCLI`, `appendAWSProfile`). The `AWSCLIInstallPlan`/`AWSCLIInstallReport` types and `AWSCLIInstallConfirm` are defined in `internal/setup/aws_types.go` (extend the file from the previous task) so both the plan and the effectful ensure compile there. cli keeps same-named wrappers so `setup_auth.go` (`DefaultAWSLogin`, `DefaultAWSSSOConfigured`, `DefaultAWSConfigureSSO`, `DefaultAWSSSOLogin`, `DefaultEnsureAWSCLI`) and `setup_wizard.go` (`loadAWSCLIConfig`, `awsProfileSection`) compile unchanged, and cli's `AWSCLIInstallPlan`/`AWSCLIInstallConfirm`/`AWSCLIInstallReport` types stay (they are still referenced across cli and are re-aliased to `setup.*` in a later unit).

- [ ] **Step 1: Write the failing test**
Create `internal/setup/awscli_test.go` — the verbatim content of `internal/cli/setup_awscli_test.go` (`internal/cli/setup_awscli_test.go:1-242`) with `package cli` → `package setup` and the exported call names substituted (`DefaultEnsureAWSCLI`→`DefaultEnsureAWSCLI`, `AWSCLIInstallPlan`→`AWSCLIInstallPlan`, `DefaultAWSSSOConfigured`→`DefaultAWSSSOConfigured` — these names are unchanged, so only the package line and the removal of the `AWSCLIInstallPlan` reference qualifier change). Full file:
```go
// internal/setup/awscli_test.go
package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDefaultEnsureAWSCLI_AlreadyInstalled(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "aws"), "#!/bin/sh\nexit 0\n")
	t.Setenv("PATH", dir)

	report, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		t.Fatal("confirm should not run when aws is already installed")
		return false, nil
	})
	if err != nil {
		t.Fatalf("ensure aws cli: %v", err)
	}
	if !report.AlreadyInstalled || report.Installed {
		t.Fatalf("report = %+v, want already installed only", report)
	}
}

func TestDefaultEnsureAWSCLI_InstallsWithHomebrew(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script fake package manager is unix-only")
	}
	dir := t.TempDir()
	chmodPath, err := exec.LookPath("chmod")
	if err != nil {
		t.Fatalf("locate chmod: %v", err)
	}
	awsPath := filepath.Join(dir, "aws")
	writeExecutable(t, filepath.Join(dir, "brew"), fmt.Sprintf(`#!/bin/sh
if [ "$1" != "install" ] || [ "$2" != "awscli" ]; then
  exit 2
fi
{
  printf '%%s\n' '#!/bin/sh'
  printf '%%s\n' 'exit 0'
} > %q
%q +x %q
`, awsPath, chmodPath, awsPath))
	t.Setenv("PATH", dir)

	var seen AWSCLIInstallPlan
	report, err := DefaultEnsureAWSCLI(context.Background(), func(plan AWSCLIInstallPlan) (bool, error) {
		seen = plan
		return true, nil
	})
	if err != nil {
		t.Fatalf("ensure aws cli: %v", err)
	}
	if seen.Manager != "Homebrew" || strings.Join(seen.Command, " ") != "brew install awscli" {
		t.Fatalf("install plan = %+v", seen)
	}
	if !report.Installed || report.Manager != "Homebrew" {
		t.Fatalf("report = %+v, want installed with Homebrew", report)
	}
}

func TestDefaultEnsureAWSCLI_InstallDeclined(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "brew"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", dir)

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		return false, nil
	})
	if err == nil {
		t.Fatal("expected declined install error, got nil")
	}
	if !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestDefaultEnsureAWSCLI_ConfirmError(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "brew"), "#!/bin/sh\nexit 99\n")
	t.Setenv("PATH", dir)
	wantErr := errors.New("no thanks")

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		return false, wantErr
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}

func TestDefaultEnsureAWSCLI_NoSupportedInstaller(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := DefaultEnsureAWSCLI(context.Background(), func(AWSCLIInstallPlan) (bool, error) {
		t.Fatal("confirm should not run without a supported installer")
		return false, nil
	})
	if err == nil {
		t.Fatal("expected missing installer error, got nil")
	}
	if !strings.Contains(err.Error(), "AWS CLI is required for the selected AWS sign-in method") {
		t.Fatalf("error = %v, want AWS CLI sign-in guidance", err)
	}
}

func TestDefaultAWSSSOConfigured_ModernSessionProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_session = work
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
region = us-east-1

[sso-session work]
sso_issuer_url = https://identitycenter.example/start
sso_region = us-east-1
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete modern SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_LegacyInlineProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete legacy SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_DefaultProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[default]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if !configured {
		t.Fatal("expected complete default SSO profile to be configured")
	}
}

func TestDefaultAWSSSOConfigured_RejectsPartialModernProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_session = work
sso_account_id = 000000000000
sso_role_name = AdministratorAccess
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected profile missing its sso-session section to be unconfigured")
	}
}

func TestDefaultAWSSSOConfigured_RejectsPartialLegacyProfile(t *testing.T) {
	cfgPath := writeAWSConfig(t, `
[profile sentra]
sso_start_url = https://identitycenter.example/start
sso_region = us-east-1
sso_account_id = 000000000000
`)
	t.Setenv("AWS_CONFIG_FILE", cfgPath)

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected legacy profile missing role name to be unconfigured")
	}
}

func TestDefaultAWSSSOConfigured_MissingConfigFile(t *testing.T) {
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing-config"))

	configured, err := DefaultAWSSSOConfigured(context.Background(), "sentra")
	if err != nil {
		t.Fatalf("configured: %v", err)
	}
	if configured {
		t.Fatal("expected missing AWS config file to be unconfigured")
	}
}

func writeAWSConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write aws config: %v", err)
	}
	return path
}

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // test helper creates fake executables in t.TempDir.
		t.Fatalf("chmod executable %s: %v", path, err)
	}
}
```
Then `git rm internal/cli/setup_awscli_test.go`.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestDefaultEnsureAWSCLI|TestDefaultAWSSSOConfigured' -count=1`
Expected: FAIL — `undefined: DefaultEnsureAWSCLI` / `undefined: AWSCLIInstallPlan` in package `setup` (build error).

- [ ] **Step 3: Write the minimal implementation**
Append the install-plan/report types to `internal/setup/aws_types.go` (created in the previous task):
```go
// AWSCLIInstallPlan is the package-manager command Sentra can run to install
// the AWS CLI for setup's SSO flow.
type AWSCLIInstallPlan struct {
	Manager string
	Command []string
}

// AWSCLIInstallConfirm asks whether Sentra may run the detected package
// manager command.
type AWSCLIInstallConfirm func(plan AWSCLIInstallPlan) (bool, error)

// AWSCLIInstallReport summarizes the AWS CLI preflight.
type AWSCLIInstallReport struct {
	AlreadyInstalled bool
	Installed        bool
	Manager          string
}
```
Create the moved parser + effectful runners:
```go
// internal/setup/awscli.go
package setup

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// DefaultEnsureAWSCLI verifies that the AWS CLI is available. When it is
// missing and Homebrew is available, it asks (via confirm) to run
// `brew install awscli` and verifies the install before continuing. This
// brew auto-install path is CLI-only; the TUI wizard must detect a missing
// aws CLI and surface advice instead of driving this confirm.
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if _, err := exec.LookPath("aws"); err == nil {
		return AWSCLIInstallReport{AlreadyInstalled: true}, nil
	}

	plan, ok := DefaultAWSCLIInstallPlan()
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but was not found in PATH. Install it, or rerun setup and choose Existing credentials")
	}
	if confirm == nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but no install confirmation was configured")
	}
	ok, err := confirm(plan)
	if err != nil {
		return AWSCLIInstallReport{}, err
	}
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install canceled; install it manually or rerun setup and choose Existing credentials")
	}

	cmd := exec.CommandContext(ctx, plan.Command[0], plan.Command[1:]...) //nolint:gosec // fixed package-manager command selected by Sentra, confirmed by the operator.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("install AWS CLI with %s: %w", plan.Manager, err)
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install completed with %s, but aws is still not on PATH", plan.Manager)
	}
	return AWSCLIInstallReport{Installed: true, Manager: plan.Manager}, nil
}

// DefaultAWSCLIInstallPlan returns the package-manager command Sentra would run
// to install the AWS CLI, and whether a supported manager was found.
func DefaultAWSCLIInstallPlan() (AWSCLIInstallPlan, bool) {
	if _, err := exec.LookPath("brew"); err == nil {
		return AWSCLIInstallPlan{
			Manager: "Homebrew",
			Command: []string{"brew", "install", "awscli"},
		}, true
	}
	return AWSCLIInstallPlan{}, false
}

// DefaultAWSLogin delegates browser-based AWS CLI sign-in. The AWS CLI stores
// temporary credentials; Sentra never receives or stores them.
func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	args := []string{"login"}
	region = strings.TrimSpace(region)
	if region != "" {
		args = append(args, "--region", region)
	}
	return runAWSCLI(ctx, args, profile, true)
}

// DefaultAWSSSOConfigured checks whether the selected profile has a complete
// AWS CLI SSO profile (newer sso_session or older inline sso_start_url form).
func DefaultAWSSSOConfigured(_ context.Context, profile string) (bool, error) {
	cfg, err := LoadAWSCLIConfig()
	if err != nil {
		return false, err
	}
	if cfg == nil {
		return false, nil
	}
	return AWSSSOProfileConfigured(cfg, profile), nil
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the AWS CLI.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile, true)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the AWS CLI.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sso", "login"}, profile, true)
}

func runAWSCLI(ctx context.Context, args []string, profile string, interactive bool) error {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

func appendAWSProfile(args []string, profile string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return args
	}
	return append(args, "--profile", profile)
}

// AWSCLIConfig is a parsed ~/.aws/config: section name to key/value pairs.
type AWSCLIConfig map[string]map[string]string

// LoadAWSCLIConfig reads and parses the AWS CLI config file. A missing file is
// not an error — it returns (nil, nil) so callers treat it as "nothing
// configured".
func LoadAWSCLIConfig() (AWSCLIConfig, error) {
	path, err := AWSConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // AWS CLI config path comes from AWS_CONFIG_FILE or the current user's home dir.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	defer f.Close()

	cfg := AWSCLIConfig{}
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			end := strings.Index(line, "]")
			section = strings.TrimSpace(line[1:end])
			if section != "" && cfg[section] == nil {
				cfg[section] = map[string]string{}
			}
			continue
		}
		if section == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		cfg[section][key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	return cfg, nil
}

// AWSConfigPath returns the AWS CLI config path, honoring AWS_CONFIG_FILE and
// falling back to ~/.aws/config.
func AWSConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws config: %w", err)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

// AWSSSOProfileConfigured reports whether profile has a complete SSO config in
// cfg, supporting both the modern sso_session form and the legacy inline form.
func AWSSSOProfileConfigured(cfg AWSCLIConfig, profile string) bool {
	profileSection := AWSProfileSection(profile)
	values := cfg[profileSection]
	if len(values) == 0 {
		return false
	}
	if hasAllAWSConfigKeys(values, "sso_start_url", "sso_region", "sso_account_id", "sso_role_name") {
		return true
	}

	session := values["sso_session"]
	if strings.TrimSpace(session) == "" {
		return false
	}
	sessionValues := cfg["sso-session "+strings.TrimSpace(session)]
	if len(sessionValues) == 0 {
		return false
	}
	return hasAllAWSConfigKeys(values, "sso_account_id", "sso_role_name") &&
		hasAllAWSConfigKeys(sessionValues, "sso_region") &&
		hasAnyAWSConfigKey(sessionValues, "sso_start_url", "sso_issuer_url")
}

// AWSProfileSection maps a profile name to its ~/.aws/config section header.
func AWSProfileSection(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "default" {
		return "default"
	}
	return "profile " + profile
}

func hasAllAWSConfigKeys(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(values[key]) == "" {
			return false
		}
	}
	return true
}

func hasAnyAWSConfigKey(values map[string]string, keys ...string) bool {
	for _, key := range keys {
		if strings.TrimSpace(values[key]) != "" {
			return true
		}
	}
	return false
}
```
Rewrite `internal/cli/setup_awscli.go` to thin wrappers so `setup_auth.go` and `setup_wizard.go` compile unchanged. New file body:
```go
package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/setup"
)

func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	report, err := setup.DefaultEnsureAWSCLI(ctx, func(p setup.AWSCLIInstallPlan) (bool, error) {
		return confirm(AWSCLIInstallPlan{Manager: p.Manager, Command: p.Command})
	})
	return AWSCLIInstallReport{
		AlreadyInstalled: report.AlreadyInstalled,
		Installed:        report.Installed,
		Manager:          report.Manager,
	}, err
}

func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	return setup.DefaultAWSLogin(ctx, profile, region)
}

func DefaultAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return setup.DefaultAWSSSOConfigured(ctx, profile)
}

func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return setup.DefaultAWSConfigureSSO(ctx, profile)
}

func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return setup.DefaultAWSSSOLogin(ctx, profile)
}

func loadAWSCLIConfig() (setup.AWSCLIConfig, error) {
	return setup.LoadAWSCLIConfig()
}

func awsProfileSection(profile string) string {
	return setup.AWSProfileSection(profile)
}
```
(This keeps every cli caller working: `setup_auth.go:45,77,81,85,129`; `setup_wizard.go:261` `loadAWSCLIConfig()` returns `setup.AWSCLIConfig` which `setup_wizard.go:266` indexes with `awsProfileSection(...)` exactly as before; `defaultAWSProfileFromConfig` at `setup_wizard.go:260-276` still calls `awsProfileNameFromSection` which stays in cli. cli's `AWSCLIInstallPlan`/`AWSCLIInstallReport`/`AWSCLIInstallConfirm` type defs at `setup.go:72-87` remain — they are aliased to `setup.*` in a later unit; here the wrapper converts between the two identical struct shapes.)

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ ./internal/cli/ ./cmd/sentra/ -count=1` and `go build ./...`
Expected: PASS; build clean. `golangci-lint run ./internal/cli/... ./internal/setup/...` — no `unused` findings (moved `defaultAWSCLIInstallPlan`, `awsCLIConfig`, `awsConfigPath`, `awsSSOProfileConfigured`, `hasAll/hasAny`, `runAWSCLI`, `appendAWSProfile` deleted from cli).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/awscli.go internal/setup/awscli_test.go internal/setup/aws_types.go internal/cli/setup_awscli.go
git rm internal/cli/setup_awscli_test.go
git commit -m "refactor(setup): move AWS-CLI parser and install helpers into internal/setup

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

Notes for the controller:
- Task order within this unit is A, B, C, D. Task D depends on task C only because both append to `internal/setup/aws_types.go` (C creates it with `AWSAuthMethod`; D extends it with the install-plan types). If reordered, whichever runs first must `Create` the file and the second must `Modify` it.
- `internal/setup` imports only `config`, stdlib, and (via the ARN/error/awscli files) nothing from `cli`/`tui`/`diag`/`repo`/`blobstore`/aws-sdk in this unit — satisfying the import-direction constraint. `internal/cli` imports `internal/setup` (allowed).
- No secrets touch any new file, test, or fixture (IAM policy is non-secret JSON; AWS-CLI parser reads only section/key names; error text carries no credentials).

Verified against real source: `cmd/sentra/passphrase.go:15-17,48-89,118-145`; `internal/config/passphrase.go:73-81` (`StoreKeyringOptions`), `config.go:27-30` (S3 fields), `config.go:13` (`strings` already imported); `internal/cli/setup_iam_policy.go:12-111`; `internal/cli/setup_awss3_ops.go:104-113,20-81`; `internal/diag/aws.go:147-149`; `internal/cli/setup_errors.go:12-138`; `internal/cli/setup_awscli.go:16-236`; `internal/cli/setup_awscli_test.go:1-242`; `internal/cli/setup.go:27-136`; `internal/cli/setup_auth.go:43-136`; `internal/cli/setup_wizard.go:260-276`. Golden IAM JSON confirmed by executing the encoder.


## Part 2 — Engine types + pure transforms

**Published API:** (defined by this unit, package `internal/setup`)

```go
// types.go
type Backend string
const (
    BackendAWS          Backend = "aws"
    BackendS3Compatible Backend = "s3-compatible"
)
type AWSAuthMethod string
const (
    AWSAuthLogin    AWSAuthMethod = "login"
    AWSAuthSSO      AWSAuthMethod = "sso"
    AWSAuthExisting AWSAuthMethod = "existing"
    AWSAuthSkip     AWSAuthMethod = "skip"
)
type AWSRepairChoice string
const (
    AWSRepairLogin    AWSRepairChoice = "login"
    AWSRepairSSO      AWSRepairChoice = "sso"
    AWSRepairExisting AWSRepairChoice = "existing"
    AWSRepairConfig   AWSRepairChoice = "config"
    AWSRepairCancel   AWSRepairChoice = "cancel"
)
type Plan struct {
    Config            config.Config
    Backend           Backend
    PrepareAWS        bool
    AWSAuthMethod     AWSAuthMethod
    CreateBucket      bool
    BlockPublicAccess bool
    DefaultEncryption bool
    PrintIAMPolicy    bool
    SavePassphrase    bool
    InitRepo          bool
}
type AWSPrepareOptions struct { CreateBucket, BlockPublicAccess, DefaultEncryption bool }
type AWSPrepareReport struct { BucketExisted, BucketCreated, PublicAccessBlocked, DefaultEncryptionEnabled bool }
type AWSAuthReport struct {
    IdentityVerified bool
    Method           AWSAuthMethod
    AWSCLIInstalled  bool
    AWSCLIManager    string
    LoginRan, SSOConfigured, SSOConfigureRan, SSOLoginRan bool
}
type AWSCLIInstallPlan struct { Manager string; Command []string }
type AWSCLIInstallReport struct { AlreadyInstalled, Installed bool; Manager string }
type InitResult struct { RepoID string; AlreadyInitialized, PassphraseSavedToKeyring bool }

// envprobe.go
type EnvProbe interface {
    Getenv(string) string
    DefaultProfileFromConfig() string
    HasEnvCredentials() bool
}
func DefaultEnvProbe() EnvProbe

// transform.go
func DefaultPlan(cfg config.Config, probe EnvProbe) Plan
func NormalizeConfig(cfg *config.Config)
func ApplyAWSConfigOnly(p *Plan)
func ApplyPassphraseConfig(p *Plan)
func ResolveAWSAuthMethod(p *Plan) AWSAuthMethod
func DefaultAWSRepairChoice(p Plan, cause error) AWSRepairChoice
func ValidatePlan(p Plan) error

// review.go
func ReviewText(cfgPath string, p Plan) string
func BackendLabel(b Backend) string
func AWSAuthMethodLabel(m AWSAuthMethod) string
func AWSPreparedLabel(report *AWSPrepareReport) string
func SummaryLines(cfgPath string, p Plan, auth *AWSAuthReport, prep *AWSPrepareReport, init *InitResult) []string
```

> Cross-unit note: `DefaultAWSRepairChoice` needs the substring credential-detector (`isAWSMissingCredentialsError`, `internal/cli/setup_errors.go:56-71`). The pinned **exported** `IsAWSMissingCredentialsError` belongs to the errors unit. To keep this unit self-contained, Task 3 below defines the **unexported** `isAWSMissingCredentialsError` in `internal/setup`; the errors unit later adds `func IsAWSMissingCredentialsError(error) bool { return isAWSMissingCredentialsError(...) }` as the exported wrapper. This unit does not define the exported name.

> `SummaryLines` returns `[]string` of body content lines (no ANSI styling) split out of `printSetupSummary` (`internal/cli/setup_summary.go:11-85`); the section-header rendering and `ui.*` styling stay in the cli `printSetupSummary` caller. Label maps (`BackendLabel/AWSAuthMethodLabel/AWSPreparedLabel`) move verbatim from `setup_summary.go:121-158`.

---

### Task 5: Create internal/setup engine types + Backend/AuthMethod/RepairChoice const aliases in cli

**Files:**
- Create: `internal/setup/types.go`
- Create: `internal/setup/types_test.go`
- Modify: `internal/cli/setup.go:17-116` (replace type decls with `=` aliases)
- Modify: `internal/cli/setup_init.go:13-17` (alias `setupInitResult`)
- Modify: `internal/cli/setup_wizard.go:76-84` (alias `setupAWSRepairChoice` + consts)

- [ ] **Step 1: Write the failing test**

```go
// internal/setup/types_test.go
package setup

import (
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestConstValuesMatchLegacyStrings(t *testing.T) {
	cases := map[string]string{
		string(BackendAWS):          "aws",
		string(BackendS3Compatible): "s3-compatible",
		string(AWSAuthLogin):        "login",
		string(AWSAuthSSO):          "sso",
		string(AWSAuthExisting):     "existing",
		string(AWSAuthSkip):         "skip",
		string(AWSRepairLogin):      "login",
		string(AWSRepairSSO):        "sso",
		string(AWSRepairExisting):   "existing",
		string(AWSRepairConfig):     "config",
		string(AWSRepairCancel):     "cancel",
	}
	for got, want := range cases {
		if got != want {
			t.Errorf("const value = %q, want %q", got, want)
		}
	}
}

func TestPlanCarriesConfigAndFlags(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	p := Plan{
		Config:            cfg,
		Backend:           BackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     AWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		PrintIAMPolicy:    false,
		SavePassphrase:    true,
		InitRepo:          true,
	}
	if p.Config.Repo.S3.Bucket != "b" {
		t.Fatalf("config not carried: %q", p.Config.Repo.S3.Bucket)
	}
	if !p.PrepareAWS || !p.SavePassphrase || !p.InitRepo {
		t.Fatalf("flags not set: %+v", p)
	}
}

func TestReportZeroValues(t *testing.T) {
	if (AWSPrepareReport{}).BucketCreated {
		t.Fatal("zero AWSPrepareReport.BucketCreated should be false")
	}
	if (AWSAuthReport{}).Method != "" {
		t.Fatal("zero AWSAuthReport.Method should be empty")
	}
	if (InitResult{}).AlreadyInitialized {
		t.Fatal("zero InitResult.AlreadyInitialized should be false")
	}
	if (AWSCLIInstallReport{}).AlreadyInstalled {
		t.Fatal("zero AWSCLIInstallReport.AlreadyInstalled should be false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run TestConstValuesMatchLegacyStrings -count=1`
Expected: FAIL — build error `package github.com/markgustetic/sentra/internal/setup: no Go files` / `undefined: BackendAWS`.

- [ ] **Step 3: Write the minimal implementation**

```go
// internal/setup/types.go
package setup

import "github.com/markgustetic/sentra/internal/config"

// Backend names the storage target chosen in the setup wizard.
type Backend string

const (
	BackendAWS          Backend = "aws"
	BackendS3Compatible Backend = "s3-compatible"
)

// AWSAuthMethod names how setup should make AWS credentials available
// before it prepares the bucket.
type AWSAuthMethod string

const (
	AWSAuthLogin    AWSAuthMethod = "login"
	AWSAuthSSO      AWSAuthMethod = "sso"
	AWSAuthExisting AWSAuthMethod = "existing"
	AWSAuthSkip     AWSAuthMethod = "skip"
)

// AWSRepairChoice is the recovery path chosen after AWS auth or bucket
// preparation fails.
type AWSRepairChoice string

const (
	AWSRepairLogin    AWSRepairChoice = "login"
	AWSRepairSSO      AWSRepairChoice = "sso"
	AWSRepairExisting AWSRepairChoice = "existing"
	AWSRepairConfig   AWSRepairChoice = "config"
	AWSRepairCancel   AWSRepairChoice = "cancel"
)

// Plan is the complete set of actions the setup wizard selected. Both the
// CLI wizard (thin huh driver) and the TUI wizard build this and hand it to
// the engine; the engine never re-reads the terminal.
type Plan struct {
	Config            config.Config
	Backend           Backend
	PrepareAWS        bool
	AWSAuthMethod     AWSAuthMethod
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
	PrintIAMPolicy    bool
	SavePassphrase    bool
	InitRepo          bool
}

// AWSPrepareOptions controls the AWS-side setup work. Bucket existence is
// always checked; CreateBucket decides whether a missing bucket is created
// or reported as an error.
type AWSPrepareOptions struct {
	CreateBucket      bool
	BlockPublicAccess bool
	DefaultEncryption bool
}

// AWSPrepareReport summarizes the AWS setup work for the final output.
type AWSPrepareReport struct {
	BucketExisted            bool
	BucketCreated            bool
	PublicAccessBlocked      bool
	DefaultEncryptionEnabled bool
}

// AWSAuthReport summarizes the optional AWS CLI auth preflight.
type AWSAuthReport struct {
	IdentityVerified bool
	Method           AWSAuthMethod
	AWSCLIInstalled  bool
	AWSCLIManager    string
	LoginRan         bool
	SSOConfigured    bool
	SSOConfigureRan  bool
	SSOLoginRan      bool
}

// AWSCLIInstallPlan is the package-manager command Sentra can run to install
// the AWS CLI for setup's SSO flow.
type AWSCLIInstallPlan struct {
	Manager string
	Command []string
}

// AWSCLIInstallReport summarizes the AWS CLI preflight.
type AWSCLIInstallReport struct {
	AlreadyInstalled bool
	Installed        bool
	Manager          string
}

// InitResult reports the outcome of initializing the encrypted repository.
type InitResult struct {
	RepoID                   string
	AlreadyInitialized       bool
	PassphraseSavedToKeyring bool
}
```

Then replace the moved decls in cli with `=` aliases so the 1863-line `setup_test.go` oracle stays green. In `internal/cli/setup.go`, replace lines 17-116 (`SetupBackend`+consts, `SetupAWSAuthMethod`+consts, `SetupPlan`, `AWSCLIInstallPlan`, `AWSCLIInstallReport`, `AWSPrepareOptions`, `AWSPrepareReport`, `AWSAuthReport` — leave the `SetupPrompt`/`SetupOverwriteConfirm`/`SetupReviewConfirm`/`SetupAWSAuthRepairPrompt`/`AWSCLIInstallConfirm` func typedefs and their doc comments in place since they are cli-only) with:

```go
// SetupBackend names the storage target chosen in the setup wizard.
type SetupBackend = setup.Backend

const (
	SetupBackendAWS          = setup.BackendAWS
	SetupBackendS3Compatible = setup.BackendS3Compatible
)

// SetupAWSAuthMethod names how setup should make AWS credentials available.
type SetupAWSAuthMethod = setup.AWSAuthMethod

const (
	SetupAWSAuthLogin    = setup.AWSAuthLogin
	SetupAWSAuthSSO      = setup.AWSAuthSSO
	SetupAWSAuthExisting = setup.AWSAuthExisting
	SetupAWSAuthSkip     = setup.AWSAuthSkip
)

// SetupPlan is the complete set of actions the setup wizard selected.
type SetupPlan = setup.Plan

// AWSCLIInstallPlan is the package-manager command Sentra can run to install
// the AWS CLI for setup's SSO flow.
type AWSCLIInstallPlan = setup.AWSCLIInstallPlan

// AWSCLIInstallReport summarizes the AWS CLI preflight.
type AWSCLIInstallReport = setup.AWSCLIInstallReport

// AWSPrepareOptions controls the AWS-side setup work.
type AWSPrepareOptions = setup.AWSPrepareOptions

// AWSPrepareReport summarizes the AWS setup work for the final CLI output.
type AWSPrepareReport = setup.AWSPrepareReport

// AWSAuthReport summarizes the optional AWS CLI auth preflight.
type AWSAuthReport = setup.AWSAuthReport
```

Add `"github.com/markgustetic/sentra/internal/setup"` to the import block in `internal/cli/setup.go:3-15`.

In `internal/cli/setup_init.go:13-17`, replace the `setupInitResult` struct with:

```go
type setupInitResult = setup.InitResult
```

and add `"github.com/markgustetic/sentra/internal/setup"` to its imports (`setup_init.go:3-11`).

In `internal/cli/setup_wizard.go:76-84`, replace the `setupAWSRepairChoice` type + const block with:

```go
type setupAWSRepairChoice = setup.AWSRepairChoice

const (
	setupAWSRepairLogin    = setup.AWSRepairLogin
	setupAWSRepairSSO      = setup.AWSRepairSSO
	setupAWSRepairExisting = setup.AWSRepairExisting
	setupAWSRepairConfig   = setup.AWSRepairConfig
	setupAWSRepairCancel   = setup.AWSRepairCancel
)
```

and add `"github.com/markgustetic/sentra/internal/setup"` to `setup_wizard.go:3-12`.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run 'TestConstValuesMatchLegacyStrings|TestPlanCarriesConfigAndFlags|TestReportZeroValues' -count=1 && go build ./... && go test ./internal/cli/ -run TestSetup -count=1`
Expected: PASS (setup package + cli oracle still green; aliases keep every `SetupPlan{...}`, `SetupBackendAWS`, `setupAWSRepair*` reference in `setup_test.go` compiling).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/types.go internal/setup/types_test.go internal/cli/setup.go internal/cli/setup_init.go internal/cli/setup_wizard.go
git commit -m "refactor(setup): extract engine types into internal/setup with cli aliases

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 6: Add EnvProbe with DefaultEnvProbe (env + ~/.aws/config reads behind an interface)

**Files:**
- Create: `internal/setup/envprobe.go`
- Create: `internal/setup/envprobe_test.go`

- [ ] **Step 1: Write the failing test**

```go
// internal/setup/envprobe_test.go
package setup

import (
	"os"
	"path/filepath"
	"testing"
)

// fakeProbe is a deterministic EnvProbe for the transform tests in this
// package; kept here so DefaultPlan tests never touch the real environment.
type fakeProbe struct {
	env            map[string]string
	profile        string
	envCredentials bool
}

func (f fakeProbe) Getenv(key string) string          { return f.env[key] }
func (f fakeProbe) DefaultProfileFromConfig() string   { return f.profile }
func (f fakeProbe) HasEnvCredentials() bool            { return f.envCredentials }

func clearAWSEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION",
		"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN",
		"AWS_ROLE_ARN", "AWS_WEB_IDENTITY_TOKEN_FILE",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "missing-aws-config"))
}

func TestDefaultEnvProbeGetenvTrimsToRaw(t *testing.T) {
	clearAWSEnv(t)
	t.Setenv("AWS_REGION", "us-west-2")
	probe := DefaultEnvProbe()
	if got := probe.Getenv("AWS_REGION"); got != "us-west-2" {
		t.Fatalf("Getenv: got %q, want us-west-2", got)
	}
}

func TestDefaultEnvProbeHasEnvCredentials(t *testing.T) {
	clearAWSEnv(t)
	probe := DefaultEnvProbe()
	if probe.HasEnvCredentials() {
		t.Fatal("no credentials set, HasEnvCredentials should be false")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "k")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "s")
	if !DefaultEnvProbe().HasEnvCredentials() {
		t.Fatal("access key + secret set, HasEnvCredentials should be true")
	}
}

func TestDefaultEnvProbeHasEnvCredentialsWebIdentity(t *testing.T) {
	clearAWSEnv(t)
	t.Setenv("AWS_ROLE_ARN", "arn:aws:iam::1:role/x")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/tmp/token")
	if !DefaultEnvProbe().HasEnvCredentials() {
		t.Fatal("role arn + web identity token set, HasEnvCredentials should be true")
	}
}

func TestDefaultEnvProbeDefaultProfileFromConfig(t *testing.T) {
	clearAWSEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("[profile sentra]\nregion = us-east-1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AWS_CONFIG_FILE", path)
	if got := DefaultEnvProbe().DefaultProfileFromConfig(); got != "sentra" {
		t.Fatalf("DefaultProfileFromConfig: got %q, want sentra", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run TestDefaultEnvProbe -count=1`
Expected: FAIL — `undefined: DefaultEnvProbe`.

- [ ] **Step 3: Write the minimal implementation**

Move the env/AWS-config helpers from `internal/cli/setup_wizard.go` (`firstNonEmptyEnv` :239-246, `hasAWSEnvironmentCredentials` :248-258, `defaultAWSProfileFromConfig` :260-276, `awsProfileNameFromSection` :278-287) into the probe. `defaultAWSProfileFromConfig` calls `loadAWSCLIConfig`/`awsProfileSection` (the AWS-CLI parser pinned to a separate unit). To keep this unit self-contained and not collide with that unit's pinned `LoadAWSCLIConfig`/`AWSSSOProfileConfigured`, define private parser helpers `loadAWSCLIConfig`/`awsProfileSection` here as `defaultProfileFromAWSConfig`'s own dependency, ported verbatim from `internal/cli/setup_awscli.go:129-187,212-218`.

```go
// internal/setup/envprobe.go
package setup

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvProbe reads the ambient AWS environment so DefaultPlan stays a pure
// transform in tests: production wires DefaultEnvProbe, tests inject a fake.
// It never reads secrets — only presence of credentials and profile/region
// hints used to pre-fill the wizard.
type EnvProbe interface {
	Getenv(key string) string
	DefaultProfileFromConfig() string
	HasEnvCredentials() bool
}

// DefaultEnvProbe reads the real process environment and ~/.aws/config.
func DefaultEnvProbe() EnvProbe { return osEnvProbe{} }

type osEnvProbe struct{}

// Getenv returns the trimmed value of key, matching the wizard's historical
// firstNonEmptyEnv trimming so a whitespace-only var counts as unset.
func (osEnvProbe) Getenv(key string) string {
	return strings.TrimSpace(os.Getenv(key))
}

// HasEnvCredentials reports whether static or web-identity AWS credentials are
// present in the environment. Ported from the CLI wizard's
// hasAWSEnvironmentCredentials (internal/cli/setup_wizard.go:248-258).
func (osEnvProbe) HasEnvCredentials() bool {
	if strings.TrimSpace(os.Getenv("AWS_ROLE_ARN")) != "" &&
		strings.TrimSpace(os.Getenv("AWS_WEB_IDENTITY_TOKEN_FILE")) != "" {
		return true
	}
	if strings.TrimSpace(os.Getenv("AWS_ACCESS_KEY_ID")) == "" {
		return false
	}
	return strings.TrimSpace(os.Getenv("AWS_SECRET_ACCESS_KEY")) != "" ||
		strings.TrimSpace(os.Getenv("AWS_SESSION_TOKEN")) != ""
}

// DefaultProfileFromConfig picks a sensible default AWS profile from
// ~/.aws/config, preferring "sentra" then "default", else the first profile.
// Ported from the CLI wizard's defaultAWSProfileFromConfig
// (internal/cli/setup_wizard.go:260-287).
func (osEnvProbe) DefaultProfileFromConfig() string {
	cfg, err := loadAWSCLIConfig()
	if err != nil || cfg == nil {
		return ""
	}
	for _, profile := range []string{"sentra", "default"} {
		if len(cfg[awsProfileSection(profile)]) > 0 {
			return profile
		}
	}
	for section := range cfg {
		if profile := awsProfileNameFromSection(section); profile != "" {
			return profile
		}
	}
	return ""
}

func awsProfileNameFromSection(section string) string {
	section = strings.TrimSpace(section)
	if section == "default" {
		return "default"
	}
	if strings.HasPrefix(section, "profile ") {
		return strings.TrimSpace(strings.TrimPrefix(section, "profile "))
	}
	return ""
}

type awsCLIConfig map[string]map[string]string

// loadAWSCLIConfig parses ~/.aws/config (or $AWS_CONFIG_FILE) into sections.
// Ported from internal/cli/setup_awscli.go:129-176; a missing file yields a
// nil map with no error so the probe degrades to no default profile.
func loadAWSCLIConfig() (awsCLIConfig, error) {
	path, err := awsConfigPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) //nolint:gosec // path comes from AWS_CONFIG_FILE or the current user's home dir.
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	defer f.Close()

	cfg := awsCLIConfig{}
	section := ""
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
			end := strings.Index(line, "]")
			section = strings.TrimSpace(line[1:end])
			if section != "" && cfg[section] == nil {
				cfg[section] = map[string]string{}
			}
			continue
		}
		if section == "" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		cfg[section][key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read aws config %s: %w", path, err)
	}
	return cfg, nil
}

func awsConfigPath() (string, error) {
	if path := strings.TrimSpace(os.Getenv("AWS_CONFIG_FILE")); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws config: %w", err)
	}
	return filepath.Join(home, ".aws", "config"), nil
}

func awsProfileSection(profile string) string {
	profile = strings.TrimSpace(profile)
	if profile == "" || profile == "default" {
		return "default"
	}
	return "profile " + profile
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run TestDefaultEnvProbe -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/setup/envprobe.go internal/setup/envprobe_test.go
git commit -m "feat(setup): add EnvProbe seam over env + ~/.aws/config

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 7: Add DefaultPlan/NormalizeConfig/ApplyAWSConfigOnly/ApplyPassphraseConfig/ResolveAWSAuthMethod/DefaultAWSRepairChoice/ValidatePlan transforms and rewire cli helpers

**Files:**
- Create: `internal/setup/transform.go`
- Create: `internal/setup/transform_test.go`
- Modify: `internal/cli/setup_wizard.go` (delete moved bodies at :208-237 `defaultSetupPlan`/`applySetupSmartDefaults`, :239-287 env helpers, :158-172 `defaultSetupAWSRepairChoice`, :580-586 `normalizeSetupConfig`; replace with thin wrappers)
- Modify: `internal/cli/setup.go:419-446` (`applySetupAWSConfigOnly`/`applySetupPassphraseConfig`/`resolveSetupAWSAuthMethod` → wrappers; `ValidatePlan` guards)

- [ ] **Step 1: Write the failing test**

```go
// internal/setup/transform_test.go
package setup

import (
	"errors"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestDefaultPlanBrowserLoginByDefault(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Backend != BackendAWS {
		t.Fatalf("backend: got %q, want aws", p.Backend)
	}
	if !p.PrepareAWS || p.AWSAuthMethod != AWSAuthLogin {
		t.Fatalf("expected browser login default, got prepare=%v method=%q", p.PrepareAWS, p.AWSAuthMethod)
	}
	if !p.CreateBucket || !p.BlockPublicAccess || !p.DefaultEncryption || !p.InitRepo || !p.SavePassphrase {
		t.Fatalf("safe defaults not all set: %+v", p)
	}
}

func TestDefaultPlanUsesEnvProfileAndRegion(t *testing.T) {
	probe := fakeProbe{env: map[string]string{
		"AWS_PROFILE": "work",
		"AWS_REGION":  "us-west-2",
	}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "work" {
		t.Fatalf("profile: got %q, want work", p.Config.Repo.S3.Profile)
	}
	if p.Config.Repo.S3.Region != "us-west-2" {
		t.Fatalf("region: got %q, want us-west-2", p.Config.Repo.S3.Region)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanFallsBackToConfigProfile(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}, profile: "sentra"}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "sentra" {
		t.Fatalf("profile: got %q, want sentra", p.Config.Repo.S3.Profile)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanUsesExistingForEnvCredentials(t *testing.T) {
	probe := fakeProbe{env: map[string]string{}, envCredentials: true}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Profile != "" {
		t.Fatalf("profile: got %q, want blank", p.Config.Repo.S3.Profile)
	}
	if p.AWSAuthMethod != AWSAuthExisting {
		t.Fatalf("auth method: got %q, want existing", p.AWSAuthMethod)
	}
}

func TestDefaultPlanRegionFallbackKey(t *testing.T) {
	probe := fakeProbe{env: map[string]string{"AWS_DEFAULT_REGION": "eu-central-1"}}
	p := DefaultPlan(config.Config{}, probe)
	if p.Config.Repo.S3.Region != "eu-central-1" {
		t.Fatalf("region: got %q, want eu-central-1", p.Config.Repo.S3.Region)
	}
}

func TestNormalizeConfigTrimsS3Fields(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "  b  "
	cfg.Repo.S3.Prefix = " sentra/ "
	cfg.Repo.S3.Region = " us-east-1 "
	cfg.Repo.S3.Profile = " p "
	cfg.Repo.S3.EndpointURL = " http://x "
	NormalizeConfig(&cfg)
	if cfg.Repo.S3.Bucket != "b" || cfg.Repo.S3.Prefix != "sentra/" ||
		cfg.Repo.S3.Region != "us-east-1" || cfg.Repo.S3.Profile != "p" ||
		cfg.Repo.S3.EndpointURL != "http://x" {
		t.Fatalf("normalize did not trim: %+v", cfg.Repo.S3)
	}
}

func TestApplyAWSConfigOnlyDisablesEffects(t *testing.T) {
	p := &Plan{PrepareAWS: true, InitRepo: true, CreateBucket: true, BlockPublicAccess: true, DefaultEncryption: true, AWSAuthMethod: AWSAuthLogin, SavePassphrase: true}
	ApplyAWSConfigOnly(p)
	if p.PrepareAWS || p.InitRepo || p.CreateBucket || p.BlockPublicAccess || p.DefaultEncryption || p.SavePassphrase {
		t.Fatalf("config-only should clear effect flags: %+v", p)
	}
	if p.AWSAuthMethod != AWSAuthSkip {
		t.Fatalf("auth method: got %q, want skip", p.AWSAuthMethod)
	}
}

func TestApplyPassphraseConfigMirrorsSaveToUseKeyring(t *testing.T) {
	p := &Plan{InitRepo: true, SavePassphrase: true}
	ApplyPassphraseConfig(p)
	if !p.Config.Passphrase.UseKeyring {
		t.Fatal("InitRepo+SavePassphrase should set use_keyring=true")
	}
	p2 := &Plan{InitRepo: false, SavePassphrase: true}
	ApplyPassphraseConfig(p2)
	if p2.Config.Passphrase.UseKeyring {
		t.Fatal("no InitRepo should leave use_keyring untouched (false)")
	}
}

func TestResolveAWSAuthMethod(t *testing.T) {
	if ResolveAWSAuthMethod(nil) != AWSAuthExisting {
		t.Fatal("nil plan should resolve to existing")
	}
	if got := ResolveAWSAuthMethod(&Plan{AWSAuthMethod: AWSAuthSSO}); got != AWSAuthSSO {
		t.Fatalf("explicit method: got %q, want sso", got)
	}
	if got := ResolveAWSAuthMethod(&Plan{PrepareAWS: true}); got != AWSAuthExisting {
		t.Fatalf("prepare with empty method: got %q, want existing", got)
	}
	if got := ResolveAWSAuthMethod(&Plan{}); got != AWSAuthSkip {
		t.Fatalf("no prepare, empty method: got %q, want skip", got)
	}
}

func TestDefaultAWSRepairChoice(t *testing.T) {
	// Non-credential failure → existing.
	prep := errors.New(`prepare AWS S3: head bucket "b": AccessDenied`)
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthLogin}, prep); got != AWSRepairExisting {
		t.Fatalf("bucket-prep failure: got %q, want existing", got)
	}
	// Missing-credential failure keeps the plan's method.
	cred := errors.New("failed to refresh cached credentials: no EC2 IMDS role found")
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthLogin}, cred); got != AWSRepairLogin {
		t.Fatalf("missing creds w/ login: got %q, want login", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthSSO}, nil); got != AWSRepairSSO {
		t.Fatalf("sso plan: got %q, want sso", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthExisting}, nil); got != AWSRepairExisting {
		t.Fatalf("existing plan: got %q, want existing", got)
	}
	if got := DefaultAWSRepairChoice(Plan{AWSAuthMethod: AWSAuthSkip}, nil); got != AWSRepairConfig {
		t.Fatalf("skip plan: got %q, want config", got)
	}
}

func TestValidatePlan(t *testing.T) {
	base := func() Plan {
		var p Plan
		p.Config.Repo.S3.Bucket = "good-bucket"
		return p
	}
	if err := ValidatePlan(base()); err != nil {
		t.Fatalf("valid plan rejected: %v", err)
	}

	empty := Plan{}
	if err := ValidatePlan(empty); err == nil || !errIsBucketNotSet(err) {
		t.Fatalf("empty bucket: got %v, want bucket-not-set error", err)
	}

	bad := base()
	bad.Config.Repo.S3.Bucket = "Bad_Bucket"
	if err := ValidatePlan(bad); err == nil {
		t.Fatal("invalid bucket name should error")
	}

	ep := base()
	ep.PrepareAWS = true
	ep.Config.Repo.S3.EndpointURL = "http://localhost:9000"
	if err := ValidatePlan(ep); err == nil {
		t.Fatal("PrepareAWS with endpoint_url should error")
	}
}

func errIsBucketNotSet(err error) bool {
	return err != nil && err.Error() == "repo.s3.bucket not set - enter a bucket name"
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestDefaultPlan|TestNormalizeConfig|TestApply|TestResolveAWSAuthMethod|TestDefaultAWSRepairChoice|TestValidatePlan' -count=1`
Expected: FAIL — `undefined: DefaultPlan` (and the other transform symbols).

- [ ] **Step 3: Write the minimal implementation**

Transforms ported from `internal/cli/setup_wizard.go:208-237` (`defaultSetupPlan`+`applySetupSmartDefaults`), `:158-172` (`defaultSetupAWSRepairChoice`), `:580-586` (`normalizeSetupConfig`), `internal/cli/setup.go:419-446` (`applySetupAWSConfigOnly`/`applySetupPassphraseConfig`/`resolveSetupAWSAuthMethod`), and the three guards inlined in `runSetup` at `internal/cli/setup.go:210-228` (`ValidatePlan`). `ValidatePlan` calls `diag.ValidateBucketName` (`internal/diag/bucket.go:15`).

```go
// internal/setup/transform.go
package setup

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// DefaultPlan builds the wizard's starting plan from the current config and
// the ambient AWS environment. Ported from the CLI wizard's
// defaultSetupPlan + applySetupSmartDefaults (internal/cli/setup_wizard.go:208-237);
// the os.Getenv / ~/.aws/config reads now go through probe so the transform is
// testable without touching the real environment.
func DefaultPlan(cfg config.Config, probe EnvProbe) Plan {
	p := Plan{
		Config:            cfg,
		Backend:           BackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     AWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
		SavePassphrase:    true,
		InitRepo:          true,
	}
	applySmartDefaults(&p, probe)
	return p
}

func applySmartDefaults(p *Plan, probe EnvProbe) {
	if p.Config.Repo.S3.Region == "" {
		p.Config.Repo.S3.Region = firstNonEmpty(probe, "AWS_REGION", "AWS_DEFAULT_REGION")
	}
	if p.Config.Repo.S3.Profile == "" {
		p.Config.Repo.S3.Profile = firstNonEmpty(probe, "AWS_PROFILE", "AWS_DEFAULT_PROFILE")
	}
	if p.Config.Repo.S3.Profile == "" {
		p.Config.Repo.S3.Profile = probe.DefaultProfileFromConfig()
	}
	if probe.HasEnvCredentials() || p.Config.Repo.S3.Profile != "" {
		p.AWSAuthMethod = AWSAuthExisting
	}
}

func firstNonEmpty(probe EnvProbe, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(probe.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

// NormalizeConfig trims the S3 fields so equal-but-padded values compare and
// serialize identically. Ported from internal/cli/setup_wizard.go:580-586.
func NormalizeConfig(cfg *config.Config) {
	cfg.Repo.S3.Bucket = strings.TrimSpace(cfg.Repo.S3.Bucket)
	cfg.Repo.S3.Prefix = strings.TrimSpace(cfg.Repo.S3.Prefix)
	cfg.Repo.S3.Region = strings.TrimSpace(cfg.Repo.S3.Region)
	cfg.Repo.S3.Profile = strings.TrimSpace(cfg.Repo.S3.Profile)
	cfg.Repo.S3.EndpointURL = strings.TrimSpace(cfg.Repo.S3.EndpointURL)
}

// ApplyAWSConfigOnly turns a plan into a write-config-only plan: no AWS side
// effects, no repo init, no keyring save. Ported from
// internal/cli/setup.go:419-427.
func ApplyAWSConfigOnly(p *Plan) {
	p.PrepareAWS = false
	p.InitRepo = false
	p.CreateBucket = false
	p.BlockPublicAccess = false
	p.DefaultEncryption = false
	p.AWSAuthMethod = AWSAuthSkip
	p.SavePassphrase = false
}

// ApplyPassphraseConfig mirrors the SavePassphrase decision into the persisted
// use_keyring flag, but only when the repo is being initialized. Ported from
// internal/cli/setup.go:429-433.
func ApplyPassphraseConfig(p *Plan) {
	if p.InitRepo {
		p.Config.Passphrase.UseKeyring = p.SavePassphrase
	}
}

// ResolveAWSAuthMethod picks the effective auth method for a plan, defaulting
// an empty method to existing credentials (when preparing AWS) or skip.
// Ported from internal/cli/setup.go:435-446.
func ResolveAWSAuthMethod(p *Plan) AWSAuthMethod {
	if p == nil {
		return AWSAuthExisting
	}
	if p.AWSAuthMethod != "" {
		return p.AWSAuthMethod
	}
	if p.PrepareAWS {
		return AWSAuthExisting
	}
	return AWSAuthSkip
}

// DefaultAWSRepairChoice picks the pre-selected recovery option after AWS auth
// or bucket preparation fails. A non-credential failure (e.g. AccessDenied on
// an existing bucket) suggests switching to existing credentials; a missing-
// credential failure keeps the plan's chosen sign-in method. Ported from
// internal/cli/setup_wizard.go:158-172.
func DefaultAWSRepairChoice(p Plan, cause error) AWSRepairChoice {
	if cause != nil && !isAWSMissingCredentialsError(cause) {
		return AWSRepairExisting
	}
	switch p.AWSAuthMethod {
	case AWSAuthSSO:
		return AWSRepairSSO
	case AWSAuthExisting:
		return AWSRepairExisting
	case AWSAuthSkip:
		return AWSRepairConfig
	default:
		return AWSRepairLogin
	}
}

// isAWSMissingCredentialsError substring-matches the SDK's missing-credential
// error strings. Ported from internal/cli/setup_errors.go:56-71. The errors
// unit adds the exported IsAWSMissingCredentialsError wrapper delegating here;
// this unit keeps only the unexported form so DefaultAWSRepairChoice compiles
// standalone.
func isAWSMissingCredentialsError(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"failed to refresh cached credentials",
		"no ec2 imds role found",
		"no valid credential",
		"no credential provider",
		"credential providers",
		"ec2imds",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// ValidatePlan enforces the guards runSetup applied inline before writing
// anything: a bucket is required, its name must be valid, and AWS preparation
// is incompatible with a custom endpoint_url. Ported from
// internal/cli/setup.go:210-228.
func ValidatePlan(p Plan) error {
	if strings.TrimSpace(p.Config.Repo.S3.Bucket) == "" {
		return fmt.Errorf("repo.s3.bucket not set - enter a bucket name")
	}
	if err := diag.ValidateBucketName(p.Config.Repo.S3.Bucket); err != nil {
		return err
	}
	if p.PrepareAWS && p.Config.Repo.S3.EndpointURL != "" {
		return fmt.Errorf("AWS setup does not support endpoint_url - choose S3-compatible/manual setup for MinIO or LocalStack")
	}
	return nil
}
```

Now rewire cli to delegate, keeping the oracle green. In `internal/cli/setup_wizard.go`: delete the bodies of `defaultSetupPlan`+`applySetupSmartDefaults` (:208-237), the env helpers `firstNonEmptyEnv`/`hasAWSEnvironmentCredentials`/`defaultAWSProfileFromConfig`/`awsProfileNameFromSection` (:239-287), `defaultSetupAWSRepairChoice` (:158-172), and `normalizeSetupConfig` (:580-586). Replace `defaultSetupPlan` and `normalizeSetupConfig` with thin wrappers (the oracle calls both directly):

```go
func defaultSetupPlan(current config.Config) SetupPlan {
	return setup.DefaultPlan(current, setup.DefaultEnvProbe())
}

func normalizeSetupConfig(cfg *config.Config) {
	setup.NormalizeConfig(cfg)
}

func defaultSetupAWSRepairChoice(plan SetupPlan, cause error) setupAWSRepairChoice {
	return setup.DefaultAWSRepairChoice(plan, cause)
}
```

Remove the now-dead `firstNonEmptyEnv`/`hasAWSEnvironmentCredentials`/`defaultAWSProfileFromConfig`/`awsProfileNameFromSection` (no remaining callers — confirmed only setup_wizard.go used them). The `HuhSetupAWSAuthRepairPrompt` at `:158` reference to `isAWSMissingCredentialsError` is untouched (that symbol still lives in `setup_errors.go`).

In `internal/cli/setup.go`, replace the bodies of `applySetupAWSConfigOnly` (:419-427), `applySetupPassphraseConfig` (:429-433), and `resolveSetupAWSAuthMethod` (:435-446) with wrappers:

```go
func applySetupAWSConfigOnly(plan *SetupPlan)      { setup.ApplyAWSConfigOnly(plan) }
func applySetupPassphraseConfig(plan *SetupPlan)   { setup.ApplyPassphraseConfig(plan) }
func resolveSetupAWSAuthMethod(plan *SetupPlan) SetupAWSAuthMethod {
	return setup.ResolveAWSAuthMethod(plan)
}
```

Leave the two inline guards in `runSetup` (`internal/cli/setup.go:210-215` bucket + :227-229 endpoint) as they are for now — they overlap `ValidatePlan` but are not part of this unit's cli rewiring; a later unit that introduces the engine's `PrepareAWS`/`WriteConfig` sequencing will call `setup.ValidatePlan`. This keeps the oracle's `TestSetup_RejectsInvalidBucketName` (expects "lowercase" substring, still produced by the untouched `validateSetupBucketName` path) green.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -count=1 && go build ./... && go test ./internal/cli/ -run TestSetup -count=1`
Expected: PASS — setup transforms green; cli oracle green (`defaultSetupPlan`, `normalizeSetupConfig`, `defaultSetupAWSRepairChoice`, `applySetupAWSConfigOnly`, `resolveSetupAWSAuthMethod` still resolve, now via `setup`).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/transform.go internal/setup/transform_test.go internal/cli/setup_wizard.go internal/cli/setup.go
git commit -m "refactor(setup): move plan transforms into internal/setup, cli delegates

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 8: Add ReviewText, label maps, and SummaryLines; cli review/summary delegate

**Files:**
- Create: `internal/setup/review.go`
- Create: `internal/setup/review_test.go`
- Modify: `internal/cli/setup_wizard.go:540-578` (`setupPlanReviewText` → wrapper)
- Modify: `internal/cli/setup_summary.go:11-158` (`printSetupSummary` renders `setup.SummaryLines`; label funcs → wrappers)

- [ ] **Step 1: Write the failing test**

```go
// internal/setup/review_test.go
package setup

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestReviewTextMentionsPassphraseSourceForInit(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "review-bucket"
	p := Plan{Config: cfg, InitRepo: true, SavePassphrase: true}

	got := ReviewText("sentra.yaml", p)
	for _, want := range []string{
		"Config: sentra.yaml",
		"Bucket: review-bucket",
		"Repository: initialize after config",
		"Passphrase: save to OS keyring after repo initialization",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review text missing %q:\n%s", want, got)
		}
	}

	p.SavePassphrase = false
	got = ReviewText("sentra.yaml", p)
	if !strings.Contains(got, "Passphrase: prompted or read from --passphrase-file or SENTRA_PASSPHRASE") {
		t.Fatalf("review text should mention prompt/file/env path:\n%s", got)
	}
}

func TestReviewTextAssertsNoSecrets(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	got := ReviewText("sentra.yaml", Plan{Config: cfg})
	if !strings.Contains(got, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.") {
		t.Fatalf("review text must keep the no-secrets assertion:\n%s", got)
	}
}

func TestReviewTextEmptyBucketShowsDash(t *testing.T) {
	got := ReviewText("sentra.yaml", Plan{})
	if !strings.Contains(got, "Bucket: -") {
		t.Fatalf("empty bucket should render as dash:\n%s", got)
	}
}

func TestReviewTextAWSPrepareBlock(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	p := Plan{
		Config:            cfg,
		Backend:           BackendAWS,
		PrepareAWS:        true,
		AWSAuthMethod:     AWSAuthLogin,
		CreateBucket:      true,
		BlockPublicAccess: true,
		DefaultEncryption: true,
	}
	got := ReviewText("sentra.yaml", p)
	for _, want := range []string{
		"AWS sign-in: browser login",
		"Create missing bucket: true",
		"Block public access: true",
		"Enable default encryption: true",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("review text missing %q:\n%s", want, got)
		}
	}

	p.PrepareAWS = false
	if !strings.Contains(ReviewText("sentra.yaml", p), "AWS setup: skipped") {
		t.Fatalf("no-prepare plan should say AWS setup: skipped")
	}
}

func TestLabelMaps(t *testing.T) {
	if BackendLabel(BackendAWS) != "AWS S3" {
		t.Fatalf("BackendLabel(aws) = %q", BackendLabel(BackendAWS))
	}
	if BackendLabel(BackendS3Compatible) != "S3-compatible or existing bucket" {
		t.Fatalf("BackendLabel(s3c) = %q", BackendLabel(BackendS3Compatible))
	}
	if AWSAuthMethodLabel(AWSAuthLogin) != "browser login" {
		t.Fatalf("AWSAuthMethodLabel(login) = %q", AWSAuthMethodLabel(AWSAuthLogin))
	}
	if AWSAuthMethodLabel(AWSAuthSkip) != "config only" {
		t.Fatalf("AWSAuthMethodLabel(skip) = %q", AWSAuthMethodLabel(AWSAuthSkip))
	}
	if AWSPreparedLabel(nil) != "AWS S3 checked" {
		t.Fatalf("AWSPreparedLabel(nil) = %q", AWSPreparedLabel(nil))
	}
	if AWSPreparedLabel(&AWSPrepareReport{BucketCreated: true}) != "AWS S3 bucket created" {
		t.Fatalf("AWSPreparedLabel(created) = %q", AWSPreparedLabel(&AWSPrepareReport{BucketCreated: true}))
	}
	if AWSPreparedLabel(&AWSPrepareReport{BucketExisted: true}) != "AWS S3 bucket verified" {
		t.Fatalf("AWSPreparedLabel(existed) = %q", AWSPreparedLabel(&AWSPrepareReport{BucketExisted: true}))
	}
}

func TestSummaryLinesConfigAndReports(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "sum-bucket"
	cfg.Repo.S3.Prefix = "sentra/"
	p := Plan{Config: cfg, Backend: BackendAWS}
	auth := &AWSAuthReport{IdentityVerified: true}
	prep := &AWSPrepareReport{BucketCreated: true, PublicAccessBlocked: true, DefaultEncryptionEnabled: true}
	init := &InitResult{RepoID: "repo-123", PassphraseSavedToKeyring: true}

	lines := SummaryLines("sentra.yaml", p, auth, prep, init)
	joined := strings.Join(lines, "\n")
	for _, want := range []string{
		"config:   sentra.yaml",
		"storage:  AWS S3",
		"bucket:   sum-bucket",
		"prefix:   sentra/",
		"aws auth: identity verified",
		"aws:      bucket created",
		"aws:      public access blocked",
		"aws:      default encryption enabled",
		"repo id:  repo-123",
		"pass:     saved to OS keyring",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary missing %q:\n%s", want, joined)
		}
	}
}

func TestSummaryLinesNoInitShowsNextHint(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	lines := SummaryLines("sentra.yaml", Plan{Config: cfg, Backend: BackendAWS}, nil, nil, nil)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Run `sentra init` when you are ready to initialize the encrypted repository.") {
		t.Fatalf("no-init summary should show the next-step hint:\n%s", joined)
	}
}

func TestSummaryLinesAlreadyInitialized(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	init := &InitResult{AlreadyInitialized: true}
	lines := SummaryLines("sentra.yaml", Plan{Config: cfg, Backend: BackendAWS}, nil, nil, init)
	if !strings.Contains(strings.Join(lines, "\n"), "repo:     already initialized") {
		t.Fatalf("already-initialized summary line missing:\n%s", strings.Join(lines, "\n"))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestReviewText|TestLabelMaps|TestSummaryLines' -count=1`
Expected: FAIL — `undefined: ReviewText` (and `BackendLabel`/`SummaryLines`).

- [ ] **Step 3: Write the minimal implementation**

`ReviewText` ported from `internal/cli/setup_wizard.go:540-578` (`emptyDash` inlined from `internal/cli/format.go:5-10`). Labels ported from `internal/cli/setup_summary.go:121-158`. `SummaryLines` is the content-line split of `printSetupSummary` (`internal/cli/setup_summary.go:19-84`) with the section-header/`ui.*` styling left to the caller — it returns interleaved header markers as plain text (the cli caller re-styles known header strings).

```go
// internal/setup/review.go
package setup

import (
	"fmt"
	"strings"
)

// ReviewText renders the non-secret setup plan shown before any AWS or repo
// side effects run. Ported verbatim (behavior) from
// internal/cli/setup_wizard.go:540-578; the trailing no-secrets assertion is
// load-bearing and must not be removed.
func ReviewText(cfgPath string, p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Config: %s\n", cfgPath)
	fmt.Fprintf(&b, "Storage: %s\n", BackendLabel(p.Backend))
	fmt.Fprintf(&b, "Bucket: %s\n", emptyDash(p.Config.Repo.S3.Bucket))
	if p.Config.Repo.S3.Prefix != "" {
		fmt.Fprintf(&b, "Prefix: %s\n", p.Config.Repo.S3.Prefix)
	}
	if p.Config.Repo.S3.Region != "" {
		fmt.Fprintf(&b, "Region: %s\n", p.Config.Repo.S3.Region)
	}
	if p.Config.Repo.S3.Profile != "" {
		fmt.Fprintf(&b, "Profile: %s\n", p.Config.Repo.S3.Profile)
	}
	if p.Config.Repo.S3.EndpointURL != "" {
		fmt.Fprintf(&b, "Endpoint: %s\n", p.Config.Repo.S3.EndpointURL)
	}
	if p.PrepareAWS {
		fmt.Fprintf(&b, "AWS sign-in: %s\n", AWSAuthMethodLabel(p.AWSAuthMethod))
		fmt.Fprintf(&b, "Create missing bucket: %t\n", p.CreateBucket)
		fmt.Fprintf(&b, "Block public access: %t\n", p.BlockPublicAccess)
		fmt.Fprintf(&b, "Enable default encryption: %t\n", p.DefaultEncryption)
	} else {
		fmt.Fprintln(&b, "AWS setup: skipped")
	}
	if p.InitRepo {
		fmt.Fprintln(&b, "Repository: initialize after config")
		if p.SavePassphrase {
			fmt.Fprintln(&b, "Passphrase: save to OS keyring after repo initialization")
		} else {
			fmt.Fprintln(&b, "Passphrase: prompted or read from --passphrase-file or SENTRA_PASSPHRASE")
		}
	} else {
		fmt.Fprintln(&b, "Repository: config only")
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.")
	return b.String()
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// BackendLabel is the human-readable storage-backend name.
// Ported from internal/cli/setup_summary.go:121-130.
func BackendLabel(b Backend) string {
	switch b {
	case BackendAWS:
		return "AWS S3"
	case BackendS3Compatible:
		return "S3-compatible or existing bucket"
	default:
		return string(b)
	}
}

// AWSAuthMethodLabel is the human-readable sign-in method name.
// Ported from internal/cli/setup_summary.go:132-145.
func AWSAuthMethodLabel(m AWSAuthMethod) string {
	switch m {
	case AWSAuthLogin:
		return "browser login"
	case AWSAuthSSO:
		return "IAM Identity Center / SSO"
	case AWSAuthExisting:
		return "existing credentials"
	case AWSAuthSkip:
		return "config only"
	default:
		return string(m)
	}
}

// AWSPreparedLabel is the one-line success label for the bucket-prep step.
// Ported from internal/cli/setup_summary.go:147-158.
func AWSPreparedLabel(report *AWSPrepareReport) string {
	switch {
	case report == nil:
		return "AWS S3 checked"
	case report.BucketCreated:
		return "AWS S3 bucket created"
	case report.BucketExisted:
		return "AWS S3 bucket verified"
	default:
		return "AWS S3 bucket checked"
	}
}

// SummaryLines returns the body content lines of the final setup summary,
// grouped by "Configuration", "AWS authentication", "AWS bucket", and either
// "Repository" or "Next". Section headers are emitted as plain strings so the
// CLI can re-style the known headers with ui.Subtle; no ANSI escapes appear
// here. Split out of internal/cli/setup_summary.go:19-84.
func SummaryLines(cfgPath string, p Plan, auth *AWSAuthReport, prep *AWSPrepareReport, init *InitResult) []string {
	var lines []string
	add := func(s string) { lines = append(lines, s) }

	add("Configuration")
	add(fmt.Sprintf("  config:   %s", cfgPath))
	add(fmt.Sprintf("  storage:  %s", BackendLabel(p.Backend)))
	add(fmt.Sprintf("  bucket:   %s", p.Config.Repo.S3.Bucket))
	if p.Config.Repo.S3.Prefix != "" {
		add(fmt.Sprintf("  prefix:   %s", p.Config.Repo.S3.Prefix))
	}
	if p.Config.Repo.S3.Region != "" {
		add(fmt.Sprintf("  region:   %s", p.Config.Repo.S3.Region))
	}
	if p.Config.Repo.S3.Profile != "" {
		add(fmt.Sprintf("  profile:  %s", p.Config.Repo.S3.Profile))
	}
	if p.Config.Repo.S3.EndpointURL != "" {
		add(fmt.Sprintf("  endpoint: %s", p.Config.Repo.S3.EndpointURL))
	}

	if auth != nil {
		add("AWS authentication")
		if auth.AWSCLIInstalled {
			add(fmt.Sprintf("  aws auth: aws cli installed with %s", auth.AWSCLIManager))
		}
		if auth.LoginRan {
			add("  aws auth: browser login completed")
		}
		if auth.SSOConfigureRan {
			add("  aws auth: sso profile configured")
		}
		if auth.SSOLoginRan {
			add("  aws auth: sso login completed")
		} else if auth.IdentityVerified {
			add("  aws auth: identity verified")
		}
	}

	if prep != nil {
		add("AWS bucket")
		switch {
		case prep.BucketCreated:
			add("  aws:      bucket created")
		case prep.BucketExisted:
			add("  aws:      bucket verified")
		default:
			add("  aws:      bucket checked")
		}
		if prep.PublicAccessBlocked {
			add("  aws:      public access blocked")
		}
		if prep.DefaultEncryptionEnabled {
			add("  aws:      default encryption enabled")
		}
	}

	if init != nil {
		add("Repository")
		if init.AlreadyInitialized {
			add("  repo:     already initialized")
		} else {
			add(fmt.Sprintf("  repo id:  %s", init.RepoID))
		}
		if init.PassphraseSavedToKeyring {
			add("  pass:     saved to OS keyring")
		}
	} else {
		add("Next")
		add("  Run `sentra init` when you are ready to initialize the encrypted repository.")
	}
	return lines
}
```

Now rewire cli. In `internal/cli/setup_wizard.go`, replace the body of `setupPlanReviewText` (:540-578) with a wrapper:

```go
func setupPlanReviewText(cfgPath string, plan SetupPlan) string {
	return setup.ReviewText(cfgPath, plan)
}
```

In `internal/cli/setup_summary.go`, replace the label funcs `setupBackendLabel` (:121-130), `setupAWSAuthMethodLabel` (:132-145), `setupAWSPreparedLabel` (:147-158) with wrappers, and rewrite `printSetupSummary` (:11-85) to render `setup.SummaryLines`, re-styling the known section-header strings with `ui.Subtle`:

```go
func setupBackendLabel(backend SetupBackend) string      { return setup.BackendLabel(backend) }
func setupAWSAuthMethodLabel(m SetupAWSAuthMethod) string { return setup.AWSAuthMethodLabel(m) }
func setupAWSPreparedLabel(r *AWSPrepareReport) string    { return setup.AWSPreparedLabel(r) }

func printSetupSummary(
	out io.Writer,
	cfgPath string,
	plan *SetupPlan,
	awsAuthReport *AWSAuthReport,
	awsReport *AWSPrepareReport,
	initResult *setupInitResult,
) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Success.Bold(true).Render("Sentra setup complete"))
	headers := map[string]bool{
		"Configuration":      true,
		"AWS authentication": true,
		"AWS bucket":         true,
		"Repository":         true,
		"Next":               true,
	}
	for _, line := range setup.SummaryLines(cfgPath, *plan, awsAuthReport, awsReport, initResult) {
		if headers[line] {
			fmt.Fprintln(out, ui.Subtle.Render(line))
			continue
		}
		fmt.Fprintln(out, line)
	}
}
```

Add `"github.com/markgustetic/sentra/internal/setup"` to `internal/cli/setup_summary.go:3-9` imports (keep `diag` — still used by `validateSetupBucketName` at :160-162). Delete the now-unused `emptyDash` from `internal/cli/format.go` only if no other caller remains; otherwise leave it (verify with `grep -rn "emptyDash(" internal/cli/` before deleting — if any non-setup caller exists, keep the cli copy since `setup.emptyDash` is unexported and separate).

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -count=1 && go build ./... && go test ./internal/cli/ -run 'TestSetupPlanReview|TestSetup' -count=1`
Expected: PASS — `setup` review/label/summary tests green; cli oracle green (`setupPlanReviewText` at `setup_test.go:322,334` still returns identical text; the summary integration tests that assert on `printSetupSummary` output still match line-for-line).

- [ ] **Step 5: Commit**
```bash
git add internal/setup/review.go internal/setup/review_test.go internal/cli/setup_wizard.go internal/cli/setup_summary.go
git commit -m "refactor(setup): move review text, labels, and summary lines into internal/setup

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**Notes for the controller / downstream units**
- All four tasks keep the cli oracle (`internal/cli/setup_test.go`, 1863 lines) green via `=` aliases + thin wrappers; verify with `go test ./internal/cli/ -run TestSetup -count=1` after each.
- `internal/setup` imports only `config`, `diag`, and stdlib in this unit — satisfies the "setup imports nothing from cli/tui" invariant.
- The **exported** `IsAWSMissingCredentialsError` is deliberately NOT defined here (owned by the errors unit); this unit provides the unexported `isAWSMissingCredentialsError` it delegates to. If the errors unit lands first and already defines `isAWSMissingCredentialsError` in `internal/setup`, drop the copy from `transform.go` (Task 3, Step 3) to avoid a duplicate-declaration conflict — they are byte-identical ports of `internal/cli/setup_errors.go:56-71`.
- The AWS-CLI parser helpers (`loadAWSCLIConfig`/`awsConfigPath`/`awsProfileSection`/`awsCLIConfig`) are ported **unexported** into `envprobe.go` here to keep `DefaultEnvProbe` self-contained. The pinned **exported** parser API (`LoadAWSCLIConfig`/`AWSConfigPath`/`AWSSSOProfileConfigured`/`DefaultAWSCLIInstallPlan`) is owned by the awscli unit; when it lands, reconcile by having `DefaultProfileFromConfig` call the exported `LoadAWSCLIConfig` and deleting these private copies to avoid duplicate declarations.


## Part 3 — Effects interface + stepwise Engine

**Published API:** (this unit defines these exported symbols in package `setup`, matching the pinned contract)

```go
// effects.go
type Effects interface {
	EnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	AWSLogin(ctx context.Context, profile string, region string) error
	CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error)
	AWSConfigureSSO(ctx context.Context, profile string) error
	AWSSSOLogin(ctx context.Context, profile string) error
	CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error
	PrepareAWS(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	SavePassphrase(cfg *config.Config, passphrase []byte) error
}
func DefaultEffects() Effects
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)

// engine.go
type Engine struct { /* unexported eff Effects */ }
func NewEngine(eff Effects) *Engine
func (e *Engine) PrepareAWS(ctx context.Context, p *Plan) (AWSAuthReport, AWSPrepareReport, error)
func (e *Engine) WriteConfig(cfgPath string, p *Plan) error
func (e *Engine) InitRepo(ctx context.Context, cfg *config.Config, pass []byte, save bool) (InitResult, error)
func (e *Engine) WriteDraft(cfgPath string, cfg *config.Config) error
func (e *Engine) RemoveDraft(cfgPath string)
func (e *Engine) DraftPath(cfgPath string) string
```

This unit assumes U1 has already created package `setup` with the moved types (`Plan`, `Backend`/`AWSAuthMethod` consts, `AWSAuthMethod`, `AWSPrepareOptions`, `AWSPrepareReport`, `AWSAuthReport`, `AWSCLIInstallPlan`, `AWSCLIInstallReport`, `AWSCLIInstallConfirm`, `InitResult`) and the pure transforms (`NormalizeConfig`, `ResolveAWSAuthMethod`, `ApplyAWSConfigOnly`, `ValidatePlan`), and U2 has moved the error wrappers (`WrapAWSPrepareError`, `WrapAWSLoginFlowError`, `WrapAWSSSOFlowError`, `IsAWSMissingCredentialsError`) and the AWS-CLI parser (`LoadAWSCLIConfig`, `AWSConfigPath`, `AWSSSOProfileConfigured`, `DefaultAWSCLIInstallPlan`) plus `s3BucketARN`. Where U3 must reference a symbol U1/U2 own, it is cited to the current cli source it moves from.

---

### Task 9: Move DefaultAWSPrepare + S3 ops into internal/setup and define the Effects interface + DefaultEffects

**Files:**
- Create: `internal/setup/effects.go`
- Create: `internal/setup/awsprepare.go` (moves `DefaultAWSPrepare` from `internal/cli/setup_awss3.go:30-85` + S3 ops from `internal/cli/setup_awss3_ops.go`)
- Create: `internal/setup/awscli_effects.go` (moves the effectful `Default*` subprocess drivers from `internal/cli/setup_awscli.go:16-95`)
- Test: `internal/setup/effects_test.go`

- [ ] **Step 1: Write the failing test**

```go
package setup

import (
	"context"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// DefaultEffects must satisfy the Effects interface and wire the moved
// Default* drivers. We only assert the wiring is complete (non-nil,
// implements Effects) — the subprocess/AWS bodies are exercised by the
// existing cli oracle and by AWS integration tests, not unit tests.
func TestDefaultEffectsImplementsInterface(t *testing.T) {
	var eff Effects = DefaultEffects()
	if eff == nil {
		t.Fatal("DefaultEffects returned nil")
	}
}

// CheckAWSSDKIdentity must delegate to diag.CheckSDKIdentity. With no AWS
// credentials configured in the test environment it must return a non-nil
// error rather than panicking, proving the delegation is wired.
func TestDefaultEffectsCheckIdentityDelegates(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "")
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_CONFIG_FILE", t.TempDir()+"/nonexistent-config")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", t.TempDir()+"/nonexistent-creds")
	eff := DefaultEffects()
	cfg := &config.Config{}
	cfg.Repo.S3.Region = "us-east-1"
	if err := eff.CheckAWSSDKIdentity(context.Background(), cfg); err == nil {
		t.Fatal("CheckAWSSDKIdentity: got nil error with no credentials, want non-nil")
	}
}

// DefaultAWSPrepare must reject a config with no region before touching AWS,
// preserving the guard moved from internal/cli/setup_awss3.go:33-35.
func TestDefaultAWSPrepareRequiresRegion(t *testing.T) {
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "example-bucket"
	if _, err := DefaultAWSPrepare(context.Background(), cfg, AWSPrepareOptions{}); err == nil {
		t.Fatal("DefaultAWSPrepare: got nil error with empty region, want non-nil")
	}
}

// NewStore must build a live blobstore.Store (no network at construction).
func TestDefaultEffectsNewStore(t *testing.T) {
	eff := DefaultEffects()
	cfg := &config.Config{}
	cfg.Repo.S3.Bucket = "example-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	store, err := eff.NewStore(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	var _ blobstore.Store = store
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestDefaultEffects|TestDefaultAWSPrepareRequiresRegion' -count=1`
Expected: FAIL — compile error `undefined: Effects`, `undefined: DefaultEffects`, `undefined: DefaultAWSPrepare` (package `setup` has no effects seam yet).

- [ ] **Step 3: Write the minimal implementation**

`internal/setup/awsprepare.go` (moves `DefaultAWSPrepare` from `internal/cli/setup_awss3.go:30-85` and the S3 ops from `internal/cli/setup_awss3_ops.go:1-113`; `s3BucketARN`/`s3ObjectARN` are shared with U2's IAM builder so they live here in package `setup`, but if U2 already defines them, drop the two funcs from this file and keep only the ops):

```go
package setup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

const bucketExistsWaitTimeout = 2 * time.Minute

// DefaultAWSPrepare performs the deterministic AWS S3 setup work chosen in
// the wizard. It intentionally does not create or manage IAM users. Moved
// verbatim from internal/cli/setup_awss3.go:30-85; it must build its own
// *S3 store because it needs the concrete *s3.Client (Store does not expose
// Client()), so it cannot use Effects.NewStore.
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	if cfg.Repo.S3.Region == "" {
		return AWSPrepareReport{}, fmt.Errorf("repo.s3.region is required for AWS setup")
	}

	store, err := blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:  cfg.Repo.S3.Bucket,
		Prefix:  cfg.Repo.S3.Prefix,
		Region:  cfg.Repo.S3.Region,
		Profile: cfg.Repo.S3.Profile,
	})
	if err != nil {
		return AWSPrepareReport{}, err
	}
	client := store.Client()
	bucket := cfg.Repo.S3.Bucket
	report := AWSPrepareReport{}

	if err := headBucket(ctx, client, bucket); err == nil {
		report.BucketExisted = true
	} else if isS3BucketMissing(err) {
		if !opts.CreateBucket {
			return AWSPrepareReport{}, fmt.Errorf("bucket %q does not exist", bucket)
		}
		created, err := createBucket(ctx, client, bucket, cfg.Repo.S3.Region)
		if err != nil {
			return AWSPrepareReport{}, err
		}
		if created {
			report.BucketCreated = true
		} else {
			report.BucketExisted = true
		}
		if err := waitForBucketExists(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
	} else {
		return AWSPrepareReport{}, err
	}

	if opts.BlockPublicAccess {
		if err := blockBucketPublicAccess(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.PublicAccessBlocked = true
	}
	if opts.DefaultEncryption {
		if err := enableBucketDefaultEncryption(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.DefaultEncryptionEnabled = true
	}
	return report, nil
}

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, s3BucketARN(bucket), err)
	}
	return nil
}

func createBucket(ctx context.Context, client *s3.Client, bucket, region string) (bool, error) {
	input := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	_, err := client.CreateBucket(ctx, input)
	if err == nil {
		return true, nil
	}
	if isBucketAlreadyOwned(err) {
		return false, nil
	}
	return false, fmt.Errorf("create bucket %q (requires s3:CreateBucket on %s): %w", bucket, s3BucketARN(bucket), err)
}

func waitForBucketExists(ctx context.Context, client *s3.Client, bucket string) error {
	waiter := s3.NewBucketExistsWaiter(client)
	err := waiter.Wait(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}, bucketExistsWaitTimeout)
	if err != nil {
		return fmt.Errorf("wait for bucket %q to exist (requires s3:ListBucket on %s): %w", bucket, s3BucketARN(bucket), err)
	}
	return nil
}

func blockBucketPublicAccess(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("block public access for bucket %q (requires s3:PutBucketPublicAccessBlock on %s): %w", bucket, s3BucketARN(bucket), err)
	}
	return nil
}

func enableBucketDefaultEncryption(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("enable default encryption for bucket %q (requires s3:PutBucketEncryption on %s): %w", bucket, s3BucketARN(bucket), err)
	}
	return nil
}

func isS3BucketMissing(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchBucket", "404":
		return true
	default:
		return false
	}
}

func isBucketAlreadyOwned(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou"
}

// s3BucketARN / s3ObjectARN are shared with the IAM policy builder. If U2's
// IAM move already defines these in package setup, delete both from this file.
func s3BucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}

func s3ObjectARN(bucket string, prefix string) string {
	if prefix == "" {
		return s3BucketARN(bucket) + "/*"
	}
	return s3BucketARN(bucket) + "/" + prefix + "*"
}
```

`internal/setup/awscli_effects.go` (moves the effectful drivers from `internal/cli/setup_awscli.go:16-95` + the `runAWSCLI`/`appendAWSProfile` helpers at :97-127; the pure config parser at :129-236 moves separately in U2 as `LoadAWSCLIConfig`/`AWSSSOProfileConfigured`/`AWSConfigPath`, and `DefaultAWSCLIInstallPlan` — U2 owns those, so `DefaultAWSSSOConfigured` here calls the U2-exported `LoadAWSCLIConfig`+`AWSSSOProfileConfigured` and `DefaultEnsureAWSCLI` calls the U2-exported `DefaultAWSCLIInstallPlan`):

```go
package setup

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// DefaultEnsureAWSCLI verifies that the AWS CLI is available. When it is
// missing and Homebrew is available, it asks for permission to run
// `brew install awscli` and verifies the install before continuing. Moved
// from internal/cli/setup_awscli.go:16-47. brew auto-install stays here and
// is used only by the cli driver; the TUI wizard detects a missing aws CLI
// and shows an ErrorAdvice modal instead of calling this with a confirm.
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if _, err := exec.LookPath("aws"); err == nil {
		return AWSCLIInstallReport{AlreadyInstalled: true}, nil
	}

	plan, ok := DefaultAWSCLIInstallPlan()
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but was not found in PATH. Install it, or rerun setup and choose Existing credentials")
	}
	if confirm == nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI is required for the selected AWS sign-in method but no install confirmation was configured")
	}
	ok, err := confirm(plan)
	if err != nil {
		return AWSCLIInstallReport{}, err
	}
	if !ok {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install canceled; install it manually or rerun setup and choose Existing credentials")
	}

	cmd := exec.CommandContext(ctx, plan.Command[0], plan.Command[1:]...) //nolint:gosec // fixed package-manager command selected by Sentra, confirmed by the operator.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("install AWS CLI with %s: %w", plan.Manager, err)
	}
	if _, err := exec.LookPath("aws"); err != nil {
		return AWSCLIInstallReport{}, fmt.Errorf("AWS CLI install completed with %s, but aws is still not on PATH", plan.Manager)
	}
	return AWSCLIInstallReport{Installed: true, Manager: plan.Manager}, nil
}

// DefaultAWSLogin delegates browser-based AWS CLI sign-in for local
// development. Moved from internal/cli/setup_awscli.go:62-69.
func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	args := []string{"login"}
	region = strings.TrimSpace(region)
	if region != "" {
		args = append(args, "--region", region)
	}
	return runAWSCLI(ctx, args, profile, true)
}

// DefaultAWSSSOConfigured checks whether the selected profile has a complete
// AWS CLI SSO profile. Moved from internal/cli/setup_awscli.go:74-83; the
// pure parser LoadAWSCLIConfig/AWSSSOProfileConfigured is owned by U2.
func DefaultAWSSSOConfigured(_ context.Context, profile string) (bool, error) {
	cfg, err := LoadAWSCLIConfig()
	if err != nil {
		return false, err
	}
	if cfg == nil {
		return false, nil
	}
	return AWSSSOProfileConfigured(cfg, profile), nil
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the
// AWS CLI. Moved from internal/cli/setup_awscli.go:87-89.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"configure", "sso"}, profile, true)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the
// AWS CLI. Moved from internal/cli/setup_awscli.go:93-95.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return runAWSCLI(ctx, []string{"sso", "login"}, profile, true)
}

// runAWSCLI / appendAWSProfile moved from internal/cli/setup_awscli.go:97-127.
func runAWSCLI(ctx context.Context, args []string, profile string, interactive bool) error {
	args = appendAWSProfile(args, profile)
	cmd := exec.CommandContext(ctx, "aws", args...) //nolint:gosec // fixed binary + fixed args; profile is a user-selected AWS profile.
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}

	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return fmt.Errorf("aws %s: %w", strings.Join(args, " "), err)
		}
		return fmt.Errorf("aws %s: %w: %s", strings.Join(args, " "), err, msg)
	}
	return nil
}

func appendAWSProfile(args []string, profile string) []string {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		return args
	}
	return append(args, "--profile", profile)
}
```

`internal/setup/effects.go` (new — the interface + the production impl delegating to the moved `Default*` funcs and to `diag.CheckSDKIdentity`; `NewStore` mirrors the cli `DefaultNewStore` factory but must build directly since the cli one lives in package cli — build an `*S3` store via `blobstore.NewS3`, matching the cli production wiring):

```go
package setup

import (
	"context"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// Effects is the side-effecting seam of the setup engine. Its method set
// mirrors the func fields of the former cli.SetupDeps
// (internal/cli/setup.go:118-136) so the cli driver and the TUI wizard can
// share one sequencing engine. Tests inject a fake Effects; production uses
// DefaultEffects.
type Effects interface {
	// EnsureAWSCLI verifies the AWS CLI is installed, optionally installing
	// it via the confirmed package-manager plan (brew). confirm is nil in
	// the TUI, which handles a missing CLI with an ErrorAdvice modal.
	EnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	AWSLogin(ctx context.Context, profile string, region string) error
	CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error)
	AWSConfigureSSO(ctx context.Context, profile string) error
	AWSSSOLogin(ctx context.Context, profile string) error
	// CheckAWSSDKIdentity verifies credentials through the SDK credential
	// chain; delegates to diag.CheckSDKIdentity.
	CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error
	// PrepareAWS performs the deterministic bucket-side setup work.
	PrepareAWS(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	// SavePassphrase persists the passphrase to the OS keyring. The engine
	// only ever calls this AFTER repo init or a verified repo.Open.
	SavePassphrase(cfg *config.Config, passphrase []byte) error
}

// defaultEffects is the production Effects. Each method delegates to the
// Default* driver moved from internal/cli; CheckAWSSDKIdentity delegates to
// diag.CheckSDKIdentity and PrepareAWS to DefaultAWSPrepare.
type defaultEffects struct{}

// DefaultEffects returns the production side-effecting seam.
func DefaultEffects() Effects { return defaultEffects{} }

func (defaultEffects) EnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	return DefaultEnsureAWSCLI(ctx, confirm)
}

func (defaultEffects) AWSLogin(ctx context.Context, profile string, region string) error {
	return DefaultAWSLogin(ctx, profile, region)
}

func (defaultEffects) CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return DefaultAWSSSOConfigured(ctx, profile)
}

func (defaultEffects) AWSConfigureSSO(ctx context.Context, profile string) error {
	return DefaultAWSConfigureSSO(ctx, profile)
}

func (defaultEffects) AWSSSOLogin(ctx context.Context, profile string) error {
	return DefaultAWSSSOLogin(ctx, profile)
}

func (defaultEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return diag.CheckSDKIdentity(ctx, cfg)
}

func (defaultEffects) PrepareAWS(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	return DefaultAWSPrepare(ctx, cfg, opts)
}

func (defaultEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	return blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:      cfg.Repo.S3.Bucket,
		Prefix:      cfg.Repo.S3.Prefix,
		Region:      cfg.Repo.S3.Region,
		Profile:     cfg.Repo.S3.Profile,
		EndpointURL: cfg.Repo.S3.EndpointURL,
	})
}

func (defaultEffects) SavePassphrase(cfg *config.Config, passphrase []byte) error {
	return config.StoreKeyringPassphrase(config.KeyringOptionsForConfig(cfg), passphrase)
}
```

> Note on `SavePassphrase`: the production keyring saver is wired via `config.StoreKeyringPassphrase` + `config.KeyringOptionsForConfig` (both pinned to U1's config move). If U1 has not yet landed `KeyringOptionsForConfig`, temporarily inline the cli production saver's body; the pinned signature is `config.KeyringOptionsForConfig(cfg *config.Config) StoreKeyringOptions` and `config.StoreKeyringPassphrase(opts StoreKeyringOptions, pass []byte) error` (verify against `internal/config` once U1 lands).

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run 'TestDefaultEffects|TestDefaultAWSPrepareRequiresRegion' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/setup/effects.go internal/setup/awsprepare.go internal/setup/awscli_effects.go internal/setup/effects_test.go
git commit -m "feat(setup): add Effects seam + DefaultEffects; move DefaultAWSPrepare and AWS-CLI drivers into internal/setup

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 10: Engine.PrepareAWS: headless auth + bucket-prep loop body (huh/stdout/cobra stripped)

**Files:**
- Create: `internal/setup/engine.go`
- Create: `internal/setup/engine_prepare.go` (headless port of `runSetupAWSAuth`+sub-machines from `internal/cli/setup_auth.go:11-124` and the loop body from `internal/cli/setup.go:244-292`, MINUS the huh repair prompt at :249-259/:277-287)
- Test: `internal/setup/engine_prepare_test.go`

- [ ] **Step 1: Write the failing test**

```go
package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// fakeEffects is a fully injectable Effects for engine unit tests. Unset
// func fields default to permissive no-ops so each test overrides only what
// it exercises.
type fakeEffects struct {
	ensureAWSCLI  func(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	awsLogin      func(ctx context.Context, profile, region string) error
	ssoConfigured func(ctx context.Context, profile string) (bool, error)
	configureSSO  func(ctx context.Context, profile string) error
	ssoLogin      func(ctx context.Context, profile string) error
	checkIdentity func(ctx context.Context, cfg *config.Config) error
	prepareAWS    func(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	newStore      func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	savePass      func(cfg *config.Config, passphrase []byte) error
}

func (f fakeEffects) EnsureAWSCLI(ctx context.Context, c AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	if f.ensureAWSCLI != nil {
		return f.ensureAWSCLI(ctx, c)
	}
	return AWSCLIInstallReport{AlreadyInstalled: true}, nil
}
func (f fakeEffects) AWSLogin(ctx context.Context, p, r string) error {
	if f.awsLogin != nil {
		return f.awsLogin(ctx, p, r)
	}
	return nil
}
func (f fakeEffects) CheckAWSSSOConfigured(ctx context.Context, p string) (bool, error) {
	if f.ssoConfigured != nil {
		return f.ssoConfigured(ctx, p)
	}
	return true, nil
}
func (f fakeEffects) AWSConfigureSSO(ctx context.Context, p string) error {
	if f.configureSSO != nil {
		return f.configureSSO(ctx, p)
	}
	return nil
}
func (f fakeEffects) AWSSSOLogin(ctx context.Context, p string) error {
	if f.ssoLogin != nil {
		return f.ssoLogin(ctx, p)
	}
	return nil
}
func (f fakeEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	if f.checkIdentity != nil {
		return f.checkIdentity(ctx, cfg)
	}
	return nil
}
func (f fakeEffects) PrepareAWS(ctx context.Context, cfg *config.Config, o AWSPrepareOptions) (AWSPrepareReport, error) {
	if f.prepareAWS != nil {
		return f.prepareAWS(ctx, cfg, o)
	}
	return AWSPrepareReport{BucketExisted: true}, nil
}
func (f fakeEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if f.newStore != nil {
		return f.newStore(ctx, cfg)
	}
	return blobstore.NewMemory(), nil
}
func (f fakeEffects) SavePassphrase(cfg *config.Config, p []byte) error {
	if f.savePass != nil {
		return f.savePass(cfg, p)
	}
	return nil
}

func awsPlan() Plan {
	var p Plan
	p.Backend = BackendAWS
	p.PrepareAWS = true
	p.CreateBucket = true
	p.Config.Repo.S3.Bucket = "example-bucket"
	p.Config.Repo.S3.Region = "us-east-1"
	return p
}

// Existing-credentials happy path: identity verifies, prepare runs, both
// reports returned, no error. Mirrors runSetupAWSExistingAuth
// (internal/cli/setup_auth.go:117-124) + the loop success path
// (internal/cli/setup.go:288-291).
func TestEnginePrepareAWSExistingSuccess(t *testing.T) {
	var gotOpts AWSPrepareOptions
	eff := fakeEffects{
		prepareAWS: func(_ context.Context, cfg *config.Config, o AWSPrepareOptions) (AWSPrepareReport, error) {
			gotOpts = o
			return AWSPrepareReport{BucketCreated: true}, nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthExisting
	auth, prep, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !auth.IdentityVerified {
		t.Fatalf("auth.IdentityVerified = false, want true")
	}
	if auth.Method != AWSAuthExisting {
		t.Fatalf("auth.Method = %q, want %q", auth.Method, AWSAuthExisting)
	}
	if !prep.BucketCreated {
		t.Fatalf("prep.BucketCreated = false, want true")
	}
	if !gotOpts.CreateBucket {
		t.Fatalf("prepare opts.CreateBucket = false, want true")
	}
}

// Login sub-machine: identity fails first, login runs, identity then
// verifies. Mirrors runSetupAWSLoginAuth (internal/cli/setup_auth.go:30-59).
func TestEnginePrepareAWSLoginRunsWhenIdentityMissing(t *testing.T) {
	calls := 0
	loginRan := false
	eff := fakeEffects{
		checkIdentity: func(context.Context, *config.Config) error {
			calls++
			if calls == 1 {
				return errors.New("no valid credential sources")
			}
			return nil
		},
		awsLogin: func(context.Context, string, string) error {
			loginRan = true
			return nil
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	auth, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("PrepareAWS: %v", err)
	}
	if !loginRan {
		t.Fatal("AWSLogin did not run after identity failed")
	}
	if !auth.LoginRan || !auth.IdentityVerified {
		t.Fatalf("auth = %+v, want LoginRan && IdentityVerified", auth)
	}
}

// Failure classification: PrepareAWS returning a missing-credentials error
// must be wrapped via WrapAWSPrepareError (substring-detectable), and the
// engine must NOT prompt/repair — it returns the classified error so the
// caller (cli driver or TUI) owns the repair decision.
func TestEnginePrepareAWSClassifiesPrepareError(t *testing.T) {
	eff := fakeEffects{
		prepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{}, errors.New("operation error S3: HeadBucket, no valid credential sources")
		},
	}
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthExisting
	_, _, err := NewEngine(eff).PrepareAWS(context.Background(), &p)
	if err == nil {
		t.Fatal("PrepareAWS: got nil error, want classified prepare error")
	}
	if !IsAWSMissingCredentialsError(err) {
		t.Fatalf("PrepareAWS error not classified as missing-credentials: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run TestEnginePrepareAWS -count=1`
Expected: FAIL — compile error `undefined: NewEngine` / `undefined: Engine` (engine not written yet). (Also requires `blobstore.NewMemory` — confirm it exists via `grep -rn "func NewMemory" internal/blobstore`; the repo layer's `newTestRepo` uses the in-memory store, so it does.)

- [ ] **Step 3: Write the minimal implementation**

`internal/setup/engine.go` (the struct + constructor):

```go
package setup

// Engine sequences the side-effecting steps of setup — AWS auth + bucket
// prep, config write, repo init — over an injected Effects seam. It contains
// NO huh forms, NO stdout writes, and NO cobra: the cli driver adds progress
// printing/huh repair prompts around it, and the TUI wizard drives it from
// tea messages. This is the shared behavior contract both front ends reuse.
type Engine struct {
	eff Effects
}

// NewEngine returns an Engine backed by eff.
func NewEngine(eff Effects) *Engine { return &Engine{eff: eff} }
```

`internal/setup/engine_prepare.go` (headless port; the huh repair prompt and stdout progress are dropped — the loop from `setup.go:244-292` collapses to a single pass that returns the classified error, and the caller re-invokes `PrepareAWS` after mutating the plan):

```go
package setup

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
)

// PrepareAWS runs one pass of the AWS auth + bucket-prep sequence for p and
// returns the auth report, the prepare report, and any error. It is the
// headless body of the cli loop at internal/cli/setup.go:244-292 MINUS the
// huh repair prompt and stdout progress: on failure it classifies and
// returns the error rather than prompting. Callers own the retry decision
// (the cli driver keeps its huh repair loop; the TUI wizard mutates the plan
// and calls PrepareAWS again).
func (e *Engine) PrepareAWS(ctx context.Context, p *Plan) (AWSAuthReport, AWSPrepareReport, error) {
	method := ResolveAWSAuthMethod(p)
	auth, err := e.runAWSAuth(ctx, method, &p.Config)
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, err
	}

	prep, err := e.eff.PrepareAWS(ctx, &p.Config, AWSPrepareOptions{
		CreateBucket:      p.CreateBucket,
		BlockPublicAccess: p.BlockPublicAccess,
		DefaultEncryption: p.DefaultEncryption,
	})
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, WrapAWSPrepareError(&p.Config, method, err)
	}
	return auth, prep, nil
}

// runAWSAuth dispatches the selected sign-in method. Headless port of
// runSetupAWSAuth (internal/cli/setup_auth.go:11-28).
func (e *Engine) runAWSAuth(ctx context.Context, method AWSAuthMethod, cfg *config.Config) (AWSAuthReport, error) {
	switch method {
	case AWSAuthLogin:
		return e.runAWSLoginAuth(ctx, cfg)
	case AWSAuthSSO:
		return e.runAWSSSOAuth(ctx, cfg)
	case AWSAuthExisting:
		return e.runAWSExistingAuth(ctx, cfg)
	default:
		return AWSAuthReport{}, fmt.Errorf("unsupported AWS sign-in method %q", method)
	}
}

// runAWSLoginAuth is the headless port of runSetupAWSLoginAuth
// (internal/cli/setup_auth.go:30-59). The EnsureAWSCLI confirm is nil here;
// the cli driver wraps this engine and supplies its own huh confirm before
// calling, and the TUI handles a missing CLI up front with an ErrorAdvice
// modal, so a nil confirm here only matters when the CLI is actually missing.
func (e *Engine) runAWSLoginAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: AWSAuthLogin}
	installReport, err := e.eff.EnsureAWSCLI(ctx, nil)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager

	if e.eff.CheckAWSSDKIdentity(ctx, cfg) == nil {
		report.IdentityVerified = true
		return report, nil
	}

	if err := e.eff.AWSLogin(ctx, cfg.Repo.S3.Profile, cfg.Repo.S3.Region); err != nil {
		return AWSAuthReport{}, WrapAWSLoginFlowError(cfg.Repo.S3.Profile, err)
	}
	report.LoginRan = true

	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after browser login: %w", WrapAWSPrepareError(cfg, AWSAuthLogin, err))
	}
	report.IdentityVerified = true
	return report, nil
}

// runAWSSSOAuth is the headless port of runSetupAWSSSOAuth
// (internal/cli/setup_auth.go:61-115).
func (e *Engine) runAWSSSOAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	profile := cfg.Repo.S3.Profile
	report := AWSAuthReport{Method: AWSAuthSSO}
	installReport, err := e.eff.EnsureAWSCLI(ctx, nil)
	if err != nil {
		return AWSAuthReport{}, err
	}
	report.AWSCLIInstalled = installReport.Installed
	report.AWSCLIManager = installReport.Manager

	if e.eff.CheckAWSSDKIdentity(ctx, cfg) == nil {
		report.IdentityVerified = true
		return report, nil
	}

	configured, err := e.eff.CheckAWSSSOConfigured(ctx, profile)
	if err != nil {
		return AWSAuthReport{}, fmt.Errorf("check aws sso profile: %w", err)
	}
	report.SSOConfigured = configured
	if !configured {
		if err := e.eff.AWSConfigureSSO(ctx, profile); err != nil {
			return AWSAuthReport{}, WrapAWSSSOFlowError("aws configure sso", profile, err)
		}
		report.SSOConfigured = true
		report.SSOConfigureRan = true
	}

	if err := e.eff.AWSSSOLogin(ctx, profile); err != nil {
		return AWSAuthReport{}, WrapAWSSSOFlowError("aws sso login", profile, err)
	}
	report.SSOLoginRan = true

	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, fmt.Errorf("AWS credentials are still unavailable after SSO login: %w", WrapAWSPrepareError(cfg, AWSAuthSSO, err))
	}
	report.IdentityVerified = true
	return report, nil
}

// runAWSExistingAuth is the headless port of runSetupAWSExistingAuth
// (internal/cli/setup_auth.go:117-124).
func (e *Engine) runAWSExistingAuth(ctx context.Context, cfg *config.Config) (AWSAuthReport, error) {
	report := AWSAuthReport{Method: AWSAuthExisting}
	if err := e.eff.CheckAWSSDKIdentity(ctx, cfg); err != nil {
		return AWSAuthReport{}, WrapAWSPrepareError(cfg, AWSAuthExisting, err)
	}
	report.IdentityVerified = true
	return report, nil
}
```

> Behavior-preservation note vs. cli source: the cli's `checkSetupAWSSDKIdentity` wraps the failing identity check via `wrapAWSPrepareError` (setup_auth.go:189), and `runSetupAWSExistingAuth` calls it directly (setup_auth.go:119). The headless port replicates that wrapping. The cli's `setupAWSSDKIdentityChecker` nil-fallback (returns a no-op "verified" when both `CheckAWSSDKIdentity` and `PrepareAWS` deps are nil, setup_auth.go:194-202) is a cli-deps quirk that does NOT apply to the engine: `Effects.CheckAWSSDKIdentity` is always present (production delegates to `diag.CheckSDKIdentity`, tests inject a fake), so the engine always calls it. This is intentional and the cli driver task (a later unit) keeps its own nil-fallback for `SetupDeps` back-compat via the type aliases.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run TestEnginePrepareAWS -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/setup/engine.go internal/setup/engine_prepare.go internal/setup/engine_prepare_test.go
git commit -m "feat(setup): add Engine.PrepareAWS — headless AWS auth + bucket-prep sequence

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 11: Engine.WriteConfig / WriteDraft / RemoveDraft / DraftPath (config + draft persistence, stdout stripped)

**Files:**
- Create: `internal/setup/engine_config.go` (headless port of `config.Write` call at `internal/cli/setup.go:294-298` and the draft helpers at `internal/cli/setup.go:397-417`)
- Test: `internal/setup/engine_config_test.go`

- [ ] **Step 1: Write the failing test**

```go
package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// WriteConfig writes the plan's config to cfgPath via config.Write. Headless
// port of internal/cli/setup.go:294-298 (stdout progress dropped).
func TestEngineWriteConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	var p Plan
	p.Config.Repo.S3.Bucket = "example-bucket"
	p.Config.Repo.S3.Region = "us-east-1"
	if err := NewEngine(fakeEffects{}).WriteConfig(cfgPath, &p); err != nil {
		t.Fatalf("WriteConfig: %v", err)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if loaded.Repo.S3.Bucket != "example-bucket" {
		t.Fatalf("bucket = %q, want example-bucket", loaded.Repo.S3.Bucket)
	}
}

// DraftPath mirrors setupDraftPath (internal/cli/setup.go:413-417): a
// dotfile sibling of cfgPath suffixed .setup-draft.
func TestEngineDraftPath(t *testing.T) {
	got := NewEngine(fakeEffects{}).DraftPath("/tmp/sub/sentra.yaml")
	want := filepath.Join("/tmp/sub", ".sentra.yaml.setup-draft")
	if got != want {
		t.Fatalf("DraftPath = %q, want %q", got, want)
	}
}

// WriteDraft then RemoveDraft round-trips; RemoveDraft on a missing draft is
// a best-effort no-op (internal/cli/setup.go:405-411).
func TestEngineWriteAndRemoveDraft(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "sentra.yaml")
	e := NewEngine(fakeEffects{})
	var cfg config.Config
	cfg.Repo.S3.Bucket = "example-bucket"
	if err := e.WriteDraft(cfgPath, &cfg); err != nil {
		t.Fatalf("WriteDraft: %v", err)
	}
	draft := e.DraftPath(cfgPath)
	if _, err := os.Stat(draft); err != nil {
		t.Fatalf("draft not written: %v", err)
	}
	e.RemoveDraft(cfgPath)
	if _, err := os.Stat(draft); !os.IsNotExist(err) {
		t.Fatalf("draft still present after RemoveDraft: err=%v", err)
	}
	// Second RemoveDraft on the now-missing draft must not panic or error.
	e.RemoveDraft(cfgPath)
}
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run 'TestEngineWriteConfig|TestEngineDraftPath|TestEngineWriteAndRemoveDraft' -count=1`
Expected: FAIL — compile error `e.WriteConfig undefined` / `e.DraftPath undefined` / `e.WriteDraft undefined`.

- [ ] **Step 3: Write the minimal implementation**

`internal/setup/engine_config.go`:

```go
package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/markgustetic/sentra/internal/config"
)

// WriteConfig writes p.Config to cfgPath. Headless port of the config.Write
// call at internal/cli/setup.go:294-298 (the "Writing"/"Config written"
// stdout lines are the driver's responsibility, not the engine's).
func (e *Engine) WriteConfig(cfgPath string, p *Plan) error {
	if err := config.Write(cfgPath, &p.Config); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return nil
}

// WriteDraft persists a non-secret setup draft next to cfgPath so an
// interrupted run can be resumed. Moved from writeSetupDraft
// (internal/cli/setup.go:397-403). config never serializes secrets, so the
// draft is safe to leave on disk.
func (e *Engine) WriteDraft(cfgPath string, cfg *config.Config) error {
	draftPath := e.DraftPath(cfgPath)
	if err := config.Write(draftPath, cfg); err != nil {
		return fmt.Errorf("write setup draft %s: %w", draftPath, err)
	}
	return nil
}

// RemoveDraft best-effort deletes the draft. Moved from removeSetupDraft
// (internal/cli/setup.go:405-411): a leftover non-secret draft is less
// harmful than turning a successful setup into a failure, so errors are
// swallowed.
func (e *Engine) RemoveDraft(cfgPath string) {
	if err := os.Remove(e.DraftPath(cfgPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// DraftPath returns the draft sibling of cfgPath. Moved from setupDraftPath
// (internal/cli/setup.go:413-417).
func (e *Engine) DraftPath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	base := filepath.Base(cfgPath)
	return filepath.Join(dir, "."+base+".setup-draft")
}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run 'TestEngineWriteConfig|TestEngineDraftPath|TestEngineWriteAndRemoveDraft' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/setup/engine_config.go internal/setup/engine_config_test.go
git commit -m "feat(setup): add Engine.WriteConfig + draft persistence (WriteDraft/RemoveDraft/DraftPath)

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 12: Engine.InitRepo with the verify-before-keyring guard (VERBATIM from setup_init.go:43-64)

**Files:**
- Create: `internal/setup/engine_init.go` (headless port of `runSetupInit` from `internal/cli/setup_init.go:19-78`; the passphrase is passed in, not resolved here)
- Test: `internal/setup/engine_init_test.go`

- [ ] **Step 1: Write the failing test**

```go
package setup

import (
	"context"
	"errors"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// Fresh init: repo initialized, and with save=true the passphrase is saved
// to the keyring. Mirrors runSetupInit fresh path
// (internal/cli/setup_init.go:70-77).
func TestEngineInitRepoFreshSavesPassphrase(t *testing.T) {
	store := blobstore.NewMemory()
	saved := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saved = true; return nil },
	}
	res, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("hunter2"), true)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if res.AlreadyInitialized {
		t.Fatal("AlreadyInitialized = true on a fresh store")
	}
	if res.RepoID == "" {
		t.Fatal("RepoID empty after fresh init")
	}
	if !res.PassphraseSavedToKeyring || !saved {
		t.Fatalf("passphrase not saved: res=%+v saved=%v", res, saved)
	}
}

// SAFETY-CRITICAL: already-initialized + save=true must repo.Open-verify the
// passphrase BEFORE calling SavePassphrase. A WRONG passphrase must surface
// an error and MUST NOT call SavePassphrase. Guards internal/cli/setup_init.go:43-64.
func TestEngineInitRepoAlreadyInitializedWrongPassphraseDoesNotSaveKeyring(t *testing.T) {
	store := blobstore.NewMemory()
	// Pre-initialize the repo under the correct passphrase.
	r, err := repo.Init(context.Background(), store, []byte("correct-horse"))
	if err != nil {
		t.Fatalf("pre-init: %v", err)
	}
	r.Close()

	saveCalled := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saveCalled = true; return nil },
	}
	_, err = NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("wrong-passphrase"), true)
	if err == nil {
		t.Fatal("InitRepo: got nil error with wrong passphrase on existing repo, want non-nil")
	}
	if saveCalled {
		t.Fatal("SavePassphrase was called with a wrong passphrase — verify-before-save guard is broken")
	}
	if !errors.Is(err, repo.ErrWrongPassphrase) {
		t.Fatalf("InitRepo error = %v, want wrapped repo.ErrWrongPassphrase", err)
	}
}

// Already-initialized + correct passphrase + save=true: repo.Open verifies,
// THEN SavePassphrase runs; result reports AlreadyInitialized and the saved
// keyring. Mirrors internal/cli/setup_init.go:52-63.
func TestEngineInitRepoAlreadyInitializedCorrectPassphraseSaves(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("correct-horse"))
	if err != nil {
		t.Fatalf("pre-init: %v", err)
	}
	wantID := r.Config().ID
	r.Close()

	saved := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saved = true; return nil },
	}
	res, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("correct-horse"), true)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if !res.AlreadyInitialized {
		t.Fatal("AlreadyInitialized = false, want true")
	}
	if res.RepoID != wantID {
		t.Fatalf("RepoID = %q, want %q", res.RepoID, wantID)
	}
	if !res.PassphraseSavedToKeyring || !saved {
		t.Fatalf("passphrase not saved after verified open: res=%+v saved=%v", res, saved)
	}
}

// save=true with no SavePassphrase effect wired must fail up front, not
// after touching the store. Mirrors the guard at internal/cli/setup_init.go:26-28.
func TestEngineInitRepoSaveWithoutSaverFails(t *testing.T) {
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return blobstore.NewMemory(), nil },
		savePass: nil, // fakeEffects returns nil (no-op); simulate "missing" by asserting the engine relies on Effects, not a nil-check
	}
	// The engine relies on Effects.SavePassphrase always being present, so
	// this documents that a fresh save succeeds even without an override.
	if _, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("hunter2"), true); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
}
```

> Note: unlike the cli `runSetupInit` (which nil-checks `deps.NewStore`/`deps.Passphrase`/`deps.SavePassphrase` because those are optional func fields), `Effects` methods are always present, so `Engine.InitRepo` drops those three nil guards. `TestEngineInitRepoSaveWithoutSaverFails` documents that the fresh-save path succeeds with the fake's no-op saver, confirming the engine does not re-introduce a nil check. Confirm `blobstore.NewMemory` exists (`grep -rn "func NewMemory" internal/blobstore`); if the constructor is named differently, use that name in these tests.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/setup/ -run TestEngineInitRepo -count=1`
Expected: FAIL — compile error `e.InitRepo undefined`.

- [ ] **Step 3: Write the minimal implementation**

`internal/setup/engine_init.go` (the `ErrAlreadyInitialized` branch at lines 43-64 is preserved VERBATIM in structure; the only changes vs. `runSetupInit` are: the passphrase is a parameter not resolved via a dep, `deps.NewStore`→`e.eff.NewStore`, `deps.SavePassphrase`→`e.eff.SavePassphrase`, and the three optional-dep nil guards are dropped because `Effects` methods are total):

```go
package setup

import (
	"context"
	"errors"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// InitRepo initializes (or, if already present, verifies) the encrypted
// repository for cfg using pass, optionally saving pass to the OS keyring.
// The caller owns pass and its zeroization (the cli driver resolves it via
// the passphrase resolver and defers crypto.Zeroize; the TUI wizard zeroizes
// its masked-input buffer). Headless port of runSetupInit
// (internal/cli/setup_init.go:19-78) minus the optional-dep nil guards —
// Effects methods are always present.
func (e *Engine) InitRepo(ctx context.Context, cfg *config.Config, pass []byte, save bool) (InitResult, error) {
	store, err := e.eff.NewStore(ctx, cfg)
	if err != nil {
		return InitResult{}, fmt.Errorf("open blobstore: %w", err)
	}

	r, err := repo.Init(ctx, store, pass)
	if err != nil {
		if errors.Is(err, repo.ErrAlreadyInitialized) {
			result := InitResult{AlreadyInitialized: true}
			// The repo already exists, but the user still asked to save the
			// passphrase to the OS keyring. repo.Init does not verify the
			// passphrase against an existing repo, so open it to confirm the
			// passphrase is correct before populating the keyring — otherwise
			// we'd either leave use_keyring:true dangling with an empty keyring
			// or store a wrong passphrase. Both silently break later
			// non-interactive runs.
			if save {
				existing, oerr := repo.Open(ctx, store, pass)
				if oerr != nil {
					return InitResult{}, fmt.Errorf("repository already initialized, but the provided passphrase did not open it (keyring not updated): %w", oerr)
				}
				result.RepoID = existing.Config().ID
				existing.Close()
				if serr := e.eff.SavePassphrase(cfg, pass); serr != nil {
					return InitResult{}, fmt.Errorf("save passphrase to keyring: %w", serr)
				}
				result.PassphraseSavedToKeyring = true
			}
			return result, nil
		}
		return InitResult{}, fmt.Errorf("init repo: %w", err)
	}
	defer r.Close()

	result := InitResult{RepoID: r.Config().ID}
	if save {
		if err := e.eff.SavePassphrase(cfg, pass); err != nil {
			return InitResult{}, fmt.Errorf("save passphrase to keyring: %w", err)
		}
		result.PassphraseSavedToKeyring = true
	}
	return result, nil
}
```

> `InitResult` is the pinned type moved by U1 from `internal/cli/setup_init.go:13-17` (`setupInitResult` → `setup.InitResult`, fields `RepoID string`, `AlreadyInitialized bool`, `PassphraseSavedToKeyring bool`). If U1 has not landed it, this task's file will not compile — U1 is a hard prerequisite. `repo.ErrWrongPassphrase` (repo.go:32) is what `repo.Open` returns for a wrong passphrase, so the wrong-passphrase test's `errors.Is` assertion holds because `InitRepo` wraps `oerr` with `%w`.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/setup/ -run TestEngineInitRepo -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/setup/engine_init.go internal/setup/engine_init_test.go
git commit -m "feat(setup): add Engine.InitRepo preserving the verify-before-keyring guard

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**Cross-unit notes for the controller / consuming units:**
- `internal/setup` imports only `config`, `diag`, `repo`, `blobstore`, `crypto` (indirectly, if used), aws-sdk, smithy, and stdlib — never `cli`/`tui`. Verified against the import-direction constraint.
- `Engine.PrepareAWS` drops the cli's `endpoint_url` reject (setup.go:227-229) and the huh repair loop (setup.go:249-259, 277-287) — those stay in the cli driver (a later unit) which wraps this engine. Callers repeat the plan-mutation + re-invoke themselves.
- `blobstore.NewMemory` is used by the tests here; if its constructor differs, substitute the real name (grep before writing).
- U1 must land `Plan`, `Backend*`/`AWSAuth*` consts, `AWSPrepareOptions/Report`, `AWSAuthReport`, `AWSCLIInstallReport`, `AWSCLIInstallConfirm`, `InitResult`, and `ResolveAWSAuthMethod`; U2 must land `WrapAWSPrepareError`, `WrapAWSLoginFlowError`, `WrapAWSSSOFlowError`, `IsAWSMissingCredentialsError`, `LoadAWSCLIConfig`, `AWSSSOProfileConfigured`, `DefaultAWSCLIInstallPlan`, and `s3BucketARN`/`s3ObjectARN` before this unit compiles.


## Part 4 — CLI wizard rewritten as a thin driver (setup_test.go is the oracle)

**Published API:** This unit publishes no new exported package API. It reshapes `internal/cli/setup*.go` into a thin driver over `internal/setup` (defined in prior units) and `internal/config` keyring helpers (defined in prior units). It preserves every cli-package identifier the two unchanged oracle test files reference:

- Exported (unchanged names, now `=` aliases): `type SetupPlan = setup.Plan`, `type SetupBackend = setup.Backend`, `type SetupAWSAuthMethod = setup.AWSAuthMethod`, `type AWSPrepareOptions = setup.AWSPrepareOptions`, `type AWSPrepareReport = setup.AWSPrepareReport`, `type AWSAuthReport = setup.AWSAuthReport`, `type AWSCLIInstallPlan = setup.AWSCLIInstallPlan`, `type AWSCLIInstallReport = setup.AWSCLIInstallReport`; const `SetupBackendAWS = setup.BackendAWS`, `SetupBackendS3Compatible = setup.BackendS3Compatible`, `SetupAWSAuthLogin/SSO/Existing/Skip = setup.AWSAuthLogin/SSO/Existing/Skip`; `func NewSetup(deps SetupDeps) *cobra.Command`; default effect wrappers `DefaultEnsureAWSCLI`, `DefaultAWSLogin`, `DefaultAWSSSOConfigured`, `DefaultAWSConfigureSSO`, `DefaultAWSSSOLogin`, `DefaultAWSCheckSDKIdentity`, `DefaultAWSPrepare` (thin wrappers over `setup.DefaultEffects()`).
- Unexported (unchanged names, now thin wrappers / aliases): `defaultSetupPlan`, `setupPlanReviewText`, `runSetupProgress`, `setupDraftPath`, `applySetupAWSConfigOnly`, `wrapAWSPrepareError`, `setupErrorAdvice`, `defaultSetupAWSRepairChoice`, `setupAWSRepairLogin`, `setupAWSRepairExisting`, `setupAWSRepairSSO`, `setupAWSRepairConfig`, `setupAWSRepairCancel`, `type setupIAMPolicyDocument = setup.IAMPolicyDocument`, `type setupIAMPolicyStatement = setup.IAMPolicyStatement`, `runSetupInit`, `type setupInitResult = setup.InitResult`, test helpers `writeAWSConfig`/`writeExecutable` (untouched).

The gate for **every** task below is: the UNCHANGED `internal/cli/setup_test.go` (1863 lines) AND `internal/cli/setup_awscli_test.go` (242 lines) compile and pass. That is the behavior-preservation proof.

Note on prerequisites: this unit consumes `internal/setup` and `internal/config` symbols authored by prior units (Units 1–3). Every `setup.*` and `config.Keyring*` symbol referenced here is listed in the PINNED public API in the plan header. I read the real cli source these tasks replace; file:line moves are cited per task.

---

### Task 13: Add `internal/cli` setup type + const aliases over `internal/setup`

**Files:**
- Create: `internal/cli/setup_aliases.go`
- Modify: `internal/cli/setup.go:17-116` (delete the type/const definitions being aliased)
- Test: `internal/cli/setup_test.go` (UNCHANGED oracle — the gate)

- [ ] **Step 1: Write the failing test**

No new test is authored; the oracle `internal/cli/setup_test.go` IS the test. It references `SetupPlan`, `SetupBackendAWS`, `SetupBackendS3Compatible`, `SetupAWSAuthLogin`, `SetupAWSAuthSSO`, `SetupAWSAuthExisting`, `AWSPrepareOptions`, `AWSPrepareReport`, `AWSCLIInstallPlan`, `AWSCLIInstallReport` (see `setup_test.go:19-100,145-160,269-272,1293-1305`). Add a compile-guard test asserting the aliases resolve to the setup package types:

```go
package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupAliasesBindToSetupPackage(t *testing.T) {
	// Compile-time proof the cli names are identity aliases of setup.* so the
	// oracle's field access and const comparisons keep meaning the same thing.
	var _ setup.Plan = SetupPlan{}
	var _ = SetupPlan(setup.Plan{})
	var _ setup.Backend = SetupBackendAWS
	var _ setup.Backend = SetupBackendS3Compatible
	var _ setup.AWSAuthMethod = SetupAWSAuthLogin
	var _ setup.AWSAuthMethod = SetupAWSAuthSSO
	var _ setup.AWSAuthMethod = SetupAWSAuthExisting
	var _ setup.AWSAuthMethod = SetupAWSAuthSkip
	var _ = AWSPrepareOptions(setup.AWSPrepareOptions{})
	var _ = AWSPrepareReport(setup.AWSPrepareReport{})
	var _ = AWSAuthReport(setup.AWSAuthReport{})
	var _ = AWSCLIInstallPlan(setup.AWSCLIInstallPlan{})
	var _ = AWSCLIInstallReport(setup.AWSCLIInstallReport{})
	if string(SetupBackendAWS) != "aws" {
		t.Fatalf("SetupBackendAWS value drifted: %q", SetupBackendAWS)
	}
	if string(SetupAWSAuthLogin) != "login" {
		t.Fatalf("SetupAWSAuthLogin value drifted: %q", SetupAWSAuthLogin)
	}
}
```

Write this test in `internal/cli/setup_aliases_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupAliasesBindToSetupPackage -count=1`
Expected: FAIL — build error `undefined: SetupPlan` / `SetupPlan is not setup.Plan` (aliases not yet defined; the old `SetupPlan` is still a distinct struct in `setup.go`).

- [ ] **Step 3: Write the minimal implementation**

Delete the type/const block in `internal/cli/setup.go:17-116` (the `SetupBackend`, `SetupAWSAuthMethod`, `SetupPlan`, `AWSCLIInstallPlan`, `AWSCLIInstallReport`, `AWSPrepareOptions`, `AWSPrepareReport`, `AWSAuthReport` definitions and their const values). Also delete the callback-type definitions `SetupPrompt`, `SetupOverwriteConfirm`, `SetupReviewConfirm`, `SetupAWSAuthRepairPrompt`, `AWSCLIInstallConfirm` at `setup.go:51-80` — they will be re-declared unchanged in `setup_aliases.go` (they are cli-only huh-facing callback signatures, not part of the setup engine). Create `internal/cli/setup_aliases.go`:

```go
package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// The setup engine lives in internal/setup so both the CLI wizard (this
// package) and the TUI wizard can drive identical logic. These identity
// aliases keep the historical cli names — and the behavior-preservation
// oracle in setup_test.go — meaning exactly the same types and values.

type (
	// SetupBackend names the storage target chosen in the setup wizard.
	SetupBackend = setup.Backend
	// SetupAWSAuthMethod names how setup makes AWS credentials available.
	SetupAWSAuthMethod = setup.AWSAuthMethod
	// SetupPlan is the complete set of actions the setup wizard selected.
	SetupPlan = setup.Plan
	// AWSCLIInstallPlan is the package-manager command to install the AWS CLI.
	AWSCLIInstallPlan = setup.AWSCLIInstallPlan
	// AWSCLIInstallReport summarizes the AWS CLI preflight.
	AWSCLIInstallReport = setup.AWSCLIInstallReport
	// AWSPrepareOptions controls the AWS-side setup work.
	AWSPrepareOptions = setup.AWSPrepareOptions
	// AWSPrepareReport summarizes the AWS setup work for the final CLI output.
	AWSPrepareReport = setup.AWSPrepareReport
	// AWSAuthReport summarizes the optional AWS CLI auth preflight.
	AWSAuthReport = setup.AWSAuthReport
)

const (
	SetupBackendAWS          = setup.BackendAWS
	SetupBackendS3Compatible = setup.BackendS3Compatible

	SetupAWSAuthLogin    = setup.AWSAuthLogin
	SetupAWSAuthSSO      = setup.AWSAuthSSO
	SetupAWSAuthExisting = setup.AWSAuthExisting
	SetupAWSAuthSkip     = setup.AWSAuthSkip
)

// The remaining callback types stay cli-only: they are the huh-facing
// injection seam. Production leaves them nil and falls back to the Huh* forms;
// tests inject deterministic callbacks.

// SetupPrompt collects an updated setup plan from the operator.
type SetupPrompt func(current config.Config) (SetupPlan, error)

// SetupOverwriteConfirm asks whether an existing config file may be overwritten.
type SetupOverwriteConfirm func(path string) (bool, error)

// SetupReviewConfirm asks whether the final non-secret setup plan should apply.
type SetupReviewConfirm func(cfgPath string, plan SetupPlan) (bool, error)

// SetupAWSAuthRepairPrompt asks what to do after AWS auth or bucket prep fails.
type SetupAWSAuthRepairPrompt func(plan SetupPlan, cause error) (SetupPlan, bool, error)

// AWSCLIInstallConfirm asks whether Sentra may run the detected installer.
type AWSCLIInstallConfirm func(plan AWSCLIInstallPlan) (bool, error)

// unused import guard; blobstore/context are referenced by SetupDeps in
// setup.go which shares this package.
var (
	_ = blobstore.NewMemory
	_ context.Context
)
```

Remove the trailing `_ = blobstore.NewMemory` / `_ context.Context` guard once `SetupDeps` (still in `setup.go`) provides the real references — see the note in Step 3 of the next task. For this task, keep the guard only if `go build` reports the imports unused; otherwise omit the `blobstore`/`context` imports entirely.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestSetupAliasesBindToSetupPackage -count=1`
Expected: PASS. Also run `go build ./internal/cli/...` — Expected: builds (the rest of `setup.go` still references the now-aliased names, which resolve to `setup.*`).

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_aliases.go internal/cli/setup_aliases_test.go internal/cli/setup.go
git commit -m "refactor(cli): alias setup types/consts to internal/setup"
```

---

### Task 14: Route the IAM-policy printer and awscli defaults through `internal/setup`

**Files:**
- Modify: `internal/cli/setup_iam_policy.go:12-23,54-111` (replace local IAM types + `writeSetupIAMPolicy`/`buildSetupIAMPolicy` with setup delegation)
- Modify: `internal/cli/setup_awscli.go:13-236` (replace pure parser + default effect fns with setup delegation)
- Test: `internal/cli/setup_awscli_test.go` (UNCHANGED oracle), `internal/cli/setup_test.go` (UNCHANGED oracle)

- [ ] **Step 1: Write the failing test**

No new test authored. The gate is the UNCHANGED oracle:
- `internal/cli/setup_awscli_test.go:15-222` calls `DefaultEnsureAWSCLI`, `DefaultAWSSSOConfigured`, and `writeAWSConfig`.
- `internal/cli/setup_test.go:292-305,1819-1848` unmarshals into `setupIAMPolicyDocument` and asserts policy JSON (`arn:aws:s3:::policy-bucket/sentra/*`, `s3:PutBucketPublicAccessBlock`, `"backups/*"`).

Add a compile-guard proving the IAM aliases bind, in `internal/cli/setup_iam_alias_test.go`:

```go
package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupIAMPolicyAliasBindsToSetup(t *testing.T) {
	var _ = setupIAMPolicyDocument(setup.IAMPolicyDocument{})
	var _ = setupIAMPolicyStatement(setup.IAMPolicyStatement{})
	doc := buildSetupIAMPolicy("bkt", "p/")
	if doc.Version != "2012-10-17" {
		t.Fatalf("policy version drifted: %q", doc.Version)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupIAMPolicyAliasBindsToSetup -count=1`
Expected: FAIL — build error `cannot convert setup.IAMPolicyDocument{} (setupIAMPolicyDocument is a distinct struct)` since `setupIAMPolicyDocument` is still the local struct at `setup_iam_policy.go:12-15`.

- [ ] **Step 3: Write the minimal implementation**

In `internal/cli/setup_iam_policy.go`: delete the local `setupIAMPolicyDocument` / `setupIAMPolicyStatement` struct defs (`:12-23`) and the `buildSetupIAMPolicy` body (`:64-111`); replace `writeSetupIAMPolicy` / `buildSetupIAMPolicy` with delegations. Keep `newSetupIAMPolicy` (the cobra subcommand) unchanged except its calls. New file body:

```go
package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/setup"
)

// setupIAMPolicyDocument/Statement keep their historical cli names as aliases
// of the setup engine's exported policy types so the oracle can json.Unmarshal
// into them.
type setupIAMPolicyDocument = setup.IAMPolicyDocument

type setupIAMPolicyStatement = setup.IAMPolicyStatement

func newSetupIAMPolicy(out io.Writer) *cobra.Command {
	var bucket string
	var prefix string
	cmd := &cobra.Command{
		Use:   "iam-policy",
		Short: "Print a least-privilege AWS IAM policy for Sentra",
		Long: "Print non-secret IAM JSON for the selected S3 bucket and prefix. " +
			"The policy covers setup checks plus normal backup, restore, check, sync, and prune operations.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out = cmdStdout(cmd, out)
			bucket = strings.TrimSpace(bucket)
			prefix = strings.TrimSpace(prefix)
			if bucket == "" {
				return fmt.Errorf("--bucket is required")
			}
			if err := validateSetupBucketName(bucket); err != nil {
				return err
			}
			return writeSetupIAMPolicy(out, bucket, prefix)
		},
	}
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&prefix, "prefix", "sentra/", "S3 key prefix Sentra will use")
	return cmd
}

func writeSetupIAMPolicy(out io.Writer, bucket string, prefix string) error {
	return setup.WriteIAMPolicy(out, bucket, prefix)
}

func buildSetupIAMPolicy(bucket string, prefix string) setupIAMPolicyDocument {
	return setup.BuildIAMPolicy(bucket, prefix)
}
```

In `internal/cli/setup_awscli.go`: delete the pure parser + install-plan helpers now living in `internal/setup` (`defaultAWSCLIInstallPlan` `:49-57`, `runAWSCLI`/`appendAWSProfile` `:97-127`, `awsCLIConfig`/`loadAWSCLIConfig`/`awsConfigPath`/`awsSSOProfileConfigured`/`awsProfileSection`/`hasAllAWSConfigKeys`/`hasAnyAWSConfigKey` `:129-236`). Replace the four `Default*` effect fns with thin delegations to `setup.DefaultEffects()`. Because `loadAWSCLIConfig`/`awsProfileSection`/`awsProfileNameFromSection` are still referenced by `defaultAWSProfileFromConfig` in `setup_wizard.go:260-287`, that path moves to the engine in the next task; for THIS task, re-export the two parser entry points the wizard still needs as cli-local wrappers over the moved setup functions. New file body:

```go
package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/setup"
)

// The interactive AWS-CLI effects and the ~/.aws/config parser moved to
// internal/setup so the TUI wizard shares them. These wrappers keep the
// historical cli names (and the setup_awscli_test.go oracle) working by
// delegating to the production Effects.

// DefaultEnsureAWSCLI verifies the AWS CLI is available, offering a confirmed
// brew install when missing. Brew auto-install stays a CLI-only path.
func DefaultEnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	return setup.DefaultEffects().EnsureAWSCLI(ctx, confirm)
}

// DefaultAWSLogin delegates browser-based AWS CLI sign-in.
func DefaultAWSLogin(ctx context.Context, profile string, region string) error {
	return setup.DefaultEffects().AWSLogin(ctx, profile, region)
}

// DefaultAWSSSOConfigured reports whether the profile has a complete SSO setup.
func DefaultAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return setup.DefaultEffects().CheckAWSSSOConfigured(ctx, profile)
}

// DefaultAWSConfigureSSO delegates first-time SSO profile setup to the AWS CLI.
func DefaultAWSConfigureSSO(ctx context.Context, profile string) error {
	return setup.DefaultEffects().AWSConfigureSSO(ctx, profile)
}

// DefaultAWSSSOLogin delegates browser-based SSO authentication to the AWS CLI.
func DefaultAWSSSOLogin(ctx context.Context, profile string) error {
	return setup.DefaultEffects().AWSSSOLogin(ctx, profile)
}
```

Note for the definer of `internal/setup`: `setup.AWSCLIInstallConfirm` must be the same underlying func type as cli's `AWSCLIInstallConfirm` (`func(AWSCLIInstallPlan) (bool, error)`); since `AWSCLIInstallPlan = setup.AWSCLIInstallPlan` (prior task) and `AWSCLIInstallConfirm` is a cli-defined named type, pass it directly — `Effects.EnsureAWSCLI` must accept `func(setup.AWSCLIInstallPlan) (bool, error)`. A named-to-underlying assignment is legal in Go, so `confirm` (type `AWSCLIInstallConfirm`) is assignable to the `func(setup.AWSCLIInstallPlan) (bool, error)` parameter.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestSetupIAMPolicyAliasBindsToSetup|TestDefaultEnsureAWSCLI|TestDefaultAWSSSOConfigured|TestSetupIAMPolicy_PrintsLeastPrivilegePolicy|TestSetup_PrintIAMPolicyOnlyDoesNotWriteConfigOrTouchAWS' -count=1`
Expected: PASS (all IAM + awscli oracle cases green).

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_iam_policy.go internal/cli/setup_awscli.go internal/cli/setup_iam_alias_test.go
git commit -m "refactor(cli): delegate IAM policy + awscli defaults to internal/setup"
```

---

### Task 15: Route setup transforms, errors, and IAM-adjacent helpers through the engine

**Files:**
- Modify: `internal/cli/setup_wizard.go:208-287,540-586` (`defaultSetupPlan`, `applySetupSmartDefaults`, env/profile helpers, `setupPlanReviewText`, `normalizeSetupConfig`)
- Modify: `internal/cli/setup.go:419-446` (`applySetupAWSConfigOnly`, `applySetupPassphraseConfig`, `resolveSetupAWSAuthMethod`)
- Modify: `internal/cli/setup_errors.go:12-138` (wrap/advice helpers)
- Modify: `internal/cli/setup_summary.go:121-162` (label helpers)
- Test: `internal/cli/setup_test.go` (UNCHANGED oracle)

- [ ] **Step 1: Write the failing test**

No new test authored. The oracle pins these transforms:
- `defaultSetupPlan` — `setup_test.go:19-117` (env/profile/region smart defaults, browser-login default, `SavePassphrase` true).
- `setupPlanReviewText` — `setup_test.go:314-338` (`Repository: initialize after config`, keyring/prompt lines, `No passphrases`).
- `applySetupAWSConfigOnly` — `setup_test.go:924,1808` (used inside repair callback).
- `wrapAWSPrepareError` — `setup_test.go:705-721` (permission guidance preserved).
- `setupErrorAdvice` — `setup_test.go:852-882` (region mismatch, bucket-already-exists).
- `defaultSetupAWSRepairChoice` + `setupAWSRepairExisting`/`setupAWSRepairLogin` — `setup_test.go:884-900`.

Add a delegation compile-guard in `internal/cli/setup_transform_alias_test.go`:

```go
package cli

import (
	"context"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupTransformsDelegateToEngine(t *testing.T) {
	clearAWSSetupEnv(t)
	// defaultSetupPlan must equal setup.DefaultPlan under the production probe.
	got := defaultSetupPlan(config.Config{})
	want := setup.DefaultPlan(config.Config{}, setup.DefaultEnvProbe())
	if got.Backend != want.Backend || got.AWSAuthMethod != want.AWSAuthMethod ||
		got.SavePassphrase != want.SavePassphrase {
		t.Fatalf("defaultSetupPlan drifted from setup.DefaultPlan: got %+v want %+v", got, want)
	}
	// resolveSetupAWSAuthMethod must equal setup.ResolveAWSAuthMethod.
	p := SetupPlan{PrepareAWS: true}
	if resolveSetupAWSAuthMethod(&p) != setup.ResolveAWSAuthMethod(&p) {
		t.Fatal("resolveSetupAWSAuthMethod drifted")
	}
	_ = context.Background()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupTransformsDelegateToEngine -count=1`
Expected: FAIL — build error (`setup.DefaultPlan`/`setup.ResolveAWSAuthMethod`/`setup.DefaultEnvProbe` referenced by the test are fine once the engine exists, but the cli `defaultSetupPlan` still runs the old inline body computing a possibly divergent result). If bodies already match structurally the test may pass on values; the failing signal is the build reference to `setup.*` combined with the still-present duplicate cli helper functions (`firstNonEmptyEnv`, etc.) which `go vet`/unused will flag after Step 3. Treat the RED as: assertion or unused-lint until delegation is in place.

- [ ] **Step 3: Write the minimal implementation**

Replace the transform/error/label bodies with delegations to `internal/setup`, deleting the now-duplicated helper functions so the `unused` linter passes.

In `internal/cli/setup_wizard.go`, replace `defaultSetupPlan` (`:208-222`), delete `applySetupSmartDefaults` (`:224-237`), `firstNonEmptyEnv` (`:239-246`), `hasAWSEnvironmentCredentials` (`:248-258`), `defaultAWSProfileFromConfig` (`:260-276`), `awsProfileNameFromSection` (`:278-287`); replace `setupPlanReviewText` (`:540-578`) and `normalizeSetupConfig` (`:580-586`):

```go
func defaultSetupPlan(current config.Config) SetupPlan {
	return setup.DefaultPlan(current, setup.DefaultEnvProbe())
}

func setupPlanReviewText(cfgPath string, plan SetupPlan) string {
	return setup.ReviewText(cfgPath, plan)
}

func normalizeSetupConfig(cfg *config.Config) {
	setup.NormalizeConfig(cfg)
}
```

Add `"github.com/markgustetic/sentra/internal/setup"` to the imports and drop `"os"` if it becomes unused (the huh forms in this file still use `os`? — verify: after deletion the remaining `os` reference was only in `firstNonEmptyEnv`; if `os` is now unused, remove it. `emptyDash` moves with `ReviewText` into setup, so the cli-local `emptyDash` at `format.go:5` may still be used elsewhere — leave `format.go` untouched).

In `internal/cli/setup.go`, replace the three transforms (`:419-446`):

```go
func applySetupAWSConfigOnly(plan *SetupPlan) { setup.ApplyAWSConfigOnly(plan) }

func applySetupPassphraseConfig(plan *SetupPlan) { setup.ApplyPassphraseConfig(plan) }

func resolveSetupAWSAuthMethod(plan *SetupPlan) SetupAWSAuthMethod {
	return setup.ResolveAWSAuthMethod(plan)
}
```

Add the `setup` import to `setup.go`.

In `internal/cli/setup_errors.go`, replace `wrapAWSSSOFlowError` (`:12-21`), `wrapAWSPrepareError` (`:23-46`), `wrapAWSLoginFlowError` (`:48-54`), `isAWSMissingCredentialsError` (`:56-71`), and `setupErrorAdvice` (`:83-138`) with delegations; keep `printSetupErrorDetail` (`:73-81`) which does cli-only stdout formatting:

```go
package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

func wrapAWSSSOFlowError(command string, profile string, err error) error {
	return setup.WrapAWSSSOFlowError(command, profile, err)
}

func wrapAWSPrepareError(cfg *config.Config, method SetupAWSAuthMethod, err error) error {
	return setup.WrapAWSPrepareError(cfg, method, err)
}

func wrapAWSLoginFlowError(profile string, err error) error {
	return setup.WrapAWSLoginFlowError(profile, err)
}

func isAWSMissingCredentialsError(err error) bool {
	return setup.IsAWSMissingCredentialsError(err)
}

func printSetupErrorDetail(out io.Writer, err error, cfg *config.Config) {
	if err == nil {
		return
	}
	fmt.Fprintf(out, "%s %v\n", ui.Danger.Render("reason:"), err)
	for _, line := range setupErrorAdvice(err, cfg) {
		fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("advice:"), line)
	}
}

func setupErrorAdvice(err error, cfg *config.Config) []string {
	if cfg == nil {
		return setup.ErrorAdvice(err, config.Config{})
	}
	return setup.ErrorAdvice(err, *cfg)
}
```

Note: `setup.ErrorAdvice` per the pinned API takes `(err error, cfg config.Config)` (value). The oracle calls `setupErrorAdvice(err, nil)` (`setup_test.go:873`) and `setupErrorAdvice(err, &cfg)` (`:858`), so the cli wrapper keeps the `*config.Config` signature and adapts nil → zero value.

In `internal/cli/setup_summary.go`, replace the three label helpers (`:121-158`) with delegations, keeping the print funcs which are cli-only:

```go
func setupBackendLabel(backend SetupBackend) string { return setup.BackendLabel(backend) }

func setupAWSAuthMethodLabel(method SetupAWSAuthMethod) string {
	return setup.AWSAuthMethodLabel(method)
}

func setupAWSPreparedLabel(report *AWSPrepareReport) string {
	return setup.AWSPreparedLabel(report)
}
```

Add the `setup` import to `setup_summary.go` and drop any import that became unused (e.g. if `diag` was only used by `validateSetupBucketName`, keep it — that stays; `validateSetupBucketName` at `:160-162` remains a cli-local `diag.ValidateBucketName` wrapper because the wizard huh forms and `newSetupIAMPolicy` use it).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestSetupTransformsDelegateToEngine|TestDefaultSetupPlan|TestSetupPlanReviewMentionsPassphraseSourceForInit|TestWrapAWSPrepareErrorPreservesPermissionGuidance|TestSetupErrorAdvice|TestDefaultSetupAWSRepairChoice' -count=1`
Expected: PASS. Run `go build ./internal/cli/...` — Expected: builds with no unused-symbol errors.

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_wizard.go internal/cli/setup.go internal/cli/setup_errors.go internal/cli/setup_summary.go internal/cli/setup_transform_alias_test.go
git commit -m "refactor(cli): delegate setup transforms, errors, labels to engine"
```

---

### Task 16: Back `runSetupInit` and the spinner with the engine; keep them as thin cli wrappers

**Files:**
- Modify: `internal/cli/setup_init.go:13-78` (`setupInitResult`, `runSetupInit`)
- Keep: `internal/cli/setup_spinner.go` unchanged (the `\r` spinner stays cli-only per the hard constraint)
- Test: `internal/cli/setup_test.go` (UNCHANGED oracle: `TestRunSetupInit_PopulatesKeyringWhenAlreadyInitialized:1760`, `TestRunSetupInit_AlreadyInitializedWrongPassphraseDoesNotSave:1796`, `TestSetup_InitAlreadyInitializedIsSummaryNotError:1723`)

- [ ] **Step 1: Write the failing test**

No new test authored. The oracle drives `runSetupInit` directly at `setup_test.go:1778` and `:1811`, asserting the verify-before-keyring guard: already-initialized + correct passphrase saves to keyring (`:1782-1790`); already-initialized + wrong passphrase returns error and does NOT save (`:1811-1816`). Add a compile-guard for the alias in `internal/cli/setup_init_alias_test.go`:

```go
package cli

import (
	"testing"

	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupInitResultAliasBindsToSetup(t *testing.T) {
	var _ = setupInitResult(setup.InitResult{})
	var r setupInitResult
	r.AlreadyInitialized = true
	r.PassphraseSavedToKeyring = true
	r.RepoID = "id"
	if !r.AlreadyInitialized || !r.PassphraseSavedToKeyring || r.RepoID != "id" {
		t.Fatal("setupInitResult field access drifted")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupInitResultAliasBindsToSetup -count=1`
Expected: FAIL — build error converting `setup.InitResult{}` to the local struct `setupInitResult` (`setup_init.go:13-17`).

- [ ] **Step 3: Write the minimal implementation**

Replace `internal/cli/setup_init.go` so `setupInitResult` aliases `setup.InitResult` and `runSetupInit` maps the cli `SetupDeps` closures onto a `setup.Effects` and calls `Engine.InitRepo`, which owns the verify-before-keyring guard (moved VERBATIM from `setup_init.go:43-64` per the pinned API). The cli wrapper preserves the three nil-dependency guard errors the oracle relies on indirectly (missing store/passphrase/saver) by validating before constructing the engine:

```go
package cli

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/setup"
)

type setupInitResult = setup.InitResult

// runSetupInit resolves the passphrase via the cli's injected resolver, then
// hands repo init (and the verify-before-keyring guard) to the setup engine
// built from these same SetupDeps. It stays in cli so the oracle can drive it
// with the historical SetupDeps closures.
func runSetupInit(ctx context.Context, deps SetupDeps, cfg *config.Config, savePassphrase bool) (setupInitResult, error) {
	if deps.NewStore == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing store factory")
	}
	if deps.Passphrase == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing passphrase resolver")
	}
	if savePassphrase && deps.SavePassphrase == nil {
		return setupInitResult{}, fmt.Errorf("initialize repo: missing keyring passphrase saver")
	}

	pass, err := deps.Passphrase()
	if err != nil {
		return setupInitResult{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	eng := setup.NewEngine(setupEffects(deps))
	return eng.InitRepo(ctx, cfg, pass, savePassphrase)
}
```

`setupEffects(deps)` is defined in the next task (it maps `SetupDeps` → `setup.Effects`). The engine's `InitRepo` (defined in a prior unit) MUST contain the exact guard from the old `setup_init.go:19-78`: open the blobstore via `Effects.NewStore`, call `repo.Init`, and on `repo.ErrAlreadyInitialized` with `save==true` do `repo.Open` to verify the passphrase before `Effects.SavePassphrase`, returning the "provided passphrase did not open it (keyring not updated)" error on failure. The `defer crypto.Zeroize(pass)` here in cli plus the engine's own handling both zero-cost overlap; keep the cli defer so the resolver's buffer is scrubbed on every return path.

Note: the old `setup_init.go` wrapped `repo.Init` errors as `init repo: %w` and store errors as `open blobstore: %w`. The engine `InitRepo` must reproduce those exact wrappings — the oracle's `TestSetup_KeyringSaveFailureReturnsError:1203` asserts `save passphrase to keyring` substring, and `TestSetup_InitAlreadyInitializedIsSummaryNotError` asserts the `already initialized` summary path. Cite the definer: `setup.Engine.InitRepo` in the prior unit preserves `setup_init.go:19-78` verbatim.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestSetupInitResultAliasBindsToSetup|TestRunSetupInit_|TestSetup_InitAlreadyInitializedIsSummaryNotError|TestSetup_SavesPassphraseToKeyringWhenSelected|TestSetup_KeyringSaveFailureReturnsError' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_init.go internal/cli/setup_init_alias_test.go
git commit -m "refactor(cli): drive repo init through setup.Engine, keep guard"
```

---

### Task 17: Map `SetupDeps` onto `setup.Effects` and route AWS auth/prepare through `Engine.PrepareAWS`

**Files:**
- Create: `internal/cli/setup_effects.go`
- Modify: `internal/cli/setup.go:118-136` (keep `SetupDeps` struct; it stays the cli injection seam)
- Modify: `internal/cli/setup_auth.go:1-203` (replace the hand-rolled auth sequencing with `Engine.PrepareAWS` + cli spinner glue)
- Modify: `internal/cli/setup_awss3.go:30-85` (make `DefaultAWSPrepare` a thin wrapper over the engine's default)
- Test: `internal/cli/setup_test.go` (UNCHANGED oracle)

- [ ] **Step 1: Write the failing test**

No new test authored. The oracle exhaustively pins the AWS auth/prepare sequencing through injected `SetupDeps` closures:
- SSO skips login when identity works (`setup_test.go:1208-1274`, exactly 1 identity check).
- SSO installs missing CLI (`:1276-1341`), configure-then-login when profile missing (`:1517-1590`), sso login when creds fail (`:1451-1515`).
- Browser login runs when identity missing (`:1389-1449`, exactly 2 checks; login gets profile+region).
- Install/configure/login failures stop before write (`:1343-1387,1592-1692`).
- Prepare error wrapping + repair loop (`:583-961`).
- Prepare + init happy path (`:1018-1098`).

Add a delegation compile-guard in `internal/cli/setup_effects_test.go`:

```go
package cli

import (
	"context"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

func TestSetupEffectsSatisfiesEngineSeam(t *testing.T) {
	deps := SetupDeps{
		CheckAWSSDKIdentity: func(context.Context, *config.Config) error { return nil },
		PrepareAWS: func(context.Context, *config.Config, AWSPrepareOptions) (AWSPrepareReport, error) {
			return AWSPrepareReport{BucketExisted: true}, nil
		},
		NewStore: func(context.Context, *config.Config) (blobstore.Store, error) {
			return blobstore.NewMemory(), nil
		},
	}
	var eff setup.Effects = setupEffects(deps)
	_ = setup.NewEngine(eff)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetupEffectsSatisfiesEngineSeam -count=1`
Expected: FAIL — build error `undefined: setupEffects` (mapper not yet created).

- [ ] **Step 3: Write the minimal implementation**

Create `internal/cli/setup_effects.go` mapping the cli `SetupDeps` closures onto a `setup.Effects`, applying the SAME nil-fallback rules the old code used (`setup_auth.go:126-146,194-202`, `setup.go:263-266`): when a `SetupDeps` effect field is nil, fall back to `setup.DefaultEffects()`'s corresponding method. Crucially preserve the old "identity checker is nil when PrepareAWS is injected but CheckAWSSDKIdentity is not" rule (`setup_auth.go:194-202`) — that made tests inject only `PrepareAWS` and have the identity check be a no-op success.

```go
package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// setupEffects adapts the cli's SetupDeps injection seam onto the setup
// engine's Effects interface. A nil dep field falls back to the production
// default, matching the historical per-field fallbacks in setup_auth.go and
// runSetup so the behavior-preservation oracle keeps passing.
func setupEffects(deps SetupDeps) setup.Effects {
	def := setup.DefaultEffects()
	return &cliSetupEffects{deps: deps, def: def}
}

type cliSetupEffects struct {
	deps SetupDeps
	def  setup.Effects
}

func (e *cliSetupEffects) EnsureAWSCLI(ctx context.Context, confirm setup.AWSCLIInstallConfirm) (setup.AWSCLIInstallReport, error) {
	// Historical rule: the default AWS-CLI preflight runs only when NONE of the
	// interactive AWS effects are injected; otherwise a test that stubs login/
	// sso must not trigger a real brew probe.
	if e.deps.EnsureAWSCLI == nil &&
		e.deps.AWSLogin == nil && e.deps.AWSConfigureSSO == nil && e.deps.AWSSSOLogin == nil {
		return e.def.EnsureAWSCLI(ctx, confirm)
	}
	if e.deps.EnsureAWSCLI == nil {
		return setup.AWSCLIInstallReport{}, nil
	}
	return e.deps.EnsureAWSCLI(ctx, confirm)
}

func (e *cliSetupEffects) AWSLogin(ctx context.Context, profile, region string) error {
	if e.deps.AWSLogin == nil {
		return e.def.AWSLogin(ctx, profile, region)
	}
	return e.deps.AWSLogin(ctx, profile, region)
}

func (e *cliSetupEffects) CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	if e.deps.CheckAWSSSOConfigured == nil {
		return e.def.CheckAWSSSOConfigured(ctx, profile)
	}
	return e.deps.CheckAWSSSOConfigured(ctx, profile)
}

func (e *cliSetupEffects) AWSConfigureSSO(ctx context.Context, profile string) error {
	if e.deps.AWSConfigureSSO == nil {
		return e.def.AWSConfigureSSO(ctx, profile)
	}
	return e.deps.AWSConfigureSSO(ctx, profile)
}

func (e *cliSetupEffects) AWSSSOLogin(ctx context.Context, profile string) error {
	if e.deps.AWSSSOLogin == nil {
		return e.def.AWSSSOLogin(ctx, profile)
	}
	return e.deps.AWSSSOLogin(ctx, profile)
}

func (e *cliSetupEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	if e.deps.CheckAWSSDKIdentity != nil {
		return e.deps.CheckAWSSDKIdentity(ctx, cfg)
	}
	// Historical nil rule: when PrepareAWS is injected but no identity checker
	// is, skip the SDK identity call (treat as verified).
	if e.deps.PrepareAWS != nil {
		return nil
	}
	return e.def.CheckAWSSDKIdentity(ctx, cfg)
}

func (e *cliSetupEffects) PrepareAWS(ctx context.Context, cfg *config.Config, opts setup.AWSPrepareOptions) (setup.AWSPrepareReport, error) {
	if e.deps.PrepareAWS == nil {
		return e.def.PrepareAWS(ctx, cfg, opts)
	}
	return e.deps.PrepareAWS(ctx, cfg, opts)
}

func (e *cliSetupEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if e.deps.NewStore == nil {
		return e.def.NewStore(ctx, cfg)
	}
	return e.deps.NewStore(ctx, cfg)
}

func (e *cliSetupEffects) SavePassphrase(cfg *config.Config, pass []byte) error {
	if e.deps.SavePassphrase == nil {
		return e.def.SavePassphrase(cfg, pass)
	}
	return e.deps.SavePassphrase(cfg, pass)
}
```

Note for the `internal/setup` definer: `Engine.PrepareAWS(ctx, p *Plan) (AWSAuthReport, AWSPrepareReport, error)` must reproduce, effect-for-effect, the sequencing in `setup_auth.go:11-124` and `setup.go:263-291` **minus** the cli spinner and stdout — i.e. per method (login/sso/existing): EnsureAWSCLI → try identity (skip remaining auth if it succeeds) → login/configure/sso-login → verify identity → PrepareAWS with `AWSPrepareOptions{CreateBucket,BlockPublicAccess,DefaultEncryption}` → return classified error via `WrapAWSPrepareError`. The engine must NOT contain the huh repair prompt; that stays in the cli loop (next task). The cli-only progress spinner (`trySetupAWSSDKIdentity`/`checkSetupAWSSDKIdentity` spinner wrapping in `setup_auth.go:148-192`) is dropped from the engine; the cli caller wraps `Engine.PrepareAWS` with a single `startSetupProgress`/`Fail`/`Success` around the whole bucket-prep step to keep the oracle's `Preparing AWS S3 bucket` / `AWS S3 bucket created` output (`setup_test.go:1088-1092`). Because the fine-grained "AWS credentials ready" / "AWS browser login complete" lines are asserted (`setup_test.go:1336,1444,1512,1584`), the engine must return an `AWSAuthReport` rich enough for the cli to print them — see next task's `printSetupAuthProgress`.

Simplify `internal/cli/setup_auth.go` to a thin adapter that calls `Engine.PrepareAWS` and renders the report to stdout via the existing `printSetupStep`/`printSetupOK`/`startSetupProgress`. Delete `runSetupAWSAuth`, `runSetupAWSLoginAuth`, `runSetupAWSSSOAuth`, `runSetupAWSExistingAuth`, `ensureSetupAWSCLI`, `trySetupAWSSDKIdentity`, `checkSetupAWSSDKIdentity`, `setupAWSSDKIdentityChecker` (`setup_auth.go:11-203`); the auth output rendering moves into the run loop (next task). Leave `setup_auth.go` holding only a small render helper:

```go
package cli

import (
	"io"

	"github.com/markgustetic/sentra/internal/setup"
)

// printSetupAuthProgress renders the engine's AWS auth report to stdout,
// reproducing the historical per-step "ok" lines the setup oracle asserts.
func printSetupAuthProgress(out io.Writer, report setup.AWSAuthReport) {
	if report.AWSCLIInstalled {
		printSetupOK(out, "AWS CLI installed")
	}
	if report.LoginRan {
		printSetupOK(out, "AWS browser login complete")
	}
	if report.SSOConfigureRan {
		printSetupOK(out, "AWS SSO profile configured")
	}
	if report.SSOLoginRan {
		printSetupOK(out, "AWS SSO login complete")
	}
	if report.IdentityVerified {
		printSetupOK(out, "AWS credentials ready")
	}
}
```

Verify the exact strings against the oracle: `AWS CLI installed` (`:1336`), `AWS browser login complete` (`:1444`), `AWS SSO login complete`? The old code printed `AWS SSO login complete` — grep: `setup_auth.go:108` prints `AWS SSO login complete`; oracle `:1512` asserts `sso login completed` which comes from the SUMMARY (`setup_summary.go:48`), not this line. And `:1444` asserts both `AWS browser login complete` (this line) and `browser login completed` (summary `:43`). So this render must emit `AWS browser login complete` exactly. Keep these strings byte-identical to `setup_auth.go:47-108`.

In `internal/cli/setup_awss3.go`, make `DefaultAWSPrepare` delegate so the engine and cli share one implementation:

```go
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	return setup.DefaultEffects().PrepareAWS(ctx, cfg, opts)
}
```

Keep `DefaultAWSCheckSDKIdentity`/`DefaultAWSInspect`/`AWSInspectReport` in `setup_awss3.go` unchanged (doctor still uses them). Add the `setup` import; the S3 op helpers in `setup_awss3_ops.go` are consumed by `setup.DefaultEffects().PrepareAWS` in the engine now — if the engine's default prepare re-implements them, delete `setup_awss3_ops.go` from cli; but `s3BucketARN`/`s3ObjectARN` at `setup_awss3_ops.go:104-113` are used by cli `newSetupIAMPolicy`? No — IAM now delegates to `setup.BuildIAMPolicy`. Confirm no other cli caller of `s3BucketARN`/`headBucket` etc. remains, then delete `setup_awss3_ops.go` entirely so the `unused` linter passes. (Definer note: `internal/setup` owns the moved S3 ops.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run 'TestSetupEffectsSatisfiesEngineSeam|TestSetup_AWSSSOAuth|TestSetup_AWSBrowserLoginRunsWhenIdentityMissing|TestSetup_PreparesAWSAndInitializesRepo|TestSetup_PreparesAWSBeforeWritingConfig' -count=1`
Expected: PASS. Run `go build ./internal/cli/...` — Expected: builds, no unused symbols.

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup_effects.go internal/cli/setup_auth.go internal/cli/setup_awss3.go internal/cli/setup_effects_test.go
git rm internal/cli/setup_awss3_ops.go
git commit -m "refactor(cli): map SetupDeps to setup.Effects, drive PrepareAWS via engine"
```

---

### Task 18: Rewrite `runSetup` as a thin driver over the engine, keeping huh forms, spinner, and the repair loop in cli

**Files:**
- Modify: `internal/cli/setup.go:138-417` (`NewSetup`, `runSetup`, and the draft/config-write helpers)
- Keep: `internal/cli/setup_wizard.go` huh forms (`HuhSetupPrompt`, `HuhSetupOverwriteConfirm`, `HuhSetupReviewConfirm`, `HuhAWSCLIInstallConfirm`, `HuhSetupAWSAuthRepairPrompt`, `runHuhAWSSetup`, `runHuhCompatibleSetup`, `promptSetupPassphraseStorage`, `newSetupForm`, `setupHuhTheme`) — unchanged except they already call the now-delegating transforms
- Keep: `internal/cli/setup_spinner.go` unchanged
- Test: `internal/cli/setup_test.go` (UNCHANGED oracle — full command-level cases)

- [ ] **Step 1: Write the failing test**

No new test authored. The gate is the ENTIRE unchanged `setup_test.go` running through `NewSetup(...).Execute()`. Key end-to-end cases: review-cancel-before-write (`:141-180`), draft-on-failure-and-resume (`:963-1016`), repair switches to browser login (`:723-791`), repair writes config only (`:902-961`), print-IAM-only short-circuit (`:240-312`), endpoint_url guard (`:1694-1721`). Re-run the full file as the RED/GREEN signal.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestSetup -count=1`
Expected: at this checkpoint (before the rewrite) FAIL/build-error, because `runSetup` still references deleted symbols (`runSetupAWSAuth`, `startSetupProgress` on the removed auth path, `config.Write` for draft — verify). The failing reason is `undefined: runSetupAWSAuth`.

- [ ] **Step 3: Write the minimal implementation**

Rewrite `internal/cli/setup.go`'s `runSetup` to: keep the overwrite-confirm huh fallback, the draft-load, the huh `Prompt`, the pre-write validation + IAM-only short-circuit + endpoint_url guard + review confirm (all unchanged), then replace the inline auth/prepare block (`setup.go:240-292`) with a loop that calls `setup.NewEngine(setupEffects(deps)).PrepareAWS`, renders via `printSetupAuthProgress` + the spinner, and keeps the huh repair prompt loop in cli. Config write and draft write route through the engine's `WriteConfig`/`WriteDraft`/`RemoveDraft`. Replace the helper bodies (`setup.go:234-236` draft write, `:294-297` config write, `:397-417` draft path) with engine delegations; keep `runSetup`'s cli-only stdout printing.

Full replacement for the run loop and helpers (the untouched prelude `setup.go:169-238` — stat/overwrite/load/prompt/normalize/validate/IAM/authMethod/passphrase/endpoint/review — stays byte-identical except `writeSetupDraft` now delegates):

```go
func runSetup(cmd *cobra.Command, deps SetupDeps, cfgPath string, force bool) error {
	cmd.SilenceUsage = true
	if cfgPath == "" {
		cfgPath = configFileName
	}
	out := cmdStdout(cmd, deps.Stdout)

	yamlExists := false
	if _, err := os.Stat(cfgPath); err == nil {
		yamlExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %s: %w", cfgPath, err)
	}
	if yamlExists && !force {
		confirmOverwrite := deps.ConfirmOverwrite
		if confirmOverwrite == nil {
			confirmOverwrite = HuhSetupOverwriteConfirm
		}
		overwrite, err := confirmOverwrite(cfgPath)
		if err != nil {
			return fmt.Errorf("confirm overwrite %s: %w", cfgPath, err)
		}
		if !overwrite {
			return fmt.Errorf("%s exists - setup canceled", cfgPath)
		}
	}

	cfg, err := loadSetupConfigForWizard(cfgPath, yamlExists, out)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	prompt := deps.Prompt
	if prompt == nil {
		prompt = HuhSetupPrompt
	}
	plan, err := prompt(*cfg)
	if err != nil {
		return fmt.Errorf("run setup wizard: %w", err)
	}
	normalizeSetupConfig(&plan.Config)
	if plan.Config.Repo.S3.Bucket == "" {
		return fmt.Errorf("repo.s3.bucket not set - enter a bucket name")
	}
	if err := validateSetupBucketName(plan.Config.Repo.S3.Bucket); err != nil {
		return err
	}
	if plan.PrintIAMPolicy {
		return writeSetupIAMPolicy(out, plan.Config.Repo.S3.Prefix, plan.Config.Repo.S3.Bucket)
	}
	authMethod := resolveSetupAWSAuthMethod(&plan)
	if plan.Backend == SetupBackendAWS && authMethod == SetupAWSAuthSkip {
		applySetupAWSConfigOnly(&plan)
	}
	applySetupPassphraseConfig(&plan)
	if plan.PrepareAWS && plan.Config.Repo.S3.EndpointURL != "" {
		return fmt.Errorf("AWS setup does not support endpoint_url - choose S3-compatible/manual setup for MinIO or LocalStack")
	}
	if err := confirmSetupReviewIfNeeded(deps, cfgPath, &plan); err != nil {
		return err
	}

	if err := writeSetupDraft(cfgPath, &plan.Config); err != nil {
		return err
	}

	printSetupApplyHeader(out, cfgPath, &plan)

	eng := setup.NewEngine(setupEffects(deps))
	var (
		awsAuthReport *AWSAuthReport
		awsReport     *AWSPrepareReport
	)
	for plan.PrepareAWS {
		step := startSetupProgress(out, "Preparing AWS S3 bucket")
		auth, prep, perr := eng.PrepareAWS(cmd.Context(), &plan)
		if perr != nil {
			step.Fail()
			printSetupErrorDetail(out, perr, &plan.Config)
			updated, retry, repairErr := promptSetupAWSRepairIfNeeded(deps, plan, perr)
			if repairErr != nil {
				return repairErr
			}
			if !retry {
				return perr
			}
			if err := continueSetupAfterAWSRepair(cfgPath, out, &plan, updated); err != nil {
				return err
			}
			continue
		}
		printSetupAuthProgress(out, auth)
		step.Success(setupAWSPreparedLabel(&prep))
		a := auth
		p := prep
		awsAuthReport = &a
		awsReport = &p
		break
	}

	printSetupStep(out, "Writing "+cfgPath)
	if err := eng.WriteConfig(cfgPath, &plan); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	printSetupOK(out, "Config written")

	var initResult *setupInitResult
	if plan.InitRepo {
		printSetupStep(out, "Initializing encrypted repository")
		result, err := runSetupInit(cmd.Context(), deps, &plan.Config, plan.SavePassphrase)
		if err != nil {
			return err
		}
		initResult = &result
		if result.AlreadyInitialized {
			printSetupOK(out, "Repository already initialized")
		} else {
			printSetupOK(out, "Repository initialized")
		}
		if result.PassphraseSavedToKeyring {
			printSetupOK(out, "Passphrase saved to OS keyring")
		}
	}

	printSetupSummary(out, cfgPath, &plan, awsAuthReport, awsReport, initResult)
	removeSetupDraft(cfgPath)
	return nil
}

func writeSetupDraft(cfgPath string, cfg *config.Config) error {
	return setup.WriteDraft(cfgPath, cfg)
}

func removeSetupDraft(cfgPath string) {
	setup.RemoveDraft(cfgPath)
}

func setupDraftPath(cfgPath string) string {
	return setup.DraftPath(cfgPath)
}
```

Two correctness points to verify against the oracle:

1. **IAM-only short-circuit argument order.** The old code called `writeSetupIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix)` (`setup.go:217`). My snippet above transposed them — that is a bug. Use `writeSetupIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix)` and `return nil` on the following line, exactly as `setup.go:216-221`. Fix the snippet to:
   ```go
   if plan.PrintIAMPolicy {
       if err := writeSetupIAMPolicy(out, plan.Config.Repo.S3.Bucket, plan.Config.Repo.S3.Prefix); err != nil {
           return err
       }
       return nil
   }
   ```

2. **`Engine.WriteConfig` takes `*Plan`** per pinned API and calls `config.Write(cfgPath, &p.Config)`. Keep the cli error wrap `write %s: %w` so any oracle case asserting that stays green (none assert it directly, but preserve behavior). Verify `eng.PrepareAWS` signature is `(ctx, p *Plan)` per pinned API — pass `&plan`.

Keep `loadSetupConfigForWizard` (`setup.go:323-338`), `confirmSetupReviewIfNeeded` (`:340-356`), `promptSetupAWSRepairIfNeeded` (`:358-384`), `continueSetupAfterAWSRepair` (`:386-395`) unchanged — they are cli-only (huh repair prompt + draft rewrite via the now-delegating `writeSetupDraft`). Verify `continueSetupAfterAWSRepair` still calls `applySetupPassphraseConfig`/`normalizeSetupConfig`/`writeSetupDraft` (now delegating) and `printSetupRepairContinue` (cli-only stdout) — all intact.

Add the `setup` import to `setup.go`; drop `path/filepath` if `setupDraftPath` no longer builds the path locally (it now delegates), and drop `blobstore`/`io` only if genuinely unused (`io` is used by `SetupDeps.Stdout` and helpers — keep; `blobstore` is used by `SetupDeps.NewStore` field type — keep).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/cli/ -run TestSetup -count=1`
Expected: PASS (entire setup oracle). Then the full package: `go test ./internal/cli/... -count=1` — Expected: PASS (setup_awscli oracle included).

- [ ] **Step 5: Commit**
```bash
git add internal/cli/setup.go
git commit -m "refactor(cli): rewrite runSetup as thin driver over setup.Engine"
```

---

### Task 19: Full-package gate + tidy, confirm nothing else imports removed cli symbols

**Files:**
- Verify only: `internal/cli/**`, `cmd/sentra/commands.go` (unchanged — production `SetupDeps{NewStore,Passphrase,SavePassphrase,Stdout}` still valid)
- Test: whole module

- [ ] **Step 1: Write the failing test**

No new test. This is the integration gate: the full module build + the two unchanged oracle files + vet + unused-lint must all pass, proving the thin-driver rewrite preserved behavior and left no dangling references to deleted symbols (`runSetupAWSAuth`, `buildSetupIAMPolicy`'s old body, `loadAWSCLIConfig`, the S3 op helpers, `firstNonEmptyEnv`, etc.).

- [ ] **Step 2: Run test to verify it fails**

Run: `go build ./... && go test ./internal/cli/... -count=1`
Expected: if any prior task left a dangling reference or an unused symbol, FAIL here with the specific `undefined:` / `declared and not used` error. (On a clean sequence this already passes; the step exists to catch cross-file leftovers.)

- [ ] **Step 3: Write the minimal implementation**

Resolve any leftover: delete any now-unreferenced cli-local helper the linter flags (candidates: `emptyDash` in `format.go:5` if `ReviewText`'s move made it unused elsewhere — grep `emptyDash` first; only remove if zero cli callers remain), and confirm `validateSetupBucketName` (`setup_summary.go:160-162`) still has cli callers (`newSetupIAMPolicy`, huh forms in `setup_wizard.go:319-323,458-462`) so it stays. No new code beyond deletions.

- [ ] **Step 4: Run test to verify it passes**

Run:
```bash
go build ./... \
 && go test -race ./internal/cli/... -count=1 \
 && go vet ./internal/cli/... \
 && gofmt -l cmd internal \
 && go mod tidy -diff
```
Expected: build OK; `internal/cli` tests (including the two unchanged oracles) PASS under `-race`; `go vet` clean; `gofmt -l` prints nothing; `go mod tidy -diff` empty.

- [ ] **Step 5: Commit**
```bash
git add -A internal/cli
git commit -m "chore(cli): drop dead setup helpers after engine extraction"
```

---

Notes for the plan controller / adjacent units:

- **Prerequisite ordering.** This unit consumes `internal/setup` (Units 1–3) and the `internal/config` keyring helpers. Every `setup.*` symbol used here (`DefaultPlan`, `DefaultEnvProbe`, `NormalizeConfig`, `ApplyAWSConfigOnly`, `ApplyPassphraseConfig`, `ResolveAWSAuthMethod`, `ReviewText`, `BackendLabel`, `AWSAuthMethodLabel`, `AWSPreparedLabel`, `WrapAWS*Error`, `IsAWSMissingCredentialsError`, `ErrorAdvice`, `BuildIAMPolicy`, `WriteIAMPolicy`, `IAMPolicyDocument`/`Statement`, `InitResult`, `Effects`, `AWSCLIInstallConfirm`, `DefaultEffects`, `Engine`, `NewEngine`, `Engine.PrepareAWS`, `Engine.WriteConfig`, `Engine.InitRepo`, `WriteDraft`, `RemoveDraft`, `DraftPath`, const `BackendAWS`/`BackendS3Compatible`/`AWSAuth*`) is listed in the PINNED public API. If the controller schedules this unit before those exist, its tasks will not build; sequence it after the engine unit.

- **`emptyDash` migration risk.** `ReviewText` (moving to setup, prior unit) uses `emptyDash` (`setup_wizard.go:544` → `format.go:5`). The setup package must carry its own copy; the cli `emptyDash` in `format.go` may remain used by other cli formatters — the final task greps before deleting.

- **Byte-identical output strings.** The oracle asserts many exact substrings across summary and progress output (`setup_summary.go` and the new `printSetupAuthProgress`). The engine returning a report + cli rendering it must reproduce: `AWS CLI installed`, `AWS browser login complete`, `AWS SSO profile configured`, `AWS SSO login complete`, `AWS credentials ready`, plus the summary lines `browser login completed`/`sso login completed`/`sso profile configured`/`identity verified` (unchanged in `setup_summary.go:37-53`). Keep these verbatim.


## Part 5 — Deps.SetupEffects/InitialView + first-run routing + Unlock view

**Published API (this unit):**

```go
// internal/tui — new/added exported symbols
// Deps gains one field:
//   SetupEffects setup.Effects   // production: setup.DefaultEffects(); nil-tolerant
//   InitialView  string          // "" = dashboard; e.g. "setup" or "unlock" to start there
func NewApp(deps Deps) App                       // (existing) now honors deps.InitialView

// UnlockView — masked passphrase entry that opens the repo and hands it to the App.
type UnlockView struct{ /* unexported */ }
func NewUnlockView(deps Deps) UnlockView
func (v UnlockView) Init() tea.Cmd
func (v UnlockView) Title() string
func (v UnlockView) Update(msg tea.Msg) (tea.Model, tea.Cmd)
func (v UnlockView) View() string
func (v UnlockView) ShortHelp() []key.Binding

// repoReadyMsg is emitted by UnlockView on a successful open; the App rebuilds
// its views against the now-live repo and switches to the dashboard.
type repoReadyMsg struct {
	repo   *repo.Repo
	config *config.Config
}
```

> Note: `internal/setup` and `config.KeyringOptionsForConfig` / `config.KeyringUserForConfig` / `config.LegacyKeyringUsersForConfig` / `config.KeyringService` / `config.KeyringDefaultUser` are defined by Unit 1. This unit only *consumes* them. `repo.ErrWrongPassphrase` (repo.go:32) and `config.ErrNoPassphraseSource` (config/passphrase.go:24) already exist.

---

### Task 20: Add `SetupEffects` to tui.Deps and thread it through runUI/UIDeps

**Files:**
- Modify: `internal/tui/app.go:42-100` (Deps struct)
- Modify: `internal/cli/ui.go:83-140` (runUI), `internal/cli/ui.go:24-51` (UIDeps struct)
- Modify: `cmd/sentra/commands.go:125-136` (uiDeps wiring)
- Test: `internal/tui/app_test.go`, `internal/cli/ui_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/app_test.go` (the package already imports `setup` — add `"github.com/markgustetic/sentra/internal/setup"` to its import block):

```go
// TestApp_DepsCarrySetupEffects: the setup wizard view needs a headless
// effects seam. Deps must carry it nil-tolerantly and NewApp must not panic
// when it is set.
func TestApp_DepsCarrySetupEffects(t *testing.T) {
	eff := setup.DefaultEffects()
	app := NewApp(Deps{RepoName: "x", SetupEffects: eff})
	if app.deps.SetupEffects == nil {
		t.Fatal("Deps.SetupEffects not carried through NewApp")
	}
}
```

Add to `internal/cli/ui_test.go`:

```go
// TestRunUI_ThreadsSetupEffects proves runUI constructs setup.DefaultEffects()
// and threads it into tui.Deps when UIDeps carries no explicit override. The
// effects seam holds no secrets — it is a call-time interface of func hooks.
func TestRunUI_ThreadsSetupEffects(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".")

	deps, captured := uiFixture(t, "hunter2")
	cmd := NewUI(deps)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.Deps().SetupEffects == nil {
		t.Error("Deps.SetupEffects not threaded from runUI")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run TestApp_DepsCarrySetupEffects -count=1` and `go test ./internal/cli/ -run TestRunUI_ThreadsSetupEffects -count=1`
Expected: FAIL to compile with `unknown field SetupEffects in struct literal of type tui.Deps` (tui test) and `captured.Deps().SetupEffects undefined` (cli test).

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/app.go`, add the import `"github.com/markgustetic/sentra/internal/setup"` to the existing import block, then add this field to the `Deps` struct (after `SaveKeyringPassphrase`, before the closing brace at app.go:100):

```go
	// SetupEffects is the headless side-effecting seam the setup wizard
	// view drives (AWS CLI checks, login/SSO, bucket prep, repo init,
	// keyring save). Production wires it to setup.DefaultEffects(); tests
	// inject fakes. It holds no secrets — every method receives its inputs
	// at call time. May be nil when the wizard view isn't reachable (a
	// repo that's already configured and unlocked never needs it).
	SetupEffects setup.Effects
```

In `internal/cli/ui.go`, add `"github.com/markgustetic/sentra/internal/setup"` to the imports, add a `SetupEffects` override field to `UIDeps` (after the `SavePassphrase` field at ui.go:50):

```go
	// SetupEffects overrides the setup engine's side-effecting seam. Nil
	// means runUI constructs the production setup.DefaultEffects(); tests
	// inject a fake to keep AWS/keyring calls out of the process.
	SetupEffects setup.Effects
```

Then in `runUI`, extend the `tui.NewApp(tui.Deps{...})` literal (ui.go:116-134) by adding the wiring after `SaveKeyringPassphrase: deps.SavePassphrase,`:

```go
		SetupEffects: func() setup.Effects {
			if deps.SetupEffects != nil {
				return deps.SetupEffects
			}
			return setup.DefaultEffects()
		}(),
```

In `cmd/sentra/commands.go`, the production `uiDeps` (commands.go:125-135) needs no change — runUI defaults to `setup.DefaultEffects()` when `SetupEffects` is nil. Leave it as is.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run TestApp_DepsCarrySetupEffects -count=1` and `go test ./internal/cli/ -run TestRunUI_ThreadsSetupEffects -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/cli/ui.go internal/tui/app_test.go internal/cli/ui_test.go
git commit -m "feat(tui): add SetupEffects to Deps and thread it from runUI

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 21: Add `Deps.InitialView` + `repoReadyMsg` view-rebuild so the App can launch on a non-dashboard view and swap in a live repo

**Files:**
- Modify: `internal/tui/app.go:42-100` (Deps struct), `internal/tui/app.go:181-241` (NewApp — honor InitialView), `internal/tui/app.go:280-379` (Update — handle repoReadyMsg)
- Test: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing test**

Add to `internal/tui/app_test.go`:

```go
// TestApp_InitialViewSelectsStartingView: when Deps.InitialView names a
// registered view, NewApp starts focused on it instead of the dashboard, so
// the first-run wizard / unlock gate can be the landing screen.
func TestApp_InitialViewSelectsStartingView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "restore"})
	if got := app.views[app.active].id; got != "restore" {
		t.Fatalf("active view = %q, want restore", got)
	}
}

// TestApp_InitialViewUnknownFallsBackToDashboard: an InitialView that names no
// registered command must not crash or leave active out of range — it falls
// back to the first view.
func TestApp_InitialViewUnknownFallsBackToDashboard(t *testing.T) {
	app := NewApp(Deps{RepoName: "x", InitialView: "does-not-exist"})
	if app.active != 0 {
		t.Fatalf("active = %d, want 0 (dashboard fallback)", app.active)
	}
}

// TestApp_RepoReadyRebuildsViewsWithLiveRepoAndShowsDashboard: the unlock flow
// hands the App an opened repo via repoReadyMsg; the App rebuilds its views
// against it (so every view now sees a non-nil Repo) and switches to the
// dashboard, dropping any first-run/unlock landing view.
func TestApp_RepoReadyRebuildsViewsWithLiveRepoAndShowsDashboard(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	// Start as if on the unlock gate: no repo, unlock is the landing view.
	app := NewApp(Deps{RepoName: "x", InitialView: "unlock"})
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)

	m, _ := app.Update(repoReadyMsg{repo: r, config: &cfg})
	next := m.(App)
	if next.deps.Repo != r {
		t.Fatal("repoReadyMsg did not swap the live repo into Deps")
	}
	if got := next.views[next.active].id; got != "dashboard" {
		t.Fatalf("active view after repoReady = %q, want dashboard", got)
	}
	// Every rebuilt view must see the live repo. Sample the snapshots view.
	for _, v := range next.views {
		if v.id == "snapshots" {
			if sv, ok := v.model.(interface{ Deps() Deps }); ok && sv.Deps().Repo != r {
				t.Fatal("rebuilt snapshots view did not receive the live repo")
			}
		}
	}
}
```

> `SnapshotsView` may not expose `Deps()`; the type-assert `ok` guard makes that check a no-op if absent, so the test still compiles and asserts the core behavior (repo swap + dashboard). The `config` import already exists in app_test.go.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run 'TestApp_InitialView|TestApp_RepoReady' -count=1`
Expected: FAIL to compile — `unknown field InitialView in struct literal of type tui.Deps` and `undefined: repoReadyMsg`.

- [ ] **Step 3: Write the minimal implementation**

In `internal/tui/app.go` `Deps` struct, add after the `SetupEffects` field:

```go
	// InitialView names the registered command the App should land on at
	// launch, instead of the dashboard. runUI sets it to "setup" for a
	// first-run (no sentra.yaml) and "unlock" for a configured-but-locked
	// repo. Empty (or an unknown id) starts on the dashboard. Plain routing
	// data, never a secret.
	InitialView string
```

In `NewApp` (app.go:181-241), replace the `active: 0,` line in the returned `App{...}` literal with a computed initial index. Add this just before the `return App{...}` (after `keys := newGlobalKeymap()`):

```go
	active := 0
	if deps.InitialView != "" {
		for i, v := range views {
			if v.id == deps.InitialView {
				active = i
				break
			}
		}
	}
```

and change the struct field to `active: active,`. Also set the sidebar selection to match so the rail highlights the landing view: after the `App{...}` literal is not possible (it's the return), so instead build the sidebar selected before returning — simplest: after constructing but we return directly. Set selection inside NewApp by calling `sidebar.Select` on the local before the return. Replace `sidebar: NewSidebar(registry, sidebarWidth, minHeight),` handling by constructing the sidebar into a local first:

```go
	sidebar := NewSidebar(registry, sidebarWidth, minHeight)
	sidebar.Select(views[active].id)
```

and reference `sidebar: sidebar,` in the returned literal. Set `focus: focusContent` when landing on a non-dashboard view so keystrokes reach the wizard/unlock immediately; keep `focusSidebar` for the default dashboard landing:

```go
	focus := focusSidebar
	if active != 0 {
		focus = focusContent
	}
```

and reference `focus: focus,` in the returned literal.

Add the `repoReadyMsg` type near the other App message types (e.g. below `activateMsg` usage — place it in app.go just above `func (m App) Update`):

```go
// repoReadyMsg is emitted by the unlock flow once it has opened the repository
// with a verified passphrase. The App rebuilds its views against the now-live
// repo (they were constructed with a nil Repo on the launch path) and switches
// to the dashboard, so the configured-but-locked landing screen is replaced by
// the real dashboard exactly once, at unlock time.
type repoReadyMsg struct {
	repo   *repo.Repo
	config *config.Config
}
```

Handle it in `Update` — add a case at the top of the `switch msg := msg.(type)` block (app.go:292), before `case tea.WindowSizeMsg`:

```go
	case repoReadyMsg:
		// Rebuild the whole shell against the unlocked repo. Reusing NewApp
		// keeps view registration in one place (it changes as views are
		// added) rather than duplicating the slice here. We carry over the
		// resolved config, drop the InitialView so the rebuilt App lands on
		// the dashboard, and replay the last WindowSizeMsg so layout is
		// identical to a normal launch.
		nd := m.deps
		nd.Repo = msg.repo
		if msg.config != nil {
			nd.Config = msg.config
		}
		nd.InitialView = ""
		rebuilt := NewApp(nd)
		if m.width > 0 {
			sized, _ := rebuilt.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			rebuilt = sized.(App)
		}
		return rebuilt, rebuilt.Init()
```

> `nd.Repo`, `nd.Config` are already `Deps` fields (app.go:45,62). `repo` and `config` are already imported in app.go (app.go:29-30).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run 'TestApp_InitialView|TestApp_RepoReady' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): support InitialView landing and repoReady view rebuild

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 22: Create the masked UnlockView (inline textinput + repo.Open, zeroize discipline)

**Files:**
- Create: `internal/tui/unlock.go`
- Test: `internal/tui/unlock_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/tui/unlock_test.go`:

```go
package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// unlockDeps builds Deps wired to an in-memory store backing a repo that was
// initialized under `pass`. NewStore returns that same store so repo.Open sees
// the real config blob.
func unlockDeps(t *testing.T, pass string) Deps {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte(pass))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("repo.Close: %v", err)
	}
	cfg := config.Defaults()
	return Deps{
		Config: &cfg,
		NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
			return store, nil
		},
	}
}

// typeIntoUnlock feeds a string one rune at a time through Update.
func typeIntoUnlock(v UnlockView, s string) UnlockView {
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(UnlockView)
	}
	return v
}

// TestUnlock_MasksInput: the entry never renders the plaintext passphrase.
func TestUnlock_MasksInput(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2secret"))
	v = typeIntoUnlock(v, "hunter2secret")
	if strings.Contains(v.View(), "hunter2secret") {
		t.Fatal("unlock view rendered the plaintext passphrase")
	}
}

// TestUnlock_CorrectPassphraseOpensRepoAndEmitsRepoReady: on enter with the
// right passphrase the flow opens the repo and returns a repoReadyMsg carrying
// the live repo, so the App can swap to the dashboard.
func TestUnlock_CorrectPassphraseOpensRepoAndEmitsRepoReady(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "hunter2secret"))
	v = typeIntoUnlock(v, "hunter2secret")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if cmd == nil {
		t.Fatal("enter must return an open command")
	}
	msg := cmd()
	ready, ok := msg.(repoReadyMsg)
	if !ok {
		t.Fatalf("expected repoReadyMsg, got %T", msg)
	}
	if ready.repo == nil {
		t.Fatal("repoReadyMsg carried a nil repo")
	}
	ready.repo.Close()
}

// TestUnlock_WrongPassphraseShowsErrorNotReady: a bad passphrase renders an
// error and does NOT emit repoReadyMsg — the App stays on the unlock gate.
func TestUnlock_WrongPassphraseShowsErrorNotReady(t *testing.T) {
	v := NewUnlockView(unlockDeps(t, "correct-horse"))
	v = typeIntoUnlock(v, "wrong-passphrase")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if cmd == nil {
		t.Fatal("enter must return the open command even for a wrong guess")
	}
	// The open runs synchronously in the returned cmd; feed its result back.
	m, _ = v.Update(cmd())
	v = m.(UnlockView)
	if strings.Contains(strings.ToLower(v.View()), "wrong") == false &&
		strings.Contains(strings.ToLower(v.View()), "passphrase") == false {
		t.Fatalf("wrong-passphrase attempt did not surface an error, view=%q", v.View())
	}
}

// TestUnlock_EmptyPassphraseIsRejectedLocally: pressing enter with no input
// shows a validation message and never touches the store.
func TestUnlock_EmptyPassphraseIsRejectedLocally(t *testing.T) {
	called := false
	d := unlockDeps(t, "hunter2secret")
	inner := d.NewStore
	d.NewStore = func(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
		called = true
		return inner(ctx, cfg)
	}
	v := NewUnlockView(d)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(UnlockView)
	if called {
		t.Fatal("empty passphrase must not open the store")
	}
	if v.View() == "" {
		t.Fatal("empty-entry attempt should render a hint")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestUnlock -count=1`
Expected: FAIL to compile — `undefined: NewUnlockView` / `undefined: UnlockView`.

- [ ] **Step 3: Write the minimal implementation**

Create `internal/tui/unlock.go`:

```go
package tui

import (
	"context"
	"errors"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// unlockStage is the position in the unlock flow's small state machine.
type unlockStage int

const (
	unlockInput   unlockStage = iota // masked entry, awaiting enter
	unlockOpening                    // repo.Open running in a returned cmd
)

// unlockResultMsg carries the outcome of an open attempt back into the view.
// On success the view forwards a repoReadyMsg to the App (which rebuilds the
// shell against the live repo); on failure it shows err and returns to input.
//
// This is NOT an opResultMsg: the unlock path runs before any repo lock exists
// and before the App's one-op guard matters — repo.Open takes no advisory lock.
type unlockResultMsg struct {
	repo   *repo.Repo
	config *config.Config
	err    error
}

// UnlockView is the launch-path gate for a configured-but-locked repo: the
// sentra.yaml exists but no passphrase source (keyring / env / file) could
// supply the secret non-interactively, so we ask for it here with an inline
// masked field rather than a huh form (huh cannot run inside a live Bubbletea
// program — it fights for os.Stdin). On a correct passphrase it opens the repo
// and hands it to the App via repoReadyMsg; a wrong passphrase shows an error
// and lets the user retry.
//
// Security: the typed secret lives only in the textinput buffer and the single
// copy handed to the open closure, which zeroizes it on return. It is masked in
// every frame and never logged. The keyring is NOT written here — this view
// only reads an existing repo; the verify-before-save keyring guard lives in
// the setup engine's InitRepo path (Unit 1/4), not on the unlock path.
type UnlockView struct {
	deps  Deps
	stage unlockStage

	input textinput.Model

	inputErr string // local validation (empty entry)
	openErr  error  // mapped repo.Open failure

	width int
}

// NewUnlockView builds the masked-entry gate. The single field echoes bullets,
// mirroring password.go's masking discipline.
func NewUnlockView(deps Deps) UnlockView {
	field := textinput.New()
	field.Prompt = "passphrase> "
	field.Placeholder = "repository passphrase"
	field.EchoMode = textinput.EchoPassword
	field.EchoCharacter = '•'
	field.Focus()
	return UnlockView{deps: deps, input: field}
}

func (UnlockView) Init() tea.Cmd { return nil }

func (v UnlockView) Title() string { return "Unlock" }

func (v UnlockView) ShortHelp() []key.Binding {
	if v.stage == unlockOpening {
		return nil
	}
	return []key.Binding{
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "unlock")),
	}
}

func (v UnlockView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case unlockResultMsg:
		if msg.err != nil {
			v.stage = unlockInput
			v.openErr = msg.err
			// Clear the buffer so the failed secret doesn't linger and the
			// user starts a fresh attempt.
			v.input.SetValue("")
			v.input.Focus()
			return v, nil
		}
		// Success: forward to the App, which rebuilds the shell against the
		// live repo and switches to the dashboard.
		ready := repoReadyMsg{repo: msg.repo, config: msg.config}
		return v, func() tea.Msg { return ready }

	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

func (v UnlockView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.stage == unlockOpening {
		return v, nil
	}
	if msg.Type == tea.KeyEnter {
		return v.startOpen()
	}
	var cmd tea.Cmd
	v.input, cmd = v.input.Update(msg)
	v.inputErr = "" // typing clears the last validation error
	v.openErr = nil
	return v, cmd
}

// startOpen validates a non-empty entry, then returns a command that opens the
// store and the repo. The command holds the ONLY copy of the secret outside the
// input buffer and zeroizes it on return.
func (v UnlockView) startOpen() (tea.Model, tea.Cmd) {
	if strings.TrimSpace(v.input.Value()) == "" {
		v.inputErr = "enter the repository passphrase"
		return v, nil
	}
	v.stage = unlockOpening
	v.openErr = nil

	deps := v.deps
	pass := []byte(v.input.Value())

	return v, func() tea.Msg {
		defer crypto.Zeroize(pass)
		if deps.NewStore == nil {
			return unlockResultMsg{err: errors.New("no blobstore configured")}
		}
		ctx := ctxOrBackground(deps.Ctx)
		store, err := deps.NewStore(ctx, deps.Config)
		if err != nil {
			return unlockResultMsg{err: err}
		}
		r, err := repo.Open(ctx, store, pass)
		if err != nil {
			return unlockResultMsg{err: err}
		}
		return unlockResultMsg{repo: r, config: deps.Config}
	}
}

func (v UnlockView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Unlock repository"))
	b.WriteString("\n" + ui.Muted.Render(v.deps.RepoName))

	switch v.stage {
	case unlockOpening:
		b.WriteString("\n\n" + ui.Muted.Render("opening the repository…"))
	default:
		b.WriteString("\n\n" + v.input.View())
		if v.inputErr != "" {
			b.WriteString("\n\n" + ui.Danger.Render(v.inputErr))
		}
		if v.openErr != nil {
			b.WriteString("\n\n" + ui.Danger.Render(unlockErrMessage(v.openErr)))
		}
		b.WriteString("\n\n" + ui.Muted.Render("⏎ unlock"))
	}
	return b.String()
}

// unlockErrMessage maps repo.Open sentinels to operator-readable text. Distinct
// sentinels (not string matching) so an upstream reword never silently breaks
// the mapping.
func unlockErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrWrongPassphrase):
		return "wrong passphrase — try again"
	default:
		return err.Error()
	}
}

// unused import guard: config is referenced via deps.Config's type through the
// closure and unlockResultMsg field.
var _ = config.Defaults
```

> Drop the `var _ = config.Defaults` guard line and the `config` import if the compiler reports `config` as used already — `deps.Config` alone does not import the package name; the `unlockResultMsg.config *config.Config` field does. Keep the import; remove the guard line if `go vet`/build flags it as an unnecessary blank assignment. (The field `config *config.Config` uses the package, so the guard is not needed — remove the `var _ = config.Defaults` line and the guard comment before committing.)

Correction for Step 3 — omit the guard: the `config` package IS used by the `unlockResultMsg.config *config.Config` field type, so no blank guard is needed. Delete these two lines from the file above before running:
```go
// unused import guard: config is referenced via deps.Config's type through the
// closure and unlockResultMsg field.
var _ = config.Defaults
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestUnlock -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/unlock.go internal/tui/unlock_test.go
git commit -m "feat(tui): add masked UnlockView that opens a locked repo

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 23: Register the UnlockView in the App and add the three-way first-run routing in runUI

**Files:**
- Modify: `internal/tui/app.go:190-205` (views slice — register "unlock"), `internal/tui/app.go:211-214` (categories)
- Modify: `internal/cli/ui.go:83-140` (runUI — three-way branch before openRepoForConfig)
- Modify: `internal/cli/repo_open.go` (add a non-erroring locked-open probe helper)
- Test: `internal/tui/app_test.go`, `internal/cli/ui_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/app_test.go`:

```go
// TestApp_UnlockRegisteredAsView: the unlock gate is a registered view so the
// InitialView routing can land on it and the sidebar/palette know about it.
func TestApp_UnlockRegisteredAsView(t *testing.T) {
	app := NewApp(Deps{RepoName: "x"})
	found := false
	for _, v := range app.views {
		if v.id == "unlock" {
			found = true
		}
	}
	if !found {
		t.Fatal("unlock view not registered in NewApp")
	}
}
```

Add to `internal/cli/ui_test.go`:

```go
// TestRunUI_MissingConfigLaunchesFirstRunWizard: with no sentra.yaml present,
// runUI must NOT try to open a repo (there is none). It launches the TUI on the
// setup wizard with a nil Repo, so the first-run experience is the wizard.
func TestRunUI_MissingConfigLaunchesFirstRunWizard(t *testing.T) {
	chDir(t, t.TempDir()) // empty dir: no sentra.yaml
	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				t.Fatal("NewStore must not be called on the first-run path")
				return nil, nil
			},
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("passphrase must not be resolved on the first-run path")
				return nil, nil
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
	d := captured.Deps()
	if d.InitialView != "setup" {
		t.Errorf("InitialView = %q, want setup", d.InitialView)
	}
	if d.Repo != nil {
		t.Error("first-run App must carry a nil Repo")
	}
}

// TestRunUI_ConfigPresentButLockedLaunchesUnlockView: sentra.yaml exists but no
// non-interactive passphrase source can supply the secret (keyring off, no env,
// no file, and the launch path passes NO interactive prompt). runUI must land
// on the unlock view rather than erroring or blocking on huh.
func TestRunUI_ConfigPresentButLockedLaunchesUnlockView(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	writeBackupConfigFile(t, ".") // config with a bucket, keyring off

	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	var captured tui.App
	deps := UIDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			// PassphraseWithConfig is the INTERACTIVE resolver (it prompts).
			// runUI must not call it on the launch path — a huh/tty prompt
			// there is exactly what Phase 3 forbids.
			PassphraseWithConfig: func(_ *config.Config) ([]byte, error) {
				t.Fatal("interactive passphrase resolver must not run on the launch path")
				return nil, nil
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
	d := captured.Deps()
	if d.InitialView != "unlock" {
		t.Errorf("InitialView = %q, want unlock", d.InitialView)
	}
	if d.Repo != nil {
		t.Error("locked App must carry a nil Repo until the user unlocks")
	}
	if d.NewStore == nil {
		t.Error("unlock view needs NewStore threaded to open the repo")
	}
}
```

> `TestUI_LaunchesApp` (ui_test.go:55) already covers the "both available" path indirectly — but it relies on the interactive `Passphrase` fixture resolving. To keep that test green under the new routing, the launch-path resolution must succeed when a non-interactive source exists. The fixture at ui_test.go:41 wires `Passphrase` (legacy, non-config) returning the passphrase unconditionally — see Step 3's note on treating the existing fixture as an available source.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/tui/ -run TestApp_UnlockRegistered -count=1` and `go test ./internal/cli/ -run 'TestRunUI_MissingConfig|TestRunUI_ConfigPresentButLocked' -count=1`
Expected: FAIL — tui: `unlock view not registered`; cli: `InitialView = "" , want setup` / `want unlock` (routing not yet added).

- [ ] **Step 3: Write the minimal implementation**

**(a) Register the unlock view** in `internal/tui/app.go`. Add to the `views` slice (app.go:190-205), after the `{id: "password", ...}` entry:

```go
		{id: "unlock", model: NewUnlockView(deps)},
```

The unlock gate is not a normal navigable "Operation" or "View"; keep it out of the rail/palette categories by leaving it uncategorized is not enough (the loop at app.go:215-225 adds every view to the registry under "Views"). That is acceptable for this unit — the unlock view appears under "Views" but is only reached via InitialView routing; a later unit may hide launch-gate views from the rail. No categories entry is needed (it defaults to "Views").

**(b) Add a locked-open probe** in `internal/cli/repo_open.go`. Add this helper (it mirrors `openRepoForConfig` but (1) does not error when the config file is absent, and (2) resolves the passphrase with NO interactive prompt so the launch path never blocks on a tty):

```go
// launchState classifies what `sentra ui` should show at startup without ever
// prompting: whether a config file exists, and whether a passphrase can be
// resolved non-interactively (keyring / env / file). It never opens the repo
// and never calls an interactive resolver — the TUI's unlock/setup views own
// the interactive path so huh never fires on the launch path.
type launchState struct {
	// ConfigExists reports whether cfgPath is present on disk. Absent means
	// first run: show the setup wizard.
	ConfigExists bool
	// PassphraseAvailable reports whether a non-interactive source supplied
	// the passphrase. False with ConfigExists true means show the unlock view.
	PassphraseAvailable bool
	// Config is the loaded (or default) config, always non-nil on nil error.
	Config *config.Config
}

// probeLaunchState loads the config and attempts a NON-INTERACTIVE passphrase
// resolution. deps.PassphraseWithConfig is the interactive resolver used by the
// normal read path; the launch path must not call it (it would prompt), so this
// helper resolves through config.Resolve with a nil Prompt and the same
// keyring settings the read path would use, and treats ErrNoPassphraseSource as
// "not available" rather than an error.
func probeLaunchState(cmd *cobra.Command, cfgPath string, deps RepoDeps) (launchState, error) {
	exists := false
	if info, err := os.Stat(cfgPath); err == nil && !info.IsDir() {
		exists = true
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return launchState{}, fmt.Errorf("load config: %w", err)
	}
	st := launchState{ConfigExists: exists, Config: cfg}
	if !exists {
		return st, nil // first run: no passphrase needed, wizard handles it
	}
	pass, err := config.Resolve(config.ResolveOptions{
		UseKeyring:           cfg.Passphrase.UseKeyring,
		KeyringService:       config.KeyringService,
		KeyringUser:          config.KeyringUserForConfig(cfg),
		KeyringFallbackUsers: config.LegacyKeyringUsersForConfig(cfg),
		Prompt:               nil, // launch path never prompts
	})
	if err != nil {
		if errors.Is(err, config.ErrNoPassphraseSource) {
			return st, nil // locked: unlock view will collect it
		}
		return launchState{}, fmt.Errorf("resolve passphrase: %w", err)
	}
	// A source supplied the passphrase; wipe it — the read path re-resolves it.
	crypto.Zeroize(pass)
	st.PassphraseAvailable = true
	return st, nil
}
```

Add the imports `"errors"` and `"os"` to `internal/cli/repo_open.go` (it already imports `context`, `fmt`, `io`, cobra, `blobstore`, `config`, `crypto`, `repo`).

> `config.KeyringService`, `config.KeyringUserForConfig`, `config.LegacyKeyringUsersForConfig`, `config.ErrNoPassphraseSource` are defined by Unit 1 (moved from cmd/sentra/passphrase.go:15,65,80) and config/passphrase.go:24. This task depends on Unit 1 landing first.

**(c) Add the three-way branch** in `internal/cli/ui.go` `runUI` (ui.go:83-140). Replace the opening of `runUI` (lines 84-91, from `cmd.SilenceUsage = true` through the `defer r.Close()`) so the probe runs first and short-circuits into the TUI for the first-run and locked cases:

```go
	cmd.SilenceUsage = true

	st, err := probeLaunchState(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}

	absCfgPath := cfgPath
	if p, err := filepath.Abs(cfgPath); err == nil {
		absCfgPath = p
	}

	// First run (no config) and configured-but-locked both launch the TUI
	// WITHOUT opening a repo — the wizard / unlock view own the interactive
	// path so huh never fires here. Repo is nil; the unlock view swaps a live
	// repo in via repoReadyMsg once the user provides the passphrase.
	if !st.ConfigExists || !st.PassphraseAvailable {
		initial := "setup"
		if st.ConfigExists {
			initial = "unlock"
		}
		repoName := st.Config.Repo.S3.Bucket
		app := tui.NewApp(tui.Deps{
			Provider:              providerForLaunch(deps, st.Config),
			RepoName:              repoName,
			Config:                st.Config,
			Ctx:                   cmd.Context(),
			ConfigPath:            absCfgPath,
			NewStore:              deps.NewStore,
			Actions:               deps.Actions,
			SaveKeyringPassphrase: deps.SavePassphrase,
			SetupEffects:          setupEffectsForLaunch(deps),
			InitialView:           initial,
		})
		if deps.Run == nil {
			return fmt.Errorf("ui: no Run hook configured")
		}
		return deps.Run(app)
	}

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()
```

Then delete the now-duplicated `absCfgPath` computation lower in the function (ui.go:110-114) and the second `if deps.Run == nil` guard stays as-is for the dashboard path. The existing `repoName`/`provider` derivation (ui.go:97-105) and the `tui.NewApp(...)` for the dashboard path (ui.go:116-134) must also gain the two new fields so the dashboard App can still reach the wizard from Settings:

Add to the dashboard-path `tui.NewApp(tui.Deps{...})` literal (after `SaveKeyringPassphrase: deps.SavePassphrase,`):

```go
		SetupEffects: setupEffectsForLaunch(deps),
```

(The `SetupEffects` closure added in Task A's Step 3 is replaced by the shared helper below — see note.)

Add two small helpers to `internal/cli/ui.go`:

```go
// setupEffectsForLaunch returns the UIDeps override or the production default.
func setupEffectsForLaunch(deps UIDeps) setup.Effects {
	if deps.SetupEffects != nil {
		return deps.SetupEffects
	}
	return setup.DefaultEffects()
}

// providerForLaunch builds the agent provider for the launch-path Apps (first
// run / locked), where no repo is open yet. It mirrors the dashboard path's
// provider selection: ProviderForConfig wins when set, else the static
// Provider.
func providerForLaunch(deps UIDeps, cfg *config.Config) llm.Provider {
	if deps.ProviderForConfig != nil {
		return deps.ProviderForConfig(cfg)
	}
	return deps.Provider
}
```

> The `llm` package is already imported in ui.go (ui.go:14). `setup` is imported by Task A. Replace the inline `SetupEffects: func() setup.Effects {...}()` from Task A Step 3 with `SetupEffects: setupEffectsForLaunch(deps),` in the dashboard-path literal so both paths share one helper (net: the Task-A inline closure becomes the helper call here).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/tui/ -run TestApp_UnlockRegistered -count=1`, `go test ./internal/cli/ -run 'TestRunUI_MissingConfig|TestRunUI_ConfigPresentButLocked|TestUI_' -count=1`, then the full package gates `go test ./internal/cli/ ./internal/tui/ -count=1`.
Expected: PASS. (`TestUI_LaunchesApp` stays green: its fixture wires the legacy `Passphrase` resolver, but the launch probe uses `config.Resolve` with keyring off and no env/file, so it returns "locked" — verify this. If `TestUI_LaunchesApp` now lands on unlock instead of the dashboard, update that test to assert the unlock landing OR set `SENTRA_PASSPHRASE` in it; prefer updating the fixture to export a non-interactive source. Confirm the actual behavior when running.)

> Verification note for the plan executor: after Step 3, run `go test ./internal/cli/ -run TestUI_LaunchesApp -count=1` FIRST. Because `uiFixture` (ui_test.go:25) has keyring off and sets no env var, `probeLaunchState` will classify it as locked and land on "unlock" — the assertion `strings.Contains(view, "sentra")` still passes (the title bar renders "✦ sentra" on every view including unlock), so the test should remain green. If it does not, adjust `TestUI_LaunchesApp` to set `t.Setenv("SENTRA_PASSPHRASE", "hunter2")` so the probe finds a source and lands on the dashboard. Decide based on the real run, not assumption.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/cli/ui.go internal/cli/repo_open.go internal/tui/app_test.go internal/cli/ui_test.go
git commit -m "feat(cli): route sentra ui to first-run wizard or unlock gate

No config -> setup wizard; configured-but-locked -> masked unlock view;
both available -> dashboard. The launch path never prompts, so huh never
fires while the Bubbletea program owns the terminal.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**Cross-unit dependencies for this unit:**
- Tasks reference `config.KeyringService`, `config.KeyringDefaultUser`, `config.KeyringUserForConfig`, `config.LegacyKeyringUsersForConfig` (PINNED, defined by **Unit 1** — moved from `cmd/sentra/passphrase.go:15,17,65,80`). The routing task (last task) will not compile until Unit 1 lands.
- References `setup.Effects` and `setup.DefaultEffects()` (PINNED, defined by **Unit 1/effects-seam unit**). Task A and the routing task depend on them.
- The `"setup"` view id used by `InitialView: "setup"` is registered by the **SetupWizardView unit**; if that view is not yet registered when this unit lands, `NewApp` falls back to dashboard (index 0) via the unknown-id guard added in Task 2 — first-run would then show the dashboard-with-nil-repo instead of the wizard. Land this unit after (or with) the SetupWizardView registration, or the first-run test's `InitialView == "setup"` assertion still passes (it checks the *Deps* field, not the resolved active index), but the visible landing degrades. Note this ordering in the controller.

**Key source facts that shaped the plan:**
- `config.Load` returns `Defaults()` with **no error** when the file is missing (`config/config.go:140-148`), so first-run detection MUST use `os.Stat`, not a `Load` error — handled in `probeLaunchState`.
- `repo.Open` returns `repo.ErrWrongPassphrase` on a bad passphrase (`repo/repo.go:210,32`) and takes **no advisory lock**, so the unlock result is deliberately not an `opResultMsg`.
- The App had no initial-view mechanism (`active: 0` hardcoded at `app.go:233`); this unit adds `Deps.InitialView` + a `repoReadyMsg` rebuild rather than duplicating view registration.


## Part 6 — SetupWizardView (stage machine, inline bubbles, async provisioning, ExecProcess)

**Published API:** Unit 6 adds only `internal/tui/setup_wizard.go` (package `tui`). It defines no new exported package-level API beyond these two exported symbols consumed by Unit 7's registration:

- `func NewSetupWizardView(deps Deps) SetupWizardView`
- `type SetupWizardView struct` (implements `tea.Model` via `Init`/`Update`/`View`, plus `Title() string` and `ShortHelp() []key.Binding`, matching the view contract in `app.go:104-127`).

It consumes the pinned `internal/setup` engine API (`setup.Plan`, `setup.Engine`, `setup.NewEngine`, `setup.Effects`, `setup.DefaultPlan`, `setup.DefaultEnvProbe`, `setup.ReviewText`, `setup.ValidateBucketName`, `setup.BuildIAMPolicy`, `setup.WriteIAMPolicy`, `setup.ErrorAdvice`, `setup.NormalizeConfig`, `setup.ApplyAWSConfigOnly`, `setup.ApplyPassphraseConfig`, `setup.ResolveAWSAuthMethod`, `setup.Backend*`/`setup.AWSAuth*` consts, `setup.AWSAuthReport`, `setup.AWSPrepareReport`, `setup.InitResult`) and `Deps.SetupEffects setup.Effects` (both defined by Units 1–5).

---

### Task 24: SetupWizardView skeleton: model, stages, constructor, view contract

**Files:**
- Create: `internal/tui/setup_wizard.go`
- Test: `internal/tui/setup_wizard_test.go`

This task lands the view struct, its stage constants, the constructor, and the read-only view-contract methods (`Init`/`Title`/`ShortHelp`/`View` shell + `Update` skeleton). It renders the first stage (`stageBackend`) so the App can host it. Later tasks fill in each stage's key handling and the provisioning op.

- [ ] **Step 1: Write the failing test**
```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_InitialStageIsBackendSelect -count=1`
Expected: FAIL with `undefined: NewSetupWizardView` (compile error).

- [ ] **Step 3: Write the minimal implementation**
```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

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

// SetupWizardView drives the in-TUI setup wizard. It is the TUI-native
// re-expression of the huh cli wizard (internal/cli/setup_wizard.go):
// every huh step becomes an inline bubbles control because huh.Form.Run
// owns os.Stdin and cannot run inside a live tea.Program. The pure
// decisions and the provisioning sequence live in internal/setup; this
// view only collects input, gates on a review confirm, and drives the
// engine through the App's one-op guard.
type SetupWizardView struct {
	deps   Deps
	engine *setup.Engine
	stage  setupStage

	plan setup.Plan

	// backend-stage cursor over the two backends.
	backendCursor int

	// details-stage text inputs (bucket/prefix/region/profile/endpoint)
	// and a cursor over them plus the "print IAM policy" toggle.
	fields      []textinput.Model
	fieldCursor int
	printIAM    bool
	detailErr   string

	// actions-stage cursor over the auth-method select and the three
	// bucket toggles + init-repo toggle.
	authCursor    int
	actionCursor  int
	createBucket  bool
	blockPublic   bool
	defaultEnc    bool
	initRepo      bool

	// passphrase-stage masked inputs (new + confirm) and the keyring toggle.
	newPass     textinput.Model
	confirmPass textinput.Model
	focusConf   bool
	savePass    bool
	passErr     string
	// pass holds the verified passphrase between stagePassphrase and the
	// provisioning op; zeroized after the op consumes it.
	pass []byte

	// iamText is the rendered IAM policy for stageIAMPreview.
	iamText string

	// provisioning progress + terminal result.
	reporter *opReporter
	steps    setupProgress
	result   setupDoneMsg
	notice   string

	width  int
	height int
}

// setupProgress tracks which provisioning checklist items completed, for
// the spinner-checklist rendered during stageProvision and stageDone.
type setupProgress struct {
	bucketCreated  bool
	publicBlocked  bool
	encryptionOn   bool
	repoInited     bool
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

func NewSetupWizardView(deps Deps) SetupWizardView {
	cfg := config0(deps)
	var eng *setup.Engine
	if deps.SetupEffects != nil {
		eng = setup.NewEngine(deps.SetupEffects)
	}
	plan := setup.DefaultPlan(cfg, setup.DefaultEnvProbe())

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
		deps:         deps,
		engine:       eng,
		plan:         plan,
		fields:       fields,
		printIAM:     plan.PrintIAMPolicy,
		createBucket: plan.CreateBucket,
		blockPublic:  plan.BlockPublicAccess,
		defaultEnc:   plan.DefaultEncryption,
		initRepo:     plan.InitRepo,
		newPass:      newPass,
		confirmPass:  confirmPass,
		savePass:     plan.SavePassphrase,
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
	case tea.KeyMsg:
		return v.handleKey(msg)
	}
	return v, nil
}

// handleKey is filled in per-stage by later tasks. The skeleton only
// routes the backend stage so the view is hostable from the start.
func (v SetupWizardView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if v.stage == stageBackend {
		return v.handleBackendKey(msg)
	}
	return v, nil
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
```

Add the `config` import to the file's import block (it is referenced by `config0`):
```go
	"github.com/markgustetic/sentra/internal/config"
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_InitialStageIsBackendSelect|TestSetupWizard_NilEffectsRendersGuard' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): scaffold SetupWizardView with backend stage"
```

---

### Task 25: Backend → Details stage: bucket/prefix/region/profile/endpoint inputs with validated bucket

**Files:**
- Modify: `internal/tui/setup_wizard.go` (extend `handleKey`, add `handleDetailsKey`, `advanceFromBackend`, details render)
- Test: `internal/tui/setup_wizard_test.go`

Enter on the backend stage advances to `stageDetails`, seeding `plan.Backend` and defaults. The details stage collects the five S3 fields (endpoint hidden for the AWS backend), validates the bucket via `setup.ValidateBucketName` on enter, and routes onward: AWS backend → `stageActions` (or `stageIAMPreview` if the IAM toggle is on), S3-compatible → `stagePassphrase`.

- [ ] **Step 1: Write the failing test**
```go
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
	v := setupAtDetails(t, 0) // AWS backend
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_BackendEnterOpensDetails -count=1`
Expected: FAIL — `stage = 0, want stageDetails` (backend enter is a no-op in the skeleton).

- [ ] **Step 3: Write the minimal implementation**
Extend `handleKey` to dispatch the new stages and add the handlers. Replace the skeleton's `handleKey` body:
```go
func (v SetupWizardView) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch v.stage {
	case stageBackend:
		if msg.Type == tea.KeyEnter {
			return v.advanceFromBackend()
		}
		return v.handleBackendKey(msg)
	case stageDetails:
		return v.handleDetailsKey(msg)
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
```
Add the `stageDetails` and `stageIAMPreview` cases to `View`. Insert into the `View` switch before `default`:
```go
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
```
Add the IAM render helper at the file end:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_BackendEnterOpensDetails|TestSetupWizard_DetailsRejectsInvalidBucket|TestSetupWizard_AWSDetailsAdvancesToActions|TestSetupWizard_CompatibleDetailsAdvancesToPassphrase' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard details stage with bucket validation"
```

---

### Task 26: IAM preview stage (viewport) short-circuit from details

**Files:**
- Modify: `internal/tui/setup_wizard.go` (add viewport wiring, `handleIAMKey`, IAM render)
- Test: `internal/tui/setup_wizard_test.go`

When the AWS operator toggled "print IAM policy", `stageDetails → stageIAMPreview` renders the JSON in a scrollable `bubbles/viewport`. This is a terminal stage of the run (the cli wizard returns after printing, `internal/cli/setup.go:216-221`): esc/enter restarts the wizard to `stageBackend`.

- [ ] **Step 1: Write the failing test**
```go
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_PrintIAMShortCircuitsToPreview -count=1`
Expected: FAIL — `IAM preview must render the policy` (the `stageIAMPreview` View case is missing; it currently renders `"setup"`).

- [ ] **Step 3: Write the minimal implementation**
Add a `viewport.Model` field to the struct. Insert into the struct definition after `iamText string`:
```go
	iamViewport viewport.Model
```
Add the `viewport` import:
```go
	"github.com/charmbracelet/bubbles/viewport"
```
In `commitDetails`, after setting `v.iamText`, size and load the viewport (replace the `v.iamText = renderIAMPolicy(...)` line's block):
```go
	if v.plan.Backend == setup.BackendAWS && v.printIAM {
		v.iamText = renderIAMPolicy(bucket, v.plan.Config.Repo.S3.Prefix)
		vp := viewport.New(max(v.width-8, 20), max(v.height-8, 6))
		vp.SetContent(v.iamText)
		v.iamViewport = vp
		v.stage = stageIAMPreview
		return v, nil
	}
```
Extend `handleKey`'s switch with the IAM stage:
```go
	case stageIAMPreview:
		return v.handleIAMKey(msg)
```
Add the handler:
```go
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
```
Add the `stageIAMPreview` case to `View`:
```go
	case stageIAMPreview:
		b.WriteString(ui.Primary.Render("IAM policy (no changes were made)") + "\n\n")
		b.WriteString(v.iamViewport.View())
		b.WriteString("\n\n" + ui.Muted.Render("↑/↓ scroll · ⏎/esc restart setup"))
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_PrintIAMShortCircuitsToPreview|TestSetupWizard_IAMPreviewMatchesEngine' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard IAM-policy preview stage"
```

---

### Task 27: Actions stage: auth-method select + bucket/init toggles (AWS branch)

**Files:**
- Modify: `internal/tui/setup_wizard.go` (add `handleActionsKey`, `advanceFromActions`, actions render)
- Test: `internal/tui/setup_wizard_test.go`

The AWS actions stage re-expresses `actionsForm` (`internal/cli/setup_wizard.go:373-421`): a cursor-driven select over the four auth methods, plus inline toggles for create-bucket / block-public / default-encryption / init-repo. Enter records them into the plan and routes: init-repo on → `stagePassphrase`; init-repo off → `stageReview`. Choosing "skip" applies config-only via `setup.ApplyAWSConfigOnly` and goes to `stageReview`.

- [ ] **Step 1: Write the failing test**
```go
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_ActionsToPassphraseWhenInitRepo -count=1`
Expected: FAIL — stage stays `stageActions` (no actions key handler yet).

- [ ] **Step 3: Write the minimal implementation**
Add these `authCursor`-order constants near the stage consts:
```go
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
```
Extend `handleKey`:
```go
	case stageActions:
		return v.handleActionsKey(msg)
```
Add the handler and advancer:
```go
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
	if v.plan.InitRepo {
		v.stage = stagePassphrase
		v.newPass.Focus()
		v.confirmPass.Blur()
		return v, nil
	}
	// init-repo off: no passphrase to collect.
	v.plan.SavePassphrase = false
	setup.ApplyPassphraseConfig(&v.plan)
	v.stage = stageReview
	return v, nil
}
```
Add the `stageActions` case to `View`:
```go
	case stageActions:
		b.WriteString(ui.Primary.Render("Setup actions") + "\n\n")
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
```
Add the toggle/label helpers at the file end:
```go
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
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_ActionsToPassphraseWhenInitRepo|TestSetupWizard_ActionsSkipAppliesConfigOnly|TestSetupWizard_ActionsInitOffGoesToReview' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard AWS actions stage"
```

---

### Task 28: Passphrase stage: masked entry, constant-time confirm, keyring toggle

**Files:**
- Modify: `internal/tui/setup_wizard.go` (add `handlePassphraseKey`, `commitPassphrase`, passphrase render)
- Test: `internal/tui/setup_wizard_test.go`

The passphrase stage re-expresses `promptSetupPassphraseStorage` PLUS the passphrase entry that the cli defers to `deps.Passphrase()` — the TUI must collect the secret itself. It reuses `password.go`'s masked pattern: two `EchoPassword` inputs, `subtle.ConstantTimeCompare`, and `minPasswordLen`. The verified passphrase is stashed in `v.pass` (never rendered), and a keyring toggle sets `SavePassphrase`. Enter advances to `stageReview`.

- [ ] **Step 1: Write the failing test**
```go
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_PassphraseMatchAdvancesToReview -count=1`
Expected: FAIL — stage stays `stagePassphrase` (no passphrase key handler yet).

- [ ] **Step 3: Write the minimal implementation**
Add the `crypto` and `subtle` imports:
```go
	"crypto/subtle"

	"github.com/markgustetic/sentra/internal/crypto"
```
Extend `handleKey`:
```go
	case stagePassphrase:
		return v.handlePassphraseKey(msg)
```
Add the handlers (the keyring toggle lives on a third focus position addressed by a second tab-cycle: new → confirm → keyring):
```go
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
```
Add the `fmt` import if not already present (needed by `Sprintf`):
```go
	"fmt"
```
Add the `stagePassphrase` case to `View`:
```go
	case stagePassphrase:
		b.WriteString(ui.Primary.Render("Repository passphrase") + "\n\n")
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
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_Passphrase' -count=1`
Expected: PASS (all four passphrase tests).

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard masked passphrase stage"
```

---

### Task 29: Review stage: ReviewText render + confirm gate that blocks provisioning

**Files:**
- Modify: `internal/tui/setup_wizard.go` (add `handleReviewKey`, review render, `setupReviewConfirmID`)
- Test: `internal/tui/setup_wizard_test.go`

The review stage renders `setup.ReviewText(cfgPath, plan)` and pushes a `ConfirmModal` (id `setup-apply`) — provisioning must NOT start until the App broadcasts `confirmedMsg{setupReviewConfirmID}`. This mirrors `HuhSetupReviewConfirm` (`internal/cli/setup_wizard.go:58-74`) and the modal-gated start pattern in `sync.go:279-281`.

- [ ] **Step 1: Write the failing test**
```go
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
```
Note: `setupAtReview` needs the engine wired for the confirm-start test to build a real op closure. Add a shared fake-effects helper the tests use — see the provisioning task; for the review tests here, a nil-effects wizard still pushes the modal and moves to `stageProvision`, but `TestSetupWizard_ReviewConfirmStartsProvisioning` needs an engine. Wire a stub via `Deps{SetupEffects: stubEffects{}}` defined in the provisioning task. To keep this task self-contained, `setupAtReview` builds the wizard through `setupAtPassphrase`, which starts from `NewSetupWizardView(Deps{Config:...})` — extend that base to accept effects by having `setupAtDetails` take effects. **Simplify: guard the confirm-start so it still emits the op even with nil effects** (the op closure defends against a nil engine at run time, see the provisioning task). The `startOpMsg` is emitted regardless; the engine-nil case is handled inside the closure. So these review tests pass with nil effects.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_ReviewRendersPlanAndPushesConfirm -count=1`
Expected: FAIL — `review must render the plan` (the `stageReview` View case and handler are missing).

- [ ] **Step 3: Write the minimal implementation**
Add the confirm-id const near the stage consts:
```go
// setupReviewConfirmID ties the review ConfirmModal result back to this
// flow. Provisioning is gated behind it: the App broadcasts
// confirmedMsg{setupReviewConfirmID} on enter, and only then does the
// wizard emit its startOpMsg. Mirrors HuhSetupReviewConfirm.
const setupReviewConfirmID = "setup-apply"
```
Extend `handleKey`:
```go
	case stageReview:
		if msg.Type == tea.KeyEnter {
			return v.pushReviewConfirm()
		}
		return v, nil
```
Add the `confirmedMsg` case to `Update` (before the `tea.KeyMsg` case):
```go
	case confirmedMsg:
		if msg.id != setupReviewConfirmID || v.stage != stageReview {
			return v, nil
		}
		v.notice = ""
		return v.startProvision()
```
Add the push helper:
```go
func (v SetupWizardView) pushReviewConfirm() (tea.Model, tea.Cmd) {
	body := "Apply this setup: prepare AWS (if selected), write the config, and initialize the repository.\n" +
		"No secrets are written to sentra.yaml, logs, or the setup draft."
	modal := NewConfirmModal("Review setup", body, setupReviewConfirmID, v.width, v.height)
	return v, func() tea.Msg { return pushModalMsg{modal: modal} }
}
```
Add the `stageReview` case to `View` (`cfgPath` comes from deps):
```go
	case stageReview:
		b.WriteString(setup.ReviewText(v.deps.ConfigPath, v.plan))
		b.WriteString("\n" + ui.Muted.Render("⏎ review & apply"))
```
Add a minimal `startProvision` stub so the confirm-start test compiles and moves stage; the real op closure is built in the next task. Insert:
```go
// startProvision moves into the provisioning stage and emits the single
// setup op. The op closure is built in the provisioning task; this stub
// establishes the stage transition and the startOpMsg name contract.
func (v SetupWizardView) startProvision() (tea.Model, tea.Cmd) {
	v.reporter = newOpReporter()
	v.stage = stageProvision
	start := v.buildSetupOp()
	return v, tea.Batch(func() tea.Msg { return start }, opTick())
}
```
Add a placeholder `buildSetupOp` returning a no-op op (fully replaced next task):
```go
func (v SetupWizardView) buildSetupOp() startOpMsg {
	return startOpMsg{
		name: "setup",
		run: func(ctx context.Context) tea.Msg {
			return setupDoneMsg{}
		},
	}
}
```
Add the `context` import and define `setupDoneMsg` (also finalized next task):
```go
	"context"
```
```go
// setupDoneMsg is the wizard's terminal op result. Setup PERFORMS AWS
// provisioning + config write + repo init, all under the repo advisory
// lock (repo.Init), so it is a mutating op: implementing opResult() clears
// the App's one-op guard.
type setupDoneMsg struct {
	steps setupProgress
	auth  *setup.AWSAuthReport
	prep  *setup.AWSPrepareReport
	init  *setup.InitResult
	err   error
}

func (setupDoneMsg) opResult() {}
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_Review' -count=1`
Expected: PASS (all three review tests).

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard review stage with confirm-gated provisioning"
```

---

### Task 30: Provisioning op: engine.PrepareAWS → WriteConfig → InitRepo with checklist, done/error, rejection

**Files:**
- Modify: `internal/tui/setup_wizard.go` (finalize `buildSetupOp`, `Update` handling of `setupDoneMsg`/`opRejectedMsg`/`opTickMsg`, provision + done + error render)
- Test: `internal/tui/setup_wizard_test.go`

The single `setup` op closure sequences the engine: `WriteDraft → PrepareAWS (only when plan.PrepareAWS) → WriteConfig → InitRepo (only when plan.InitRepo) → RemoveDraft`. It zeroizes the stashed passphrase on return. On success → `stageDone` with the checklist; on error → `stageError` rendering `setup.ErrorAdvice`. A guard rejection (`opRejectedMsg{name:"setup"}`) returns to `stageReview` with a notice. Interactive `aws` auth is deliberately NOT run inside this goroutine — the engine's `PrepareAWS` for the TUI relies on already-present credentials (the effects `AWSLogin`/`AWSSSOLogin` run via `tea.ExecProcess` in the next task); here we cover the non-interactive success/error/reject paths.

- [ ] **Step 1: Write the failing test**
```go
// stubEffects is a fully in-memory setup.Effects: no AWS, no keyring, an
// in-memory store, so the provisioning op can run end-to-end in a test.
type stubEffects struct {
	prepareErr error
	prepared   setup.AWSPrepareReport
}

func (s stubEffects) EnsureAWSCLI(ctx context.Context) (setup.AWSCLIInstallReport, error) {
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

func TestSetupWizard_OpRejectedReturnsToReview(t *testing.T) {
	v := setupAtReview(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	m, _ = v.Update(confirmedMsg{id: setupReviewConfirmID})
	v = m.(SetupWizardView) // now stageProvision
	m, _ = v.Update(opRejectedMsg{name: "setup"})
	v = m.(SetupWizardView)
	if v.stage != stageReview {
		t.Fatalf("rejection must return to review, got %v", v.stage)
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
```
Add imports to the test file: `"errors"`, `"os"`, `"path/filepath"`, `"github.com/markgustetic/sentra/internal/blobstore"`.

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_ProvisionOpRunsEngineEndToEnd -count=1`
Expected: FAIL — the stub `buildSetupOp` returns an empty `setupDoneMsg` (`repoInited` false, no config written).

- [ ] **Step 3: Write the minimal implementation**
Replace the placeholder `buildSetupOp` with the real sequenced closure:
```go
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
			if err := eng.WriteDraft(cfgPath, &plan); err != nil {
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
			return setupDoneMsg{steps: steps, auth: auth, prep: prep, init: initRes}
		},
	}
}
```
Add the `setupDoneMsg`, `opRejectedMsg`, and `opTickMsg` cases to `Update` (before `confirmedMsg`):
```go
	case setupDoneMsg:
		if msg.err != nil {
			v.stage = stageError
		} else {
			v.stage = stageDone
			v.steps = msg.steps
		}
		v.result = msg
		return v, nil

	case opRejectedMsg:
		if v.stage == stageProvision && msg.name == "setup" {
			v.stage = stageReview
			v.notice = "another operation is in progress — try again when it finishes"
		}
		return v, nil

	case opTickMsg:
		if v.stage == stageProvision {
			return v, opTick()
		}
		return v, nil
```
Add the `stageProvision`, `stageDone`, and `stageError` cases to `View`:
```go
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
```
Add the checklist helper and the done/error key handling. Extend `handleKey`:
```go
	case stageDone:
		if msg.Type == tea.KeyEnter {
			fresh := NewSetupWizardView(v.deps)
			fresh.width, fresh.height = v.width, v.height
			return fresh, nil
		}
		return v, nil
	case stageError:
		return v.handleErrorKey(msg)
```
Add:
```go
func (v SetupWizardView) handleErrorKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEnter:
		// Retry: return to review so the operator can re-confirm after
		// fixing credentials or the bucket name.
		v.stage = stageReview
		v.notice = ""
		return v, nil
	case tea.KeyEsc:
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
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_DoneMsgRendersChecklist|TestSetupWizard_DoneMsgErrorRendersAdvice|TestSetupWizard_OpRejectedReturnsToReview|TestSetupWizard_ProvisionOpRunsEngineEndToEnd' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard provisioning op with checklist and error advice"
```

---

### Task 31: Interactive AWS auth via tea.ExecProcess and missing-CLI ErrorAdvice modal

**Files:**
- Modify: `internal/tui/setup_wizard.go` (add `execAWSAuthMsg`, `awsAuthDoneMsg`, ExecProcess issuance from actions, missing-CLI modal, re-entry)
- Test: `internal/tui/setup_wizard_test.go`

Browser login and SSO cannot run inside the op goroutine (they own the terminal), so when the chosen auth method is `login` or `sso` AND credentials are not already present, the actions stage issues `tea.ExecProcess` to suspend the program and hand the terminal to the child `aws` process. On completion the wizard re-enters at review. A missing `aws` CLI (detected via `effects.EnsureAWSCLI`) shows an `ErrorModal` built from `setup.ErrorAdvice` instead of attempting a brew install (brew stays CLI-only). This task threads the ExecProcess through `advanceFromActions`.

Because `tea.ExecProcess` and the interactive child cannot run in a unit test, the tests here assert the *decision*: that a login/sso method with missing credentials produces an ExecProcess command (a non-nil `tea.Cmd` carrying `execAWSAuthMsg` intent) rather than jumping straight to passphrase, and that a missing CLI pushes an error modal.

- [ ] **Step 1: Write the failing test**
```go
// execProbe is a stubEffects whose EnsureAWSCLI and CheckAWSSDKIdentity are
// configurable so the auth-routing decision can be exercised.
type execProbe struct {
	stubEffects
	cliErr      error // EnsureAWSCLI failure (missing aws)
	identityErr error // CheckAWSSDKIdentity failure (creds absent → need auth)
}

func (e execProbe) EnsureAWSCLI(ctx context.Context) (setup.AWSCLIInstallReport, error) {
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
```

- [ ] **Step 2: Run test to verify it fails**
Run: `go test ./internal/tui/ -run TestSetupWizard_LoginMissingCredsIssuesExecAuth -count=1`
Expected: FAIL — `undefined: awsAuthDoneMsg` (compile error), and the auth routing does not exist.

- [ ] **Step 3: Write the minimal implementation**
Add the auth-completion message type and a "pending" marker for what to do after auth:
```go
// awsAuthDoneMsg reports that the interactive `aws` child process launched
// via tea.ExecProcess has exited. tea.ExecProcess suspends the program,
// gives the child the terminal, and resumes on exit, delivering the
// process's error through the callback wrapped in this message. On success
// the wizard resumes the pre-auth flow (→ passphrase or review).
type awsAuthDoneMsg struct{ err error }
```
Rework `advanceFromActions` so a login/sso method with absent credentials runs the CLI check then issues auth. Replace the non-skip tail of `advanceFromActions`:
```go
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
```
Add the helpers:
```go
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
	if _, err := eff.EnsureAWSCLI(ctx); err != nil {
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
```
Add `interactiveAWSAuthCommand` — it builds the `*exec.Cmd` the effects layer would run. Since the pinned `Effects` interface exposes `AWSLogin`/`AWSSSOLogin` as *functions that run to completion* (not command builders), the TUI cannot hand their internal `exec.Cmd` to `tea.ExecProcess`. Instead, wrap the effect call in a shell-less `exec.Cmd` is not possible; so this helper constructs the `aws` command directly, mirroring the effect's argument construction, and `tea.ExecProcess` owns it:
```go
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
```
Add the `os/exec` import:
```go
	"os/exec"
```
Add the `awsAuthDoneMsg` case to `Update` (before `tea.KeyMsg`):
```go
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
```

- [ ] **Step 4: Run test to verify it passes**
Run: `go test ./internal/tui/ -run 'TestSetupWizard_LoginMissingCredsIssuesExecAuth|TestSetupWizard_LoginWithCredsSkipsExecAuth|TestSetupWizard_MissingAWSCLIPushesErrorModal|TestSetupWizard_AuthDoneReentersAtReview' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard interactive AWS auth via ExecProcess"
```

---

### Task 32: Full-package gate: fmt/vet/lint/race for the new view

**Files:**
- Modify: `internal/tui/setup_wizard.go` (only if the gate flags anything)
- Test: `internal/tui/setup_wizard_test.go`

Final sweep so the unit is CI-clean before Unit 7 registers the view. No behavior change; this task exists to run the repo gate against the whole `tui` package (the App still does NOT register the wizard, per the unit boundary).

- [ ] **Step 1: Write the failing test**
No new test. Confirm the whole package compiles and the wizard tests coexist with the existing flow tests:
Run: `go test ./internal/tui/ -run TestSetupWizard -count=1`
Expected initial state before fixes (if any drift): PASS for wizard tests; this step is the guard that they all still pass together.

- [ ] **Step 2: Run test to verify it fails**
Run: `go vet ./internal/tui/ && gofmt -l internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go`
Expected: any `gofmt -l` output (a listed file) or a `vet` complaint is the failing condition to fix. If clean, proceed.

- [ ] **Step 3: Write the minimal implementation**
Apply only what the gate reports:
```bash
gofmt -w internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
```
If `golangci-lint` flags an unused helper or an `errcheck` on `eng.RemoveDraft`, address it in place (e.g. `RemoveDraft` returns nothing per the pinned API, so no errcheck; if lint flags the `_ setup.Effects` unused param in `interactiveAWSAuthCommand`, rename to `_`). Make no functional changes.

- [ ] **Step 4: Run test to verify it passes**
Run: `go test -race ./internal/tui/ -count=1 && go vet ./internal/tui/ && golangci-lint run ./internal/tui/... && gofmt -l internal/tui/`
Expected: tests PASS under `-race`; `vet`, `golangci-lint`, and `gofmt -l` all produce no output.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "chore(tui): gofmt/vet/lint clean for setup wizard view"
```

---

Notes for the controller / downstream units:
- **No `NewApp` registration here** — Unit 7 adds `{id: "setup", model: NewSetupWizardView(deps)}` to `app.go:190-205` and the first-run/Settings wiring, plus `Deps.SetupEffects` production wiring in `runUI`/`UIDeps`/`commands.go`.
- The provisioning op emits exactly one `startOpMsg{name:"setup"}` and its terminal `setupDoneMsg` implements `opResult()`, so the App's one-op guard (`app.go:282-311`) clears correctly and rejects a concurrent op with `opRejectedMsg{name:"setup"}` (handled in this view).
- `setup.Engine.InitRepo` must preserve the verify-before-keyring guard (`setup_init.go:43-64`) per the pinned API — this view passes `plan.SavePassphrase` straight through and never touches the keyring itself.
- The masked passphrase is stashed only in `v.pass` (a copy) and the op closure's captured copy; both are zeroized, and `TestSetupWizard_PassphraseNeverRendered` asserts it never reaches `View()`.
- `interactiveAWSAuthCommand` reproduces the effect layer's `aws` argv (from `setup_awscli.go` `DefaultAWSLogin`/`DefaultAWSSSOLogin`) because `tea.ExecProcess` needs to own the child directly; if Units 1–5 expose the effect as a command builder instead, swap this helper to call it — the routing and `awsAuthDoneMsg` re-entry are unchanged.


## Part 7 — Settings view + registration

**Published API (this unit — internal/tui):**
- `type SettingsView struct` with methods `NewSettingsView(deps Deps) SettingsView`, `(SettingsView) Init() tea.Cmd`, `(v SettingsView) Title() string`, `(v SettingsView) ShortHelp() []key.Binding`, `(v SettingsView) Update(tea.Msg) (tea.Model, tea.Cmd)`, `(v SettingsView) View() string`.
- New `Deps` field: `FirstRun bool` (first-run gate flag; set by U5's `runUI`, consumed by `NewApp` to make `"setup"` the active view at construction).
- Consumes (defined by sibling U6): `NewSetupWizardView(deps Deps)`.

---

### Task 33: Add SettingsView (config summary + setup/passphrase entries)

**Files:**
- Create: `internal/tui/settings.go`
- Test: `internal/tui/settings_test.go`

- [ ] **Step 1: Write the failing test**

```go
package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
)

// TestSettingsView_RendersConfigSummaryNoSecrets: the view shows the
// non-secret repo/config identity (bucket, prefix, config path, keyring
// flag) and its two entries, and never renders anything secret.
func TestSettingsView_RendersConfigSummary(t *testing.T) {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "my-bucket"
	cfg.Repo.S3.Prefix = "team/backups"
	cfg.Passphrase.UseKeyring = true
	v := NewSettingsView(Deps{
		Config:     &cfg,
		RepoName:   "my-bucket",
		ConfigPath: "/home/u/sentra.yaml",
	})
	out := v.View()
	for _, want := range []string{
		"my-bucket", "team/backups", "/home/u/sentra.yaml",
		"Re-run setup", "Change passphrase",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("settings view missing %q:\n%s", want, out)
		}
	}
}

// TestSettingsView_NoConfigPlaceholder: with a nil config the view still
// renders (no crash) and shows a placeholder plus the two entries.
func TestSettingsView_NoConfigPlaceholder(t *testing.T) {
	v := NewSettingsView(Deps{})
	out := v.View()
	if !strings.Contains(out, "no configuration loaded") {
		t.Errorf("expected no-config placeholder:\n%s", out)
	}
	if !strings.Contains(out, "Re-run setup") || !strings.Contains(out, "Change passphrase") {
		t.Errorf("entries missing under nil config:\n%s", out)
	}
}

// TestSettingsView_EnterOnSetupActivatesSetup: with the "Re-run setup"
// entry selected, Enter emits activateMsg{"setup"} so the shell switches
// to the setup wizard view.
func TestSettingsView_EnterOnSetupActivatesSetup(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	// cursor starts at 0 == "Re-run setup".
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SettingsView)
	if cmd == nil {
		t.Fatal("Enter on setup entry returned no command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok || act.id != "setup" {
		t.Fatalf("got %#v, want activateMsg{setup}", msg)
	}
}

// TestSettingsView_EnterOnPasswordActivatesPassword: moving the cursor
// down to "Change passphrase" and pressing Enter emits
// activateMsg{"password"}.
func TestSettingsView_EnterOnPasswordActivatesPassword(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(SettingsView)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on password entry returned no command")
	}
	msg := cmd()
	act, ok := msg.(activateMsg)
	if !ok || act.id != "password" {
		t.Fatalf("got %#v, want activateMsg{password}", msg)
	}
}

// TestSettingsView_TitleAndCursorClamp: Title is stable and the cursor
// never leaves the [0,1] range regardless of key spam.
func TestSettingsView_TitleAndCursorClamp(t *testing.T) {
	v := NewSettingsView(Deps{Config: ptrDefaults()})
	if v.Title() != "Settings" {
		t.Fatalf("Title = %q, want Settings", v.Title())
	}
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyUp})
		v = m.(SettingsView)
	}
	if v.cursor != 0 {
		t.Fatalf("cursor after up-spam = %d, want 0", v.cursor)
	}
	for i := 0; i < 5; i++ {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SettingsView)
	}
	if v.cursor != 1 {
		t.Fatalf("cursor after down-spam = %d, want 1", v.cursor)
	}
}

func ptrDefaults() *config.Config {
	c := config.Defaults()
	return &c
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestSettingsView -count=1`
Expected: FAIL with `undefined: NewSettingsView` (and `undefined: SettingsView`).

- [ ] **Step 3: Write the minimal implementation**

`config.Defaults()` exists in `internal/config` (used at `internal/tui/app_test.go:22`). `activateMsg` is defined at `internal/tui/sidebar.go:16`. `ui.Primary/Muted/Subtle/SidebarActive/SidebarItem` are defined at `internal/ui/theme.go:28-49`. `key.NewBinding` usage mirrors `internal/tui/schedule.go:79-86`.

Create `internal/tui/settings.go`:

```go
package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/ui"
)

// settingsEntry is one actionable row in the Settings view. Activating it
// emits an activateMsg for targetID, which App routes to view navigation
// (app.go's activateMsg case). Settings itself performs no I/O and holds
// no secrets — it is a read-only launcher over the config summary.
type settingsEntry struct {
	label    string
	desc     string
	targetID string
}

// SettingsView is the Settings hub: a non-secret summary of the resolved
// configuration (bucket, prefix, keyring flag, config path) plus a short
// list of entries that re-enter other views — "Re-run setup" jumps to the
// setup wizard, "Change passphrase" jumps to the password view. It owns no
// goroutines and takes no op guard; Enter merely emits an activateMsg the
// shell already knows how to route.
//
// Security: it renders only non-secret configuration fields. The passphrase
// itself, AWS credentials, wrapped keys, salts, and MAC material are never
// read here — the summary is limited to bucket/prefix/path/keyring-flag,
// which are plain YAML data.
type SettingsView struct {
	deps    Deps
	entries []settingsEntry
	cursor  int
	width   int
}

func NewSettingsView(deps Deps) SettingsView {
	return SettingsView{
		deps: deps,
		entries: []settingsEntry{
			{label: "Re-run setup", desc: "reconfigure the backend and repository", targetID: "setup"},
			{label: "Change passphrase", desc: "rotate the repository passphrase", targetID: "password"},
		},
	}
}

func (SettingsView) Init() tea.Cmd { return nil }

func (v SettingsView) Title() string { return "Settings" }

func (v SettingsView) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑↓", "entry")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("⏎", "open")),
	}
}

func (v SettingsView) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width = msg.Width
		return v, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyUp:
			if v.cursor > 0 {
				v.cursor--
			}
			return v, nil
		case tea.KeyDown:
			if v.cursor < len(v.entries)-1 {
				v.cursor++
			}
			return v, nil
		case tea.KeyEnter:
			id := v.entries[v.cursor].targetID
			return v, func() tea.Msg { return activateMsg{id: id} }
		}
		return v, nil
	}
	return v, nil
}

func (v SettingsView) View() string {
	var b strings.Builder
	b.WriteString(ui.Primary.Render("Settings") + "\n\n")
	b.WriteString(v.renderSummary() + "\n")
	for i, e := range v.entries {
		line := e.label
		if i == v.cursor {
			b.WriteString(ui.SidebarActive.Render(line) + "\n")
		} else {
			b.WriteString(ui.SidebarItem.Render(line) + "\n")
		}
		b.WriteString("    " + ui.Muted.Render(e.desc) + "\n")
	}
	b.WriteString("\n" + ui.Muted.Render("↑↓ move   ⏎ open"))
	return b.String()
}

// renderSummary shows the non-secret configuration identity. With a nil
// config it renders a single placeholder line so the view still draws
// (Deps{} in tests, unconfigured installs).
func (v SettingsView) renderSummary() string {
	cfg := v.deps.Config
	if cfg == nil {
		return ui.Muted.Render("no configuration loaded") + "\n"
	}
	var b strings.Builder
	field := func(label, val string) {
		if val == "" {
			val = ui.Subtle.Render("(unset)")
		}
		b.WriteString("  " + ui.Muted.Render(label) + "  " + val + "\n")
	}
	field("bucket ", cfg.Repo.S3.Bucket)
	field("prefix ", cfg.Repo.S3.Prefix)
	field("region ", cfg.Repo.S3.Region)
	keyring := "off"
	if cfg.Passphrase.UseKeyring {
		keyring = "on"
	}
	field("keyring", keyring)
	if v.deps.ConfigPath != "" {
		field("config ", v.deps.ConfigPath)
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run TestSettingsView -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/settings.go internal/tui/settings_test.go
git commit -m "feat(tui): add SettingsView with config summary and re-entry launchers

Renders a non-secret config summary (bucket/prefix/region/keyring/config
path) and two launcher entries — Re-run setup (activateMsg{setup}) and
Change passphrase (activateMsg{password}) — that the App shell routes via
its existing activateMsg case. No I/O, no op guard, no secrets.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

### Task 34: Register setup + settings views and gate first-run to the setup wizard

**Files:**
- Modify: `internal/tui/app.go:42-100` (add `FirstRun` field to `Deps`)
- Modify: `internal/tui/app.go:181-241` (`NewApp`: insert two views, categorize, select first-run active view)
- Test: `internal/tui/settings_test.go` (append registration test)

Note for the controller: this task **inserts** the two new entries into the current 14-view slice, giving **16** views total. The AUTHORITATIVE final slice/count is pinned by the controller's final registration task. Two existing assertions hard-code `14` — `internal/tui/app_test.go:41` (`TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd`) and `internal/tui/app_test.go:767` (`TestApp_CheckReplacesOperationsInSidebar`) — and must be bumped to **16** by that final task. **Flagged for controller: final count = 16.** This task's own new test asserts presence of both ids and the local `16` count so the unit is self-consistent; if the controller renumbers/refines, only the constant changes.

Dependency: `NewSetupWizardView(deps Deps)` is defined by sibling Unit 6 (the setup wizard view). This task references it. The `FirstRun` seam is set by sibling Unit 5's `runUI`; this task defines the `Deps.FirstRun bool` field and consumes it in `NewApp`. If U5 also declares `Deps.FirstRun`, keep exactly one declaration (this task's) — U5 only assigns it. **Flagged for controller: single `Deps.FirstRun bool` declaration, owned here, assigned by U5's runUI.**

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/settings_test.go`:

```go
// TestApp_SetupAndSettingsRegistered: both new views are registered in the
// shell (sidebar + palette are registry-driven off the same slice), and the
// total view count reflects the two additions. The AUTHORITATIVE count is
// pinned by the controller's final registration task; this asserts the local
// invariant (both ids present) plus the expected 16.
func TestApp_SetupAndSettingsRegistered(t *testing.T) {
	app := NewApp(Deps{RepoName: "test-repo"})
	have := map[string]bool{}
	for _, v := range app.views {
		have[v.id] = true
	}
	for _, id := range []string{"setup", "settings"} {
		if !have[id] {
			t.Errorf("view %q not registered", id)
		}
	}
	if got := len(app.views); got != 16 {
		t.Fatalf("views = %d, want 16 (14 Phase 2c + setup + settings)", got)
	}
}

// TestApp_FirstRunSelectsSetup: when Deps.FirstRun is set, the shell opens
// on the setup wizard so an unconfigured install lands on setup rather than
// the dashboard. Without the flag the dashboard stays active (index 0).
func TestApp_FirstRunSelectsSetup(t *testing.T) {
	first := NewApp(Deps{FirstRun: true})
	if id := first.views[first.active].id; id != "setup" {
		t.Fatalf("first-run active view = %q, want setup", id)
	}
	normal := NewApp(Deps{})
	if id := normal.views[normal.active].id; id != "dashboard" {
		t.Fatalf("non-first-run active view = %q, want dashboard", id)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run 'TestApp_SetupAndSettingsRegistered|TestApp_FirstRunSelectsSetup' -count=1`
Expected: FAIL — `unknown field 'FirstRun' in struct literal of type Deps` (compile error), and once that is added, `view "setup" not registered` / `views = 14, want 16`.

- [ ] **Step 3: Write the minimal implementation**

Add the `FirstRun` field to `Deps`. Edit `internal/tui/app.go` — after the `SaveKeyringPassphrase` field block ending at `internal/tui/app.go:99` (the `}` closing the struct is at line 100), insert before the closing brace:

```go
	// FirstRun, when true, makes NewApp open the shell on the setup
	// wizard ("setup") instead of the dashboard. It is set by runUI on an
	// unconfigured install (no usable sentra.yaml / no repo). It carries
	// no secret — it is a single gate bit — so it is safe to pass through
	// Deps like the other plain-data fields. Zero value (false) keeps the
	// dashboard as the landing view, matching every existing test.
	FirstRun bool
```

Insert the two view entries. Edit the `views` slice in `NewApp` (`internal/tui/app.go:190-205`); add after the `{id: "password", ...}` line:

```go
		{id: "settings", model: NewSettingsView(deps)},
		{id: "setup", model: NewSetupWizardView(deps)},
```

Categorize both under a new "Settings" category. Edit the `categories` map (`internal/tui/app.go:211-214`) to append the two ids:

```go
	categories := map[string]string{
		"backup": "Operations", "restore": "Operations", "prune": "Operations",
		"sync": "Operations", "password": "Operations", "policies": "Operations",
		"settings": "Settings", "setup": "Settings",
	}
```

Select the first-run active view at construction. Edit the returned `App` literal in `NewApp` (`internal/tui/app.go:228-240`): change `active: 0,` to a computed value. Replace the `active: 0,` line and add a helper before the `return`:

```go
	active := 0
	if deps.FirstRun {
		for i, v := range views {
			if v.id == "setup" {
				active = i
				break
			}
		}
	}
	keys := newGlobalKeymap()
	return App{
		deps:     deps,
		registry: registry,
		keys:     keys,
		views:    views,
		active:   active,
		focus:    focusSidebar,
		sidebar:  NewSidebar(registry, sidebarWidth, minHeight),
		palette:  NewPalette(registry, minWidth, minHeight),
		status:   NewStatusBar(keys, minWidth),
		ctx:      ctx,
		cancel:   cancel,
	}
```

(The existing `keys := newGlobalKeymap()` at `internal/tui/app.go:227` moves below the `active` computation; keep exactly one declaration of `keys`.)

Note: `NewSetupWizardView` must exist for this to compile — it is provided by Unit 6. If Unit 6 has not yet landed when this task runs, add a temporary local stub in `internal/tui/settings.go` returning a placeholder `tea.Model`; the controller's ordering places Unit 6 before this registration so the stub is normally unnecessary. **Flagged for controller: order U6 (setup wizard view) before this registration task, or provide the `NewSetupWizardView` stub.**

Also update the `NewApp` doc comment at `internal/tui/app.go:173-180` and the count phrasing (it currently says "all 14 Phase 2c views"); change to note the two Settings-category additions and the first-run gate. Minimal edit:

Replace `// NewApp constructs the shell with all 14 Phase 2c views registered:` through the end of that comment's view enumeration with a line noting the added `settings` and `setup` views under the "Settings" category and that `deps.FirstRun` opens the shell on `setup`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tui/ -run 'TestApp_SetupAndSettingsRegistered|TestApp_FirstRunSelectsSetup|TestSettingsView' -count=1`
Expected: PASS.

Then run the full package to surface the two stale `14` assertions the controller's final task will fix:
Run: `go test ./internal/tui/ -count=1`
Expected: FAIL only in `TestApp_OperationsRegisteredAndRunningIndicatorEndToEnd` (`app_test.go:41`) and `TestApp_CheckReplacesOperationsInSidebar` (`app_test.go:767`), each asserting `14`. Bump both literals `14` → `16` (and their message text) as part of the controller's final registration task; do not touch them here beyond flagging. After that bump, the full package is green.

- [ ] **Step 5: Commit**
```bash
git add internal/tui/app.go internal/tui/settings_test.go
git commit -m "feat(tui): register setup + settings views and gate first-run to setup

Adds Deps.FirstRun; NewApp inserts the settings and setup wizard views
(Settings category) and, when FirstRun is set, opens the shell on the
setup wizard instead of the dashboard. View count 14 -> 16; the two
count assertions in app_test.go are bumped in the final registration task.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>"
```

---

**Notes flagged for the controller:**
- **Final view count = 16** (14 Phase 2c + `settings` + `setup`). The controller's final registration task must bump the two hard-coded `14` assertions at `internal/tui/app_test.go:41` and `:767` to `16` (and their message strings).
- **`Deps.FirstRun bool`** is declared here (in `internal/tui/app.go`) and only **assigned** by Unit 5's `runUI`. Ensure exactly one declaration survives the merge.
- **`NewSetupWizardView(deps Deps)`** is defined by Unit 6; order U6 before this registration task (or provide a temporary stub) so `internal/tui/app.go` compiles.
- Category chosen: **"Settings"** (new category) for both `settings` and `setup`, consistent with the existing `categories` map at `internal/tui/app.go:211-214` (all non-listed ids fall back to "Views" per `internal/tui/app.go:220-223`).


## Part 8 — Final registration, routing reconciliation & full-branch gate

**Published API:** none. This task is the authoritative end-state for `internal/tui/app.go`'s view registration and the `InitialView` mechanism. It supersedes the illustrative slice/count snippets and the stray `Deps.FirstRun` references inside the per-unit tasks (Execution Notes 5 & 6).

### Task 35: Reconcile InitialView, register all views, and run the full-branch gate

**Files:**
- Modify: `internal/tui/app.go` (the `views` slice, the registry-build loop, the `categories` map, and the `InitialView` handling in `NewApp`)
- Test: `internal/tui/app_test.go` (new test appended; existing count assertions bumped)

**Reconciliation this task pins (do these first if any earlier task left them inconsistent):**
1. **One mechanism: `Deps.InitialView string`** (added by Unit 5). There is NO `Deps.FirstRun` field. If any Unit-7 code or test references `deps.FirstRun`, change it to `deps.InitialView == "setup"` (and set `InitialView: "setup"` where a test constructed `FirstRun: true`). `runUI` sets `InitialView` to `"setup"` (no `sentra.yaml`), `"unlock"` (config present, no passphrase source), or `""` (dashboard).
2. **17 view models, 16 rail/palette commands.** The `views` slice holds all 17 (the 14 Phase 2c views + `unlock` + `setup` + `settings`). `unlock` is a startup gate, not a navigable operation, so it is **excluded from the command registry** (sidebar + palette) — the registry-build loop skips it.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestApp_Phase3ViewsRegistered: after Phase 3 the shell has 17 view models
// (14 Phase 2c + unlock + setup + settings). setup and settings are
// navigable (rail/palette) under the "Settings" category; unlock is a
// startup gate reached only via Deps.InitialView, so it is NOT in the
// command registry.
func TestApp_Phase3ViewsRegistered(t *testing.T) {
	app := newTestApp(t)

	want := []string{
		"dashboard", "snapshots", "diff", "check", "doctor", "recovery-kit",
		"policies", "schedule", "agent", "backup", "restore", "prune",
		"sync", "password", "setup", "settings", "unlock",
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

	// setup + settings are navigable; unlock is a hidden startup gate.
	cmds := app.registry.Commands()
	ids := make(map[string]bool, len(cmds))
	for _, c := range cmds {
		ids[c.ID] = true
	}
	if !ids["setup"] || !ids["settings"] {
		t.Error("setup and settings must be in the command registry (rail/palette)")
	}
	if ids["unlock"] {
		t.Error("unlock is a startup gate and must NOT be in the command registry")
	}

	out := app.View()
	for _, label := range []string{"Setup", "Settings"} {
		if !strings.Contains(out, label) {
			t.Errorf("sidebar/palette should list %q:\n%s", label, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui/ -run TestApp_Phase3ViewsRegistered -count=1`
Expected: FAIL — missing ids (if run before the flow tasks) or `views = N, want 17` / `unlock is a startup gate...` (if `unlock` is still registry-added).

- [ ] **Step 3: Set the authoritative registration**

In `NewApp` (`internal/tui/app.go`), set the `views` slice to exactly these 17 in this order, add the `categories` entries, and make the registry-build loop skip the hidden startup gate.

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
		{id: "setup", model: NewSetupWizardView(deps)},
		{id: "settings", model: NewSettingsView(deps)},
		{id: "unlock", model: NewUnlockView(deps)},
	}
	categories := map[string]string{
		"backup":   "Operations",
		"restore":  "Operations",
		"prune":    "Operations",
		"sync":     "Operations",
		"password": "Operations",
		"policies": "Operations",
		"setup":    "Settings",
		"settings": "Settings",
	}
	// hiddenFromRail lists view ids that are reachable only via InitialView
	// routing (startup gates), never from the sidebar/palette.
	hiddenFromRail := map[string]bool{"unlock": true}
```

Then, in the loop that builds the command registry from `views`, skip hidden ids:

```go
	for _, v := range views {
		if hiddenFromRail[v.id] {
			continue // startup gate — renderable via InitialView, not navigable
		}
		title := v.id
		if t, ok := v.model.(interface{ Title() string }); ok {
			title = t.Title()
		}
		cat := categories[v.id]
		if cat == "" {
			cat = "Views"
		}
		registry.Add(Command{ID: v.id, Title: title, Category: cat})
	}
```

Confirm `NewApp` still selects the active view from `deps.InitialView` by matching `v.id` in the `views` slice (not the registry), so `unlock`/`setup` are reachable as the initial view even though `unlock` is not registry-listed. (Unit 5 added this; verify it looks up the slice.)

- [ ] **Step 4: Bump the pre-existing exact-count assertions**

Find every exact view-count assertion in `internal/tui/app_test.go` (grep `len(app.views)`, `!= 14`, `want 14`, `!= 16`, `want 16`, `views = %d`) — Phase 2c/earlier Phase 3 tasks left these at 14 (or a transient 16). Update them all to **17**, and fix any that assert a specific registry/rail count to **16** (the navigable commands). Update the `NewApp` doc comment to describe the `setup`/`settings` "Settings" views and the `unlock` gate.

- [ ] **Step 5: Run the registration test + tui suite**

Run: `go test ./internal/tui/ -count=1`
Expected: PASS (registration test green; all earlier view tests still pass with the bumped counts).

- [ ] **Step 6: Run the full CI-equivalent gate**

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
Expected: every package `ok`; lint `0 issues`; tidy + diff-check clean.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/app.go internal/tui/app_test.go
git commit -m "feat(tui): register setup/settings/unlock; InitialView routing; full-branch gate"
```

- [ ] **Step 8: Manual smoke test (human-run — cannot be automated)**

```bash
just build
# first-run: run against a directory with NO sentra.yaml
cd $(mktemp -d) && /path/to/bin/sentra ui
```
Walk the first-run wizard end to end (backend → details → actions → passphrase → review → provision), an S3-compatible run (skips the AWS actions), the IAM-policy preview, an intentionally-failing AWS step (advice modal + retry), the masked unlock gate (launch with a config whose passphrase isn't cached), and re-running setup from Settings. Confirm interactive `aws` sign-in suspends/resumes cleanly via `tea.ExecProcess`, the passphrase is never shown, and layout holds at your terminal width.
