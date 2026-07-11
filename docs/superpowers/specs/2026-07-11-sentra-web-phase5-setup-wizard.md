# Sentra Web — Phase 5: First-run setup wizard

**Status:** approved (design), 2026-07-11
**Predecessors:** Phases 1–4 (merged to `main` through `1e0f134`).

## Goal

Let a fresh `sentra web` (no `sentra.yaml`) provision a repository from the
browser instead of erroring out. The wizard is a third driver over the shared
`internal/setup` engine — `internal/web` adds no crypto, storage, provisioning,
or config logic; it drives `DefaultPlan → transforms → ValidatePlan →
WriteDraft → PrepareAWS → WriteConfig → InitRepo → RemoveDraft` from
browser-collected form data.

## Constraints inherent to the engine (shape the whole design)

- **AWS credentials are never collected or written.** The engine relies on
  *ambient* credentials (env vars, shared profile, SSO session, instance role)
  resolved through the AWS SDK chain. The web form collects **no** access
  key / secret key. Nothing secret is written to `sentra.yaml`, drafts, logs, or
  responses.
- **Interactive AWS login/SSO can't run over HTTP** (the CLI shells out to a
  TTY). The web AWS path is **`AWSAuthExisting`** only; a missing-credentials
  error is surfaced with `ErrorAdvice` (e.g. "run `aws sso login --profile …`")
  plus a retry. The **S3-compatible / MinIO** path (`AWSAuthSkip`,
  `PrepareAWS=false`) is full parity.
- **`setup.DefaultEffects()` is TTY-free for these two paths** (only
  `CheckAWSSDKIdentity → PrepareAWS → NewStore → SavePassphrase` run), so it is
  used as-is server-side.

## Server: a "setup mode"

`internal/cli/web.go` currently errors when `!st.ConfigExists`. Replace that: on
a missing config, construct the server in **setup mode** (`repo == nil`,
`setupNeeded == true`), wired with the setup engine and config path.

### Deps additions (`web.Deps` + `cli.WebDeps`)

- `SetupNeeded bool` — start in setup mode.
- `SetupEffects setup.Effects` — the engine's side-effect seam; nil →
  `setup.DefaultEffects()`.
- `SetupSeedConfig *config.Config` — optional pre-fill (endpoint/bucket/region),
  mirroring `UIDeps.SetupSeedConfig` for a future `sentra local --web`. Never
  written to disk; non-secret coordinates only.

`cli.WebDeps` gains `SetupEffects`/`SetupSeedConfig`; `commands.go` wires
`SetupEffects: setup.DefaultEffects()`.

### In-place unlock after setup (contained refactor)

The engine's `InitRepo` opens/verifies/closes the repo and returns only a
report, so after a successful apply the server **re-opens** the repo
(`SetupEffects.NewStore(&plan.Config)` + `repo.Open(pass)`) and transitions from
setup-mode to unlocked in place. To make that safe, three server fields become
`mu`-guarded and are read through accessors:

- `s.cfg *config.Config` ← `currentConfig()` (replaces the ~10 `s.deps.Config`
  reads). The pointer is swapped under the lock; the pointed-to config is never
  mutated in place, so field reads after the swap are race-free.
- `s.name string` ← `currentRepoName()` (replaces the 3 `s.deps.RepoName`
  reads in `handleSession`/`handleUnlock`).
- `s.setupNeeded bool` ← `setupRequired()`; flipped false after a successful
  apply.

`ConfigPath` is immutable (the fixed write target) — no change.

### Routing / auth

