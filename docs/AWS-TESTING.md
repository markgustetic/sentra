# Testing Sentra against real AWS S3

A walkthrough for exercising Sentra end-to-end against a real S3 bucket, using
the `just aws-*` recipes. For the zero-cloud MinIO path, see the `local-*`
recipes and [`QUICKSTART.md`](QUICKSTART.md) instead.

> **This is real data and real (tiny) S3 cost.** The recipes default to a
> dedicated throwaway payload (`aws-test-data/`) and a scratch restore dir, not
> your real files.

---

## Prerequisites

- **AWS CLI v2** — required for the browser sign-in flow (`aws --version` → `2.x`).
- **`jq`** — used by `just aws-smoke` (`brew install jq`).
- An AWS account you can create a bucket in (or an existing bucket to point at).

---

## The recipes at a glance

| Command | Purpose | Touches S3? |
|---|---|---|
| `just aws` | Build + run `sentra`. First run opens the **setup wizard**; once configured, the dashboard. | reads |
| `just aws-doctor` | Verify identity + bucket + repo without changing anything. | reads |
| `just aws-seed` | Create the `aws-test-data/` test payload, including a fresh dedup fixture. | no |
| `just aws-smoke` | **Automated test**: backup → restore + verify → incremental (dedup) → check. | read/write |
| `just aws-reset` | Wipe **local** state so the next `just aws` is a fresh wizard. | no |
| `just aws-s3-empty` | **DESTRUCTIVE** — empty the repo's S3 prefix for a truly from-scratch test. | **deletes** |
| `just aws-open-bucket` | Open the configured bucket in the S3 console. | no |

---

## First-time setup

`sentra` opens the setup wizard automatically whenever there's no `sentra.yaml`
(fresh checkout, or right after `just aws-reset`). So the whole flow is:

```bash
just aws
```

In the wizard:

1. Choose **AWS S3**.
2. Enter your **bucket**, **region** (e.g. `us-east-1`), and **prefix** (`sentra/`).
3. Sign-in method → **Browser login** → complete the browser flow.
4. Set the repository **passphrase** → **Save in keychain**.
5. Review the non-secret plan → **Initialize**.

Sentra then verifies/creates the bucket, blocks public access, enables default
encryption, writes `sentra.yaml`, and initializes the encrypted repo.

> Want the exact IAM permissions first? `sentra setup iam-policy --bucket <name>
> --prefix sentra/` prints the non-secret least-privilege policy.

---

## The automated smoke test

Once setup is done and the passphrase resolves (from the keychain, or
`SENTRA_AWS_PASSPHRASE`):

```bash
just aws-smoke
```

It runs, in order:

1. `doctor` — sanity-check config, identity, and bucket.
2. **Full backup** of `aws-test-data/`.
3. `snapshots` — list, and grab the latest snapshot id.
4. **Restore + `--verify`**, then an exact-byte `diff` against the source.
5. **Incremental backup** after appending one line to the dedup fixture.
6. `check` — repository integrity audit.

Steps 2 and 5 **assert** and exit non-zero on failure; they don't just print.

It reads and writes S3 but **never deletes** S3 data.

### How the dedup check avoids fooling itself

`aws-seed` writes `dedup-fixture.bin` — 16 MiB from `/dev/urandom`, overriding
with `SENTRA_AWS_FIXTURE_MIB`. Three properties make the assertion mean
something, and each has a way of quietly going wrong:

- **Big enough to span chunks.** FastCDC averages 1 MiB per chunk (max 4 MiB),
  so the original two-file, 67-byte payload was a single chunk — appending to it
  re-cut the only chunk there was, and no possible result proved anything.
- **Incompressible.** `new_bytes` is counted after zstd. Compressible filler
  collapses it toward zero whether or not one chunk was reused, so the check
  would pass on a repo with dedup entirely broken.
- **Regenerated every run.** A stable fixture is already in the repo on the
  second run, so snapshot 1 would dedup away to nothing and leave the
  incremental with no baseline.

The full backup guards the latter two by requiring `new_bytes >= bytes / 2` —
fresh random bytes upload roughly what they read. If that trips, the fixture
stopped being fresh or incompressible and the dedup number below it is
meaningless. The incremental then requires `new_bytes < bytes / 2`: appending at
EOF leaves every FastCDC boundary but the last intact, so only the tail chunk is
new. Typical result is ~1.5%; the ceiling is one 4 MiB chunk against 16 MiB.

---

## Resetting to test from scratch

There are two tiers, because clearing local config alone leaves the encrypted
repo living in your bucket.

**Local reset** — next `just aws` is a clean wizard:

```bash
just aws-reset
```

Removes `sentra.yaml`, the setup draft, the saved keyring passphrase, and the
local test/restore dirs. Leaves S3 untouched.

**Full reset** — also wipe the repo out of S3:

```bash
just aws-s3-empty   # DESTRUCTIVE: empties the repo's S3 prefix; confirm by typing the bucket name
just aws-reset
just aws            # fresh wizard
```

Alternatively, skip the deletion entirely and just point setup at a **new
bucket or prefix** — the old objects stay put but out of the way.

---

## Notes & gotchas

- **The TUI needs a real terminal.** `just aws` launches the full-screen UI; it
  refuses to run into a pipe. For scripted/non-interactive use, `just aws-smoke`
  and the CLI subcommands are the path.
- **Passphrase resolution order** is file → `SENTRA_PASSPHRASE` → OS keyring →
  prompt. "Save in keychain" during setup lets `aws-smoke` run without any
  prompt or env var.
- **The AWS recipes clear `SENTRA_PASSPHRASE` before running.** `.env` belongs
  to the MinIO flow, but `set dotenv-load` in the Justfile injects it into every
  recipe — and since env outranks the keyring above, the MinIO passphrase would
  reach the AWS repo and fail with `repo: wrong passphrase`. To supply one
  explicitly (CI, or a machine with no keyring), set **`SENTRA_AWS_PASSPHRASE`**;
  the `aws-*` recipes prefer it and fall back to the keyring otherwise.
- **`aws-test-data/` and `sentra.yaml` are gitignored** — they won't show up in
  `git status` or get committed.
