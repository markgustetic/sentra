# Policy Scheduling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add named backup policies and installable schedules so users can run recurring Sentra backups without hand-writing cron, launchd, or systemd files.

**Architecture:** Store non-secret named policies in `sentra.yaml`, validate them in `internal/policy`, expose scriptable CLI commands in `internal/cli`, and install scheduler entries that call `sentra policy run <name>`. Policy runs reuse the existing repo snapshot, check, and prune primitives instead of adding a daemon or background service.

**Tech Stack:** Go 1.25, Cobra, koanf YAML config loading, standard-library plist/timer file generation, existing Sentra repo/CLI helpers.

---

### Task 1: Config Schema and Policy Validation

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Create: `internal/policy/policy.go`
- Create: `internal/policy/policy_test.go`
- Modify: `internal/cli/init.go`

- [ ] **Step 1: Write failing config tests**

Add tests proving `policies:` loads from YAML, defaults to an empty map, and renders back through `renderConfigYAML` without secrets:

```go
func TestLoad_Policies(t *testing.T) {
	path := writeYAML(t, t.TempDir(), fixtureYAML+`
policies:
  home:
    paths:
      - ~/Documents
    tags:
      - home
      - daily
    schedule:
      cadence: daily
      at: "03:00"
    after_backup:
      check: true
      prune: dry-run
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p := cfg.Policies["home"]
	if len(p.Paths) != 1 || p.Paths[0] != "~/Documents" {
		t.Fatalf("paths: %+v", p.Paths)
	}
	if p.Schedule.Cadence != "daily" || p.Schedule.At != "03:00" {
		t.Fatalf("schedule: %+v", p.Schedule)
	}
	if !p.AfterBackup.Check || p.AfterBackup.Prune != "dry-run" {
		t.Fatalf("after_backup: %+v", p.AfterBackup)
	}
}
```

Run: `go test ./internal/config -run 'TestLoad_Policies|TestDefaults'`
Expected: FAIL because `Config.Policies` does not exist.

- [ ] **Step 2: Implement config types and YAML rendering**

Add `PolicyConfig`, `PolicySchedule`, and `PolicyAfterBackup` to `internal/config`, seed `Policies` to an empty map in `Defaults`, and update `renderConfigYAML` to omit the `policies:` section when empty and render deterministic sorted policy names when present.

- [ ] **Step 3: Verify config tests pass**

Run: `go test ./internal/config ./internal/cli -run 'TestLoad_Policies|TestDefaults|TestRenderConfigYAML'`
Expected: PASS.

- [ ] **Step 4: Write failing validation tests**

Add tests for valid daily policies, invalid names, missing paths, unsupported cadence, invalid `HH:MM`, and unsupported prune mode.

Run: `go test ./internal/policy`
Expected: FAIL because the package does not exist.

- [ ] **Step 5: Implement minimal validator**

Implement `policy.ValidateName`, `policy.Validate`, `policy.ParseScheduleSpec`, `policy.FormatScheduleSpec`, and helpers for cadence/time validation. Supported V1 cadence values: `hourly`, `daily`, `weekly`, `monthly`, and `manual`. Supported prune values: empty/`off`, `dry-run`, and `apply`.

- [ ] **Step 6: Verify policy tests pass**

Run: `go test ./internal/policy`
Expected: PASS.

### Task 2: Policy CLI and Runner

**Files:**
- Create: `internal/cli/policy.go`
- Create: `internal/cli/policy_test.go`
- Modify: `cmd/sentra/commands.go`
- Modify: `internal/cli/backup.go` if a shared snapshot helper is needed.

- [ ] **Step 1: Write failing command tests**

Add tests for `policy list`, `policy show`, `policy add`, `policy remove`, and `policy run`. `policy run` should create one snapshot per configured path, tag snapshots with the policy name plus configured tags, and optionally run check/prune after backup.

Run: `go test ./internal/cli -run Policy`
Expected: FAIL because `NewPolicy` is undefined.

- [ ] **Step 2: Implement config-only policy commands**

Add `NewPolicy(deps PolicyDeps)` with subcommands:

```text
sentra policy add <name> --path <path> [--path <path>] [--tag <tag>] [--schedule daily@03:00] [--check] [--prune dry-run|apply|off]
sentra policy list
sentra policy show <name>
sentra policy remove <name>
```

All config writes must use `0600` and must not write secret material.

- [ ] **Step 3: Verify config command tests pass**

Run: `go test ./internal/cli -run 'TestPolicy(Add|List|Show|Remove)'`
Expected: PASS.

- [ ] **Step 4: Implement policy run through repo primitives**

Open config, blobstore, passphrase, and repo once. For every policy path, call `repo.CreateSnapshot` using current backup walker defaults and a tag string containing `policy:<name>` plus configured tags. Then run `repo.Check` when `after_backup.check` is true. For `after_backup.prune`, use existing retention planning behavior; dry-run prints but does not delete, apply deletes dropped snapshots and runs GC.

- [ ] **Step 5: Verify runner tests pass**

Run: `go test ./internal/cli -run 'TestPolicyRun'`
Expected: PASS.

### Task 3: Scheduler CLI

**Files:**
- Create: `internal/cli/schedule.go`
- Create: `internal/cli/schedule_test.go`
- Modify: `cmd/sentra/commands.go`

- [ ] **Step 1: Write failing scheduler tests**

Add tests for install/status/uninstall using an injected scheduler home directory and fake OS value. Verify macOS writes a LaunchAgent plist, Linux writes systemd user `.service` and `.timer` files, unsupported OS returns a clear error, and manual policies cannot be installed.

Run: `go test ./internal/cli -run Schedule`
Expected: FAIL because `NewSchedule` is undefined.

- [ ] **Step 2: Implement scheduler file generation**

Add `NewSchedule(deps ScheduleDeps)` with:

```text
sentra schedule install <policy>
sentra schedule status <policy>
sentra schedule uninstall <policy>
```

The generated command must be the current executable plus `policy run <name> --config <absolute-config-path> --log-level info`. On macOS write `~/Library/LaunchAgents/com.sentra.<name>.plist`. On Linux write `~/.config/systemd/user/sentra-<name>.service` and `.timer`.

- [ ] **Step 3: Verify scheduler tests pass**

Run: `go test ./internal/cli -run Schedule`
Expected: PASS.

### Task 4: Docs, Wiring, and Verification

**Files:**
- Modify: `cmd/sentra/commands.go`
- Modify: `README.md`
- Modify: `docs/QUICKSTART.md`
- Modify: `AGENTS.md`

- [ ] **Step 1: Wire production commands**

Register `cli.NewPolicy` and `cli.NewSchedule` with the same store/passphrase dependencies as backup/check/prune.

- [ ] **Step 2: Update docs**

Document policy config, policy commands, scheduler commands, and the non-secret/no-daemon scheduling model.

- [ ] **Step 3: Run focused checks**

Run:

```bash
go test ./internal/config ./internal/policy ./internal/cli
go test ./cmd/sentra
```

Expected: PASS.

- [ ] **Step 4: Run broad checks**

Run:

```bash
just check
```

Expected: PASS, or report exact missing local tooling if the command cannot run.
