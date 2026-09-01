# Dedicated backup user provisioning in the setup wizard — design

2026-09-01. Approved in conversation.

## Problem

The wizard's default AWS sign-in is the AWS CLI browser login (`aws
login`). It is the easiest door — no IAM knowledge, just an account
password — and it hands every new user a temporary session that
expires within hours. The consequences land exactly where Sentra's
promise lives: the connect gate every morning, and scheduled backups
dying overnight (`launchd` fires, the session is gone, the run crashes,
a stale `meta/lock` is left behind). Most personal-account users also
sign in as root, so the powerful identity is the daily driver.

The durable alternative — a dedicated IAM user with a bucket-scoped
policy and static keys — takes an afternoon of manual IAM work that the
product only assists with by printing the policy JSON. There is no
bridge from "the trial login worked" to "credentials that survive the
night", and no warning that one is needed.

## Decision summary

The wizard gains an optional **create dedicated backup user** step that
runs right after a browser-login or SSO sign-in succeeds — the moment
the operator holds maximal power and attention. It creates IAM user
`sentra-backup`, attaches the canonical least-privilege policy as that
bucket's customer-managed policy,
mints an access key, writes it into `~/.aws/credentials` under a
dedicated profile (default `sentra`), verifies the new identity, and
points `sentra.yaml` at it. The login session is used once and retired.

Two product decisions, both made in conversation:

- **Default ON for browser login, offered-but-off for SSO, absent for
  existing-credentials, skip, and S3-compatible.** Browser login is the
  trap path; the others already chose durable auth.
- **Failure degrades to a warning, never a blocked setup.** The step is
  hardening; a working setup on session credentials beats no setup.

Names are constants, not operator inputs: user `sentra-backup`, managed
policy `sentra-s3-backup-<bucket>` (the bucket is its only variable part).
The only operator input is the profile name.

## Engine (`internal/setup`)

### Plan

Two new fields:

```go
ProvisionBackupUser bool   // the toggle
BackupUserProfile   string // profile the keys land in; default "sentra"
```

Plan validation (in `transform.go`, where the other plan rules live):
`BackupUserProfile` is trimmed; when the toggle is on it must be
non-empty, must not be `default`, and must be INI-section-safe (no `[`,
`]`, whitespace, or newline). The wizard defaults it to `sentra` when
the toggle is turned on with an empty field.

Gate: provisioning runs only when `ProvisionBackupUser && PrepareAWS`
and the resolved auth method is `login` or `sso`. Existing-credentials
and skip never provision. S3-compatible never reaches `PrepareAWS`.

### Effects

One new method on the `Effects` seam:

```go
ProvisionBackupUser(ctx context.Context, cfg *config.Config,
    opts BackupUserOptions) (BackupUserReport, error)
```

```go
type BackupUserOptions struct {
    Profile string // where the keys land in ~/.aws/credentials
}

