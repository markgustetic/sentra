# TUI assistant chat overlay

**Date:** 2026-08-28 · **Status:** approved (in-session) · **Owner:** Mark

## Problem

The TUI's affordances are keyboard-first; a conversational entry point
("back up my documents now, tag it pre-move", "how big was last night's
snapshot?") should compose the SAME actions without inventing a second,
less-guarded execution path.

## Decision

A chat **overlay** (not a rail view — the six-view rail stays):
`ctrl+a` opens it anywhere outside the startup gates, mirroring the
palette's overlay mechanics. Esc closes; transcript persists for the
session.

### The core rule: chat compiles intents into existing messages

The model NEVER executes anything. Its tools either answer questions from
repo metadata or emit the very messages the UI already routes — so every
existing gate still applies, by construction:

| tool | effect |
| --- | --- |
| `list_snapshots`, `repo_stats` | answered inline from the repo (metadata summaries) |
| `open_view(id)` | emits `activateMsg{id}` — same as the palette |
| `start_backup(path, tag?)` | routes to the Backup view and raises its EXISTING confirm modal (`requestBackup`); nothing runs until the human confirms |
| `restore_snapshot(id)` | emits `launchRestoreMsg{id}` — lands inside the restore flow at the destination step, human completes it |

### Engine

Reuses `internal/agent/llm.Provider` (Anthropic impl + fake for tests) —
`Deps.Provider` returns to `tui.Deps` in slim form, wired from
`UIDeps.ProviderForConfig` as before the agent-view deletion. Nil
provider → the overlay renders a "set ANTHROPIC_API_KEY" placeholder and
stays inert.

Turns run async in a `tea.Cmd` with the provider's stream channel feeding
`chatTokenMsg`s; the overlay renders the stream live. One in-flight turn
at a time; esc while streaming cancels the turn's context.

### Invariants preserved

- **Summaries only**: the system prompt and tool results carry snapshot
  metadata (ids, tags, sizes, dates) — never file contents, never secrets.
- **Read-only by default**: the only mutating paths land in existing
  confirm gates; the chat cannot skip a modal.
- **huh never runs in the program**: the overlay is inline bubbles
  (textinput + viewport), like every in-TUI form.
- **Overlay routing**: while open, the overlay captures the keyboard
  (like the palette); the shell's ctrl+c force-quit still works.

## Testing

Fake-provider tests: scripted tool calls prove `start_backup` raises the
backup confirm modal (and does NOT start an op), `open_view` activates,
`restore_snapshot` seeds the restore flow; a streaming test pins live
token rendering; a nil-provider test pins the placeholder; App-level
tests pin ctrl+a open/close routing and gate exclusion.

## Non-goals (v1)

Prune/passwd/sync via chat, multi-turn memory across sessions, voice,
running while in a startup gate.
