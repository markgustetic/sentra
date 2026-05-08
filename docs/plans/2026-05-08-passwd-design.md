# `sentra passwd` design

Phase 5 / F1 — passphrase rotation. Refined via the `superpowers:brainstorming`
protocol on 2026-05-08. This doc is the contract; the implementation follows
the test list verbatim under TDD.

`sentra reencrypt` (repo-key rotation) is **out of scope** for this PR; see
"Out of scope" below.

## What this command does

`sentra passwd` rewrites the encrypted `config` blob so that a new
passphrase wraps the (unchanged) repo key. After a successful rotation:

- The old passphrase no longer Opens the repo.
- The new passphrase Opens the repo and yields the same in-memory repo key.
- All existing chunks, manifests, and the snapshot index decrypt unchanged
  (their seal under the repo key is untouched).
- The on-disk config carries a fresh salt, the new wrapped repo key, and a
  fresh MAC computed under the new KEK.

## Design decisions

The rough idea was refined through six decision questions; locked answers:

| # | Question | Decision |
|---|---|---|
| Q1 | Scope: `passwd` alone, both, or stub? | **A** — `passwd` only this PR; `reencrypt` deferred |
| Q2 | Interactive vs scripted | **C** — interactive default + `--new-passphrase-file` opt-in |
| Q3 | Atomicity | N/A — single-blob rewrite; atomic at S3 |
| Q4 | Locking | **A** — same `meta/lock` advisory lock as GC |
| Q5 | Legacy-no-MAC migration | Implicit — auto-adds MAC via existing `signConfig` path |
| Q6 | Salt rotation | **B** — rotate; defensive hygiene at trivial cost |
| KDF params | Keep / refresh / flag | **Keep** — `passwd` rotates the passphrase, not security parameters |
| Q7 | Agent-recommendable action | Out of scope this PR |

## Module layout

```
internal/repo/
  passwd.go         new   Repo.Passwd(ctx, newPassphrase) + helpers
  passwd_test.go    new   unit tests against in-memory store

internal/cli/
  passwd.go         new   NewPasswd(deps PasswdDeps) cobra command
  passwd_test.go    new   command-level tests with stub passphrase callbacks

cmd/sentra/main.go  +5    wire PasswdDeps and AddCommand
```

No new packages; the layering matches `init`.

## Data flow inside `Repo.Passwd`

The in-memory `*Repo` was already opened with the old passphrase, so the
old-passphrase verification has already happened by the time `Passwd`
runs. The method needs only the new passphrase plus the open repo.

```
Repo.Passwd(ctx, newPassphrase) error
  │
  ├─ refuse if newPassphrase == oldPassphrase (caught at the CLI boundary,
  │  but also defensive at the repo layer if a programmer skips the check)
  │
  ├─ acquireLock(ctx, "passwd")           ← reuses Phase 4's meta/lock
  │   └─ defer releaseLock
  │
  ├─ keyOrErr() → defensive copy of in-memory repo key
  │   └─ defer zeroize on the local copy
  │
  ├─ generate fresh salt (16 bytes via crypto/rand)
  ├─ derive new KEK = Argon2id(newPassphrase, newSalt, r.cfg.KDF)
  ├─ wrap repo key under new KEK         → newWrappedRepoKey
  ├─ build new RepoConfig:
  │     - Version, ID, KDF, CreatedAt:    unchanged
  │     - Salt:                           newSalt
  │     - WrappedRepoKey:                 newWrappedRepoKey
  │     - MAC:                            signConfig(cfg, newKEK)
  │
  ├─ marshal + Put to "config"            ← single-blob, atomic at S3
  │
  └─ update r.cfg in-memory so a subsequent operation in the same process
     doesn't see stale on-disk state
```

## Invariants preserved

- **Repo key bytes never change.** Existing chunks/manifests/index decrypt
  identically before and after.
- **Lock held throughout.** No concurrent backup/GC/`passwd` can race with
  the rewrite.
- **MAC valid on success.** The new config has both the new wrapped key and
  a fresh MAC — fixes the legacy-no-MAC migration in one shot.
- **Old config not retained.** S3's `Put` overwrites; old bytes are
  replaced atomically.

## CLI surface

```
sentra passwd [flags]
```

Flags:

| Flag | Purpose |
|---|---|
| `--new-passphrase-file <path>` | Non-interactive new passphrase. Same parser as `--passphrase-file`: trailing newline stripped, BOM stripped, 0600 enforced on Unix. |
| (inherited) `--passphrase-file`, `--log-level`, etc. | Persistent root flags |

### Flow

```
$ sentra passwd
Repository passphrase: ●●●●●●●●●●          ← old, via existing chain
Set new repository passphrase: ●●●●●●●●●● ← new, with confirm-on-entry
Confirm new passphrase:        ●●●●●●●●●●
Rotating passphrase…
Done. Old passphrase is no longer accepted.
```

### Resolution chains

| Passphrase | Order |
|---|---|
| Old | `--passphrase-file` → `SENTRA_PASSPHRASE` → keyring (if enabled) → interactive prompt |
| New | `--new-passphrase-file` → interactive `huh` prompt with confirm-on-entry |

`SENTRA_PASSPHRASE` is **deliberately not** an env source for the new
passphrase — env vars persist in shell history and process listings,
which is the wrong default for the new secret.

### Refusal cases

