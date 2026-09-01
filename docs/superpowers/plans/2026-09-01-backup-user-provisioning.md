# Dedicated Backup User Provisioning Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** After a browser-login or SSO sign-in succeeds in the setup wizard, optionally create IAM user `sentra-backup` with the canonical least-privilege policy, mint an access key into a dedicated `~/.aws/credentials` profile, verify it, and point `sentra.yaml` at it — so the powerful login session is used once and retired.

**Architecture:** One new `Effects` method (`ProvisionBackupUser`) does all IAM work and the credentials-file write inside a single function, so the secret never crosses the seam. `Engine.PrepareAWS` grows two stages after bucket prep — provision, then switch the plan's profile only after the new identity verifies with a bounded retry. The TUI wizard adds a toggle + profile input on the actions stage, one review line, and a done-stage line or warning block. Failure never blocks setup: it becomes `BackupUserReport.Warning`.

**Tech Stack:** Go 1.27, aws-sdk-go-v2 (`service/iam` is the one new module), bubbles `textinput`, existing `setup.Engine`/`Effects` seam, `just check` gate.

Spec: `docs/superpowers/specs/2026-09-01-backup-user-provisioning-design.md`.

## Global Constraints

- IAM user name `sentra-backup`; inline policy name `sentra-s3-backup`; default profile `sentra`. Constants, never operator input except the profile.
- Provisioning runs only when `Plan.ProvisionBackupUser && Plan.PrepareAWS` and `ResolveAWSAuthMethod(p)` is `login` or `sso`.
- The secret access key never enters engine state, the plan, the draft, review text, the report, logs, or error strings.
- `~/.aws/config` is never modified. The `default` credentials profile is never written. A credentials section that already holds `aws_access_key_id`/`aws_secret_access_key` is never overwritten.
- Credentials file is written `0600` via temp file + rename; every byte outside the target section is preserved.
- Identity verification after the switch retries with backoff 1s, 2s, 4s, 8s, 8s… until 30s of virtual elapsed time; the profile switch sticks only after a successful check.
- Any provisioning failure → `Warning` on the report, profile untouched, `PrepareAWS` returns `nil` error.
- Repo conventions: TDD (failing test first, watch it fail for the right reason), doc comments explain *why*, sentinel errors wrapped with `%w`, `-race` on changed packages while iterating, `just check` before push, build the commit in isolation before claiming it good. Commit messages end with `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`.
- Import direction: `internal/tui` must never import `internal/cli`. Everything in this plan lives in `internal/setup` and `internal/tui`.
- Shell note: `cat`/`tail`/`head` are aliased to `bat` on this machine — use `command tail -n N`.

---

## File Structure

**Create**
- `internal/setup/credentialsfile.go` — AWS shared-credentials-file path + minimal-touch section writer + pre-check. No AWS calls.
- `internal/setup/credentialsfile_test.go`
- `internal/setup/backupuser.go` — `iamAPI` interface, `DefaultProvisionBackupUser`, `provisionBackupUser`, `BackupUserError`, IAM error classification, AWS config loader for setup.
- `internal/setup/backupuser_test.go` — fake IAM.
- `internal/setup/engine_backupuser.go` — `Engine.provisionBackupUser`, `verifyIdentityWithRetry`, `backupUserWarning`.
- `internal/setup/engine_backupuser_test.go`
- `internal/setup/backupuser_live_test.go` — optional, `//go:build live`, never runs in CI (Task 10).

**Modify**
- `internal/setup/aws_types.go` — constants, `BackupUserOptions`, `BackupUserReport`.
- `internal/setup/types.go` — `Plan.ProvisionBackupUser`, `Plan.BackupUserProfile`, `AWSPrepareReport.BackupUser`.
- `internal/setup/transform.go` — `ValidateBackupUserProfile`, `ShouldProvisionBackupUser`.
- `internal/setup/transform_test.go`
- `internal/setup/effects.go` — interface method + `defaultEffects` delegation.
- `internal/setup/engine.go` — `Engine.sleep` field + `sleepCtx`.
- `internal/setup/engine_prepare.go` — call the provisioning stage.
- `internal/setup/engine_prepare_test.go` — `fakeEffects.provisionBackupUser`.
- `internal/setup/review.go` / `review_test.go` — the review line.
- `internal/tui/setup_wizard.go` — actions rows, profile input, `CapturesText`, plan wiring, done stage.
- `internal/tui/setup_wizard_test.go` — `stubEffects.ProvisionBackupUser` + wizard tests.
- `AGENTS.md`, `README.md`, `docs/QUICKSTART.md`, `CLAUDE.md` — docs.
- `go.mod` / `go.sum` — `github.com/aws/aws-sdk-go-v2/service/iam`.

---

### Task 1: Types, constants, plan validation, and the provisioning gate

**Files:**
- Modify: `internal/setup/aws_types.go`
- Modify: `internal/setup/types.go:16-27` (Plan) and `:47-52` (AWSPrepareReport)
- Modify: `internal/setup/transform.go`
- Test: `internal/setup/transform_test.go`

**Interfaces:**
- Produces: `BackupUserName`, `BackupUserPolicyName`, `DefaultBackupUserProfile` (string consts); `BackupUserOptions{Profile string}`; `BackupUserReport{UserName, UserCreated, UserExisted, PolicyAttached, AccessKeyID, Profile, CredentialsPath, ProfileSwitched, Warning}`; `Plan.ProvisionBackupUser bool`, `Plan.BackupUserProfile string`; `AWSPrepareReport.BackupUser *BackupUserReport`; `func ValidateBackupUserProfile(name string) error`; `var ErrBackupUserProfileDefault`; `func ShouldProvisionBackupUser(p *Plan) bool`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/setup/transform_test.go`:

```go
func TestValidateBackupUserProfile(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
		wantIs  error
	}{
		{"plain", "sentra", false, nil},
		{"trimmed", "  sentra  ", false, nil},
		{"empty", "", true, nil},
		{"only spaces", "   ", true, nil},
		{"default", "default", true, ErrBackupUserProfileDefault},
		{"bracket open", "sen[tra", true, nil},
		{"bracket close", "sentra]", true, nil},
		{"inner space", "sen tra", true, nil},
		{"newline", "sentra\nx", true, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateBackupUserProfile(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateBackupUserProfile(%q) err = %v, wantErr %v", tc.in, err, tc.wantErr)
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Fatalf("err = %v, want errors.Is %v", err, tc.wantIs)
			}
		})
	}
}

// The gate is the only thing standing between an existing-credentials setup
// and an IAM mutation it never asked for, so every method is enumerated.
func TestShouldProvisionBackupUser(t *testing.T) {
	tests := []struct {
		name    string
		flag    bool
		prepare bool
		method  AWSAuthMethod
		want    bool
	}{
		{"login on", true, true, AWSAuthLogin, true},
		{"sso on", true, true, AWSAuthSSO, true},
		{"existing on", true, true, AWSAuthExisting, false},
		{"skip on", true, true, AWSAuthSkip, false},
		{"login off", false, true, AWSAuthLogin, false},
		{"login no prepare", true, false, AWSAuthLogin, false},
		{"empty method resolves to existing", true, true, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &Plan{ProvisionBackupUser: tc.flag, PrepareAWS: tc.prepare, AWSAuthMethod: tc.method}
			if got := ShouldProvisionBackupUser(p); got != tc.want {
				t.Fatalf("ShouldProvisionBackupUser = %v, want %v", got, tc.want)
			}
		})
	}
	if ShouldProvisionBackupUser(nil) {
		t.Fatal("nil plan must never provision")
	}
}
```

If `transform_test.go` does not already import `errors`, add it to the import block.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/setup/ -run 'TestValidateBackupUserProfile|TestShouldProvisionBackupUser' -count=1`
Expected: FAIL to compile — `undefined: ValidateBackupUserProfile`, `undefined: ErrBackupUserProfileDefault`, `undefined: ShouldProvisionBackupUser`, `unknown field ProvisionBackupUser`.

- [ ] **Step 3: Add the types and constants**

Append to `internal/setup/aws_types.go`:

```go
// Backup-user provisioning names. Constants, not operator inputs: one
// fewer knob in the wizard, and doctor/docs can name them without a
// lookup. Only the credentials profile is chosen by the operator.
const (
	// BackupUserName is the IAM user the wizard creates for day-to-day
	// backups. Its inline policy is BuildIAMPolicy(bucket, prefix).
	BackupUserName = "sentra-backup"
	// BackupUserPolicyName is the inline policy attached to BackupUserName.
	BackupUserPolicyName = "sentra-s3-backup"
	// DefaultBackupUserProfile is where the minted key lands in
	// ~/.aws/credentials when the operator leaves the field blank. Never
	// "default": that section is the operator's, not Sentra's.
	DefaultBackupUserProfile = "sentra"
)

// BackupUserOptions carries the operator's one choice into the provisioner.
type BackupUserOptions struct {
	// Profile is the ~/.aws/credentials section that receives the key.
	Profile string
}

// BackupUserReport is the NON-SECRET outcome of provisioning. It carries
// the access key ID (an identifier, safe to display) and never the secret:
// the secret exists only inside the Effects implementation, between
// CreateAccessKey and the credentials-file write.
type BackupUserReport struct {
	UserName        string
	UserCreated     bool // CreateUser succeeded
	UserExisted     bool // EntityAlreadyExists → reused
	PolicyAttached  bool
	AccessKeyID     string
	Profile         string
	CredentialsPath string
	// ProfileSwitched is set by the engine once the new identity verified
	// and sentra.yaml's profile now names it.
	ProfileSwitched bool
	// Warning is set by the engine on any failure; setup continues on the
	// signed-in session and the wizard shows this text.
	Warning string
}
```

In `internal/setup/types.go`, add to `Plan` after `InitRepo bool`:

```go
	// ProvisionBackupUser asks PrepareAWS to create the scoped IAM user and
	// switch the config to its static-key profile after a login/SSO sign-in.
	// Ignored for existing-credentials and skip (see ShouldProvisionBackupUser).
	ProvisionBackupUser bool
	// BackupUserProfile is the ~/.aws/credentials section for the minted
	// key; empty means DefaultBackupUserProfile.
	BackupUserProfile string
```

And to `AWSPrepareReport` after `DefaultEncryptionEnabled bool`:

```go
	// BackupUser is nil when provisioning was not attempted (gate false);
	// otherwise the non-secret outcome, Warning set on failure.
	BackupUser *BackupUserReport
```

- [ ] **Step 4: Add validation and the gate**

Append to `internal/setup/transform.go` (make sure the import block includes `errors`, `fmt`, and `strings`):

