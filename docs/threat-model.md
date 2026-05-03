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
  the running process can extract it. We do not zero the key on exit;
  the OS reclaims the page when the process exits.
- **Forward secrecy.** There's no per-snapshot key derivation in v1, so
  passphrase compromise means historical snapshot compromise, not just
  future ones. A future `sentra passwd` will rotate the passphrase
  cheaply (only the `config` object is rewritten), but it does not
  invalidate previously-derived KEKs that an attacker might already
  hold.

## Out of scope for v1

- **Tampering detection at the bucket level.** AES-GCM detects per-object
  tampering, but a malicious S3 operator could *delete* objects (causing
  manifest decode failures) or *roll back* by serving an older version.
  S3 versioning + Object Lock at the bucket level is the right answer
  for adversarial buckets; `sentra` does not enforce them.
- **Side channels.** Timing of agent scans, disk-I/O patterns during
  chunking, etc. — not modeled.
- **Post-quantum.** AES-256 and Argon2id are the v1 building blocks.
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
