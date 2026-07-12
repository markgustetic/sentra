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
| `just aws-seed` | Create the `aws-test-data/` test payload (idempotent). | no |
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
`SENTRA_PASSPHRASE` in a gitignored `.env`):

```bash
just aws-smoke
```

It runs, in order:

1. `doctor` — sanity-check config, identity, and bucket.
2. **Full backup** of `aws-test-data/`.
3. `snapshots` — list, and grab the latest snapshot id.
4. **Restore + `--verify`**, then an exact-byte `diff` against the source.
5. **Incremental backup** after appending one line — prints `new_bytes` vs
   total to prove content-defined dedup uploaded almost nothing.
6. `check` — repository integrity audit.

It reads and writes S3 but **never deletes** S3 data.

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
- **`aws-test-data/` and `sentra.yaml` are gitignored** — they won't show up in
  `git status` or get committed.