```go
// ErrBackupUserProfileDefault is returned when the operator names the
// "default" credentials profile for the backup user. That section is the
// operator's everyday identity; Sentra must never write into it.
var ErrBackupUserProfileDefault = errors.New("backup user profile must not be \"default\"")

// ValidateBackupUserProfile checks a ~/.aws/credentials section name. The
// rules are the INI file's, not AWS's: the name becomes a "[name]" header
// line, so brackets and whitespace would corrupt the file the operator's
// other tools read.
func ValidateBackupUserProfile(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("backup user profile is required")
	}
	if name == "default" {
		return ErrBackupUserProfileDefault
	}
	if strings.ContainsAny(name, "[] \t\r\n") {
		return fmt.Errorf("backup user profile %q must not contain brackets or whitespace", name)
	}
	return nil
}

// ShouldProvisionBackupUser is the single gate for the IAM provisioning
// stage. Existing-credentials and skip never provision: the operator already
// chose a durable identity, and an IAM mutation they did not ask for is the
// worst surprise a setup wizard can spring.
func ShouldProvisionBackupUser(p *Plan) bool {
	if p == nil || !p.ProvisionBackupUser || !p.PrepareAWS {
		return false
	}
	m := ResolveAWSAuthMethod(p)
	return m == AWSAuthLogin || m == AWSAuthSSO
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -run 'TestValidateBackupUserProfile|TestShouldProvisionBackupUser' -count=1`
Expected: PASS. Then `go build ./...` — Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add internal/setup/aws_types.go internal/setup/types.go internal/setup/transform.go internal/setup/transform_test.go
git commit -m "feat(setup): backup-user plan fields, report types, profile validation, gate

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: Credentials-file writer

**Files:**
- Create: `internal/setup/credentialsfile.go`
- Test: `internal/setup/credentialsfile_test.go`

**Interfaces:**
- Consumes: `ValidateBackupUserProfile` (Task 1).
- Produces: `func AWSCredentialsPath() (string, error)`; `func CheckAWSCredentialsProfileFree(path, profile string) error`; `func WriteAWSCredentialsProfile(path, profile, accessKeyID, secret string) error`; `var ErrCredentialsProfileExists`.

- [ ] **Step 1: Write the failing tests**

Create `internal/setup/credentialsfile_test.go`:

```go
package setup

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testKeyID  = "AKIAEXAMPLEEXAMPLE01"
	testSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
)

func TestWriteAWSCredentialsProfile(t *testing.T) {
	tests := []struct {
		name     string
		existing string // "" means no file
		profile  string
		wantErr  error  // errors.Is target, nil for success
		want     string // exact file content after a successful write
	}{
		{
			name:    "new file",
			profile: "sentra",
			want:    "[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "append after other sections, byte-preserved",
			existing: "; my creds\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = x\n",
			profile:  "sentra",
			want: "; my creds\n[work]\naws_access_key_id = AKIAWORK\naws_secret_access_key = x\n" +
				"\n[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "append to file without trailing newline",
			existing: "[work]\nregion = us-west-2",
			profile:  "sentra",
			want: "[work]\nregion = us-west-2\n" +
				"\n[sentra]\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n",
		},
		{
			name:     "insert into existing keyless section, later sections preserved",
			existing: "[sentra]\nregion = us-east-1\n\n[work]\naws_access_key_id = AKIAWORK\n",
			profile:  "sentra",
			want: "[sentra]\nregion = us-east-1\naws_access_key_id = " + testKeyID + "\naws_secret_access_key = " + testSecret + "\n" +
				"\n[work]\naws_access_key_id = AKIAWORK\n",
		},
		{
			name:     "refuse section that holds keys",
			existing: "[sentra]\naws_access_key_id = AKIAOLD\naws_secret_access_key = old\n",
			profile:  "sentra",
			wantErr:  ErrCredentialsProfileExists,
		},
		{
			name:     "refuse section that holds keys, mixed case",
			existing: "[sentra]\nAWS_SECRET_ACCESS_KEY=old\n",
			profile:  "sentra",
			wantErr:  ErrCredentialsProfileExists,
		},
		{
			name:    "refuse default",
			profile: "default",
			wantErr: ErrBackupUserProfileDefault,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".aws", "credentials")
			if tc.existing != "" {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.existing), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			err := WriteAWSCredentialsProfile(path, tc.profile, testKeyID, testSecret)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				// A refusal must leave the file exactly as it was.
				if tc.existing != "" {
					got, _ := os.ReadFile(path)
					if string(got) != tc.existing {
						t.Fatalf("refused write modified the file:\n%s", got)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteAWSCredentialsProfile: %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("content mismatch\n--- got\n%s\n--- want\n%s", got, tc.want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Fatalf("mode = %o, want 0600", perm)
			}
			// Atomic write leaves no temp files behind.
			entries, _ := os.ReadDir(filepath.Dir(path))
			for _, e := range entries {
				if strings.Contains(e.Name(), ".tmp") {
					t.Fatalf("temp file left behind: %s", e.Name())
				}
			}
		})
	}
}

func TestCheckAWSCredentialsProfileFree(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials")
	if err := CheckAWSCredentialsProfileFree(path, "sentra"); err != nil {
		t.Fatalf("missing file must be free: %v", err)
	}
	if err := os.WriteFile(path, []byte("[sentra]\naws_access_key_id = x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "sentra"); !errors.Is(err, ErrCredentialsProfileExists) {
		t.Fatalf("keyed section must report exists, got %v", err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "other"); err != nil {
		t.Fatalf("absent section must be free: %v", err)
	}
	if err := CheckAWSCredentialsProfileFree(path, "default"); !errors.Is(err, ErrBackupUserProfileDefault) {
		t.Fatalf("default must be refused, got %v", err)
	}
}

func TestAWSCredentialsPathHonorsEnv(t *testing.T) {
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "/tmp/x/creds")
	got, err := AWSCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if got != "/tmp/x/creds" {
		t.Fatalf("path = %q, want env override", got)
	}
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", "")
	got, err = AWSCredentialsPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, filepath.Join(".aws", "credentials")) {
		t.Fatalf("path = %q, want ~/.aws/credentials", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/setup/ -run 'TestWriteAWSCredentialsProfile|TestCheckAWSCredentialsProfileFree|TestAWSCredentialsPathHonorsEnv' -count=1`
Expected: FAIL to compile — `undefined: WriteAWSCredentialsProfile`, `ErrCredentialsProfileExists`, `CheckAWSCredentialsProfileFree`, `AWSCredentialsPath`.

- [ ] **Step 3: Write the implementation**

Create `internal/setup/credentialsfile.go`:

```go
package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrCredentialsProfileExists is returned when the target section of the
// AWS shared credentials file already holds an access key. Sentra never
// overwrites a credential it did not create: the operator may have put
// that key there on purpose, and a silent replacement would break every
// other tool reading the profile.
var ErrCredentialsProfileExists = errors.New("aws credentials profile already holds keys")

// AWSCredentialsPath returns the AWS shared credentials file path, honoring
// AWS_SHARED_CREDENTIALS_FILE and falling back to ~/.aws/credentials — the
// same resolution the AWS CLI and SDK use, so the key lands where the SDK
// will look for it.
func AWSCredentialsPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv("AWS_SHARED_CREDENTIALS_FILE")); p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate aws credentials: %w", err)
	}
	return filepath.Join(home, ".aws", "credentials"), nil
}

// CheckAWSCredentialsProfileFree reports whether WriteAWSCredentialsProfile
// would accept profile at path — the same refusals, without writing. The
// provisioner calls it BEFORE creating an access key, so a doomed run makes
// no IAM mutation it would only have to undo.
func CheckAWSCredentialsProfileFree(path, profile string) error {
	profile = strings.TrimSpace(profile)
	if err := ValidateBackupUserProfile(profile); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // path is the operator's own credentials file
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	_, err = upsertCredentialsSection(existing, profile, "", "")
	return err
}

// WriteAWSCredentialsProfile stores accessKeyID/secret under [profile] in
// the shared credentials file at path. It is a minimal-touch edit: the file
// is the operator's, so every byte outside the target section is preserved,
// including comments and unknown keys. The write is temp-file + rename and
// the result is mode 0600.
//
// Refusals (see ValidateBackupUserProfile and ErrCredentialsProfileExists)
// leave the file untouched.
func WriteAWSCredentialsProfile(path, profile, accessKeyID, secret string) error {
	profile = strings.TrimSpace(profile)
	if err := ValidateBackupUserProfile(profile); err != nil {
		return err
	}
	existing, err := os.ReadFile(path) //nolint:gosec // path is the operator's own credentials file
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	updated, err := upsertCredentialsSection(existing, profile, accessKeyID, secret)
	if err != nil {
		return err
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	// CreateTemp opens 0600; rename makes the replacement atomic, so a crash
	// mid-write can never leave a half-written credentials file behind.
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp credentials file: %w", err)
	}
	tmpPath := tmp.Name()
	fail := func(step string, err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("%s %s: %w", step, path, err)
	}
	if _, err := tmp.Write(updated); err != nil {
		return fail("write", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		return fail("chmod", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("close %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// upsertCredentialsSection returns existing with [profile] holding the two
// key lines, or ErrCredentialsProfileExists when that section already has
// either key. Pure: the writer and the pre-check share it so they can never
// disagree about what counts as "taken".
func upsertCredentialsSection(existing []byte, profile, accessKeyID, secret string) ([]byte, error) {
	header := "[" + profile + "]"
	keyLines := []string{
		"aws_access_key_id = " + accessKeyID,
		"aws_secret_access_key = " + secret,
	}
	if len(existing) == 0 {
		return []byte(header + "\n" + strings.Join(keyLines, "\n") + "\n"), nil
	}

	text := string(existing)
	lines := strings.Split(text, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == header {
			start = i
			break
		}
	}
	if start == -1 {
		var b strings.Builder
		b.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("\n" + header + "\n" + strings.Join(keyLines, "\n") + "\n")
		return []byte(b.String()), nil
	}

	end := len(lines) // exclusive: next section header or EOF
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			end = i
			break
		}
	}
	insertAt := start + 1
	for i := start + 1; i < end; i++ {
		key, _, ok := strings.Cut(lines[i], "=")
		if ok {
			switch strings.ToLower(strings.TrimSpace(key)) {
			case "aws_access_key_id", "aws_secret_access_key":
				return nil, fmt.Errorf("%w: [%s]", ErrCredentialsProfileExists, profile)
			}
		}
		if strings.TrimSpace(lines[i]) != "" {
			insertAt = i + 1 // after the section's last non-blank line
		}
	}
	out := make([]string, 0, len(lines)+len(keyLines))
	out = append(out, lines[:insertAt]...)
	out = append(out, keyLines...)
	out = append(out, lines[insertAt:]...)
	joined := strings.Join(out, "\n")
	if !strings.HasSuffix(joined, "\n") {
		joined += "\n"
	}
	return []byte(joined), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -run 'TestWriteAWSCredentialsProfile|TestCheckAWSCredentialsProfileFree|TestAWSCredentialsPathHonorsEnv' -count=1 -v`
Expected: PASS for every subtest. If the "insert into existing keyless section" case fails on a trailing blank line, compare `got` and `want` byte-for-byte in the failure output — the blank line between `[sentra]`'s block and `[work]` must be preserved exactly once.

