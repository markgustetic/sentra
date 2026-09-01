package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	putPolicyUser   string
	putPolicyName   string
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
	f.putPolicyUser = aws.ToString(in.UserName)
	f.putPolicyName = aws.ToString(in.PolicyName)
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
	// The policy must be the exact canonical document for this bucket+prefix
	// — not merely parseable JSON with the right statement count, which
	// can't tell a right bucket from a wrong one or swapped arguments.
	wantDoc, err := json.Marshal(BuildIAMPolicy("example-bucket", "sentra/"))
	if err != nil {
		t.Fatalf("marshal want policy: %v", err)
	}
	if f.putPolicyDoc != string(wantDoc) {
		t.Fatalf("putPolicyDoc =\n%s\nwant\n%s", f.putPolicyDoc, wantDoc)
	}
	// The policy must be attached to the right user under the right name.
	if f.putPolicyUser != BackupUserName || f.putPolicyName != BackupUserPolicyName {
		t.Fatalf("PutUserPolicy user/name = %q/%q, want %q/%q", f.putPolicyUser, f.putPolicyName, BackupUserName, BackupUserPolicyName)
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
