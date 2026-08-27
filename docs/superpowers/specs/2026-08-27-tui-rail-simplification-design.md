# TUI rail simplification

**Date:** 2026-08-27 · **Status:** approved (in-session) · **Owner:** Mark

## Problem

The sidebar rail lists 18 navigable views. The daily loop of a backup tool
is back up → look at snapshots → restore → occasionally prune; everything
else is setup-time or rare-maintenance work paying a permanent menu tax.

## Decision

Rail shrinks to **6 items**: Dashboard, Backup, Snapshots, Maintenance,
Settings, Help.

### Delete outright (code removed)

- **files** (`files.go` + `filegraph.go`) — the directory-topology
  showpiece; "what's in this snapshot" is already the snapshot detail's
  dir-tree.
- **stats** — the dashboard carries the repo readout; deep numbers remain
  `sentra stats` (CLI is the right surface).
- **agent** (TUI view only) — `sentra agent` stays on the CLI.

### Demote from the rail, launch from a parent

Mechanism: the view stays in the App's `views` slice (so `activateMsg`
routing and startup gates still work) but joins `hiddenFromRail`, which
keeps it out of the registry — and therefore out of the sidebar, palette,
and Help directory. Hidden must never mean unreachable: every demoted
view keeps exactly one launcher.

- **diff** → launched from Snapshots (compare flow), rail entry removed.
- **restore** → launched from Snapshots ("restore this snapshot");
  restoring starts from *which snapshot*, so the flow is snapshot-first.
- **check, prune, sync, doctor** → the new **Maintenance** launcher
  (`maintenance.go`), a Settings-style navigate list: one rail slot for
  the occasional-care jobs.
- **policies, schedule, recovery-kit, password, setup** → Settings
  navigate entries (password and setup were already there).

### Surface contract

AGENTS.md's "the TUI covers every job a human does" survives for the
demoted views (they are still in the TUI, one launcher away). For the
three deletions the job moves to the CLI (`sentra stats`, `sentra agent`)
or is absorbed by an existing view (snapshot detail dir-tree). AGENTS.md,
CLAUDE.md, and README view lists are updated in the same change.

## Non-goals

- No changes to the CLI surface.
- No changes to view internals of demoted views beyond their launch path.
- The unlock/connect/setup startup-gate routing is untouched.