- [ ] **Step 5: Commit**

```bash
git add internal/setup/credentialsfile.go internal/setup/credentialsfile_test.go
git commit -m "feat(setup): minimal-touch AWS credentials-file writer with refusals

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: IAM provisioner behind a fake-able interface

**Files:**
- Modify: `go.mod` / `go.sum` (new module)
- Create: `internal/setup/backupuser.go`
- Test: `internal/setup/backupuser_test.go`

**Interfaces:**
- Consumes: Task 1 types/constants, Task 2 `CheckAWSCredentialsProfileFree`/`WriteAWSCredentialsProfile`/`AWSCredentialsPath`, existing `BuildIAMPolicy(bucket, prefix)`.
- Produces: `type iamAPI interface{ CreateUser; PutUserPolicy; CreateAccessKey; DeleteAccessKey }` (aws-sdk-go-v2 signatures); `type credentialsWriter func(path, profile, accessKeyID, secret string) error`; `type BackupUserError struct{ Step string; AccessDenied, KeyLimit bool; KeyOrphaned string; Err error }` with `Error()`/`Unwrap()`; `func DefaultProvisionBackupUser(ctx, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error)`; `func provisionBackupUser(ctx, client iamAPI, cfg *config.Config, opts BackupUserOptions, credsPath string, write credentialsWriter) (BackupUserReport, error)`.

- [ ] **Step 1: Add the IAM module**

Run:
```bash
go get github.com/aws/aws-sdk-go-v2/service/iam@v1.62.0 && go mod tidy && go build ./...
```
Expected: `go.mod` gains `github.com/aws/aws-sdk-go-v2/service/iam v1.62.0` as a direct requirement (it may also bump shared `aws-sdk-go-v2` internals — that is fine; `go mod tidy -diff` must be clean afterwards). Then confirm the type names used below exist:

```bash
go doc github.com/aws/aws-sdk-go-v2/service/iam/types EntityAlreadyExistsException | command head -n 3
go doc github.com/aws/aws-sdk-go-v2/service/iam/types LimitExceededException | command head -n 3
go doc github.com/aws/aws-sdk-go-v2/service/iam CreateAccessKeyOutput | command head -n 12
```
Expected: each prints a type declaration; `CreateAccessKeyOutput.AccessKey` is `*types.AccessKey` with `AccessKeyId *string` and `SecretAccessKey *string`.

- [ ] **Step 2: Write the failing tests**

Create `internal/setup/backupuser_test.go`:

```go
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

const fakeSecret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"

// fakeIAM records calls; func fields default to success.
type fakeIAM struct {
	createUserErr   error
	putPolicyErr    error
	createKeyErr    error
	deleteKeyErr    error
	createUserCalls int
	putPolicyDoc    string
	createKeyCalls  int
	deletedKeyID    string
}

func (f *fakeIAM) CreateUser(_ context.Context, in *iam.CreateUserInput, _ ...func(*iam.Options)) (*iam.CreateUserOutput, error) {
	f.createUserCalls++
	if f.createUserErr != nil {
		return nil, f.createUserErr
	}
	return &iam.CreateUserOutput{User: &iamtypes.User{UserName: in.UserName}}, nil
}

func (f *fakeIAM) PutUserPolicy(_ context.Context, in *iam.PutUserPolicyInput, _ ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error) {
	if f.putPolicyErr != nil {
		return nil, f.putPolicyErr
	}
	f.putPolicyDoc = aws.ToString(in.PolicyDocument)
	return &iam.PutUserPolicyOutput{}, nil
}

func (f *fakeIAM) CreateAccessKey(_ context.Context, _ *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	f.createKeyCalls++
	if f.createKeyErr != nil {
		return nil, f.createKeyErr
	}
	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId:     aws.String("AKIAFAKEFAKEFAKEFAKE"),
		SecretAccessKey: aws.String(fakeSecret),
	}}, nil
}

func (f *fakeIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	f.deletedKeyID = aws.ToString(in.AccessKeyId)
	if f.deleteKeyErr != nil {
		return nil, f.deleteKeyErr
	}
	return &iam.DeleteAccessKeyOutput{}, nil
}

// accessDenied mimics the generic API error IAM returns for a missing
// permission — a smithy.GenericAPIError, not a typed exception.
func accessDenied() error {
	return &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}
}

type writerCall struct {
	path, profile, keyID, secret string
}

func backupUserCfg() *config.Config {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "example-bucket"
	cfg.Repo.S3.Prefix = "sentra/"
	cfg.Repo.S3.Region = "us-east-1"
	return &cfg
}

func TestProvisionBackupUser_HappyPath(t *testing.T) {
	f := &fakeIAM{}
	var got writerCall
	write := func(path, profile, keyID, secret string) error {
		got = writerCall{path, profile, keyID, secret}
		return nil
	}
	report, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds", write)
	if err != nil {
		t.Fatalf("provisionBackupUser: %v", err)
	}
	if !report.UserCreated || report.UserExisted || !report.PolicyAttached {
		t.Fatalf("report flags = %+v", report)
	}
	if report.UserName != BackupUserName || report.Profile != "sentra" || report.CredentialsPath != "/tmp/creds" {
		t.Fatalf("report identity fields = %+v", report)
	}
	if report.AccessKeyID != "AKIAFAKEFAKEFAKEFAKE" {
		t.Fatalf("AccessKeyID = %q", report.AccessKeyID)
	}
	if got.secret != fakeSecret || got.profile != "sentra" || got.path != "/tmp/creds" {
		t.Fatalf("writer got %+v", got)
	}
	// The policy is the canonical document for this bucket+prefix.
	var doc IAMPolicyDocument
	if err := json.Unmarshal([]byte(f.putPolicyDoc), &doc); err != nil {
		t.Fatalf("policy is not JSON: %v", err)
	}
	if want := BuildIAMPolicy("example-bucket", "sentra/"); len(doc.Statement) != len(want.Statement) {
		t.Fatalf("policy statements = %d, want %d", len(doc.Statement), len(want.Statement))
	}
}

func TestProvisionBackupUser_BlankProfileDefaults(t *testing.T) {
	f := &fakeIAM{}
	var gotProfile string
	write := func(_, profile, _, _ string) error { gotProfile = profile; return nil }
	report, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{}, "/tmp/creds", write)
	if err != nil {
		t.Fatal(err)
	}
	if gotProfile != DefaultBackupUserProfile || report.Profile != DefaultBackupUserProfile {
		t.Fatalf("profile = %q / %q, want %q", gotProfile, report.Profile, DefaultBackupUserProfile)
	}
}

func TestProvisionBackupUser_ReusesExistingUser(t *testing.T) {
	f := &fakeIAM{createUserErr: &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}}
	report, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds", func(_, _, _, _ string) error { return nil })
	if err != nil {
		t.Fatalf("existing user must be reused, got %v", err)
	}
	if report.UserCreated || !report.UserExisted || !report.PolicyAttached || f.createKeyCalls != 1 {
		t.Fatalf("report = %+v, createKeyCalls = %d", report, f.createKeyCalls)
	}
}

func TestProvisionBackupUser_AccessDeniedClassifiedPerStep(t *testing.T) {
	tests := []struct {
		name string
		f    *fakeIAM
		step string
	}{
		{"create user", &fakeIAM{createUserErr: accessDenied()}, "iam:CreateUser"},
		{"put policy", &fakeIAM{putPolicyErr: accessDenied()}, "iam:PutUserPolicy"},
		{"create key", &fakeIAM{createKeyErr: accessDenied()}, "iam:CreateAccessKey"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := provisionBackupUser(context.Background(), tc.f, backupUserCfg(),
				BackupUserOptions{Profile: "sentra"}, "/tmp/creds", func(_, _, _, _ string) error { return nil })
			var perr *BackupUserError
			if !errors.As(err, &perr) {
				t.Fatalf("err = %v, want *BackupUserError", err)
			}
			if !perr.AccessDenied || perr.Step != tc.step {
				t.Fatalf("classification = %+v, want AccessDenied at %s", perr, tc.step)
			}
		})
	}
}

func TestProvisionBackupUser_KeyLimit(t *testing.T) {
	f := &fakeIAM{createKeyErr: &iamtypes.LimitExceededException{Message: aws.String("2 keys")}}
	_, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds", func(_, _, _, _ string) error { return nil })
	var perr *BackupUserError
	if !errors.As(err, &perr) || !perr.KeyLimit || perr.Step != "iam:CreateAccessKey" {
		t.Fatalf("err = %v, want KeyLimit at iam:CreateAccessKey", err)
	}
}

// The one ordering hazard: a key minted in AWS with nowhere to live on disk.
func TestProvisionBackupUser_WriteFailureDeletesKey(t *testing.T) {
	f := &fakeIAM{}
	_, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds",
		func(_, _, _, _ string) error { return errors.New("disk full") })
	var perr *BackupUserError
	if !errors.As(err, &perr) || perr.Step != "credentials" {
		t.Fatalf("err = %v, want credentials-step error", err)
	}
	if f.deletedKeyID != "AKIAFAKEFAKEFAKEFAKE" {
		t.Fatalf("minted key must be deleted on write failure, deleted %q", f.deletedKeyID)
	}
	if perr.KeyOrphaned != "" {
		t.Fatalf("successful cleanup must not flag an orphan, got %q", perr.KeyOrphaned)
	}
}

func TestProvisionBackupUser_WriteFailureCleanupFailureFlagsOrphan(t *testing.T) {
	f := &fakeIAM{deleteKeyErr: errors.New("nope")}
	_, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds",
		func(_, _, _, _ string) error { return errors.New("disk full") })
	var perr *BackupUserError
	if !errors.As(err, &perr) || perr.KeyOrphaned != "AKIAFAKEFAKEFAKEFAKE" {
		t.Fatalf("err = %v, want KeyOrphaned set to the key ID", err)
	}
}

// Pre-check: a profile that is already taken must fail BEFORE any IAM call,
// so a doomed run makes no mutation it would only have to undo.
func TestProvisionBackupUser_TakenProfileFailsBeforeIAM(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/credentials"
	if err := writeFile(path, "[sentra]\naws_access_key_id = x\n"); err != nil {
		t.Fatal(err)
	}
	f := &fakeIAM{}
	_, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, path, WriteAWSCredentialsProfile)
	if !errors.Is(err, ErrCredentialsProfileExists) {
		t.Fatalf("err = %v, want ErrCredentialsProfileExists", err)
	}
	if f.createUserCalls != 0 || f.createKeyCalls != 0 {
		t.Fatalf("IAM must not be called when the profile is taken: users=%d keys=%d", f.createUserCalls, f.createKeyCalls)
	}
}

