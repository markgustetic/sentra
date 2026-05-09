# `sentra sync` design

Phase 5 / F3 — repo-to-repo replication. Refined via the
`superpowers:brainstorming` protocol on 2026-05-09. This doc is the
contract; the implementation follows the test list verbatim under
TDD.

F2 (`sentra mount`, FUSE) was deliberately skipped in favor of F3 to
keep every command working on macOS without macFUSE friction.

## What this command does

`sentra sync --dst-config dr.yaml` copies every snapshot, chunk, and
(on first run) the encrypted config from the cwd's repo to the
destination repo. After a successful sync, the destination is a
working clone:

- Same repo ID, same passphrase, same wrapped repo key.
- Every snapshot present on source is also on destination.
- Restoring snap-X from destination produces byte-identical output
  to restoring from source.
- The blob format is identical, so the sync path performs no
  decryption — every blob is an opaque byte transfer.

## Decisions locked

Four brainstorming questions:

| # | Question | Decision |
|---|---|---|
| Q1 | Relationship between source and destination | **A** — clone (same repo ID, same passphrase) |
| Q2 | How to specify both endpoints | **B** — source from cwd `sentra.yaml`, dest from `--dst-config <path>` |
| Q3 | Behavior on an empty destination | **B** — refuse by default; `--init-dest` to bootstrap |
| Q4 | What to do with snapshots on dest that aren't on source | **A** — additive only; never delete on dest |

Rationale for each is in the conversation log; the short version is
"clone is what 'backup of my backup' means in plain English; the
other shapes are different features that can land later without
changing this one's contract."

## Architecture

### What gets copied

```
SOURCE                          DESTINATION
──────                          ───────────
config                  ───►   config              (only when --init-dest)
data/<aa>/<sha256>      ───►   data/<aa>/<sha256>  (skip if dest has it)
snapshots/<id>          ───►   snapshots/<id>      (skip if dest has it)
meta/snapshots          ─/►    (NOT copied; dest rebuilds on next ListSnapshots)
meta/lock               ─/►    (NEVER — runtime state, not data)
```

