# CLAUDE.md

Guidance for Claude Code in this repository. [AGENTS.md](AGENTS.md) holds the
full per-command behavior contract and is the source of truth; this file is the
quick reference and the load-bearing invariants.

## What this is

Sentra is a single-binary Go CLI/TUI (`cmd/sentra`) that backs up local
directories to S3 / S3-compatible storage as **encrypted, deduplicated,
content-addressed** snapshots. Go 1.27, module `github.com/markgustetic/sentra`.

The TUI is the default surface: bare `sentra` falls through to `sentra ui`. With
no `sentra.yaml` it lands on the first-run setup wizard; configured but locked,
it lands on the unlock gate; otherwise the dashboard. A configured repo that
fails to *open* (expired AWS SSO, unreachable bucket) lands on the connect
gate — retry or run the profile's aws login command from inside the TUI; only
config-file errors exit to the CLI. The TUI covers the human floor through a
seven-view rail (Dashboard, Backup, Schedules, Snapshots, Maintenance,
Settings, Help) with the occasional jobs launched from inside those; stats
and the agent are CLI-only. `ctrl+a` opens the assistant chat overlay
anywhere outside the startup gates: it answers from snapshot metadata and
compiles actions into the same confirm-gated flows the keyboard drives (needs
`ANTHROPIC_API_KEY`; inert with a hint without it). The CLI is the machine
and recovery surface (see the surface contract in AGENTS.md), and
`sentra mcp` serves the repo to MCP clients over stdio — metadata-only reads,
two-phase (plan → single-use token → confirm) mutations.

Config discovery: with no `--config`, commands use `./sentra.yaml` when
present, else `$XDG_CONFIG_HOME/sentra/sentra.yaml` (default
`~/.config`); first-run setup writes the home path, so bare `sentra`
works from any directory. `init` (cwd-only) and `local`
(`.sentra-local.yaml`) are the exceptions. An explicit `--config` must
name an existing file: `resolveConfigPath` fails fast with the path, so a
typo can neither read as an empty config nor get a new file authored at
it. Only `ui`/`setup`, whose wizard creates the file, take the unchecked
`resolveConfigPathForLaunch`.

`sentra local` is the dev flow: it starts MinIO, exports the `minioadmin`
credentials **only if you have not set AWS credentials yourself**, and opens the
wizard pre-filled for MinIO. It launches against `.sentra-local.yaml` and never
touches a real `sentra.yaml`.

## Commands (prefer `just`)

- Build: `just build` (→ `bin/sentra`), or `go build ./...`
- Test: `just test` — `go test -race -coverprofile=coverage.out ./...`
- Integration (needs Docker; testcontainers + MinIO): `just integration` —
  `go test -race -tags=integration ./...`
- Vendored FastCDC is a **separate module**: `go test ./third_party/fastcdc-go/...`
- Lint / vet / fmt: `just lint` (golangci-lint), `just vet`, `just fmt`
- Vuln scan: `just vuln` (govulncheck)
- Full local gate: `just check` (build, test, vet, lint, vuln, tidy, fmt, diff)
- Before claiming done, also: `go mod tidy -diff` and `git diff --check`.
- CI (`.github/workflows/ci.yml`) enforces: `go mod tidy -diff`,
  `gofmt -l cmd internal`, `go vet`, `go test -race`, the FastCDC module tests,
  and `golangci-lint`. Keep all of these clean.

### Running it locally

- `just local` — build and open the TUI against local MinIO. Starts MinIO if
  needed; a first run lands on the wizard. The easy path.
- `just local-reset` — reinstall `sentra`, then wipe everything (MinIO volume,
  `.sentra-local.yaml`, the `sentra-test` keyring entry, demo data) so the next
  run starts fresh at the first-run wizard. Reach for this whenever a failed
  setup has left a half-written `.sentra-local.yaml` behind.
- `just install` — `go install ./cmd/sentra` so `sentra` runs by name.
  Installs to `go env GOBIN`, else `$(go env GOPATH)/bin`; that directory must
  be on `PATH`.
- `just --list` shows the granular `local-*` recipes (backup, restore, prune,
  check, agent, recovery-kit) for driving the CLI against MinIO.

### Gating a change

