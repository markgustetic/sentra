# Sentra Web — Phase 4: Management (Policies · Schedule · Agent)

**Status:** approved (design), 2026-07-11
**Predecessors:** Phases 1–3 (`docs/superpowers/specs/2026-07-11-sentra-web-phase1-design.md`),
merged to `main` through `80ce55f`.

## Goal

Expose the three remaining management surfaces of the CLI/TUI through the
localhost web UI: **named policies** (backup-job CRUD + run), **schedule**
(launchd/systemd render + install), and the **agent** (advisory scan + guarded
apply). `internal/web` stays a thin HTTP adapter — no new crypto, storage, or
scheduling logic; every handler calls the existing `config` / `policy` /
`scheduler` / `agent` core.

## Invariants preserved

- Loopback-only bind, Host/Origin allow-list, per-run SameSite=Strict session
  cookie on every endpoint (all new routes go through `requireSession`).
- **Config rewrites use `config.Update`**, never `config.Write` — so a transient
  `SENTRA_*` env overlay can't be baked into `sentra.yaml`. Reads use
  `config.Load` fresh from disk (mirroring the TUI's `reload()`); the in-memory
  `s.deps.Config` is never mutated by these handlers.
- One mutating op at a time. Repo-touching operations (**policy run**, **agent
  apply**) go through the existing `startOp` guard; config/filesystem-only
  operations (**policy CRUD**, **schedule install/uninstall**) do not take the
  repo lock, matching the TUI (`config.Update` + reload; `scheduleDoneMsg`).
- Destructive operations require a **server-enforced typed confirm word** on top
  of the session guard.
- **Agent summaries-only invariant is inherited for free**: the web only calls
  `Agent.Scan` (read-only, content-free `Recommendation`/`Finding`) and
  `action.Registry.Dispatch`. No new endpoint returns file bytes or secrets.

## 1. Policies

`config.Config.Policies` is a `map[string]PolicyConfig`. `PolicyConfig` carries
koanf tags only, so the handler layer defines JSON DTOs.

### DTOs (`api_policies.go`)

```
policyDTO {
  name        string
  paths       []string
  tags        []string
  schedule    { cadence, at, weekday string }
  scheduleSpec string   // policy.FormatScheduleSpec — the shorthand shown in the TUI
  check       bool
  prune       string    // "off" | "dry-run" | "apply" (normalized)
  valid       bool
  error       string    // policy.Validate message when !valid
}
```

### Endpoints

- `GET /api/policies` — `config.Load(cfgPath)` → sorted names → `policy.Validate`
  each → `[]policyDTO`. Never fails on an individual invalid policy; marks it
  `valid:false` with the message.
- `POST /api/policies` — body `{name, paths[], tags[], scheduleSpec, check, prune,
  replace}`. `policy.ParseScheduleSpec(scheduleSpec)` → build `PolicyConfig` →
  `policy.Validate(name, p)` → `config.Update(cfgPath, …)` with the exists-check
  **inside** the mutation (mirrors `runPolicyAdd`). Bad name/schedule/paths → 400;
  duplicate without `replace` → 409.
- `DELETE /api/policies/{name}` — `config.Update` with a not-found check → 404 if
  absent.
- `POST /api/policies/{name}/run` — body `{confirm}`. Loads the policy fresh from
  disk, validates it (a corrupt prune mode must not slip into the delete path).
  Requires typed confirm `"run"` **only when** the policy's `prune == "apply"`.
  Runs under `startOp("policy-run")`: `CreateSnapshot` per path (tag
  `policy:<name>` joined with the policy's tags) → optional `Check` → optional
  prune (`PlanRetentionExplain` against `cfg.Retention`; `apply` deletes dropped
  manifests + `GC(keepIDs)`, with the drop-everything safety rail). Streams over
  the op SSE; done payload `{snapshots, checked, pruned}`.

## 2. Schedule

Not cron — renders **launchd plists** (darwin, 1 file) or **systemd user units**
(linux, 2 files) from a policy's `Schedule`. A `manual`-cadence policy is
rejected before render (both CLI and TUI reject it).

The handlers take an injectable OS/home/exe seam so tests render for a chosen
platform into a temp `$HOME`, mirroring `cli.ScheduleDeps`:

```
web.Deps.Schedule struct {
  OS         string                 // "" → runtime.GOOS
  HomeDir    func() (string, error) // nil → os.UserHomeDir
  Executable func() (string, error) // nil → os.Executable
}
```

### Endpoints (`api_schedule.go`)

- `GET /api/schedule` — per policy: `scheduler.PathsFor` + `scheduler.Installed`
  → `[]{policy, spec, cadence, installed, manual, os}` (mirrors the TUI table).
- `GET /api/schedule/{name}/preview` — `PathsFor` → `Executable` → `Render` →
  `{os, files:[{path, body}]}`. Pure/read-only; the browser shows the exact unit
  that would be written. Manual cadence → 400.
- `POST /api/schedule/{name}/install` — body `{confirm:true}` → `Render` →
  `Install` (writes `0600`). Manual → 400. Filesystem-only, no op-guard.
- `POST /api/schedule/{name}/uninstall` — body `{confirm:true}` → `Uninstall`.

## 3. Agent

Adds to `web.Deps` (threaded from `cmd/sentra/commands.go`, reusing the same
values wired into `AgentDeps`):

```
Provider   llm.Provider              // newAgentProvider(cfg); may surface a lazy key error
Actions    *action.Registry          // action.NewDefaultRegistry()
Heuristics []heuristics.Heuristic    // defaultHeuristics()
```

Server gains `lastRecs map[string]agent.Recommendation` under `mu`, set by scan,
read by apply — so **apply references only server-held recommendations**; the
browser approves by ID and can never inject an arbitrary prune target or ignore
line.

### Streaming (`op.go` addition)

Add a read-only text-streaming op alongside the mutating `startOp`: an `op` gains
a `text chan string`; `startReadStream(run func(ctx, emit func(string),
*repo.Repo) (any, error))` registers an op **without** taking the mutating guard;
`handleOpEvents` gains a `"token"` event case (draining buffered tokens before
`done`). Scan uses this to stream the model's reasoning; the `done` event carries
the recommendation list.

### Endpoints (`api_agent.go`)

- `GET /api/agent` — `{llmConfigured: <ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN
  present>}`. The UI still offers local-only scan when false.
- `POST /api/agent/scan` — body `{root, localOnly, categories[]}`. Builds
  `heuristics.NewRegistry(deps.Heuristics...)` + `agent.Agent{Repo, Heuristics,
  Provider, Actions, Config}` (Config from `cfg.Agent` + retention + walker +
  flags, `.Defaults()`). `startReadStream` runs `Scan(root, streamChan)`, forwards
  stream tokens to `emit`, stashes recs in `s.lastRecs`, returns
  `{recommendations:[…]}`. Read-only — no op-guard.
- `POST /api/agent/apply` — body `{ids[], confirm, allowWipe}`. Requires typed
  confirm `"apply"`. Resolves each id in `s.lastRecs` (unknown id → 400); skips
  `action == "none"`. Server-side **wipe guard**: if the approved prune targets
  would leave zero snapshots, require `allowWipe` **and** typed confirm `"wipe"`
  (re-checked in-loop, mirroring the TUI's `beginConfirm`/`startApply`). Runs
  under `startOp("agent-apply")`, dispatching each via `action.Registry.Dispatch`
  with `action.Env{Repo, Stdout:&buf, Cwd, FormatBytes}`. Done payload
  `{applied:[{id, action, ok, detail}], failed}`.

## 4. Frontend (`assets/app.js`, `assets/app.css`)

New **Manage** nav group → `viewPolicies`, `viewSchedule`, `viewAgent`:

- **Policies** — table (name · schedule · paths · tags · check · prune) with a
  validity badge; an add form (name, paths, tags, schedule shorthand, check,
  prune); per-row delete (typed confirm) and run (typed `"run"` confirm when
  `prune=apply`, streamed progress).
- **Schedule** — table (policy · schedule · state); per-row install/uninstall
  (simple confirm); an expandable panel showing the rendered plist/unit text from
  `/preview`.
- **Agent** — scan controls (root, local-only, categories) → a streamed
  reasoning box (`token` events) → a recommendations table with approve
  checkboxes → guarded apply (typed `"apply"`, and typed `"wipe"` when the repo
  would be emptied). Placeholder note when `llmConfigured` is false.

Reuses the existing `typedConfirm`, `streamSSE`, `progressBox`, and synthwave CSS.
`GET /api/dashboard`'s `recCount` stays 0 (a scan is on-demand; wiring a
persistent count is out of scope).

## 5. Testing (TDD, httptest)

- **Policies:** fresh-from-disk list reflects an on-disk edit; create validates
  (bad schedule → 400; duplicate without `replace` → 409) and round-trips through
  a temp `sentra.yaml`; delete → 404 on missing; run confirm-guard (prune=apply
  without `"run"` → 400) + SSE round-trip against an in-memory repo.
- **Schedule:** status list; `/preview` body assertion for an injected
  GOOS=`darwin` (plist) and `linux` (systemd) into a temp `$HOME`; install then
  status=installed then uninstall; manual-cadence → 400.
- **Agent:** `GET /api/agent`; local-only scan (no provider needed) surfaces the
  seeded findings' recommendations; apply confirm-guard; unknown/forged id → 400;
  wipe-guard refusal without `allowWipe`; a successful `add_to_ignore` apply via
  the real default registry into a temp `Cwd`.

## Commit plan

1. `feat(web): named policies — CRUD + run`
2. `feat(web): schedule — render/install launchd + systemd units`
3. `feat(web): agent — advisory scan + guarded apply`
4. `feat(web): Phase 4 frontend — Policies, Schedule, Agent views`

Each commit builds standalone (`just commits-build`) and keeps the full
`-race ./...` + `golangci-lint` gate green.