Setup endpoints use a new **`requireSetupSession`** middleware: the session
cookie + the existing origin/Host guard (anti-CSRF/rebinding) but **not** the
repo-unlocked check, and only while `setupRequired()` is true (else 409 "already
configured"). `handleSession` gains `setupNeeded`; the frontend shows the wizard
when true, the unlock gate when configured-but-locked, the app otherwise.

## Endpoints (`api_setup.go`)

- **`GET /api/setup`** → `{setupNeeded, backend, seed:{bucket,prefix,region,
  endpointUrl,profile}, region, profile, awsCredentialsPresent,
  endpointLocked}`. Builds `DefaultPlan(seedOrDefaults, DefaultEnvProbe())` to
  surface inferred region/profile and `HasEnvCredentials()`. `endpointLocked` is
  true when a seed endpoint forces S3-compatible (the `sentra local` case).
- **`POST /api/setup/validate`** → body `{backend, bucket, prefix, region,
  profile, endpointUrl, createBucket, blockPublicAccess, defaultEncryption,
  initRepo}` → build the plan via the transforms → `ValidatePlan` → `{ok:true}`
  or `{ok:false, error}`. Also usable per-keystroke for the bucket via
  `ValidateBucketName`. No side effects.
- **`GET /api/setup/iam-policy?bucket=&prefix=`** → `{policy: <json>}` from
  `BuildIAMPolicy`. Read-only, for the "show me the policy" affordance.
- **`POST /api/setup/apply`** → body = the full plan + `{passphrase,
  savePassphrase}`. Builds & validates the plan, then runs a **streaming op**:
  `WriteDraft → (PrepareAWS if AWS) → WriteConfig → InitRepo → RemoveDraft`,
  emitting a step token per stage (`bucket-created`, `public-blocked`,
  `encrypted`, `repo-initialized`) over the op SSE. On success it re-opens the
  repo, swaps in `s.repo`/`s.cfg`/`s.name`, flips `setupNeeded` false, then
  zeroizes the passphrase; the done payload is `{repoId, summary:[…]}` (from
  `SummaryLines`, no secrets). On `PrepareAWS` failure the error + `ErrorAdvice`
  lines are returned so the browser can amend and retry; the draft persists for
  resume. Runs through the one-op guard.

### Building the plan (shared with validate + apply)

```
seed := deps.SetupSeedConfig or config.Defaults()
plan := setup.DefaultPlan(*seed, setup.DefaultEnvProbe())
plan.Config.Repo.S3.{Bucket,Prefix,Region,Profile,EndpointURL} = form values
setup.ApplyBackendChoice(&plan, backend, seed.Repo.S3.Profile)
plan.CreateBucket/BlockPublicAccess/DefaultEncryption/InitRepo = form flags
if backend == S3Compatible { setup.ApplyAWSConfigOnly(&plan) }  // PrepareAWS=false, AuthSkip
else { plan.PrepareAWS = true; plan.AWSAuthMethod = AWSAuthExisting }
setup.NormalizeConfig(&plan.Config)
setup.ApplyPassphraseConfig(&plan)     // links SavePassphrase → Passphrase.UseKeyring
setup.ValidatePlan(plan)
```

## Frontend (`assets/index.html`, `app.css`, `app.js`)

A setup overlay (peer of the unlock overlay) shown when `session.setupNeeded`.
A client-driven step flow accumulating one plan object, submitted once:

**Welcome → Backend (AWS vs S3-compatible; locked to S3-compatible when
`endpointLocked`) → Details (bucket + live validation, prefix, region, profile;
+endpoint for S3-compatible) → Actions (AWS only: create-bucket / block-public /
default-encryption toggles, an "uses your machine's existing AWS credentials"
note, and an IAM-policy preview) → Passphrase (new + confirm + keyring opt-in) →
Review → Provision (SSE checklist) → Done → reload to dashboard.**

Reuses `streamSSE`/`progressBox` and the synthwave design; a missing-credentials
apply error shows the `ErrorAdvice` lines + a "retry" that re-submits.

## Security posture (unchanged invariants)

No AWS credentials collected or written (ambient only). No secrets in
`sentra.yaml`, drafts, logs, or responses. Passphrase POSTed once over loopback,
used, zeroized. Origin/Host guard + session cookie on every setup route; setup
routes 409 once configured. One provisioning op at a time. `internal/web` stays
a thin adapter over `internal/setup`.

## Testing (TDD, httptest)

- **Setup-mode routing:** a server built with `SetupNeeded:true` and no config →
  `GET /api/session` reports `setupNeeded:true`; data routes still 401/consistent.
- **Validate:** bad bucket → `{ok:false}`; a valid S3-compatible plan →
  `{ok:true}`; AWS + endpoint → invalid (mirrors `ValidatePlan`).
- **IAM policy:** returns a JSON document mentioning the bucket.
- **Apply round-trip (no live AWS):** S3-compatible backend + a fake
  `setup.Effects` whose `NewStore` returns an in-memory store → `POST
  /api/setup/apply` → SSE completes with `repo-initialized` → the server is now
  unlocked (`GET /api/session` → `locked:false, setupNeeded:false`, `/api/dashboard`
  → 200) and a repo exists in the store.
- **Already-configured:** apply/validate on a non-setup server → 409.
- **No secrets:** the passphrase never appears in the apply response or summary.

## Commit plan

1. `feat(web): setup-mode server plumbing` — Deps + guarded config/name/needed
   accessors + `requireSetupSession` + `cli/web.go` routing swap.
2. `feat(web): setup endpoints — status, validate, IAM policy`
3. `feat(web): setup apply — provision + init + in-place unlock`
4. `feat(web): Phase 5 frontend — first-run setup wizard`

Each commit builds standalone and keeps the full `-race ./...` +
`golangci-lint` gate green.