// The secret must never leak through the report or an error string — the
// dangerous condition, asserted on every failure path that follows minting.
func TestProvisionBackupUser_SecretNeverInReportOrError(t *testing.T) {
	f := &fakeIAM{deleteKeyErr: errors.New("nope")}
	report, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, "/tmp/creds",
		func(_, _, _, _ string) error { return errors.New("disk full") })
	if err == nil {
		t.Fatal("expected a write failure")
	}
	if strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("secret leaked into error: %v", err)
	}
	for _, s := range []string{report.UserName, report.AccessKeyID, report.Profile, report.CredentialsPath, report.Warning} {
		if strings.Contains(s, fakeSecret) {
			t.Fatalf("secret leaked into report: %+v", report)
		}
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
```

Add `"os"` to that file's import block (used by `writeFile`).

- [ ] **Step 3: Run the tests to verify they fail**

Run: `go test ./internal/setup/ -run 'TestProvisionBackupUser' -count=1`
Expected: FAIL to compile — `undefined: provisionBackupUser`, `undefined: BackupUserError`.

- [ ] **Step 4: Write the implementation**

Create `internal/setup/backupuser.go`:

```go
package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

// iamAPI is the slice of the IAM client the provisioner uses. *iam.Client
// satisfies it; tests pass a fake. Keeping the surface to four calls is
// what makes every failure path unit-testable without an AWS account.
type iamAPI interface {
	CreateUser(ctx context.Context, in *iam.CreateUserInput, opts ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	PutUserPolicy(ctx context.Context, in *iam.PutUserPolicyInput, opts ...func(*iam.Options)) (*iam.PutUserPolicyOutput, error)
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, opts ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	DeleteAccessKey(ctx context.Context, in *iam.DeleteAccessKeyInput, opts ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
}

// credentialsWriter is WriteAWSCredentialsProfile's shape, injected so the
// provisioner's write-failure path can be exercised without touching disk.
type credentialsWriter func(path, profile, accessKeyID, secret string) error

// BackupUserError classifies a provisioning failure so the engine can write
// a warning that names the fix. Step is the IAM action or "credentials".
// KeyOrphaned is the access key ID left behind when a post-mint failure's
// cleanup also failed — the one outcome an operator must act on by hand.
type BackupUserError struct {
	Step         string
	AccessDenied bool
	KeyLimit     bool
	KeyOrphaned  string
	Err          error
}

func (e *BackupUserError) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *BackupUserError) Unwrap() error { return e.Err }

// DefaultProvisionBackupUser is the production Effects driver: it authenticates
// with the credential chain cfg currently names (the session that just signed
// in), then creates the scoped user and stores its key. Region/profile follow
// the same overlay as diag's identity check so "which credentials" can never
// differ between the check and the mutation.
func DefaultProvisionBackupUser(ctx context.Context, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error) {
	if cfg == nil {
		return BackupUserReport{}, errors.New("provision backup user: nil config")
	}
	path, err := AWSCredentialsPath()
	if err != nil {
		return BackupUserReport{}, err
	}
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Repo.S3.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Repo.S3.Region))
	}
	if cfg.Repo.S3.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.Repo.S3.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return BackupUserReport{}, fmt.Errorf("load AWS config: %w", err)
	}
	return provisionBackupUser(ctx, iam.NewFromConfig(awsCfg), cfg, opts, path, WriteAWSCredentialsProfile)
}

// provisionBackupUser is the ordered body: pre-check the profile, create
// (or reuse) the user, put the canonical policy, mint a key, write it. The
// secret exists only between the mint and the write and is never returned.
// A write failure deletes the just-minted key so no live secret is left
// homeless; if that cleanup fails too, the key ID is reported as orphaned.
func provisionBackupUser(ctx context.Context, client iamAPI, cfg *config.Config, opts BackupUserOptions, credsPath string, write credentialsWriter) (BackupUserReport, error) {
	profile := strings.TrimSpace(opts.Profile)
	if profile == "" {
		profile = DefaultBackupUserProfile
	}
	report := BackupUserReport{UserName: BackupUserName, Profile: profile, CredentialsPath: credsPath}

	// Refuse a taken or forbidden profile BEFORE any IAM mutation.
	if err := CheckAWSCredentialsProfileFree(credsPath, profile); err != nil {
		return report, &BackupUserError{Step: "credentials", Err: err}
	}

	_, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(BackupUserName)})
	switch {
	case err == nil:
		report.UserCreated = true
	case isIAMEntityExists(err):
		report.UserExisted = true
	default:
		return report, classifyIAMError("iam:CreateUser", err)
	}

	doc, err := json.Marshal(BuildIAMPolicy(cfg.Repo.S3.Bucket, cfg.Repo.S3.Prefix))
	if err != nil {
		return report, &BackupUserError{Step: "iam:PutUserPolicy", Err: fmt.Errorf("encode policy: %w", err)}
	}
	if _, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(BackupUserName),
		PolicyName:     aws.String(BackupUserPolicyName),
		PolicyDocument: aws.String(string(doc)),
	}); err != nil {
		return report, classifyIAMError("iam:PutUserPolicy", err)
	}
	report.PolicyAttached = true

	keyOut, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(BackupUserName)})
	if err != nil {
		return report, classifyIAMError("iam:CreateAccessKey", err)
	}
	if keyOut == nil || keyOut.AccessKey == nil || keyOut.AccessKey.AccessKeyId == nil || keyOut.AccessKey.SecretAccessKey == nil {
		return report, &BackupUserError{Step: "iam:CreateAccessKey", Err: errors.New("iam returned an empty access key")}
	}
	keyID := aws.ToString(keyOut.AccessKey.AccessKeyId)
	report.AccessKeyID = keyID

	// The secret is passed straight through to the writer and referenced
	// nowhere else: not the report, not an error, not a log line.
	if err := write(credsPath, profile, keyID, aws.ToString(keyOut.AccessKey.SecretAccessKey)); err != nil {
		perr := &BackupUserError{Step: "credentials", Err: err}
		if _, delErr := client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName:    aws.String(BackupUserName),
			AccessKeyId: aws.String(keyID),
		}); delErr != nil {
			perr.KeyOrphaned = keyID
		}
		return report, perr
	}
	return report, nil
}

func isIAMEntityExists(err error) bool {
	var exists *iamtypes.EntityAlreadyExistsException
	return errors.As(err, &exists)
}

// classifyIAMError maps the two actionable IAM failures. AccessDenied arrives
// as a generic API error (code only); the key limit has a typed exception.
func classifyIAMError(step string, err error) *BackupUserError {
	e := &BackupUserError{Step: step, Err: err}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			e.AccessDenied = true
		case "LimitExceeded", "LimitExceededException":
			e.KeyLimit = true
		}
	}
	var limit *iamtypes.LimitExceededException
	if errors.As(err, &limit) {
		e.KeyLimit = true
	}
	return e
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -run 'TestProvisionBackupUser' -count=1 -v`
Expected: PASS for all nine. Then `go vet ./internal/setup/` — Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add go.mod go.sum internal/setup/backupuser.go internal/setup/backupuser_test.go
git commit -m "feat(setup): IAM backup-user provisioner behind a fake-able iamAPI

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Effects seam — interface method and all three implementations

**Files:**
- Modify: `internal/setup/effects.go:15-33` (interface) and `:35-90` (defaultEffects)
- Modify: `internal/setup/engine_prepare_test.go:16-27` (fakeEffects)
- Modify: `internal/tui/setup_wizard_test.go` (stubEffects, near line 650)

**Interfaces:**
- Produces: `Effects.ProvisionBackupUser(ctx, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error)`; `fakeEffects.provisionBackupUser func(ctx, cfg, opts) (BackupUserReport, error)`; `stubEffects.backupUser setup.BackupUserReport`, `stubEffects.backupUserErr error`.

- [ ] **Step 1: Write the failing test**

Append to `internal/setup/effects_test.go`:

```go
// The seam must expose provisioning, and the production seam must route it
// to DefaultProvisionBackupUser (a nil config is the cheapest observable
// path through that driver).
func TestDefaultEffectsProvisionBackupUserDelegates(t *testing.T) {
	var eff Effects = DefaultEffects()
	_, err := eff.ProvisionBackupUser(context.Background(), nil, BackupUserOptions{})
	if err == nil || !strings.Contains(err.Error(), "nil config") {
		t.Fatalf("expected the default driver's nil-config error, got %v", err)
	}
}
```

Add `"strings"` to the import block if absent.

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/setup/ -run TestDefaultEffectsProvisionBackupUserDelegates -count=1`
Expected: FAIL to compile — `eff.ProvisionBackupUser undefined`.

- [ ] **Step 3: Add the method to the interface and every implementation**

In `internal/setup/effects.go`, add to the `Effects` interface after `PrepareAWS`:

```go
	// ProvisionBackupUser creates the scoped IAM user, attaches the canonical
	// policy, mints a key, and writes it to ~/.aws/credentials — all inside
	// this one call, so the secret never crosses the seam. Returns a
	// non-secret report; any error is a *BackupUserError when classifiable.
	ProvisionBackupUser(ctx context.Context, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error)
```

And the delegation after `PrepareAWS`'s method:

```go
func (defaultEffects) ProvisionBackupUser(ctx context.Context, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error) {
	return DefaultProvisionBackupUser(ctx, cfg, opts)
}
```

In `internal/setup/engine_prepare_test.go`, add to `fakeEffects`:

```go
	provisionBackupUser func(ctx context.Context, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error)
```

and the method (next to `PrepareAWS`):

```go
func (f fakeEffects) ProvisionBackupUser(ctx context.Context, cfg *config.Config, o BackupUserOptions) (BackupUserReport, error) {
	if f.provisionBackupUser != nil {
		return f.provisionBackupUser(ctx, cfg, o)
	}
	return BackupUserReport{}, nil
}
```

In `internal/tui/setup_wizard_test.go`, add two fields to `stubEffects`:

```go
	backupUser    setup.BackupUserReport
	backupUserErr error
```

and the method next to `PrepareAWS`:

```go
func (s stubEffects) ProvisionBackupUser(ctx context.Context, cfg *config.Config, opts setup.BackupUserOptions) (setup.BackupUserReport, error) {
	return s.backupUser, s.backupUserErr
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -run 'TestDefaultEffects' -count=1 && go build ./... && go vet ./internal/setup/ ./internal/tui/`
Expected: PASS; both packages compile (the interface grew and every implementation followed).

- [ ] **Step 5: Commit**

