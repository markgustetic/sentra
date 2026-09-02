# Quickstart

A copy-paste walk through `init` -> `backup` -> `snapshots` -> `check`
-> `restore` -> `agent scan`, end to end, against a local MinIO. No AWS
account needed.

## Prerequisites

- Docker (for MinIO)
- The `sentra` binary on your `$PATH`. See the [README install
  section](../README.md#install) for `brew`, `go install`, the GHCR image,
  or a prebuilt download.
- (Optional) `ANTHROPIC_API_KEY` set — required only for `sentra agent
  scan`. Everything else works without it.

## 1. Start MinIO

> **Shortcut:** from a clone of this repo, `just local` (or `sentra local`)
> does this whole page automatically — it starts MinIO, exports throwaway
> credentials, and opens the TUI on a pre-filled first-run wizard against a
> `.sentra-local.yaml` that never touches your real config. The steps below
> are the manual, CLI-first version of the same walk.

Save as `docker-compose.yaml` in a working directory:

```yaml
services:
  minio:
    image: minio/minio:latest
    command: server /data --console-address ":9001"
    ports:
      - "9000:9000"
      - "9001:9001"
    environment:
      MINIO_ROOT_USER: minioadmin
      MINIO_ROOT_PASSWORD: minioadmin
    volumes:
      - minio-data:/data
volumes:
  minio-data:
```

Start it:

```bash
docker compose up -d
```

Open `http://localhost:9001` and sign in (`minioadmin` / `minioadmin`).
Create a bucket called `sentra-test`. Or, if you have the AWS CLI:

```bash
AWS_ACCESS_KEY_ID=minioadmin AWS_SECRET_ACCESS_KEY=minioadmin \
  aws --endpoint-url http://localhost:9000 s3 mb s3://sentra-test
```

## 2. Configure sentra

Export the credentials MinIO is expecting:

```bash
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
```

When setup initializes the repo, Sentra prompts you to set the repository
passphrase unless `--passphrase-file` or `SENTRA_PASSPHRASE` supplies it.
For normal local use, leave the wizard's
`[x] save passphrase in OS keyring` checkbox ticked; Sentra saves
the passphrase in your OS keyring and writes only
`passphrase.use_keyring: true` to `sentra.yaml`. For throwaway local demos,
you can still set `SENTRA_PASSPHRASE='change-me-to-something-good'` in your
shell, or in a gitignored `.env` when running through a tool that loads it.

Run the guided setup wizard:

```bash
sentra setup
```

For real AWS S3, choose `AWS S3`. The wizard can create or verify the
bucket before initializing Sentra. The default AWS sign-in method is
browser login with the AWS CLI, which stores temporary local credentials.
You can also choose IAM Identity Center / SSO, use an existing
profile/environment/role, or write config only.

Browser-login and SSO sessions are temporary — hours, not days — so on those
paths the wizard offers **create dedicated backup user** (pre-checked for
browser login). It creates IAM user `sentra-backup` with the least-privilege
policy, stores its access key under the `sentra` profile in
`~/.aws/credentials`, and points `sentra.yaml` at that profile once the key
verifies. Leave it on if you plan to schedule backups. If it cannot run (the
signed-in identity lacks IAM permissions, say), setup still completes on the
session credentials and tells you so; you can create the user later with the
policy from `sentra setup iam-policy`.

**More than one bucket.** Each bucket gets its own customer-managed policy,
`sentra-s3-backup-<bucket>`, attached to `sentra-backup` — so running setup
for a second bucket in the same account adds a grant instead of replacing the
first one, and two machines backing up into one bucket under different
prefixes both keep access (the second run merges its prefix into that
bucket's policy). IAM attaches at most ten managed policies to one user; past
that, setup warns and continues on the session credentials until you detach
one. Installs from before per-bucket policies carried a single inline policy
named `sentra-s3-backup`; the wizard deletes it once the managed policy that
covers the same grant is attached, and otherwise leaves it in place, since it
may still be a bucket's only grant.

Setup shows a final review screen before applying changes. If you need an
AWS administrator to grant permissions first, the wizard can print the non-secret
least-privilege IAM policy and stop before writing config or touching AWS.
If AWS auth fails, setup lets you retry, switch sign-in methods, edit
profile/region, or continue with config only. For a normal AWS CLI
profile, configure it first, for example:

```bash
aws configure --profile sentra
```

You can also generate that policy directly:

```bash
sentra setup iam-policy --bucket sentra-test --prefix sentra/
```

Enter these values when prompted:

- Storage backend: `S3-compatible or existing bucket`
- S3 bucket: `sentra-test`
- S3 key prefix: leave blank
- AWS region: `us-east-1`
- AWS profile: leave blank
- S3 endpoint URL: `http://localhost:9000`
- `[x] initialize repository` — leave ticked (space toggles)
- `[x] save passphrase in OS keyring` — leave ticked

The generated file will include these settings:

```yaml
repo:
  s3:
    bucket: sentra-test
    region: us-east-1
    endpoint_url: http://localhost:9000
backup:
  ignore_file: .sentraignore
  exclude_caches: true
  concurrency: 0
retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6
passphrase:
  use_keyring: true
```

The wizard writes `sentra.yaml`, prompts for or resolves the repository
passphrase, derives a passphrase-wrapped repo key via Argon2id, encrypts and
uploads the `config` object to S3, then saves the passphrase to the OS keyring
if selected. If a run fails, setup saves a non-secret `.sentra.yaml.setup-draft`
and loads it the next time you run the wizard. If you choose `Config only`, run
`sentra init` later.

Sentra looks for `sentra.yaml` in the current directory first, then falls
back to `~/.config/sentra/sentra.yaml` (honoring `XDG_CONFIG_HOME`).
First-run setup writes the home location, so after setting up once you
can run `sentra` from any directory. Pass `--config` to use a specific
file; it must already exist (`sentra setup --config <path>` creates one).

To verify setup without changing anything:

```bash
sentra doctor
```

## 3. Take a snapshot

Pick a small directory to back up — your home folder is too aggressive
for a first run.

```bash
mkdir -p ./demo-data && echo "hello sentra" > ./demo-data/readme.txt
sentra backup ./demo-data --tag first
```

Optional reviewed workflow:

```bash
sentra backup plan ./demo-data --tag first --out first-plan.json
sentra backup apply first-plan.json
```

The first snapshot uploads everything; subsequent snapshots only upload
chunks whose content has actually changed. Try it:

```bash
echo "another file" > ./demo-data/notes.md
sentra backup ./demo-data --tag second
```

The second `backup` reports a new snapshot but uploads only the new
chunk(s).

Optional policy workflow for repeated backups:

```bash
sentra policy add demo --path ./demo-data --tag demo --schedule daily@03:00 --check --prune dry-run
sentra policy run demo
```

To let the OS run that policy for you, install a user-level schedule:

```bash
sentra schedule install demo
sentra schedule status demo
```

On macOS this writes a LaunchAgent under `~/Library/LaunchAgents`. On Linux it
writes systemd user service/timer files under `~/.config/systemd/user`. Sentra
does not install a background daemon; the scheduler launches `sentra policy run
demo` with your existing config and passphrase resolution.

## 4. List snapshots

Pretty output:

```bash
sentra snapshots
```

Machine output for scripts:

```bash
sentra snapshots --json | jq '.[].id'
```

Note the snapshot IDs look like `snap-20260502T150405Z-a1b2` — sortable
by creation time.

## 5. Diff two snapshots

```bash
sentra diff <snap-id-first> <snap-id-second>
```

You'll see `notes.md` listed under "added".

## 6. Check repository health

For config, AWS, bucket, and repository diagnostics:

```bash
sentra doctor
```

For the deeper encrypted repository integrity scan:

```bash
sentra check
```

For disaster-recovery notes you can store outside the repo:

```bash
sentra recovery-kit --out sentra-recovery-kit.md
```

The kit contains repository identity, storage location, latest snapshot,
and copyable recovery commands. It intentionally excludes passphrases and
key material.

## 7. Restore

Preview first:

```bash
sentra restore <snap-id-second> /tmp/sentra-restored --dry-run
```

Then restore and verify the destination against the snapshot manifest:

```bash
sentra restore <snap-id-second> /tmp/sentra-restored --verify
```

Verify byte-for-byte:

```bash
diff -r ./demo-data /tmp/sentra-restored
```

Should produce no output.

## 8. Run the agent

First-run ignore suggestions:

```bash
sentra agent advise-ignore ./demo-data
```

Local-only scan, with no LLM call:

```bash
sentra agent scan --local-only --root ./demo-data
```

You'll see a table of recommendations: large files, cache directories,
secret patterns, retention drift, etc. You can limit the scan:

```bash
sentra agent scan --local-only --categories secrets,large_files --root ./demo-data
```

With `ANTHROPIC_API_KEY` set, omit `--local-only` and the LLM will refine
those recommendations and may suggest combining or deduplicating them.

To act on recommendations interactively:

```bash
sentra agent scan --apply
```

`sentra` walks each recommendation through a `huh` confirm prompt before
dispatching the action.

## 9. Launch the TUI

```bash
sentra ui   # or just `sentra` with no args
```

A left rail lists seven views — **Dashboard, Backup, Scheduled backups,
Snapshots, Maintenance, Settings, Help** — with a live preview as you scroll;
digits `1`-`7` jump straight to one, and `ctrl+p` opens a command palette. The
occasional jobs live one keypress inside: on a snapshot row `r` restores it
and `d` diffs it against another; Maintenance holds check / prune / sync /
doctor; Settings holds the recovery kit, passphrase rotation, and re-running
setup. Backup is a three-step wizard: pick the folder, pick a schedule
(one-shot, or hourly/daily/weekly/monthly with a time, weekday and editable
policy name; a chosen cadence installs a named policy plus a launchd/systemd
timer), confirm. `q` quits, and the status bar always shows which keys are
live.

## 10. Prune

When you're tired of the demo:

```bash
sentra prune                  # dry-run by default, honors sentra.yaml
sentra prune --explain        # show why snapshots are kept or dropped
sentra prune --apply          # delete after confirmation
```

## Cleanup

```bash
docker compose down -v
rm -rf ./demo-data /tmp/sentra-restored sentra.yaml sentra-recovery-kit.md
```

## What's next?

- The full [README](../README.md) covers installation options, the
  command surface, and configuration keys.
- The [architecture doc](architecture.md) walks through the storage
  format and the agent loop.
- The [threat model](threat-model.md) spells out what the encryption
  protects against — and what it doesn't.
