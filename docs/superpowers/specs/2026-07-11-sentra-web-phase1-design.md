# sentra web — Phase 1 design (foundation + unlock + dashboard + snapshots + backup)

**Status:** approved skeleton; Phase 1 spec for review.
**Date:** 2026-07-11
**Author:** brainstormed with the user.

## Why

`sentra` is a Go CLI/TUI. A synthwave web preview was produced that uses
browser-only rendering (glow, a neon sun, a perspective grid, blur, sub-cell
gradients) that **no terminal can reproduce**. To get that look literally, we
add a **browser frontend** — `sentra web` — served by the same Go backend. The
TUI stays exactly as-is; the web UI is an additional surface, not a replacement.

Full parity (all 17 surfaces) is a multi-week system, so it is decomposed into
five shippable phases. **This spec covers Phase 1 only:** a real, running
`sentra web` that looks exactly like the preview and, on the operator's real
repo, provides unlock → dashboard → snapshots (list + detail) → backup (folder
pick, start, live streamed progress, typed confirm).

### Non-goals (Phase 1)

- Restore, prune, sync, password, policies, schedule, doctor, check, diff,
  recovery-kit, agent, setup wizard — later phases (2–5).
- Multi-user, remote access, TLS, accounts — the server is single-user and
  **localhost-only** by design.
- Reimplementing backup/encryption logic — the web layer calls the existing
  `repo` package; it adds no crypto or storage logic of its own.

## Architecture

### The command

`sentra web` is a new cobra command registered in `cmd/sentra/commands.go`,
built from `internal/cli/web.go` with a `WebDeps` struct (mirroring `UIDeps`)
so it is testable with a memory store and a stub server-run callback. It:

1. Loads `sentra.yaml` (same `config.Load` path as `sentra ui`).
2. Resolves the passphrase non-interactively (keyring / env / `--passphrase-file`)
   via the existing `RepoDeps` resolvers. If that succeeds, the repo opens and
   the browser lands unlocked. If it cannot (no source), the server starts in a
   **locked** state and the browser lands on the unlock gate.
3. Starts an HTTP server bound to **`127.0.0.1` on an ephemeral port** (`:0`),
   prints the URL, and opens the default browser at it. A `--port` flag
   overrides; a `--no-open` flag suppresses the browser launch (for tests/CI).
4. Blocks until interrupted (Ctrl-C) or the browser posts `/api/shutdown`
   (optional convenience), then closes the repo and zeroizes secrets.

`--host` is intentionally **not** offered: binding anywhere but loopback would
expose an unlocked backup repo to the network. The bind address is hard-coded to
`127.0.0.1`.

### Backend reuse

The web layer is a thin HTTP adapter. It holds an opened `*repo.Repo` and calls:

- `repo.ListSnapshots` — dashboard stats + snapshots list.
- `repo.LoadSnapshot(id)` — snapshot detail (manifest → file tree).
- `repo.CreateSnapshot(root, opts{Tag, Progress, Walker})` — backup, with a
  `progress.Reporter` that feeds the SSE stream.

No new business logic. The one new server-side concept is a **single-op guard**
(one mutating operation at a time), mirroring the TUI's guard and backed
ultimately by the repo's own `meta/lock` advisory lock.

### Package layout

```
internal/web/            # NEW — the HTTP layer, no crypto/storage of its own
  server.go              # Server struct, routes, lifecycle, session, guards
  session.go             # unlock state, session token, Origin/Host checks
  api_dashboard.go       # GET /api/dashboard
  api_snapshots.go       # GET /api/snapshots, GET /api/snapshots/{id}
  api_backup.go          # GET /api/fs, POST /api/backup, GET /api/backup/{id}/events (SSE)
  api_auth.go            # GET /api/session, POST /api/unlock, POST /api/lock
  assets/                # go:embed static frontend
    index.html           # the app shell
    app.css              # the synthwave design system (from the preview)
    app.js               # vanilla router + fetch + SSE, no framework
    sun.svg / grid ...   # any static decorative assets
  assets.go              # //go:embed assets/* -> http.FS
internal/cli/web.go      # NEW — cobra command + WebDeps + wiring
cmd/sentra/commands.go   # register NewWeb(...)
```