A gate run tests the **working tree**, not your commits. If the tree is dirty
with work you did not author, `git add <file>` will stage the whole file — hunks
and all — and can produce a commit that does not compile even though every check
passed. Before claiming a series is good, build each commit in isolation:

```sh
git worktree add -q --detach /tmp/chk <ref> && (cd /tmp/chk && go build ./...)
git worktree remove --force /tmp/chk
```

## Package map

- `internal/crypto` — XChaCha20-Poly1305 AEAD + Argon2id KDF (`aead.go`, `kdf.go`)
- `internal/chunker` — FastCDC chunking + zstd compression
- `internal/blobstore` — `Store` interface; S3, in-memory, and retry wrappers
- `internal/repo` — snapshots, restore, GC, retention, locking, passwd (the core)
- `internal/config` — koanf config + passphrase / OS-keyring resolution
- `internal/walker` — filesystem walk + ignore matching
- `internal/agent` — local heuristics, LLM orchestration, tools, actions
- `internal/cli` — cobra commands; `internal/tui` / `internal/ui` — Bubbletea
- `internal/policy` — named-policy validation + `NextRun` (next wall-clock
  fire); `internal/progress` — reporters
- `internal/setup` — headless setup engine: pure state model + transforms, an
  `Effects` seam for AWS/keyring, and a stepwise `Engine`
  (`PrepareAWS` → `WriteConfig` → `InitRepo`). Both wizards drive it. Also
  provisions the dedicated backup user (`backupuser.go`, `backuppolicy.go`,
  `credentialsfile.go`): one customer-managed policy per bucket so buckets
  accumulate on the user, the secret never crosses the Effects seam,
  `~/.aws/config` and the `default` credentials profile are never written.
- `internal/recoverykit` — recovery-kit rendering; `internal/scheduler` — cron
  emission; `internal/diag` — doctor's AWS/repo probes + `Explain` (known-cause
  error prose for the TUI)
- `internal/mcpserver` — `sentra mcp`: stdio MCP server; metadata-only reads,
  two-phase plan→confirm mutations (see AGENTS.md for the contract)

**Import direction: `internal/cli` imports `internal/tui`, so `internal/tui`
must never import `internal/cli`.** `setup`, `recoverykit`, `scheduler`, and
`diag` exist precisely to break that cycle — logic both surfaces need lives
below both.

## Invariants — do not break

- **Encryption.** New blobs use XChaCha20-Poly1305 with a per-blob 24-byte
  **random** nonce and the version byte bound as AEAD associated data; the data
  key is derived from the passphrase via Argon2id. The bucket must never see
  plaintext, and a nonce must never be reused under a key.
- **Content addressing.** A chunk's key is the SHA-256 of its **raw
  (decompressed) plaintext**. Restore re-derives and checks this hash on read
  (`ErrChunkHashMismatch`); restore is exact-byte by construction.
- **Restore phase order is a security property.** Dirs, then files, then
  symlinks LAST, then dir metadata: no manifest symlink exists while file
  writes happen, so a crafted manifest can't route a write through a planted
  link — and every write re-checks its `EvalSymlinks`-resolved parent stays
  inside the destination. Manifests are v2 (Kind/LinkTarget entries; loaders
  refuse newer versions).
- **Retention groups by source root.** The whole policy (keep-last included)
  applies per root, and pinned snapshots (`meta/pins`) are always kept —
  `DeleteSnapshot` refuses them at the choke point. Flat global bucketing
  lets multiple sources prune each other's dailies; never regress to it.
- **GC safety.** GC computes its live set from the snapshots **present in the
  store, listed under the repo lock** — never from a caller-supplied ID set. A
  blob referenced by any present manifest must never be reaped, including one
  committed by a concurrent backup before GC took the lock.
- **One repo lock.** `CreateSnapshot`, `GC`, `sync`, `passwd`, and
  snapshot-apply serialize on the advisory lock `meta/lock` (atomic
  `PutIfAbsent` / S3 `If-None-Match: *`). Release is fail-closed: never delete a
  lock whose ownership can't be confirmed.
