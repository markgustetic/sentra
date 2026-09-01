# `sentra mcp` — MCP server mode

**Date:** 2026-08-28 · **Status:** approved (in-session) · **Owner:** Mark

## Problem

Sentra is "agent-aware" from the inside (its own advisor). Outside agents —
Claude, editors, anything speaking MCP — have no way to ask a repository
questions or drive a backup within Sentra's guardrails.

## Decision

A new CLI verb, `sentra mcp`, runs a Model Context Protocol server over
**stdio** (the standard local-server transport; clients configure the
binary path). Implementation uses the official
`github.com/modelcontextprotocol/go-sdk` (v1.x): typed tool handlers give
schemas for free, and its in-memory transports make the server testable
end to end without a subprocess.

### Layout

- `internal/mcpserver` — the server: tool definitions + handlers over an
  opened `*repo.Repo`. Below both surfaces; imports repo/config, never cli.
- `internal/cli/mcp.go` — the cobra verb: resolve config + passphrase
  **non-interactively** (env / `--passphrase-file` / keyring — stdio is
  the protocol channel, so there is no TTY to prompt on; a missing source
  is a clear startup error), open the repo, run the server. Logs go to
  stderr (free under stdio MCP).

### Tools — read-only set

| tool | input | returns |
| --- | --- | --- |
| `list_snapshots` | limit?, tag? | id, created, tag, file count, bytes |
| `snapshot_files` | snapshot_id, prefix?, limit | manifest entries: path, size, mtime |
| `find` | pattern (substring/glob), limit | matches across snapshots: path, snapshot id, created |
| `diff_snapshots` | a, b, limit | added/removed/changed paths (capped) + counts |
| `repo_stats` | — | totals, dedup ratio, per-snapshot averages |

### Tools — mutating, two-phase confirm

MCP has no interactive prompt, so the confirm gate becomes a protocol
contract: **plan → token → confirm**.

- `plan_backup(path, tag?)` → validates the path, returns a human-readable
  plan (what will be backed up, to which repo) plus a single-use token.
- `confirm_backup(token)` → executes the backup; reports the snapshot.
- `plan_restore(snapshot_id, dest, paths?)` / `confirm_restore(token)` —
  same shape; dest must not exist or be empty (the existing restore rule).

Tokens are in-memory, single-use, expire after 10 minutes, and are bound
to the exact plan. No `prune` in v1 — destructive operations stay on the
CLI/TUI where the typed-confirm gate lives.

### Invariants preserved

- **Metadata only, never contents**: every tool returns names, sizes,
  times, ids, stats. No tool reads file bodies out of snapshots.
- **No secrets**: no passphrase, key material, or credentials in any tool
  result or error.
- **One repo lock**: mutations go through the existing `CreateSnapshot` /
  restore paths, which serialize on `meta/lock`.
- **CLI-first surface contract**: `sentra mcp` IS a CLI verb; nothing here
  is TUI-only.

## Testing

End-to-end over `mcp.NewInMemoryTransports()`: a real `mcp.Client`
against the real server over an in-memory repo — list/find/diff round
trips, the two-phase confirm (token expiry, reuse refusal, plan/confirm
mismatch), and the never-contents rule pinned by a test that walks every
tool result for a known file body.

## Non-goals (v1)

HTTP/SSE transport, prune/GC exposure, resources/prompts (tools only),
multi-repo serving.