`internal/web` may import `internal/repo`, `internal/config`, `internal/walker`,
`internal/progress`. Nothing imports `internal/web` except `internal/cli` — same
one-way rule as `internal/tui`.

## Security model (load-bearing — this is a crypto tool)

1. **Loopback only.** Listener is `127.0.0.1`. Never `0.0.0.0`, no `--host`.
2. **Origin / Host allow-list.** Every request must have `Host` ∈
   `{127.0.0.1:<port>, localhost:<port>}` and, when `Origin` is present, it must
   match. Rejects DNS-rebinding attacks where a malicious page resolves a
   hostname to 127.0.0.1 and scripts the API.
3. **Session token.** On unlock, generate a 32-byte random token; store it
   server-side with the opened repo; set it as a cookie
   (`HttpOnly; SameSite=Strict; Path=/`; `Secure` omitted because it is
   plain-HTTP loopback). Every `/api/*` request except `/api/session` and
   `/api/unlock` requires the valid cookie. State-changing requests
   (`POST /api/backup`, `/api/lock`) additionally require a matching
   `X-Sentra-Token` header echoing the cookie value (CSRF defense-in-depth
   beyond SameSite).
4. **Passphrase handling.** The passphrase is POSTed once to `/api/unlock` over
   loopback, used to `repo.Open`, then **zeroized**; it is never logged, never
   written to disk, never returned to the browser. Follows the CLAUDE.md
   no-secrets-in-artifacts invariant. The keyring/env resolvers are reused so a
   configured operator never types it.
5. **One session.** Single-user tool: a second unlock replaces the session
   (closing the prior repo). No account system.