- **Agent / LLM.** Local heuristics run first; the LLM sees **summaries only —
  never file contents or secret values**. Recommendations are read-only by
  default. The same boundary binds the TUI chat overlay and `sentra mcp`:
  metadata only, and nothing mutates without a human confirm — the chat's
  action tools emit UI intents into the existing confirm gates, and MCP
  mutations execute only via a plan's single-use, kind-bound token.
- **No secrets in artifacts.** Never write passphrases, wrapped keys, salts, MAC
  material, or AWS credentials into `sentra.yaml`, setup drafts, logs, recovery
  kits, tests, or fixtures.
- **Config rewrites never persist env overrides.** `config.Load` returns the
  *resolved* config (file + `SENTRA_*` overlay). Rendering that back out makes a
  transient override permanent. To change a field of an existing file — settings
  toggles, `policy add/remove`, `passwd forget` — use **`config.Update`**, which
  rebases the edit on the file as it exists on disk. `config.Write` authors a
  whole file from a resolved config and is correct only for `init` and `setup`,
  which must record the bucket they just provisioned against.
- **An S3-compatible endpoint never inherits an AWS profile.** A non-empty
  `Repo.S3.Profile` reaches `awsconfig.WithSharedConfigProfile`, and
  aws-sdk-go-v2's `resolveCredentialChain` tests `sharedProfileSet` **before**
  `envConfig.Credentials.HasKeys()` — so a profile silently outranks static
  credentials. Enforced in two places, because the backend can be settled two
  ways: `DefaultPlan` settles it before inferring a profile and infers none for
  S3-compatible targets, and the TUI wizard drops an inferred profile when the
  operator picks S3-compatible by hand (mirroring the AWS branch, which clears
  the endpoint). Both drop only the *inferred* value — a profile the user wrote
  into their own config still stands, since R2 and Wasabi credentials live in one.

## Conventions

- **TDD.** Write the failing test first, watch it fail for the right reason, then
  implement the minimum to pass. Repo-layer tests use the in-memory blobstore
  (`newTestRepo`); tests are table-driven where it fits.
- **Doc comments explain _why_** — rationale and failure modes, not just what.
  Match the surrounding density.
- **Errors** are sentinels wrapped with `%w`; callers branch with `errors.Is`.
- Keep changes small and within existing package boundaries. `coverage.out` and
  other local artifacts stay out of commits.

### TUI specifics

- **`huh` cannot run inside a live `tea.Program`** — it owns `os.Stdin` and
  fights the running program. Every in-TUI form is built from inline `bubbles`
  widgets. `huh` survives only in the CLI's confirmation gates
  (`internal/cli/confirm.go`: `backup apply` / `prune --apply` / `agent apply`).
- **Selection is a glyph, not a color.** Selected rows render through
  `ui.SelectRow`, which prepends `▍`. Unit tests run under lipgloss's Ascii
  color profile, which emits **no ANSI at all**, so a color-only affordance is
  both invisible to `NO_COLOR` users and untestable. The same reasoning drives
  the splash animation, which twinkles by changing shape.