```bash
git add internal/setup/effects.go internal/setup/effects_test.go internal/setup/engine_prepare_test.go internal/tui/setup_wizard_test.go
git commit -m "feat(setup): Effects.ProvisionBackupUser on the seam and every fake

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Engine — provision, verify with bounded retry, switch, warn

**Files:**
- Modify: `internal/setup/engine.go`
- Modify: `internal/setup/engine_prepare.go:16-32`
- Create: `internal/setup/engine_backupuser.go`
- Test: `internal/setup/engine_backupuser_test.go`

**Interfaces:**
- Consumes: `ShouldProvisionBackupUser`, `Effects.ProvisionBackupUser`, `Effects.CheckAWSSDKIdentity`, `BackupUserError`, `ErrCredentialsProfileExists`.
- Produces: `Engine.sleep func(ctx, d time.Duration) error` (default `sleepCtx`); `func (e *Engine) provisionBackupUser(ctx, p *Plan) *BackupUserReport`; `func (e *Engine) verifyIdentityWithRetry(ctx, cfg *config.Config) error`; `func backupUserWarning(err error) string`; consts `backupUserVerifyTimeout = 30 * time.Second`, `backupUserVerifyMaxBackoff = 8 * time.Second`.

- [ ] **Step 1: Write the failing tests**

Create `internal/setup/engine_backupuser_test.go`:

```go
package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// newTestEngine builds an Engine whose sleeps are instant, so the retry
// loop's virtual clock runs without wall time.
func newTestEngine(eff Effects) *Engine {
	return &Engine{eff: eff, sleep: func(context.Context, time.Duration) error { return nil }}
}

func loginPlanWithBackupUser() Plan {
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	p.ProvisionBackupUser = true
	p.BackupUserProfile = "sentra"
	return p
}

func TestEngineBackupUser_GatedOffForExistingCredentials(t *testing.T) {
	called := false
	eff := fakeEffects{
		provisionBackupUser: func(context.Context, *config.Config, BackupUserOptions) (BackupUserReport, error) {
			called = true
			return BackupUserReport{}, nil
		},
	}
	p := loginPlanWithBackupUser()
	p.AWSAuthMethod = AWSAuthExisting
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if called || prep.BackupUser != nil {
		t.Fatalf("existing-credentials setup must never provision (called=%v, report=%+v)", called, prep.BackupUser)
	}
}

func TestEngineBackupUser_SuccessSwitchesProfileAfterVerify(t *testing.T) {
	var verifiedProfiles []string
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			verifiedProfiles = append(verifiedProfiles, cfg.Repo.S3.Profile)
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{UserName: BackupUserName, Profile: o.Profile, AccessKeyID: "AKIAX", CredentialsPath: "/c"}, nil
		},
	}
	p := loginPlanWithBackupUser()
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if prep.BackupUser == nil || !prep.BackupUser.ProfileSwitched || prep.BackupUser.Warning != "" {
		t.Fatalf("report = %+v, want switched with no warning", prep.BackupUser)
	}
	if p.Config.Repo.S3.Profile != "sentra" {
		t.Fatalf("plan profile = %q, want sentra", p.Config.Repo.S3.Profile)
	}
	// The verification after provisioning ran through the NEW profile.
	if last := verifiedProfiles[len(verifiedProfiles)-1]; last != "sentra" {
		t.Fatalf("last identity check used profile %q, want sentra (all: %v)", last, verifiedProfiles)
	}
}

func TestEngineBackupUser_BlankProfileDefaults(t *testing.T) {
	var gotProfile string
	eff := fakeEffects{
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			gotProfile = o.Profile
			return BackupUserReport{Profile: o.Profile}, nil
		},
	}
	p := loginPlanWithBackupUser()
	p.BackupUserProfile = "  "
	if _, _, err := newTestEngine(eff).PrepareAWS(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	if gotProfile != DefaultBackupUserProfile {
		t.Fatalf("profile passed = %q, want %q", gotProfile, DefaultBackupUserProfile)
	}
}

// Fresh IAM keys take seconds to propagate: two failures then success must
// still end in a switched profile.
func TestEngineBackupUser_VerifyRetriesThenSucceeds(t *testing.T) {
	calls := 0
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			if cfg.Repo.S3.Profile != "sentra" {
				return nil // the pre-provision session check
			}
			calls++
			if calls < 3 {
				return errors.New("InvalidClientTokenId")
			}
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{Profile: o.Profile}, nil
		},
	}
	p := loginPlanWithBackupUser()
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !prep.BackupUser.ProfileSwitched {
		t.Fatalf("calls = %d, switched = %v; want 3 and true", calls, prep.BackupUser.ProfileSwitched)
	}
}

// The dangerous condition: verification never succeeds. The profile must
// NOT switch (InitRepo would run on credentials that do not work), setup
// must still succeed, and the warning must say how to switch later.
func TestEngineBackupUser_VerifyTimeoutKeepsSessionProfile(t *testing.T) {
	var slept []time.Duration
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			if cfg.Repo.S3.Profile == "sentra" {
				return errors.New("InvalidClientTokenId")
			}
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{UserName: BackupUserName, Profile: o.Profile, CredentialsPath: "/c"}, nil
		},
	}
	eng := &Engine{eff: eff, sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}
	p := loginPlanWithBackupUser()
	p.Config.Repo.S3.Profile = "login-session"
	_, prep, err := eng.PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("timeout must not fail setup: %v", err)
	}
	if p.Config.Repo.S3.Profile != "login-session" {
		t.Fatalf("profile switched despite unverified key: %q", p.Config.Repo.S3.Profile)
	}
	if prep.BackupUser.ProfileSwitched || prep.BackupUser.Warning == "" {
		t.Fatalf("report = %+v, want unswitched with warning", prep.BackupUser)
	}
	if !strings.Contains(prep.BackupUser.Warning, "repo.s3.profile") {
		t.Fatalf("warning must tell the operator how to switch later: %q", prep.BackupUser.Warning)
	}
	// Backoff 1,2,4,8,8 = 23s; a sixth 8s sleep would exceed 30s, so five sleeps.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("sleeps = %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", slept, want)
		}
	}
}

func TestEngineBackupUser_ProvisionFailureWarnsAndContinues(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string // substring of the warning
	}{
		{"access denied", &BackupUserError{Step: "iam:CreateUser", AccessDenied: true, Err: errors.New("x")}, "iam:CreateUser"},
		{"key limit", &BackupUserError{Step: "iam:CreateAccessKey", KeyLimit: true, Err: errors.New("x")}, "two access keys"},
		{"profile taken", &BackupUserError{Step: "credentials", Err: ErrCredentialsProfileExists}, "another profile name"},
		{"orphaned key", &BackupUserError{Step: "credentials", KeyOrphaned: "AKIAORPHAN", Err: errors.New("disk full")}, "AKIAORPHAN"},
		{"write failed, cleaned up", &BackupUserError{Step: "credentials", Err: errors.New("disk full")}, "deleted again"},
		{"unclassified", errors.New("boom"), "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eff := fakeEffects{
				provisionBackupUser: func(context.Context, *config.Config, BackupUserOptions) (BackupUserReport, error) {
					return BackupUserReport{UserName: BackupUserName}, tc.err
				},
			}
			p := loginPlanWithBackupUser()
			p.Config.Repo.S3.Profile = "login-session"
			_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
			if err != nil {
				t.Fatalf("provisioning failure must not fail setup: %v", err)
			}
			if prep.BackupUser == nil || prep.BackupUser.ProfileSwitched {
				t.Fatalf("report = %+v", prep.BackupUser)
			}
			if !strings.Contains(prep.BackupUser.Warning, tc.want) {
				t.Fatalf("warning %q lacks %q", prep.BackupUser.Warning, tc.want)
			}
			if !strings.Contains(prep.BackupUser.Warning, "expire") {
				t.Fatalf("every warning must say the session credentials expire: %q", prep.BackupUser.Warning)
			}
			if p.Config.Repo.S3.Profile != "login-session" {
				t.Fatalf("profile must be untouched on failure, got %q", p.Config.Repo.S3.Profile)
			}
		})
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/setup/ -run 'TestEngineBackupUser' -count=1`
Expected: FAIL to compile — `unknown field sleep in struct literal of type Engine`.

- [ ] **Step 3: Add the sleep seam to Engine**

Replace the body of `internal/setup/engine.go` with:

```go
package setup

import (
	"context"
	"time"
)

// Engine sequences the side-effecting steps of setup — AWS auth + bucket
// prep, backup-user provisioning, config write, repo init — over an injected
// Effects seam. It contains NO huh forms, NO stdout writes, and NO cobra: the
// TUI wizard is the only sequencer, driving it from tea messages. `sentra
// setup` is a thin CLI launcher for that same wizard, not a second driver of
// the engine.
type Engine struct {
	eff Effects
	// sleep is the retry loop's clock seam. Production sleeps for real;
	// tests substitute an instant sleep and assert the requested durations,
	// so a 30-second backoff schedule is verified in microseconds.
	sleep func(ctx context.Context, d time.Duration) error
}

// NewEngine returns an Engine backed by eff.
func NewEngine(eff Effects) *Engine { return &Engine{eff: eff, sleep: sleepCtx} }

// sleepCtx waits d or until ctx is done, whichever comes first, so a
// cancelled setup never sits in a retry sleep.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
```

- [ ] **Step 4: Write the provisioning stage**

Create `internal/setup/engine_backupuser.go`:

```go
package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

const (
	// backupUserVerifyTimeout bounds the wait for a freshly minted IAM key
	// to become usable. Propagation is usually a few seconds; 30s is the
	// point past which the operator is better served by a warning.
	backupUserVerifyTimeout = 30 * time.Second
	// backupUserVerifyMaxBackoff caps the doubling: 1s, 2s, 4s, 8s, 8s, …
	backupUserVerifyMaxBackoff = 8 * time.Second
)

// provisionBackupUser is the post-bucket-prep stage of PrepareAWS: create
// the scoped user through the Effects seam, then switch the plan's profile
// to it — but ONLY after the new identity verifies. Until then InitRepo
// would run on a key AWS has not finished propagating, so an unverified
// switch fails toward "setup works right now": the session profile stays.
//
// Never returns an error. Provisioning is hardening, and a working setup on
// session credentials beats no setup; every failure becomes Warning.
func (e *Engine) provisionBackupUser(ctx context.Context, p *Plan) *BackupUserReport {
	profile := strings.TrimSpace(p.BackupUserProfile)
	if profile == "" {
		profile = DefaultBackupUserProfile
	}
	report, err := e.eff.ProvisionBackupUser(ctx, &p.Config, BackupUserOptions{Profile: profile})
	if err != nil {
		report.Warning = backupUserWarning(err)
		return &report
	}

	previous := p.Config.Repo.S3.Profile
	p.Config.Repo.S3.Profile = report.Profile
	if err := e.verifyIdentityWithRetry(ctx, &p.Config); err != nil {
		p.Config.Repo.S3.Profile = previous
		report.Warning = fmt.Sprintf(
			"backup user %s was created and its key saved to profile %q in %s, but the new credentials did not verify within %s (%v). "+
				"Setup continues on the signed-in session, which expires. Once the key is active, set repo.s3.profile: %q in sentra.yaml.",
			report.UserName, report.Profile, report.CredentialsPath, backupUserVerifyTimeout, err, report.Profile)
		return &report
	}
	report.ProfileSwitched = true
	return &report
}