type BackupUserReport struct {
    UserName        string // "sentra-backup"
    UserCreated     bool   // CreateUser succeeded
    UserExisted     bool   // EntityAlreadyExists → reused
    PolicyName      string // sentra-s3-backup-<bucket>
    PolicyCreated   bool   // CreatePolicy succeeded
    PolicyUpdated   bool   // existing policy received a new default version
    PolicyAttached  bool   // AttachUserPolicy succeeded
    LegacyPolicyRemoved bool // pre-managed-policy inline policy deleted
    AccessKeyID     string // non-secret identifier; never the secret
    Profile         string
    CredentialsPath string
    ProfileSwitched bool   // set by the engine after verification
    Warning         string // set by the engine on any failure
}
```

Bucket and prefix come from `cfg.Repo.S3`; the policy is
`BuildIAMPolicy(bucket, prefix)` — the single rendering path shared
with `sentra setup iam-policy`, so the wizard and the printed policy can
never drift.

The default implementation is a new `backupuser.go`. It builds an IAM
client from the same AWS config loader `awsprepare.go` uses — i.e. the
credentials that just signed in — and runs, in order:

1. `CreateUser(sentra-backup)`; `EntityAlreadyExists` is not an error
   (`UserExisted = true`, and `GetUser` then supplies the ARN the policy
   ARN is derived from — partition included).
2. `CreatePolicy(sentra-s3-backup-<bucket>, policy JSON)`. On
   `EntityAlreadyExists` the existing policy is reconciled instead: read
   its default version, merge its per-statement resources into the
   canonical document, and write a new default version only when that
   changes something (see "Multi-bucket accounts").
3. `AttachUserPolicy(sentra-backup, <policy ARN>)` — idempotent; a rerun
   re-attaches without error.
4. Best-effort cleanup of the pre-managed-policy inline policy
   `sentra-s3-backup`, only when the managed policy covers every grant
   it made. Never an error.
5. `CreateAccessKey(sentra-backup)`.
6. Write the key into the credentials file (below).
7. Pass the secret straight from the `CreateAccessKey` output to the
   writer. It is never bound to a variable of its own and never
   returned — a Go `string` the SDK hands back as a `*string` cannot be
   wiped, so narrow scope, not zeroization, is the guarantee.

Every IAM mutation that can fail comes before step 5, so a policy-side
failure never strands a live secret.

The IAM calls sit behind a twelve-method interface (`CreateUser`,
`GetUser`, `CreatePolicy`, `ListPolicyVersions`, `GetPolicyVersion`,
`CreatePolicyVersion`, `DeletePolicyVersion`, `AttachUserPolicy`,
`GetUserPolicy`, `DeleteUserPolicy`, `CreateAccessKey`,
`DeleteAccessKey`) so the default implementation is unit-testable
against a stateful fake; production passes `*iam.Client`. This adds
`github.com/aws/aws-sdk-go-v2/service/iam`.

The secret exists only inside steps 5–7 of this one function. It is
never returned, never placed in the report, the plan, the draft, review
text, logs, or an error message.

### Credentials writer (`credentialsfile.go`)

A minimal-touch editor for the AWS shared credentials file — the file is
the operator's, not Sentra's, so every byte outside the target section
is preserved. Path: `AWS_SHARED_CREDENTIALS_FILE` if set, else
`~/.aws/credentials`; `~/.aws` is created `0700` if missing.

Rules:

- Section header match is exact on the trimmed line `[<profile>]`.
- Profile `default` is refused unconditionally (the writer enforces it
  too, not only plan validation).
- If the section exists and holds `aws_access_key_id` or
  `aws_secret_access_key` (case-insensitive), refuse with
  `ErrCredentialsProfileExists` — Sentra never overwrites a credential
  it did not create.
- If the section exists without keys, the two key lines are inserted at
  the end of that section.
- Otherwise a new section is appended at EOF, separated by one blank
  line, as:

  ```
  [sentra]
  aws_access_key_id = AKIA…
  aws_secret_access_key = …
  ```

- Written via temp file in the same directory + rename; mode `0600`.
- `~/.aws/config` is never read for writing and never modified. The
  login session (`login_session` / `sso_session`) stays where it is; a
  static-key profile of the same name takes precedence in both the CLI
  and the SDK chain (verified live 2026-08-31).

### `PrepareAWS` pipeline

Today: auth → bucket prep. Now: auth → bucket prep → **provision** →
**switch**.

- Bucket prep still runs on the session credentials (the powerful
  identity provisions the bucket; the scoped user only has to use it).
- Provision calls `eff.ProvisionBackupUser`.
- Switch: on success the engine sets `p.Config.Repo.S3.Profile` to the
  report's profile and calls `CheckAWSSDKIdentity` through it with a
  bounded retry — fresh IAM keys take seconds to propagate. Backoff
  1s, 2s, 4s, 8s, 8s… until 30s total; any error retries. Only after a
  successful check does the switch stick (`ProfileSwitched = true`).
  `WriteConfig` then records the durable profile and `InitRepo` runs
  as the backup user, proving the policy end-to-end before the wizard
  says done.
- The report rides along as `AWSPrepareReport.BackupUser
  *BackupUserReport` — `nil` when provisioning was not attempted — so
  the `PrepareAWS` signature is unchanged.

## Failure handling

Every provisioning failure ends in `Warning` on the report, the profile
untouched, and `PrepareAWS` returning `nil` error. The warning is
specific per cause:

| Cause | Warning names |
|---|---|
| `AccessDenied` on any IAM call | the exact missing action (`iam:CreateUser`, `iam:GetUser`, `iam:CreatePolicy`, `iam:ListPolicyVersions`, `iam:GetPolicyVersion`, `iam:CreatePolicyVersion`, `iam:DeletePolicyVersion`, `iam:AttachUserPolicy`, `iam:CreateAccessKey`) and that the session credentials will expire |
| `LimitExceeded` on `CreateAccessKey` | the user already has two keys; remove one in IAM and rerun setup |
| `LimitExceeded` on `AttachUserPolicy` | the user already has the maximum number of managed policies attached (ten by default); detach one in IAM and rerun setup |
| `ErrCredentialsProfileExists` / `default` | the colliding profile name; pick another |
| credentials write failed after the key was minted | the engine's one ordering hazard: a live secret in AWS with nowhere to live on disk. `DeleteAccessKey` is attempted as best-effort cleanup and the warning states whether it succeeded, naming the access key ID if it did not |
| identity verification timed out | keys were written to the named profile but not yet usable; how to switch `sentra.yaml` to it once it is |

The propagation timeout fails toward "setup works right now": the
profile is not switched, so `InitRepo` runs on credentials known to
work.

Reruns are idempotent for user, policy, and attachment. Key creation is not (IAM
allows two per user), which is why the limit case has its own warning.

## Wizard (`internal/tui/setup_wizard.go`)

- **Actions stage** (alongside create-bucket / block-public /
  encryption): a `[x] create dedicated backup user (sentra-backup)`
  toggle and a `profile` text input. Visibility and default follow the
  decision: pre-checked for login, unchecked for SSO, absent otherwise.
  The input is visible only while the toggle is on, so the cursor never
  lands on a row that cannot affect the plan.
- **Review stage** — one line from `ReviewText`:
  `Backup user: create sentra-backup, keys → ~/.aws/credentials [sentra]`
  when on; `Backup user: skipped` when offered but off; `Backup user:
  sentra-backup already created, keys in ~/.aws/credentials [sentra]` on
  a retry after the previous attempt provisioned it (see below); no line
  when not applicable. The trailing no-secrets assertion line is
  unchanged and remains load-bearing.
- **Retry after a late failure** — `PrepareAWS` switches the profile on
  the copy of the plan the op holds, so a failure after it (`WriteConfig`,
  `InitRepo`) would otherwise return to a wizard still asking to provision
  on the session profile: the retry would hit `ErrCredentialsProfileExists`
  and write the session profile over the verified one. The failure branch
  adopts a *verified* switch (`ProfileSwitched`; a warning-only report is
  left alone): the plan takes the durable profile, stops asking to
  provision, and records the section in `Plan.ProvisionedBackupUserProfile`
  for the review line above. Because every forward step rebuilds the plan
  from the inputs, the adoption lands there too — the toggle goes off and
  the details-stage profile field takes the durable name — so backing out
  to actions, details, or backend and advancing again rebuilds the same
  adopted plan. The login default no longer re-seeds the toggle on after
  adoption; an explicit space on it still asks.
- **Provision stage** — unchanged shape; the pipeline already runs
  `PrepareAWS` before `WriteConfig`, so the switched profile is what
  gets written.
- **Done stage** — success: `Backup user: sentra-backup (profile
  sentra)`. Degraded: a warning block with the report's `Warning`,
  followed by `Session credentials from <method> expire; see
  docs/QUICKSTART.md`.

The `steps` result struct gains the fields the done stage needs.

## Invariants

- The secret never enters engine state, the plan, the draft, review
  text, the report, logs, or errors.
- `~/.aws/config` is never modified; the `default` credentials profile
  is never written; a credential Sentra did not create is never
  overwritten.
- Provisioning never blocks setup.
- The profile switch happens only after identity verification.

## Testing

- **Credentials writer** — table-driven: new file; append to a file
  with other sections (byte-preserved); insert into a keyless existing
  section; refuse a keyed section; refuse `default`; `0600` mode;
  temp+rename; `AWS_SHARED_CREDENTIALS_FILE` honored.
- **Default provisioner** against the fake IAM interface: happy path;
  already-exists reuse; limit; access-denied classification per step;
  write-failure triggers `DeleteAccessKey`; secret absent from the
  report and from any error string.
- **Engine** with the existing fake `Effects`: gate on flag + method;
  success sets profile and verifies (retry then success); verification
  timeout leaves the profile untouched with the warning; any failure →
  warning, `nil` error, profile untouched.
- **Review text**: new line variants; a golden asserting no
  secret-shaped value (`AKIA…`, 40-char base64) appears — the dangerous
  condition, not the happy case.
- **Wizard**: toggle defaults per method; hidden cases; review line;
  done-stage warning rendering.
- **Live**: MinIO has no IAM, so the integration tag cannot cover
  this. Optional one-off: drive the headless engine against the real
  account via the `sentra-root` profile (exercises the reuse path,
  since `sentra-backup` exists) with a throwaway profile name, then
  delete the key and profile. The fake-IAM tests are the correctness
  gate regardless.

## Documentation

- `AGENTS.md`: the setup contract gains the step, its gate/default
  rule, the never-touch-`default`/`~/.aws/config` rule, and
  degrade-to-warning.
- `README.md` and `docs/QUICKSTART.md`: the step in the AWS flow, and
  an expectations note — browser login alone is for trying Sentra;
  scheduled backups need the dedicated user (or SSO with a long
  session).
- `CLAUDE.md`: one sentence on the setup package line.

## Multi-bucket accounts

One customer-managed policy per bucket, named `sentra-s3-backup-<bucket>`
and attached to `sentra-backup`, is what lets buckets accumulate: a
second wizard run in the same account adds a policy rather than
replacing the one grant an inline policy could hold. Decisions:

- **Naming.** The bucket is the only variable part. Bucket names are
  `[a-z0-9.-]` and at most 63 characters, so the name is always valid
  and at most 80 characters (IAM allows 128). The prefix is not in the
  name: two repos sharing a bucket under different prefixes share one
  policy and are merged into it (next point).
- **Existing policy: merge, then reuse or version.** `CreatePolicy`
  answers `EntityAlreadyExists` with no ARN, so the ARN is derived from
  the user's ARN (partition and account). The default version's
  document is read, and each canonical statement's resources are
  unioned with the stored statement of the same Sid — actions, effects,
  and conditions come from the canonical document alone, so it stays
  authoritative for WHAT is allowed while the stored one contributes
  only WHERE. Resources are sorted so a rerun reproduces the stored
  bytes and is recognised as a reuse (no version written). Anything
  else — a new prefix, a canonical policy that gained an action, a
  document not in Sentra's shape — is written as a new default version.
- **Five versions per policy.** IAM's cap is fixed. At the cap the
  oldest non-default version is deleted before the new one is created,
  so the limit never fails a run; reuse keeps ordinary reruns from
  consuming versions at all.
- **Ten managed policies per user.** `AttachUserPolicy` past the quota
  is `PolicyLimit`, its own warning naming the fix. The two-keys quota
  (`KeyLimit`) is bound to `CreateAccessKey`; a quota on any other step
  is reported verbatim with its step, so no warning names the wrong fix.
- **Migration.** Installs from before this design carried one inline
  policy `sentra-s3-backup`. After the managed policy is attached, the
  inline one is read and deleted only if the managed document covers
  every statement it makes (same Sid and effect, no conditions, actions
  and resources subsets). An inline policy for a different bucket is
  that bucket's only grant and stays. The cleanup is best-effort: a
  leftover inline policy is inert, and failing setup over it would keep
  the operator on expiring session credentials for nothing.

## Out of scope

Key rotation, MFA setup, bucket versioning, a headless CLI command for
provisioning, editing `~/.aws/config`, and a connect-gate affordance for
converting an existing browser-login setup after the fact. Each is a
separate decision.