- **The focused text field is boxed, its cursor blinks, and focus follows the
  screen.** Every text field renders through `boxedField` (`ui.FieldBox`, a
  rounded frame — a glyph, per the rule above), which frames the field when —
  and only when — `Focused()` is true. Never box on a stage flag and never
  inline the frame: snapshots and the recovery kit once boxed on their stage,
  and a stage that forgot to blur came back framed around a field nobody
  focused. Focus has exactly two sources: a stage transition inside the view
  (tab, `/`, `s`, the picker's enter…) and the shell's `viewShownMsg`, which
  re-focuses whatever the current stage owns. **Constructors and `Init` focus
  nothing** — `App.Init` batches every view's `Init`, so a construction-time
  focus ran a blink chain from launch for a view the operator might never
  open. Every `Focus()` transition returns **`Focus()`'s own cmd**, never
  `textinput.Blink`: that package var resolves to bubbles/cursor's
  *unexported* bootstrap message, which no `Update` switch can name, so it is
  silently dropped and the blink never starts. Views route `cursor.BlinkMsg`
  to the focused field so the schedule keeps itself alive, and **leaving a
  stage blurs its field, as does `viewHiddenMsg`** — a focused field nobody
  renders blinks forever and lies to every `Focused()` guard. Multi-field
  views keep one `focusField`/`blurFields` pair that tab, the stage entries,
  the show, and the one-op guard's `opRejectedMsg` bounce all go through, so
  the stage's flag and `Focused()` cannot disagree; a model swapped in on
  the spot (the "again" resets) takes `viewShownMsg` itself. Two exceptions
  take blink only, no box: inputs already inside dedicated chrome (palette,
  typed-confirm modal, chat overlay), and the setup wizard, whose rows
  already carry `ui.SelectRow`'s `▍`. Sizing an input from the pane
  interior? Subtract `ui.FieldBoxOverhead`. Assign `Focus()`'s cmd to a
  local before returning — `return v, v.f.Focus()` leaves the
  copy-vs-evaluate order unspecified. `fieldOwners()` in
  `internal/tui/fieldfocus_test.go` is the table every one of these rules is
  asserted over; a new field-owning view goes in there.
- **A `cursor.BlinkCmd` is SINGLE-USE — never cache one on a reopenable
  overlay.** `BlinkCmd` bakes `BlinkMsg{id, tag}` into its closure behind a
  one-shot context deadline, so replaying it delivers a tick tagged for a
  `blinkTag` the field has already advanced past, and `cursor.Update` drops
  it: the cursor sits solid until the first keystroke, and a non-nil check on
  the cmd cannot see it. Anything built once and reopened many times
  (`Palette`, `ChatOverlay`) must **re-`Focus()` in `Init`, with a pointer
  receiver**; only a model constructed fresh per use (`TypedConfirmModal`) may
  cache. Cover reopen, not just first open.
- **Blink ticks are delivered to every focus owner ON SCREEN at once — top
  modal, palette, chat overlay, active view — never by precedence and never
  to a hidden view** (`App.Update`'s `cursor.BlinkMsg` case). Simultaneous
  delivery is safe not because a tick addresses one field — `cursor.Model.id`
  is never assigned in bubbles v1.0.0, so tags alone discriminate and
  unrelated fields do accept each other's ticks — but because accepting one
  advances the accepting field's tag, invalidating every in-flight duplicate.
  Precedence routing instead *killed* chains: an overlay does not blur the
  field beneath it, so handing that field's tick to the overlay alone stopped
  a chain nothing re-arms. Hidden views are off the list because every change
  of `m.active` goes through `switchActive`, which sends the outgoing view
  `viewHiddenMsg` before the incoming one `viewShownMsg`, so only the active
  view can own a focused field; the launch view — never switched *to* — gets
  its show from `App.Init`'s `showActiveMsg`. A test that pumps commands back
  into the App must drop `cursor.BlinkMsg` (see `drainTurn`): a blink chain
  never settles, and each pumped step waits out a real `BlinkSpeed`.
- **Never wrap an already-styled string.** `outer.Render(s)` where `s` contains
  `ui.Muted.Render(help)` embeds an ANSI reset that terminates the outer style
  mid-line. Style the plain text and append styled fragments after it.
- **A view cannot test the shell.** Driving `view.Update()` cannot catch
  key-routing bugs — a global binding stealing `q` or a digit from a focused
  text input only shows up in an `App`-level test or a real terminal. Views
  that own the keyboard declare it via `CapturesText()`.
- **Live rail preview activates the highlighted view _before_ `enter`.** So the
  activate handler can't distinguish "enter on the view I scrolled to" (dive in)
  from "enter on the passive Dashboard I launched on" (stay) by index — both are
  `m.active`. A view with nothing to interact with declares `InertContent()`, and
  the shell keeps focus on the rail instead of moving the border into its pane so
  scrolling still works. Every other view is focusable by default.
- Mutating operations go through the App's one-op guard (`startOpMsg` /
  `opResultMsg`); read-only flows use a plain `tea.Cmd` and a spinner.
- The palette (`ctrl+p`) and the chat overlay (`ctrl+a`) are mutually
  exclusive full-screen overlays; opening one closes the other. The chat
  never executes anything itself — its action tools return the same
  messages the keyboard routes (`activateMsg` / `chatBackupMsg` /
  `launchRestoreMsg`), so the existing gates and the one-op guard apply.
  While a turn streams, `esc` cancels it; the next `esc` closes the overlay.