`meta/lock` is excluded because it represents the source's running-
process state. Copying it would propagate stale source-side locks
to the destination and block dest's own ops. `meta/snapshots` is
excluded because in additive mode the dest may have snapshots
source doesn't (from a previous sync against a since-pruned
source); copying source's index would silently drop those entries
from dest's view. The cleanest semantic is "don't copy the index;
let dest rebuild it from its actual manifests on next read"
(Phase 3's self-healing path).

### Locking

| Repo | Lock taken? | Why |
|---|---|---|
| Source | **No** | Eventual consistency. Locking source for the duration of a multi-TB first-sync would block all production backup/GC/passwd activity at primary. Matches `restic copy` and standard replication tools. |
| Destination | **Yes** (same `meta/lock` Phase 4 introduced) | Prevents a concurrent `prune --apply` or `passwd` on the dest from racing sync's writes. A GC mid-sync could otherwise delete chunks sync just wrote. |

### Two-phase order: data first, then snapshots

```
Phase 1: copy missing data/  blobs (chunks)
Phase 2: copy missing snapshots/ blobs (manifests)
```

If a manifest landed on dest before its referenced chunks, an
interrupted sync would leave a dest manifest pointing at chunks
that don't exist there yet. Restore against that snapshot from
dest would fail. The phase order ensures: **whenever a manifest is
on dest, all its chunks are too**. The reverse failure mode —
chunks landed but no manifest yet — is benign: dest's
`ListSnapshots` wouldn't surface them, and the next sync resumes
from where it left off (idempotent: chunks already there are
skipped).

### Concurrency

Bounded `errgroup.SetLimit(N)` per phase, default
`runtime.GOMAXPROCS(0)`. `--concurrency` flag overrides. Same
pattern as Phase 3's concurrent restore.

Both endpoints are wrapped in Phase 2's `RetryStore` so transient
5xx / throttling on either side is auto-retried before sync's
loop sees an error.

### Single new public method on `*Repo`

```go
// SyncTo copies every snapshot, chunk, and (on InitDest) the
// config from r to dest. Additive: never deletes anything on dest.
// Lock contract: dest acquires meta/lock; source is untouched at
// the lock level.
func (r *Repo) SyncTo(ctx context.Context, dest blobstore.Store, opts SyncOptions) (SyncStats, error)

type SyncOptions struct {
    InitDest    bool                // bootstrap empty dest
    DryRun      bool                // plan without writes
    Concurrency int                 // 0 → GOMAXPROCS
    Progress    progress.Reporter   // optional progress callback
}

type SyncStats struct {
    CopiedBlobs    int
    CopiedBytes    int64
    SkippedBlobs   int
    Bootstrapped   bool   // true if dest had no config and InitDest landed one
    DryRun         bool   // true if no writes occurred
    Elapsed        time.Duration
}
```

## CLI surface

```
sentra sync [flags]
```

| Flag | Purpose |
|---|---|
| `--dst-config <path>` | **Required.** Path to the destination's `sentra.yaml`. |
| `--init-dest` | Bootstrap an empty destination by copying source's config first. |
| `--concurrency <n>` | Cap on parallel transfers per phase. Default GOMAXPROCS. |
| `--dry-run` | List what would be copied without writing. Acquires the dest lock to read its blob set. |
| (inherited) `--passphrase-file`, `--log-level`, `--log-format`, `--log-file` | Persistent root flags |

### Resolution

| What | Where it comes from |
|---|---|
| Source repo config | cwd `sentra.yaml` |
| Source passphrase | Existing chain: `--passphrase-file` → `SENTRA_PASSPHRASE` → keyring → prompt |
| Dest repo config | `--dst-config <path>` |
| Dest passphrase | **Same as source's.** Clones share a passphrase by construction. |

### Refusal cases (fail-fast, before lock acquisition)

| Cause | Error |
|---|---|
| `--dst-config` not supplied | `sentra sync: --dst-config is required` |
| Source's bucket+prefix == dest's bucket+prefix | `sentra sync: source and destination resolve to the same S3 location` |
| Source passphrase wrong | `ErrWrongPassphrase` (no dest connection attempted) |
| Source config MAC fails | `ErrConfigTampered` (refuse to replicate possibly-tampered data) |
| Dest has a `config` blob with a different repo ID | `sentra sync: destination contains a different repository (id=...); refusing to mix data` |
| Dest empty + no `--init-dest` | `sentra sync: destination has no config blob; pass --init-dest to bootstrap a new mirror` |
| Dest lock held by another op | `ErrRepoLocked` (with diagnostic naming the holder) |

### Operator UX intentional choices

- **Same passphrase for both endpoints.** One prompt total. If
  operators want diverging passphrases, they `passwd` after sync
  (and the two repos drift; subsequent sync stops working until
  re-aligned).
- **No `--yes` skip-confirm flag.** Sync is additive only; nothing
  destructive to gate. Cron can run it directly with no flags.
- **No `--dst-passphrase-file`.** Source and destination must share
  a passphrase (clone semantic); a separate flag would invite
  operators to accidentally configure mismatched passphrases that
  "work" until restore time.

## Test list (TDD; RED-first per item)

### `internal/repo/sync_test.go` (15 tests)

| # | Test | What it pins |
|---|---|---|
| 1 | `TestSyncTo_FreshDest_InitDestBootstraps` | Empty dest + InitDest=true → dest has config, all data, all manifests. Headline first-sync contract. |
| 2 | `TestSyncTo_FreshDest_RefusesWithoutInitDest` | Empty dest + InitDest=false → ErrEmptyDest, no writes. |
| 3 | `TestSyncTo_IncrementalCopiesOnlyMissing` | Pre-populate dest with subset; sync copies only the diff. Stats reflect copied vs skipped. |
| 4 | `TestSyncTo_DifferentRepoID_Refuses` | Dest has unrelated config → ErrDifferentRepo before any data writes. |
| 5 | `TestSyncTo_DataBeforeSnapshots` | After Phase 1, every chunk a Phase-2 manifest references already exists on dest. Locks ordering invariant. |
| 6 | `TestSyncTo_PreservesDestExtras` | Dest has a snapshot source doesn't → still present after sync (additive Q4=A). |
| 7 | `TestSyncTo_AcquiresDestLock` | Manually hold dest's lock; sync fails fast with ErrRepoLocked. |
| 8 | `TestSyncTo_DoesNotLockSource` | Manually hold source's lock; sync proceeds anyway. |
| 9 | `TestSyncTo_RestoreFromDestMatchesRestoreFromSource` | End-to-end: restore same snap from src + from dst, byte-identical. |
| 10 | `TestSyncTo_DoesNotCopyMetaLock` | Even when source has meta/lock, dest doesn't get one. |
| 11 | `TestSyncTo_DoesNotCopyMetaSnapshots` | After sync, dest has no meta/snapshots; first ListSnapshots rebuilds it. |
| 12 | `TestSyncTo_DryRunMakesNoWrites` | DryRun=true → returns stats matching what would be copied; dest unchanged. |
| 13 | `TestSyncTo_RetryOnTransientFailure` | flakyStore wraps dest with N transient errors; sync still completes (RetryStore composes). |
| 14 | `TestSyncTo_ResumeAfterPartialSync` | Pre-populate dest with partial copy (some chunks, no manifests); sync completes; dest's restore works. |
| 15 | `TestSyncTo_RefusesSameSrcAndDst` | Source bucket+prefix == dest's → refuse before lock acquired. |

### `internal/cli/sync_test.go` (6 tests)

| # | Test | What it pins |
|---|---|---|
| 16 | `TestSync_CLI_Basic` | End-to-end with two in-memory stores. Stub passphrase callback once; verify dest content. |
| 17 | `TestSync_CLI_RefusesMissingDstConfig` | No --dst-config → command exits with clear error. |
| 18 | `TestSync_CLI_DryRunPrintsPlan` | --dry-run prints "would copy N blobs, M bytes"; dest unchanged. |
| 19 | `TestSync_CLI_PassphraseSharedBetweenEnds` | Stub callback returns ["p"]; assert called exactly once. |
| 20 | `TestSync_CLI_WrongPassphraseShortCircuits` | Wrong source passphrase → ErrWrongPassphrase, no dest connection attempted. |
| 21 | `TestSync_CLI_RegisteredOnRoot` | Boilerplate: `sentra sync` shows up in `sentra --help`. |

## Failure modes engineered for

| Scenario | Behavior |
|---|---|
| Network drop mid-Phase-1 | Some chunks copied, some not. Re-running sync resumes — already-copied chunks are skipped, missing ones retried via RetryStore. |
| Network drop mid-Phase-2 | Some manifests copied, some not. Resume on next run; their chunks were already copied in Phase 1. |
| SIGINT during sync | ctx cancellation propagates to errgroup. Workers stop cleanly. Dest lock released via defer. Stats reflect work done up to interruption. |
| Source pruned a snapshot DURING sync | The snapshot's manifest may or may not have been listed by sync. Either way, dest is internally consistent (additive, no cascade-delete on dest). |
| Dest lock held by another op | Fail-fast with ErrRepoLocked + diagnostic naming the holder. |
| Source's config MAC fails | sync.Open(source) fails with ErrConfigTampered before dest is touched. |
| Dest has a partial prior sync (chunks but no matching manifest) | Resume catches up. Orphan chunks are unreferenced bytes that take space; `sentra prune` against dest cleans them up later. |
| Two operators run sync concurrently against same dest | One acquires the lock, the other gets ErrRepoLocked. Loser re-runs; their work is purely additive. |

## Out of scope

- **Bidirectional sync.** Different feature; merge semantics needed.
- **Mirror (destructive) sync.** Future flag `--mirror`; operators can run `sentra prune --config dst.yaml` after additive sync today for the same effect in two commands.
- **Per-snapshot subset sync.** Future feature; sync copies all snapshots that exist on source.
- **Bandwidth limiting.** Future flag `--bytes-per-second`.
- **Cross-protocol sync.** Both endpoints must be S3-API-compatible (anything blobstore/s3.go's S3Config already supports).
- **Source locking.** Eventual consistency by design.
- **Audit log.** Operator's `--log-format=json` already gives them what they need.

## Implementation order

1. RED: write `internal/repo/sync_test.go` for the 15 unit tests
   above against a not-yet-existing `Repo.SyncTo`. Confirm
   "no such method" build error.
2. GREEN: implement `Repo.SyncTo` in `internal/repo/sync.go`.
   Iterate test-by-test.
3. RED: write `internal/cli/sync_test.go` with the 6 command tests.
4. GREEN: implement `cli/sync.go` with `NewSync(deps SyncDeps)`,
   mirroring `passwd.go`'s shape.
5. Wire `cmd/sentra/main.go`.
6. Full `go test -race ./...` + `golangci-lint run`.
7. README table row + atomic commits per logical change, same
   pattern Phases 1–4 + F1 used.
