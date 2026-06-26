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

Export the credentials MinIO is expecting and a local test passphrase:

```bash
export AWS_ACCESS_KEY_ID=minioadmin
export AWS_SECRET_ACCESS_KEY=minioadmin
export SENTRA_PASSPHRASE='change-me-to-something-good'
```

`SENTRA_PASSPHRASE` short-circuits the interactive prompt; in real use,
either let `sentra` prompt you or store the passphrase in your OS
keyring with `passphrase.use_keyring: true`.

Run the guided setup wizard:

```bash
sentra setup
```

For real AWS S3, choose `AWS S3`. The wizard can check the selected AWS
CLI profile, run `aws sso login --profile <profile>` when needed, then
create or verify the bucket before initializing Sentra.

Enter these values when prompted:

- Storage target: `S3-compatible or existing bucket`
- S3 bucket: `sentra-test`
- S3 key prefix: leave blank
- AWS region: `us-east-1`
- AWS profile: leave blank
- S3 endpoint URL: `http://localhost:9000`
- Initialize repo: `Initialize`

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
retention:
  keep_last: 10
  keep_daily: 7
  keep_weekly: 4
  keep_monthly: 6
```

The wizard writes `sentra.yaml`, derives a passphrase-wrapped repo key
via Argon2id, encrypts and uploads the `config` object to S3, and
returns. If you choose `Config only`, run `sentra init` later.

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

Tabs across the top: `[d]ashboard`, `[s]napshots`, `[D]iff`, `[a]gent`,
`[o]perations`, `[?]help`, `[q]uit`. The operations view shows repository
health from the same checks used by `sentra check`.

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