// verifyIdentityWithRetry runs CheckAWSSDKIdentity until it passes or the
// backoff schedule would exceed backupUserVerifyTimeout of VIRTUAL time —
// summed sleep requests, not wall clock — so the schedule is deterministic
// under an instant test sleep. Any error retries: the SDK reports a
// not-yet-propagated key several different ways.
func (e *Engine) verifyIdentityWithRetry(ctx context.Context, cfg *config.Config) error {
	var elapsed time.Duration
	backoff := time.Second
	for {
		err := e.eff.CheckAWSSDKIdentity(ctx, cfg)
		if err == nil {
			return nil
		}
		if elapsed+backoff > backupUserVerifyTimeout {
			return err
		}
		if serr := e.sleep(ctx, backoff); serr != nil {
			return serr
		}
		elapsed += backoff
		if backoff < backupUserVerifyMaxBackoff {
			backoff *= 2
		}
	}
}

// backupUserWarning renders a provisioning failure as operator guidance.
// Every message ends by naming the consequence — the session credentials
// expire — because that is the fact the step existed to prevent.
func backupUserWarning(err error) string {
	const tail = " Setup continues on the signed-in session, which expires; see docs/QUICKSTART.md to create the backup user later."
	var perr *BackupUserError
	if !errors.As(err, &perr) {
		return "backup user not created: " + err.Error() + "." + tail
	}
	switch {
	case perr.AccessDenied:
		return fmt.Sprintf("backup user not created: the signed-in identity is not allowed to perform %s. Ask an administrator for that permission, or create the user by hand with `sentra setup iam-policy`.", perr.Step) + tail
	case perr.KeyLimit:
		return fmt.Sprintf("backup user %s already has two access keys; remove one in IAM and rerun setup.", BackupUserName) + tail
	case perr.KeyOrphaned != "":
		return fmt.Sprintf("backup user key %s was created but could not be saved (%v), and deleting it failed — delete that key in IAM.", perr.KeyOrphaned, perr.Err) + tail
	case perr.Step == "credentials" && errors.Is(perr.Err, ErrCredentialsProfileExists):
		return fmt.Sprintf("backup user key not saved: %v — choose another profile name and rerun setup.", perr.Err) + tail
	case perr.Step == "credentials" && errors.Is(perr.Err, ErrBackupUserProfileDefault):
		return fmt.Sprintf("backup user key not saved: %v — choose another profile name and rerun setup.", perr.Err) + tail
	case perr.Step == "credentials":
		return fmt.Sprintf("backup user key could not be saved (%v); the key was deleted again.", perr.Err) + tail
	default:
		return fmt.Sprintf("backup user not created: %s failed: %v.", perr.Step, perr.Err) + tail
	}
}
```

Then in `internal/setup/engine_prepare.go`, change the end of `PrepareAWS` from

```go
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, WrapAWSPrepareError(p.Config, method, err)
	}
	return auth, prep, nil
```

to

```go
	if err != nil {
		return AWSAuthReport{}, AWSPrepareReport{}, WrapAWSPrepareError(p.Config, method, err)
	}
	// Bucket prep ran on the session identity that just signed in; the scoped
	// user only has to USE the bucket. Provisioning comes last so a failure
	// here can never undo a prepared bucket, and never fails setup.
	if ShouldProvisionBackupUser(p) {
		prep.BackupUser = e.provisionBackupUser(ctx, p)
	}
	return auth, prep, nil
```

Update the `PrepareAWS` doc comment's first sentence to "runs one pass of the AWS auth + bucket-prep + backup-user sequence".

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -count=1`
Expected: PASS — the new engine tests and every existing one (the existing `TestEnginePrepareAWS*` tests build `NewEngine(eff)` and never set the flag, so the gate keeps them unchanged). Then `go test -race ./internal/setup/ -count=1` — Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/setup/engine.go internal/setup/engine_prepare.go internal/setup/engine_backupuser.go internal/setup/engine_backupuser_test.go
git commit -m "feat(setup): PrepareAWS provisions the backup user and switches profile after verify

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: Review-screen line

**Files:**
- Modify: `internal/setup/review.go:29-34`
- Test: `internal/setup/review_test.go`

**Interfaces:**
- Produces: `func backupUserPlanLine(p Plan) string` (unexported); `ReviewText` emits `Backup user: create sentra-backup, keys → ~/.aws/credentials [<profile>]` / `Backup user: skipped` / nothing.

- [ ] **Step 1: Write the failing tests**

Append to `internal/setup/review_test.go`:

```go
func TestReviewTextBackupUserLine(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	base := Plan{Config: cfg, Backend: BackendAWS, PrepareAWS: true}

	tests := []struct {
		name    string
		method  AWSAuthMethod
		on      bool
		profile string
		want    string // substring that must appear
		absent  string // substring that must not appear ("" to skip)
	}{
		{"login on", AWSAuthLogin, true, "sentra", "Backup user: create sentra-backup, keys → ~/.aws/credentials [sentra]", ""},
		{"login on custom profile", AWSAuthLogin, true, "backups", "~/.aws/credentials [backups]", ""},
		{"login on blank profile defaults", AWSAuthLogin, true, "", "[sentra]", ""},
		{"sso off", AWSAuthSSO, false, "", "Backup user: skipped", ""},
		{"existing", AWSAuthExisting, true, "sentra", "AWS sign-in", "Backup user"},
		{"skip", AWSAuthSkip, true, "sentra", "AWS sign-in", "Backup user"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			p.AWSAuthMethod = tc.method
			p.ProvisionBackupUser = tc.on
			p.BackupUserProfile = tc.profile
			got := ReviewText("sentra.yaml", p)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("review text missing %q:\n%s", tc.want, got)
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Fatalf("review text must not mention %q:\n%s", tc.absent, got)
			}
		})
	}
}

// The dangerous condition: nothing secret-shaped may ever reach the review
// screen, whatever the plan holds. An access key ID prefix or a 40-character
// secret in this output would mean a field leaked.
func TestReviewTextBackupUserNeverRendersSecretShapes(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "b"
	p := Plan{Config: cfg, Backend: BackendAWS, PrepareAWS: true, AWSAuthMethod: AWSAuthLogin,
		ProvisionBackupUser: true, BackupUserProfile: "sentra"}
	got := ReviewText("sentra.yaml", p)
	if strings.Contains(got, "AKIA") {
		t.Fatalf("review text contains an access key ID shape:\n%s", got)
	}
	isBase64Rune := func(r rune) bool {
		return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '+' || r == '/'
	}
	for _, word := range strings.Fields(got) {
		if len(word) == 40 && strings.IndexFunc(word, func(r rune) bool { return !isBase64Rune(r) }) == -1 {
			t.Fatalf("review text contains a 40-char base64 token %q:\n%s", word, got)
		}
	}
	if !strings.Contains(got, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.") {
		t.Fatalf("no-secrets assertion line must remain:\n%s", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/setup/ -run 'TestReviewTextBackupUser' -count=1`
Expected: `TestReviewTextBackupUserLine` FAILS on "login on" with `review text missing "Backup user: create …"`; the secret-shape test passes already (it pins the invariant).

- [ ] **Step 3: Add the line**

In `internal/setup/review.go`, inside `if p.PrepareAWS {` after the `Enable default encryption` line, add:

```go
		if line := backupUserPlanLine(p); line != "" {
			fmt.Fprintf(&b, "%s\n", line)
		}
```

and append to the file:

```go
// backupUserPlanLine says what the provisioning stage will do, in the same
// voice as the other plan lines. It names the profile (a section header,
// not a secret) so the operator learns where the key will land before it
// exists. Methods that never provision get no line at all — "skipped"
// would imply a choice they were never offered.
func backupUserPlanLine(p Plan) string {
	m := ResolveAWSAuthMethod(&p)
	if m != AWSAuthLogin && m != AWSAuthSSO {
		return ""
	}
	if !p.ProvisionBackupUser {
		return "Backup user: skipped"
	}
	profile := strings.TrimSpace(p.BackupUserProfile)
	if profile == "" {
		profile = DefaultBackupUserProfile
	}
	return fmt.Sprintf("Backup user: create %s, keys → ~/.aws/credentials [%s]", BackupUserName, profile)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/setup/ -run 'TestReviewText' -count=1`
Expected: PASS, including the pre-existing `TestReviewTextAWSPrepareBlock` (its plan has `ProvisionBackupUser` false with login → the block now also contains `Backup user: skipped`, which that test does not forbid).

- [ ] **Step 5: Commit**

```bash
git add internal/setup/review.go internal/setup/review_test.go
git commit -m "feat(setup): review screen names the backup-user step and its profile

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: Wizard actions stage — toggle, profile input, key routing, plan wiring

**Files:**
- Modify: `internal/tui/setup_wizard.go` — struct (~line 140), row constants (~line 190), constructor (~line 200-290), `CapturesText` (~line 318), `handleActionsKey` (~line 942), `advanceFromActions` (~line 985), `View` stageActions (~line 1190)
- Test: `internal/tui/setup_wizard_test.go`

**Interfaces:**
- Consumes: `setup.BackupUserName`, `setup.DefaultBackupUserProfile`, `setup.ValidateBackupUserProfile`, `Plan.ProvisionBackupUser`, `Plan.BackupUserProfile`.
- Produces: fields `backupUser bool`, `backupProfile textinput.Model`; consts `actionRowBackupUser`, `actionRowProfile` (before `actionRowCount`); methods `backupUserOffered() bool`, `actionRowVisible(row int) bool`, `moveActionCursor(delta int) SetupWizardView`, `syncProfileFocus() SetupWizardView`, `seedBackupUserDefault() SetupWizardView`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/setup_wizard_test.go`:

```go
// The toggle's default is the product decision: browser login is the
// expiry-trap path, so it is pre-checked there; SSO is offered unchecked;
// existing-credentials and skip never see it.
func TestSetupWizard_BackupUserToggleDefaultsPerMethod(t *testing.T) {
	v := setupAtActions(t) // authCursor 0 = login
	if line := wizardLine(t, v.View(), "create dedicated backup user"); !strings.Contains(line, "[x]") {
		t.Fatalf("login must default the backup-user toggle ON, got %q", line)
	}
	if !strings.Contains(v.View(), "profile:") {
		t.Fatalf("an ON toggle must reveal the profile row:\n%s", v.View())
	}

	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRight}) // → sso
	v = m.(SetupWizardView)
	if line := wizardLine(t, v.View(), "create dedicated backup user"); !strings.Contains(line, "[ ]") {
		t.Fatalf("sso must default the toggle OFF, got %q", line)
	}
	if strings.Contains(v.View(), "profile:") {
		t.Fatalf("an OFF toggle must hide the profile row:\n%s", v.View())
	}

	for _, method := range []string{"existing", "skip"} {
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyRight})
		v = m.(SetupWizardView)
		if strings.Contains(v.View(), "backup user") {
			t.Fatalf("%s must not offer the backup-user step:\n%s", method, v.View())
		}
	}
}

func TestSetupWizard_BackupUserWiresPlanWithDefaultProfile(t *testing.T) {
	v := setupAtActions(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if !v.plan.ProvisionBackupUser {
		t.Fatal("login + default toggle must set plan.ProvisionBackupUser")
	}
	if v.plan.BackupUserProfile != setup.DefaultBackupUserProfile {
		t.Fatalf("plan.BackupUserProfile = %q, want %q", v.plan.BackupUserProfile, setup.DefaultBackupUserProfile)
	}
}

func TestSetupWizard_BackupUserToggleOffClearsPlan(t *testing.T) {
	v := setupAtActions(t)
	for v.actionCursor != actionRowBackupUser {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SetupWizardView)
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeySpace})
	v = m.(SetupWizardView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.plan.ProvisionBackupUser || v.plan.BackupUserProfile != "" {
		t.Fatalf("toggle off must clear the plan, got on=%v profile=%q", v.plan.ProvisionBackupUser, v.plan.BackupUserProfile)
	}
}

func TestSetupWizard_BackupUserProfileRowCapturesTextAndValidates(t *testing.T) {
	v := setupAtActions(t)
	for v.actionCursor != actionRowProfile {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SetupWizardView)
	}
	if !v.CapturesText() {
		t.Fatal("the focused profile row must capture text so digits and 'q' reach the input")
	}
	for _, r := range "default" {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(SetupWizardView)
	}
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(SetupWizardView)
	if v.stage != stageActions {
		t.Fatalf("profile \"default\" must be refused on the actions stage, got %v", v.stage)
	}
	if v.notice == "" || !strings.Contains(v.View(), "default") {
		t.Fatalf("refusal must be shown, notice=%q view:\n%s", v.notice, v.View())
	}
}

// Hidden rows must be unreachable: cycling down from the last visible row
// wraps to the auth row instead of landing on an invisible one.
func TestSetupWizard_ActionCursorSkipsHiddenRows(t *testing.T) {
	v := setupAtActions(t)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRight}) // sso: toggle visible, off → profile hidden
	v = m.(SetupWizardView)
	seen := map[int]bool{}
	for i := 0; i < actionRowCount+1; i++ {
		seen[v.actionCursor] = true
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyDown})
		v = m.(SetupWizardView)
	}
	if seen[actionRowProfile] {
		t.Fatal("cursor landed on the hidden profile row")
	}
	if !seen[actionRowBackupUser] {
		t.Fatal("cursor never reached the visible backup-user row")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSetupWizard_BackupUser|TestSetupWizard_ActionCursorSkipsHiddenRows' -count=1`
Expected: FAIL to compile — `undefined: actionRowBackupUser`, `actionRowProfile`.

- [ ] **Step 3: Add fields, constants, and helpers**

In `internal/tui/setup_wizard.go`:

(a) In the struct, after `initRepo     bool`, add:

```go
	// backupUser is the "create dedicated backup user" toggle; backupProfile
	// the ~/.aws/credentials section it writes to. Both live only on the
	// actions stage and are re-seeded when the auth method changes.
	backupUser    bool
	backupProfile textinput.Model
```

(b) Replace the action-row constant block with:

```go
// action-stage cursor rows: the auth select, the four toggles, then the
// backup-user toggle and its profile input. The last two are visible only
// for login/SSO (and the profile only while the toggle is on) — see
// actionRowVisible; the cursor never lands on a hidden row.
const (
	actionRowAuth = iota
	actionRowCreate
	actionRowBlock
	actionRowEncrypt
	actionRowInit
	actionRowBackupUser
	actionRowProfile
	actionRowCount
)
```

(c) In `NewSetupWizardView`, after the `confirmPass` setup, add:

```go
	backupProfile := textinput.New()
	backupProfile.Prompt = ""
	backupProfile.Width = 24
	backupProfile.Placeholder = setup.DefaultBackupUserProfile
```

and in the returned literal add `backupProfile: backupProfile,` and then, instead of returning the literal directly, seed the toggle:

```go
	v := SetupWizardView{
		// … existing fields …
		backupProfile:     backupProfile,
	}
	return v.seedBackupUserDefault()
```

(d) Add the helpers after `actionToggle`:

```go
// backupUserOffered: the provisioning step exists only where a powerful
// session was just obtained. Existing-credentials and skip already chose a
// durable identity, and an IAM mutation they did not ask for is the worst
// surprise a setup wizard can spring.
func (v SetupWizardView) backupUserOffered() bool {
	m := setupAuthOrder[v.authCursor]
	return m == setup.AWSAuthLogin || m == setup.AWSAuthSSO
}

func (v SetupWizardView) actionRowVisible(row int) bool {
	switch row {
	case actionRowBackupUser:
		return v.backupUserOffered()
	case actionRowProfile:
		return v.backupUserOffered() && v.backupUser
	default:
		return true
	}
}

// moveActionCursor steps the cursor by delta, skipping hidden rows, and
// keeps the profile input's focus in step with the cursor.
func (v SetupWizardView) moveActionCursor(delta int) SetupWizardView {
	for i := 0; i < actionRowCount; i++ {
		v.actionCursor = (v.actionCursor + delta + actionRowCount) % actionRowCount
		if v.actionRowVisible(v.actionCursor) {
			break
		}
	}
	return v.syncProfileFocus()
}

func (v SetupWizardView) syncProfileFocus() SetupWizardView {
	if v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile) {
		v.backupProfile.Focus()
	} else {
		v.backupProfile.Blur()
	}
	return v
}

// seedBackupUserDefault applies the per-method default — ON for browser
// login (the expiry-trap path), OFF for SSO — and parks the cursor on a
// visible row if the method change hid the one it was on.
func (v SetupWizardView) seedBackupUserDefault() SetupWizardView {
	v.backupUser = setupAuthOrder[v.authCursor] == setup.AWSAuthLogin
	if !v.actionRowVisible(v.actionCursor) {
		v.actionCursor = actionRowAuth
	}
	return v.syncProfileFocus()
}
```

- [ ] **Step 4: Route keys and wire the plan**

(a) Replace `CapturesText`:

```go
// CapturesText reports the stages that focus a text input: the details stage
// (the S3 bucket/prefix/region/… fields), the passphrase stage (the masked
// new/confirm fields), and the actions stage while the backup-user profile
// row is focused. On those the shell must route every rune here so a bucket
// name digit, a passphrase 'q', or a profile name isn't stolen by a global
// binding.
func (v SetupWizardView) CapturesText() bool {
	if v.stage == stageActions {
		return v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile)
	}
	return v.stage == stageDetails || v.stage == stagePassphrase
}
```

(b) Replace `handleActionsKey`:

```go
func (v SetupWizardView) handleActionsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	isSpace := msg.Type == tea.KeySpace ||
		(msg.Type == tea.KeyRunes && len(msg.Runes) == 1 && msg.Runes[0] == ' ')
	onProfile := v.actionCursor == actionRowProfile && v.actionRowVisible(actionRowProfile)
	switch {
	case msg.Type == tea.KeyTab || msg.Type == tea.KeyDown:
		return v.moveActionCursor(+1), nil
	case msg.Type == tea.KeyUp:
		return v.moveActionCursor(-1), nil
	case msg.Type == tea.KeyLeft && v.actionCursor == actionRowAuth:
		if v.authCursor > 0 {
			v.authCursor--
		}
		return v.seedBackupUserDefault(), nil
	case msg.Type == tea.KeyRight && v.actionCursor == actionRowAuth:
		if v.authCursor < len(setupAuthOrder)-1 {
			v.authCursor++
		}
		return v.seedBackupUserDefault(), nil
	case isSpace && !onProfile:
		switch v.actionCursor {
		case actionRowCreate:
			v.createBucket = !v.createBucket
		case actionRowBlock:
			v.blockPublic = !v.blockPublic
		case actionRowEncrypt:
			v.defaultEnc = !v.defaultEnc
		case actionRowInit:
			v.initRepo = !v.initRepo
		case actionRowBackupUser:
			v.backupUser = !v.backupUser
		}
		return v.syncProfileFocus(), nil
	case msg.Type == tea.KeyEnter:
		return v.advanceFromActions()
	}
	if onProfile {
		var cmd tea.Cmd
		v.backupProfile, cmd = v.backupProfile.Update(msg)
		v.notice = ""
		return v, cmd
	}
	return v, nil
}
```

(c) In `advanceFromActions`, after `v.plan.PrepareAWS = true` and before `if method == setup.AWSAuthSkip {`, add:

```go
	v.plan.ProvisionBackupUser = v.backupUserOffered() && v.backupUser
	v.plan.BackupUserProfile = ""
	if v.plan.ProvisionBackupUser {
		profile := strings.TrimSpace(v.backupProfile.Value())
		if profile == "" {
			profile = setup.DefaultBackupUserProfile
		}
		if err := setup.ValidateBackupUserProfile(profile); err != nil {
			// Stay here with the input focused: the profile is the only thing
			// the operator can fix, so put the cursor on it.
			v.notice = err.Error()
			v.actionCursor = actionRowProfile
			return v.syncProfileFocus(), nil
		}
		v.plan.BackupUserProfile = profile
	}
```

(d) In `View`'s `case stageActions:`, after the `actionRowInit` toggle line and before `actionLine`, add:

```go
		if v.actionRowVisible(actionRowBackupUser) {
			b.WriteString(v.actionToggle(actionRowBackupUser,
				"create dedicated backup user ("+setup.BackupUserName+")", v.backupUser))
		}
		if v.actionRowVisible(actionRowProfile) {
			// Label styled as a row, input appended after it — never wrap the
			// already-styled input inside the row style.
			fmt.Fprintf(&b, "%s%s\n", ui.SelectRow(v.actionCursor == actionRowProfile, "    profile: "), v.backupProfile.View())
		}
```

and change that stage's `actionLine` help to `"↑/↓ row · ←/→ method · space toggle · type profile"`.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -run 'TestSetupWizard' -count=1`
Expected: PASS for the five new tests and every existing wizard test. Watch specifically `TestSetupWizard_*` around line 1108 (the one that cycles to `actionRowInit`): it still reaches the init row because init precedes the new rows.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup wizard offers the dedicated backup user with a profile input

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 8: Wizard done stage — result line or warning block

**Files:**
- Modify: `internal/tui/setup_wizard.go` — `View` `case stageDone:` (~line 1246) and a helper
- Test: `internal/tui/setup_wizard_test.go`

**Interfaces:**
- Consumes: `setupDoneMsg.prep *setup.AWSPrepareReport` (already carried), `setupDoneMsg.auth *setup.AWSAuthReport`, `setupAuthMethodLabel`.
- Produces: `func (v SetupWizardView) backupUserReport() *setup.BackupUserReport`.

- [ ] **Step 1: Write the failing tests**

Append to `internal/tui/setup_wizard_test.go`:

```go
func TestSetupWizard_DoneShowsBackupUserSuccess(t *testing.T) {
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(setupDoneMsg{
		steps: setupProgress{bucketCreated: true, repoInited: true},
		prep: &setup.AWSPrepareReport{BackupUser: &setup.BackupUserReport{
			UserName: setup.BackupUserName, Profile: "sentra", ProfileSwitched: true,
		}},
	})
	v = m.(SetupWizardView)
	out := v.View()
	if !strings.Contains(out, "backup user sentra-backup (profile sentra)") {
		t.Fatalf("done screen must name the backup user and profile:\n%s", out)
	}
	if strings.Contains(out, "expire") {
		t.Fatalf("a switched profile must not warn about expiry:\n%s", out)
	}
}

func TestSetupWizard_DoneShowsBackupUserWarning(t *testing.T) {
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(setupDoneMsg{
		steps: setupProgress{bucketCreated: true, repoInited: true},
		auth:  &setup.AWSAuthReport{Method: setup.AWSAuthLogin},
		prep: &setup.AWSPrepareReport{BackupUser: &setup.BackupUserReport{
			UserName: setup.BackupUserName, Warning: "backup user not created: the signed-in identity is not allowed to perform iam:CreateUser.",
		}},
	})
	v = m.(SetupWizardView)
	out := v.View()
	for _, want := range []string{"Backup user not set up", "iam:CreateUser", "browser login", "expire"} {
		if !strings.Contains(out, want) {
			t.Fatalf("done screen missing %q:\n%s", want, out)
		}
	}
}

func TestSetupWizard_DoneWithoutProvisioningIsSilent(t *testing.T) {
	v := NewSetupWizardView(Deps{})
	m, _ := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	v = m.(SetupWizardView)
	m, _ = v.Update(setupDoneMsg{steps: setupProgress{repoInited: true}, prep: &setup.AWSPrepareReport{}})
	v = m.(SetupWizardView)
	if strings.Contains(strings.ToLower(v.View()), "backup user") {
		t.Fatalf("no provisioning attempt must render no backup-user line:\n%s", v.View())
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui/ -run 'TestSetupWizard_Done(ShowsBackupUser|WithoutProvisioning)' -count=1`
Expected: the two "Shows" tests FAIL with `done screen must name…` / `done screen missing "Backup user not set up"`; the silent test passes already.

- [ ] **Step 3: Render the result**

In `internal/tui/setup_wizard.go`, in `View`'s `case stageDone:`, after the `repository initialized` checklist line and before the `restart setup` action line, add:

```go
		if bu := v.backupUserReport(); bu != nil {
			if bu.ProfileSwitched {
				b.WriteString(v.checklistLine(true,
					fmt.Sprintf("backup user %s (profile %s)", bu.UserName, bu.Profile)))
			} else {
				method := setup.AWSAuthLogin
				if v.result.auth != nil {
					method = v.result.auth.Method
				}
				fmt.Fprintf(&b, "\n%s\n%s\n%s\n",
					ui.Warn.Render("Backup user not set up"),
					bu.Warning,
					ui.Subtle.Render("Session credentials from "+setupAuthMethodLabel(method)+" expire; see docs/QUICKSTART.md"))
			}
		}
```

And add the helper after `actionToggle`:

```go
// backupUserReport is the provisioning outcome carried by the done message,
// nil when the stage never ran (gate false) — which renders nothing, since a
// "skipped" line would imply a choice the operator may never have been offered.
func (v SetupWizardView) backupUserReport() *setup.BackupUserReport {
	if v.result.prep == nil {
		return nil
	}
	return v.result.prep.BackupUser
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/tui/ -run 'TestSetupWizard' -count=1 && go test -race ./internal/tui/ -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/setup_wizard.go internal/tui/setup_wizard_test.go
git commit -m "feat(tui): setup done screen reports the backup user or its warning

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 9: Documentation, full gate, push, reinstall

**Files:**
- Modify: `AGENTS.md:75-90` (the `sentra setup` bullet), `README.md:154-161`, `docs/QUICKSTART.md:80-95`, `CLAUDE.md:93-95`

- [ ] **Step 1: AGENTS.md**

In the `sentra setup` bullet, after the sentence ending "The review screen names the SOURCE, never the secret.", insert:

```
  After a browser-login or SSO sign-in it may create IAM user
  `sentra-backup` with the canonical `BuildIAMPolicy` inline policy, mint an
  access key into a dedicated `~/.aws/credentials` profile (default
  `sentra`), verify it, and switch `sentra.yaml` to that profile — the
  session identity is used once and retired. The step is pre-checked for
  browser login, offered unchecked for SSO, and absent for
  existing-credentials/skip/S3-compatible (`setup.ShouldProvisionBackupUser`
  is the single gate). It must never write the `default` credentials
  profile, never modify `~/.aws/config`, never overwrite a credentials
  section that already holds keys, and never let the secret reach the
  report, plan, draft, review text, logs, or an error. The profile switch
  happens only after the new identity verifies (bounded retry); any failure
  degrades to `BackupUserReport.Warning` and setup continues on the session
  credentials — provisioning never blocks setup.
```

- [ ] **Step 2: README.md and QUICKSTART.md**

In `README.md`, replace the `**AWS S3** →` bullet's first sentence with:

```
- **AWS S3** → sign in with AWS CLI browser login (the default), IAM Identity
  Center / SSO, an existing profile/role, or write config only. After a
  browser or SSO sign-in the wizard offers to **create a dedicated backup
  user** — a scoped IAM user whose static keys never expire — and switches
  your config to it, so scheduled backups survive the night. Browser login
  alone is for trying Sentra: its session expires within hours. Need an admin
  to grant permissions first? The wizard can print the least-privilege IAM
  policy and stop:
```

In `docs/QUICKSTART.md`, after the sentence "You can also choose IAM Identity Center / SSO, use an existing profile/environment/role, or write config only." add a paragraph:

```
Browser-login and SSO sessions are temporary — hours, not days — so on those
paths the wizard offers **create dedicated backup user** (pre-checked for
browser login). It creates IAM user `sentra-backup` with the least-privilege
policy, stores its access key under the `sentra` profile in
`~/.aws/credentials`, and points `sentra.yaml` at that profile once the key
verifies. Leave it on if you plan to schedule backups. If it cannot run (the
signed-in identity lacks IAM permissions, say), setup still completes on the
session credentials and tells you so; you can create the user later with the
policy from `sentra setup iam-policy`.
```

- [ ] **Step 3: CLAUDE.md**

In the package map's `internal/setup` entry, append one sentence to the description: `Also provisions the dedicated backup user (\`backupuser.go\`, \`credentialsfile.go\`): the secret never crosses the Effects seam, \`~/.aws/config\` and the \`default\` credentials profile are never written.`

- [ ] **Step 4: Full gate**

Run:
```bash
export GOFLAGS=-timeout=40m; just check 2>&1 | command tail -n 20
```
Expected: exit 0 — build, `go test -race ./...`, vet, lint, vuln, `go mod tidy -diff`, gofmt, `git diff --check` all clean. If golangci-lint's `gosec` flags the `os.ReadFile(path)` calls in `credentialsfile.go` despite the `//nolint:gosec` comments, the comment must sit on the same line as the call — fix placement rather than widening the exclusion.

- [ ] **Step 5: Commit, isolate-build, push, reinstall**

```bash
git add AGENTS.md README.md docs/QUICKSTART.md CLAUDE.md
git commit -m "docs: dedicated backup user in the setup contract, README, and quickstart

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
git worktree add -q --detach /tmp/chk HEAD && (cd /tmp/chk && go build ./... && go vet ./...) && git worktree remove --force /tmp/chk && echo ISOLATED-BUILD-OK
git pull --rebase -q && go build ./... && git push -q && just install 2>&1 | command tail -n 2
```
Expected: `ISOLATED-BUILD-OK`, push accepted (rebase first — other sessions land on main), and `Installed sentra -> ~/go/bin/sentra`.

---

### Task 10 (optional, operator-gated): live check against the real account

Never runs in CI: build-tagged `live` and env-gated. Run only with the operator's go-ahead — it creates one access key on the existing `sentra-backup` user (exercising the reuse path) under a throwaway profile, then deletes both.

**Files:**
- Create: `internal/setup/backupuser_live_test.go`

- [ ] **Step 1: Write the gated test**

```go
//go:build live

package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// TestLiveProvisionBackupUser provisions against the real account named by
// SENTRA_LIVE_ADMIN_PROFILE (a session with IAM rights) into a throwaway
// credentials file, verifies the key with STS through that file, then deletes
// the key. Requires: SENTRA_LIVE_ADMIN_PROFILE, SENTRA_LIVE_BUCKET.
func TestLiveProvisionBackupUser(t *testing.T) {
	admin := os.Getenv("SENTRA_LIVE_ADMIN_PROFILE")
	bucket := os.Getenv("SENTRA_LIVE_BUCKET")
	if admin == "" || bucket == "" {
		t.Skip("set SENTRA_LIVE_ADMIN_PROFILE and SENTRA_LIVE_BUCKET to run")
	}
	credsPath := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credsPath)

	cfg := backupUserCfg()
	cfg.Repo.S3.Bucket = bucket
	cfg.Repo.S3.Profile = admin
	ctx := context.Background()

	report, err := DefaultProvisionBackupUser(ctx, cfg, BackupUserOptions{Profile: "sentra-live-test"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(admin), awsconfig.WithRegion(cfg.Repo.S3.Region))
		if err != nil {
			t.Logf("cleanup: load admin config: %v", err)
			return
		}
		if _, err := iam.NewFromConfig(awsCfg).DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName: aws.String(BackupUserName), AccessKeyId: aws.String(report.AccessKeyID),
		}); err != nil {
			t.Errorf("cleanup: DELETE KEY %s BY HAND: %v", report.AccessKeyID, err)
		}
	})
	if !report.UserExisted {
		t.Fatalf("expected the existing sentra-backup user to be reused: %+v", report)
	}

	// Verify through the file the provisioner wrote, exactly as the engine does.
	verify := *cfg
	verify.Repo.S3.Profile = report.Profile
	eng := NewEngine(DefaultEffects())
	if err := eng.verifyIdentityWithRetry(ctx, &verify); err != nil {
		t.Fatalf("new key never verified: %v", err)
	}
}
```

- [ ] **Step 2: Run it (operator go-ahead required)**

```bash
SENTRA_LIVE_ADMIN_PROFILE=sentra-root SENTRA_LIVE_BUCKET=sentra-mg-002 go test -tags=live ./internal/setup/ -run TestLiveProvisionBackupUser -count=1 -v
```
Expected: PASS within ~30s; the key is deleted in cleanup; `~/.aws/credentials` is untouched (the test redirected the file). If `sentra-root`'s session has expired the first call fails with the session-expired error — run `aws login --profile sentra-root` and retry.

- [ ] **Step 3: Commit**

```bash
git add internal/setup/backupuser_live_test.go
git commit -m "test(setup): opt-in live provisioning check behind the live build tag

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
git pull --rebase -q && go build ./... && git push -q
```