- Old passphrase wrong → standard `ErrWrongPassphrase`. The new-passphrase
  prompt is never invoked.
- New passphrase < 8 bytes → refuse with the same `minPassphraseLen` floor
  as `init`.
- Old and new passphrase identical → refuse without writing.
- Repo locked by another op → fail-fast with the diagnostic from
  `acquireLock`.

### Operator UX choices

- **No `--yes` flag.** Passphrase rotation is irreversible; the brief
  interactive moment is deliberate even when the new passphrase comes
  from a file.
- **No keyring auto-update.** The OS keyring entry for the old passphrase
  is left alone — the operator chooses when to update it. Auto-clobbering
  would surprise users running multi-account setups.
- **Stderr summary, exit 0 on success.** No JSON output mode; `passwd` is
  interactive-by-design.

## Test list (TDD; RED-first per item)

### `internal/repo/passwd_test.go`

| # | Test | What it pins |
|---|---|---|
| 1 | `TestPasswd_RoundTrip` | Init → Passwd(new) → Close → Open(new) succeeds; Open(old) fails with `ErrWrongPassphrase`. The headline contract. |
| 2 | `TestPasswd_RotatesSalt` | After Passwd, `cfg.Salt` is different bytes than before. Locks Q6. |
| 3 | `TestPasswd_PreservesRepoKey` | Snapshot a known plaintext, run Passwd, restore — bytes match. |
| 4 | `TestPasswd_PreservesRepoID_KDF_CreatedAt` | Fields that should NOT change actually don't. |
| 5 | `TestPasswd_WritesValidMAC` | After Passwd, the new config's MAC verifies under the new KEK. |
| 6 | `TestPasswd_LegacyConfigGetsMAC` | Plant a no-MAC config (simulated pre-Phase-4 repo), Passwd, the rewritten config has a valid MAC. |
| 7 | `TestPasswd_RefusesIdenticalPassphrase` | Passwd with `new == old` returns a clear error without writing. |
| 8 | `TestPasswd_HoldsLock` | Acquire `meta/lock` manually, then call Passwd — fails with `ErrRepoLocked`. Releases on success. |
| 9 | `TestPasswd_LockReleasedOnError` | Force a write failure mid-Passwd; verify `meta/lock` is gone afterward. |
| 10 | `TestPasswd_ClosedRepoErrors` | After `r.Close()`, Passwd returns `ErrClosed`. |

### `internal/cli/passwd_test.go`

| # | Test | What it pins |
|---|---|---|
| 11 | `TestPasswd_CLI_Basic` | End-to-end: stub passphrase callbacks (old + new), run command, assert success summary printed. |
| 12 | `TestPasswd_CLI_NewPassphraseFlag` | `--new-passphrase-file` resolves from file; Phase 1 0600 enforcement applies. |
| 13 | `TestPasswd_CLI_NewPassphraseTooShort` | New passphrase < 8 chars → command exits non-zero, no S3 write. |
| 14 | `TestPasswd_CLI_NewMatchesOld` | Stub returns the same bytes for both — command refuses. |
| 15 | `TestPasswd_CLI_OldPassphraseWrong` | Open fails; the new-passphrase callback is **never** invoked (assertion: stub never called). |

## Failure modes engineered for

| Scenario | Behavior |
|---|---|
| Network drop mid-`PutObject` | S3 either has old or new config (atomic). Operator re-runs; new passphrase Opens. |
| SIGINT mid-Passwd | Lock blob is left behind (defer doesn't run cleanly on signal). Recovery: manual `aws s3 rm s3://bucket/meta/lock` per Phase 4 doc. |
| Operator typos new passphrase the same wrong way twice | Out of scope; they re-rotate. |
| Clock skew | Irrelevant; `cfg.CreatedAt` preserved, no `time.Now()` comparisons in the rotation path. |
| Concurrent two-`passwd` race | Second caller sees `ErrRepoLocked`. |
| Passwd while a v1 (AES-GCM) wrapped key is in the config | `Open` already handles v1; the new wrapping uses `crypto.Seal` which writes v3. Passwd opportunistically upgrades the wrapped-key envelope from v1→v3 — a quiet correctness bonus. |

## Out of scope

- **Audit trail.** No "passphrase rotated at T by U" record. Operator's
  shell history + their own logging is the source of truth.
- **Multi-passphrase support.** No N-of-M passphrases v1.
- **`reencrypt` (repo-key rotation).** Per Q1=A.
- **KDF parameter refresh during passwd.** Future flag.
- **Agent-recommendable `passwd` action handler.** Out of scope.

## Implementation order

1. RED: write `internal/repo/passwd_test.go` for the 10 unit tests above
   against a not-yet-existing `Repo.Passwd`. Confirm the file fails to
   compile with the expected "no such method" error.
2. GREEN: implement `Repo.Passwd` in `internal/repo/passwd.go`.
3. Iterate test-by-test, getting each to GREEN before writing the next.
4. RED: write `internal/cli/passwd_test.go`. The cobra wiring scaffold
   exists in other commands (`init.go`, `backup.go`); mirror it.
5. GREEN: implement `cli/passwd.go` with `NewPasswd(deps PasswdDeps)`.
6. Wire into `cmd/sentra/main.go`.
7. Full `go test -race ./...` + `golangci-lint run`.
8. Commit with the same atomic-commit-per-logical-change pattern Phases 1–4
   used.
