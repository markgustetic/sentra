# Threat model

`sentra` is a backup tool. The backup destination — typically an S3 bucket
— is generally a different trust boundary than the machine that creates the
snapshots. This doc spells out what client-side encryption protects against,
what it doesn't, and what's out of scope for v1. The same caveats are
referenced from the design doc's "Threat-model note" sections.

## What's protected

The repo key (a random 32-byte AES-256 key) is generated on `sentra init`
and never leaves the client. It encrypts every chunk, manifest, and index
before upload.

A passive observer with **read access** to the S3 bucket — a curious
operator, a leaked S3 audit log, an exfiltrated archive of the bucket —
sees only ciphertext. They cannot recover:

- File contents.
- File paths or directory structure.
- Snapshot tags or any other manifest metadata.
- The repo's identity or what the bucket is being used for, beyond the
  per-prefix layout (`config`, `snapshots/`, `index/`, `data/`).

The passphrase is run through Argon2id (m=64 MiB, i=3, p=4) before being
used to wrap the repo key, so an attacker who steals the encrypted `config`
object still has to brute-force the passphrase under a memory-hard KDF.

## What's not protected

Client-side encryption is not a metadata-erasure tool. The shape of the
ciphertext leaks the shape of the workload:

- **Object counts.** An observer can count the number of `data/<aa>/<...>`
  blobs and infer roughly how many distinct chunks the repo has.
- **Object sizes.** Each blob is `len(zstd(plaintext))` plus a fixed
  encryption overhead. The observer learns the approximate plaintext size
  of each chunk, which can in turn correlate with file types (photos vs.
  text) or with known reference content if the attacker has it.
- **Activity patterns.** S3 access logs reveal *when* `sentra` ran, what
  it uploaded, and how often. An attacker with access logs but not the
  bucket contents still learns "this client took a snapshot every Sunday
  at 02:00 with about 4 GiB of new data."
- **Snapshot timestamps.** Snapshot IDs encode their creation time
  (`snap-20260502T150405Z-a1b2`). This is by design — they're sortable —
  but it does mean the observer learns when each snapshot was taken even
  before they decrypt anything.

If you need to defeat size correlation, you'd want fixed-size padding on
every blob, which `sentra` does not do in v1 (the bandwidth cost is real
and the dedup story gets harder).

## Key handling

- **Passphrase storage.** `sentra` itself never persists your passphrase.
  The resolution chain is `--passphrase-file` → `SENTRA_PASSPHRASE` env →
  OS keyring (opt-in) → interactive `huh` prompt. The keyring lookup is
  off by default; turn it on with `passphrase.use_keyring: true` in
  `sentra.yaml` if you trust your OS keyring more than re-typing.
- **Keyring backend.** `zalando/go-keyring` proxies to the OS keychain
  (macOS Keychain, libsecret on Linux, Credential Manager on Windows).
  The same trust assumptions about your OS keyring apply.
- **Memory.** The plaintext repo key is held in process memory while
  `sentra` is running. A local attacker with code-execution privileges on
  the running process can extract it. `Repo.Close` zeroes the in-process
  copy and every `defer r.Close()` in the CLI flushes that copy on exit,
  but Go's runtime is free to copy the key onto the heap during
  goroutine scheduling and garbage collection — so a core dump or live
  memory acquisition during execution can still recover the key. Zeroing
  on `Close` collapses the *post-exit* window, not the in-flight one.
- **Forward secrecy.** There's no per-snapshot key derivation in v1, so
  passphrase compromise means historical snapshot compromise, not just
  future ones. A future `sentra passwd` will rotate the passphrase
  cheaply (only the `config` object is rewritten), but it does not
  invalidate previously-derived KEKs that an attacker might already
  hold.

## Tampering by an operator with bucket-write access

The threat: a malicious or compromised operator with bucket-write access
modifies on-disk objects with the goal of weakening sentra's protections
(e.g., downgrading KDF parameters so the wrapped repo key is brute-
forceable) or impersonating a different repository.

What blocks each attack:

- **Chunk / manifest / index tamper.** Each blob is sealed with
  XChaCha20-Poly1305 in the v3 envelope; the version byte is bound into
  the AEAD's associated data. Flipping any byte of a sealed blob
  invalidates the tag. Flipping the version byte on a v3 blob also
  invalidates the tag (the v3 AEAD expects `AD=[0x03]`; routing to an
  older decoder that expects `AD=nil` produces a tag failure).
- **Config tamper.** The `config` blob is plaintext JSON (the wrapped
  repo key inside is itself AEAD-protected, but the surrounding KDF
  params, salt, ID, and timestamps were not historically authenticated).
  Sentra now stores an HMAC-SHA256 over the config under a sub-key
  derived from the passphrase-derived KEK (HKDF-Expand domain
  `sentra/config-mac/v1`). `Open` verifies the MAC after deriving the
  KEK; mismatch produces `ErrConfigTampered`. KDF / salt tampering
  typically also breaks the wrapped-key unwrap (different KEK), so
  the operator sees `ErrWrongPassphrase` first — but either way the
  tampered config is rejected.
- **GC vs CreateSnapshot race.** Both operations acquire a single
  advisory lock at `meta/lock` (`PutIfAbsent`). A backup running
  alongside an in-progress GC fails fast with `ErrRepoLocked` rather
  than racing into an inconsistent state where the new manifest
  references chunks GC just deleted.
- **Legacy configs (no MAC).** Repos written by pre-MAC sentra builds
  `Open` with a warning logged; a future `sentra passwd` will rewrite
  the config with a MAC. This is the single migration window where
  KDF tampering wouldn't be detected by the MAC — it's still bounded
  by the wrap-shadow effect (downgrading KDF.Memory changes the KEK,
  unwrap fails first).

## Out of scope for v1

- **Bucket-level rollback to a stale snapshot of the entire repo.** The
  per-blob AEAD detects content tampering and the config MAC detects
  tamper of the config; but a malicious operator could *delete* objects
  or *roll back* the entire bucket to an older state. S3 versioning +
  Object Lock at the bucket level is the right answer for adversarial
  buckets; `sentra` does not enforce them.
- **Side channels.** Timing of agent scans, disk-I/O patterns during
  chunking, etc. — not modeled.
- **Post-quantum.** XChaCha20-Poly1305 and Argon2id are the v1 building blocks.
  Migration to a PQ-safe primitive is a future project, not a v1 lever.
- **Untrusted plaintext input.** `sentra` is a backup tool; if the input
  filesystem is hostile, `sentra` happily encrypts and uploads whatever
  is there. Adversarial input is the user's problem, not the tool's.

## Recommendations for operators

- Use a strong passphrase. Argon2id buys you a lot, but it doesn't turn
  `password123` into a real secret.
- Keep the bucket private. Block public access at the account level.
- Turn on S3 server-side default encryption too — it's free and adds a
  layer for the bucket-at-rest threat model. Client-side encryption is
  the primary defense; SSE is belt-and-suspenders.
- Consider S3 Object Lock for compliance / ransomware scenarios.
- Don't reuse the same repo key across mutually distrusting consumers
  — anyone with the passphrase can decrypt everything in the prefix.