6. **Destructive ops gated.** Phase 1's only mutation is backup (additive). When
   later phases add restore/prune/password, each requires an explicit typed
   confirmation echoed to the server (mirroring the TUI's `TypedConfirmModal`),
   enforced server-side, not just client-side.
7. **Agent boundary (later phases).** The agent still sees summaries only — never
   file contents or secret values — unchanged from the CLI/TUI.

## HTTP API (Phase 1)

All responses are JSON except `/` (HTML) and the SSE stream. Errors use
`{ "error": "human-readable message" }` with an appropriate status.

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | The app shell (index.html). |
| GET | `/assets/*` | Embedded CSS/JS/images. |
| GET | `/api/session` | `{ "locked": bool, "repoName": string }`. No auth. |
| POST | `/api/unlock` | Body `{ "passphrase": string }`. Opens the repo; on success sets the session cookie and returns `{ "locked": false, "repoName": ... }`; on failure `401` + mapped error. Rate-limited (small fixed delay on failure). |
| POST | `/api/lock` | Closes the repo, clears the session. |
| GET | `/api/dashboard` | `{ snapshotCount, totalBytes, lastSnapshot?: {id,createdAt,tag,files,bytes}, recCount }`. |
| GET | `/api/snapshots` | `[{ id, createdAt, tag, files, bytes }]` (newest first). |
| GET | `/api/snapshots/{id}` | `{ id, createdAt, tag, stats, files: [{path, size, mode}] }` from `LoadSnapshot`. |
| GET | `/api/fs?path=` | Folder listing for the backup picker: `{ cwd, parent?: string, dirs: [string] }`. Directories only. Defaults to the user's home when `path` empty. |
| POST | `/api/backup` | Body `{ root, tag }`. Validates `root` is a dir; takes the one-op guard; starts `CreateSnapshot` in a goroutine; returns `{ opId }`. `409` if an op is already running. |
| GET | `/api/backup/{opId}/events` | **SSE.** `event: progress` `data: {done,total}`; then `event: done` `data: {snapshot}` or `event: error` `data: {message}`. Closes on completion. |

### Backup flow detail

- The folder picker is client-side over `/api/fs`: the page shows the current
  directory, `..`, and subfolders; navigating re-fetches. A "Start backup of
  <dir>" button posts to `/api/backup` — but first shows a confirm dialog
  (client-rendered, matching the preview's modal), because starting a backup is
  a real action.
- `POST /api/backup` records `opId`, launches `CreateSnapshot` with a
  `progress.Reporter` that publishes `(done,total)` into a per-op channel.
- The page opens `EventSource("/api/backup/{opId}/events")`; the server drains
  the channel to SSE frames. On completion it emits `done` (with the snapshot
  info) or `error`, then closes the stream and releases the op guard.
- One-op guard: a server field `opRunning string` + mutex. Prevents a second
  concurrent mutation; the repo's `meta/lock` is the ultimate backstop.

## Frontend

- **Served from `go:embed`** — no CDN, no external fetches (mirrors the strict
  self-contained rule the preview already follows). All CSS/JS/images inline or
  embedded.
- **The design system is the preview, promoted to real CSS** (`app.css`): the
  neon palette as custom properties, the deep-space ground, the glowing wordmark,
  the outrun grid + sun, the breathing chrome, the shimmer — all literal now,
  because it is a browser.
- **Shell + views (SPA-lite).** One `index.html` shell renders the title logo,
  the sidebar rail, the content region, and the status bar. `app.js` is a tiny
  hash-router: on route change it `fetch`es the view's JSON and renders it into
  the content region. No framework, no build step — plain ES modules.
- **Live updates.** Dashboard and snapshots are fetched on view entry and on a
  gentle poll; backup progress is live via SSE.
- **Accessibility / motion.** Honor `prefers-reduced-motion` (the preview already
  does): the grid/glow animations pause; content stays legible.

## Error handling

- Repo errors map to operator-readable messages (reuse the sentinel→text mapping
  style already in the CLI/TUI, e.g. `ErrRepoLocked`, wrong-passphrase).
- A locked session hitting an authed endpoint gets `401` and the client routes to
  the unlock gate.
- The SSE stream reports op failures as an `error` event; the UI shows the mapped
  message and re-enables the backup form.

## Testing strategy

- **Command wiring** (`internal/cli/web_test.go`): `NewWeb(WebDeps{...})` with a
  memory store + stub `Serve` callback; assert config load, repo open, and that
  the server is handed the opened repo (mirrors `ui_test.go`).
- **API handlers** (`internal/web/*_test.go`): `net/http/httptest` against a
  `Server` wired to a real in-memory `repo.Repo` (the `newTestRepo` pattern).
  Table-driven: dashboard stats, snapshots list/detail, `/api/fs` listing, unlock
  success/failure, session/Origin/token enforcement (a request with a bad
  `Host`/missing token is rejected), and the one-op guard (second backup → 409).
- **Backup + SSE**: post a backup against a temp dir, drain the SSE stream, assert
  `progress` then `done` with a real snapshot that `ListSnapshots` then returns.
- **Security tests are first-class**: a test proves a cross-origin `Host` header
  is rejected, and that no endpoint returns the passphrase.
- **Frontend JS** stays minimal and is exercised via the served endpoints;
  heavy JS unit testing is out of scope for Phase 1 (the logic lives in Go).
- Full gate: `just check` (build, `-race`, vet, lint, vuln, tidy, fmt) stays
  green, including the new package.

## What Phase 1 deliberately defers

Phases 2–5 add: inspect surfaces (diff/check/doctor/recovery-kit), the remaining
operations (restore/prune/sync/password with SSE + typed-confirm), management
(policies/schedule/agent), and the first-run setup wizard. Each is its own
spec → plan → implementation cycle. The Phase 1 foundation (server, session,
security, SSE, embedded assets, design system, op-guard) is built so those slot
in as additional handlers + views, not a rewrite.
