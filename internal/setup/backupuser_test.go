package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

const (
	fakeSecret  = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	fakeKeyID   = "AKIAFAKEFAKEFAKEFAKE"
	fakeUserARN = "arn:aws:iam::123456789012:user/sentra-backup"
	// fakePolicyARN is what the provisioner must derive for the test bucket
	// from fakeUserARN — the fake never hands an ARN back from CreatePolicy,
	// so a wrong derivation shows up as a lookup miss on every later call.
	fakePolicyARN = "arn:aws:iam::123456789012:policy/sentra-s3-backup-example-bucket"
)

type fakePolicyVersion struct {
	id        string
	doc       string // plain JSON as submitted
	isDefault bool
	created   int // relative creation order; rendered as a CreateDate
}

// fakeIAM is a small stateful model of the account: users, customer-managed
// policies with their versions, attachments, and the legacy inline policy.
// It records every call in order so tests can assert sequencing (nothing
// mints a key before the policy is attached) as well as outcomes. errs maps
// an operation name to the error it should return.
type fakeIAM struct {
	errs       map[string]error
	userExists bool
	inlineDoc  string // legacy inline policy document, "" = absent
	policies   map[string][]fakePolicyVersion
	attached   []string
	calls      []string

	createUserCalls int
	createKeyCalls  int
	deletedKeyID    string
	createdPolicy   struct{ name, doc string }
	deletedVersions []string
}

func newFakeIAM() *fakeIAM {
	return &fakeIAM{errs: map[string]error{}, policies: map[string][]fakePolicyVersion{}}
}

func (f *fakeIAM) fail(op string, err error) *fakeIAM { f.errs[op] = err; return f }

// withPolicy seeds an existing managed policy whose default (only) version
// is the canonical document for bucket/prefix — what an earlier wizard run
// left behind.
func (f *fakeIAM) withPolicy(t *testing.T, bucket, prefix string) *fakeIAM {
	t.Helper()
	name := BackupUserPolicyNameFor(bucket)
	f.policies[name] = []fakePolicyVersion{{id: "v1", doc: mustMarshalPolicy(t, BuildIAMPolicy(bucket, prefix)), isDefault: true, created: 1}}
	return f
}

func (f *fakeIAM) withInlinePolicy(t *testing.T, bucket, prefix string) *fakeIAM {
	t.Helper()
	f.inlineDoc = mustMarshalPolicy(t, BuildIAMPolicy(bucket, prefix))
	return f
}

func (f *fakeIAM) record(op string) error {
	f.calls = append(f.calls, op)
	return f.errs[op]
}

func (f *fakeIAM) called(op string) bool {
	for _, c := range f.calls {
		if c == op {
			return true
		}
	}
	return false
}

// indexOf returns the position of the first call of op, or -1.
func (f *fakeIAM) indexOf(op string) int {
	for i, c := range f.calls {
		if c == op {
			return i
		}
	}
	return -1
}

func policyNameFromARN(arn string) string {
	return arn[strings.LastIndex(arn, "/")+1:]
}

func (f *fakeIAM) CreateUser(_ context.Context, in *iam.CreateUserInput, _ ...func(*iam.Options)) (*iam.CreateUserOutput, error) {
	f.createUserCalls++
	if err := f.record("CreateUser"); err != nil {
		return nil, err
	}
	if f.userExists {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}
	}
	f.userExists = true
	return &iam.CreateUserOutput{User: &iamtypes.User{UserName: in.UserName, Arn: aws.String(fakeUserARN)}}, nil
}

func (f *fakeIAM) GetUser(_ context.Context, in *iam.GetUserInput, _ ...func(*iam.Options)) (*iam.GetUserOutput, error) {
	if err := f.record("GetUser"); err != nil {
		return nil, err
	}
	return &iam.GetUserOutput{User: &iamtypes.User{UserName: in.UserName, Arn: aws.String(fakeUserARN)}}, nil
}

func (f *fakeIAM) CreatePolicy(_ context.Context, in *iam.CreatePolicyInput, _ ...func(*iam.Options)) (*iam.CreatePolicyOutput, error) {
	if err := f.record("CreatePolicy"); err != nil {
		return nil, err
	}
	name := aws.ToString(in.PolicyName)
	if _, ok := f.policies[name]; ok {
		return nil, &iamtypes.EntityAlreadyExistsException{Message: aws.String("exists")}
	}
	f.createdPolicy.name = name
	f.createdPolicy.doc = aws.ToString(in.PolicyDocument)
	f.policies[name] = []fakePolicyVersion{{id: "v1", doc: f.createdPolicy.doc, isDefault: true, created: 1}}
	// Deliberately no Arn in the output: the provisioner must derive it.
	return &iam.CreatePolicyOutput{Policy: &iamtypes.Policy{PolicyName: in.PolicyName}}, nil
}

func (f *fakeIAM) versions(arn string) ([]fakePolicyVersion, bool) {
	v, ok := f.policies[policyNameFromARN(arn)]
	return v, ok
}

func (f *fakeIAM) ListPolicyVersions(_ context.Context, in *iam.ListPolicyVersionsInput, _ ...func(*iam.Options)) (*iam.ListPolicyVersionsOutput, error) {
	if err := f.record("ListPolicyVersions"); err != nil {
		return nil, err
	}
	vs, ok := f.versions(aws.ToString(in.PolicyArn))
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no policy " + aws.ToString(in.PolicyArn))}
	}
	return &iam.ListPolicyVersionsOutput{Versions: fakeVersions(vs)}, nil
}

// fakeVersions renders the model as the SDK type — without documents, as
// the real ListPolicyVersions omits them.
func fakeVersions(vs []fakePolicyVersion) []iamtypes.PolicyVersion {
	out := make([]iamtypes.PolicyVersion, 0, len(vs))
	for _, v := range vs {
		out = append(out, iamtypes.PolicyVersion{
			VersionId:        aws.String(v.id),
			IsDefaultVersion: v.isDefault,
			CreateDate:       aws.Time(time.Unix(int64(v.created), 0)),
		})
	}
	return out
}

func (f *fakeIAM) GetPolicyVersion(_ context.Context, in *iam.GetPolicyVersionInput, _ ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error) {
	if err := f.record("GetPolicyVersion"); err != nil {
		return nil, err
	}
	vs, ok := f.versions(aws.ToString(in.PolicyArn))
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no policy")}
	}
	for _, v := range vs {
		if v.id == aws.ToString(in.VersionId) {
			// The real API URL-encodes the document.
			return &iam.GetPolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{
				VersionId: aws.String(v.id), IsDefaultVersion: v.isDefault, Document: aws.String(url.PathEscape(v.doc)),
			}}, nil
		}
	}
	return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no version")}
}

func (f *fakeIAM) CreatePolicyVersion(_ context.Context, in *iam.CreatePolicyVersionInput, _ ...func(*iam.Options)) (*iam.CreatePolicyVersionOutput, error) {
	if err := f.record("CreatePolicyVersion"); err != nil {
		return nil, err
	}
	name := policyNameFromARN(aws.ToString(in.PolicyArn))
	vs, ok := f.policies[name]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no policy")}
	}
	if len(vs) >= 5 {
		return nil, &iamtypes.LimitExceededException{Message: aws.String("A managed policy can have up to 5 versions")}
	}
	next, created := 0, 0
	for _, v := range vs {
		var n int
		_, _ = fmt.Sscanf(v.id, "v%d", &n)
		next = max(next, n)
		created = max(created, v.created)
	}
	if in.SetAsDefault {
		for i := range vs {
			vs[i].isDefault = false
		}
	}
	id := fmt.Sprintf("v%d", next+1)
	f.policies[name] = append(vs, fakePolicyVersion{id: id, doc: aws.ToString(in.PolicyDocument), isDefault: in.SetAsDefault, created: created + 1})
	return &iam.CreatePolicyVersionOutput{PolicyVersion: &iamtypes.PolicyVersion{VersionId: aws.String(id), IsDefaultVersion: in.SetAsDefault}}, nil
}

func (f *fakeIAM) DeletePolicyVersion(_ context.Context, in *iam.DeletePolicyVersionInput, _ ...func(*iam.Options)) (*iam.DeletePolicyVersionOutput, error) {
	if err := f.record("DeletePolicyVersion"); err != nil {
		return nil, err
	}
	name := policyNameFromARN(aws.ToString(in.PolicyArn))
	vs := f.policies[name]
	for i, v := range vs {
		if v.id != aws.ToString(in.VersionId) {
			continue
		}
		if v.isDefault {
			return nil, &iamtypes.DeleteConflictException{Message: aws.String("cannot delete the default version")}
		}
		f.deletedVersions = append(f.deletedVersions, v.id)
		f.policies[name] = append(vs[:i:i], vs[i+1:]...)
		return &iam.DeletePolicyVersionOutput{}, nil
	}
	return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no version")}
}

func (f *fakeIAM) AttachUserPolicy(_ context.Context, in *iam.AttachUserPolicyInput, _ ...func(*iam.Options)) (*iam.AttachUserPolicyOutput, error) {
	if err := f.record("AttachUserPolicy"); err != nil {
		return nil, err
	}
	arn := aws.ToString(in.PolicyArn)
	if _, ok := f.versions(arn); !ok {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no policy " + arn)}
	}
	if aws.ToString(in.UserName) != BackupUserName {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no user")}
	}
	f.attached = append(f.attached, arn)
	return &iam.AttachUserPolicyOutput{}, nil
}

func (f *fakeIAM) GetUserPolicy(_ context.Context, in *iam.GetUserPolicyInput, _ ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error) {
	if err := f.record("GetUserPolicy"); err != nil {
		return nil, err
	}
	if f.inlineDoc == "" || aws.ToString(in.PolicyName) != BackupUserPolicyName {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no inline policy")}
	}
	return &iam.GetUserPolicyOutput{
		UserName: in.UserName, PolicyName: in.PolicyName, PolicyDocument: aws.String(url.PathEscape(f.inlineDoc)),
	}, nil
}

func (f *fakeIAM) DeleteUserPolicy(_ context.Context, in *iam.DeleteUserPolicyInput, _ ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error) {
	if err := f.record("DeleteUserPolicy"); err != nil {
		return nil, err
	}
	if f.inlineDoc == "" || aws.ToString(in.PolicyName) != BackupUserPolicyName {
		return nil, &iamtypes.NoSuchEntityException{Message: aws.String("no inline policy")}
	}
	f.inlineDoc = ""
	return &iam.DeleteUserPolicyOutput{}, nil
}

func (f *fakeIAM) CreateAccessKey(_ context.Context, _ *iam.CreateAccessKeyInput, _ ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error) {
	f.createKeyCalls++
	if err := f.record("CreateAccessKey"); err != nil {
		return nil, err
	}
	return &iam.CreateAccessKeyOutput{AccessKey: &iamtypes.AccessKey{
		AccessKeyId:     aws.String(fakeKeyID),
		SecretAccessKey: aws.String(fakeSecret),
	}}, nil
}

func (f *fakeIAM) DeleteAccessKey(_ context.Context, in *iam.DeleteAccessKeyInput, _ ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error) {
	f.deletedKeyID = aws.ToString(in.AccessKeyId)
	if err := f.record("DeleteAccessKey"); err != nil {
		return nil, err
	}
	return &iam.DeleteAccessKeyOutput{}, nil
}

// accessDenied mimics the generic API error IAM returns for a missing
// permission — a smithy.GenericAPIError, not a typed exception.
func accessDenied() error {
	return &smithy.GenericAPIError{Code: "AccessDenied", Message: "not authorized"}
}

func mustMarshalPolicy(t *testing.T, doc IAMPolicyDocument) string {
	t.Helper()
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

func okWriter(_, _, _, _ string) error { return nil }

func provision(f *fakeIAM, cfg *config.Config, write credentialsWriter) (BackupUserReport, error) {
	return provisionBackupUser(context.Background(), f, cfg, BackupUserOptions{Profile: "sentra"}, "/tmp/creds", write)
}

func TestProvisionBackupUser_HappyPath(t *testing.T) {
	f := newFakeIAM()
	var got writerCall
	write := func(path, profile, keyID, secret string) error {
		got = writerCall{path, profile, keyID, secret}
		return nil
	}
	report, err := provision(f, backupUserCfg(), write)
	if err != nil {
		t.Fatalf("provisionBackupUser: %v", err)
	}
	if !report.UserCreated || report.UserExisted || !report.PolicyCreated || report.PolicyUpdated || !report.PolicyAttached {
		t.Fatalf("report flags = %+v", report)
	}
	if report.UserName != BackupUserName || report.Profile != "sentra" || report.CredentialsPath != "/tmp/creds" {
		t.Fatalf("report identity fields = %+v", report)
	}
	if report.PolicyName != "sentra-s3-backup-example-bucket" {
		t.Fatalf("PolicyName = %q", report.PolicyName)
	}
	if report.AccessKeyID != fakeKeyID {
		t.Fatalf("AccessKeyID = %q", report.AccessKeyID)
	}
	if got.secret != fakeSecret || got.profile != "sentra" || got.path != "/tmp/creds" {
		t.Fatalf("writer got %+v", got)
	}
	// The policy must be the exact canonical document for this bucket+prefix
	// — not merely parseable JSON with the right statement count, which
	// can't tell a right bucket from a wrong one or swapped arguments.
	if want := mustMarshalPolicy(t, BuildIAMPolicy("example-bucket", "sentra/")); f.createdPolicy.doc != want {
		t.Fatalf("created policy doc =\n%s\nwant\n%s", f.createdPolicy.doc, want)
	}
	if f.createdPolicy.name != "sentra-s3-backup-example-bucket" {
		t.Fatalf("created policy name = %q", f.createdPolicy.name)
	}
	// Attached to the right user by the ARN derived from the user's account.
	if len(f.attached) != 1 || f.attached[0] != fakePolicyARN {
		t.Fatalf("attached = %v, want [%s]", f.attached, fakePolicyARN)
	}
	// A fresh policy needs no version work and a fresh user has no legacy
	// inline policy to remove.
	for _, op := range []string{"CreatePolicyVersion", "DeletePolicyVersion", "DeleteUserPolicy", "GetUser"} {
		if f.called(op) {
			t.Fatalf("%s must not be called on the fresh path: %v", op, f.calls)
		}
	}
	// Ordering: the key is minted only once the grant is in place.
	if f.indexOf("AttachUserPolicy") > f.indexOf("CreateAccessKey") {
		t.Fatalf("key minted before the policy was attached: %v", f.calls)
	}
}

func TestProvisionBackupUser_BlankProfileDefaults(t *testing.T) {
	f := newFakeIAM()
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

// An existing user carries no ARN in the CreateUser response, so the
// provisioner must look it up: the policy ARN comes from the user's account.
func TestProvisionBackupUser_ReusesExistingUser(t *testing.T) {
	f := newFakeIAM()
	f.userExists = true
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatalf("existing user must be reused, got %v", err)
	}
	if report.UserCreated || !report.UserExisted || !report.PolicyAttached || f.createKeyCalls != 1 {
		t.Fatalf("report = %+v, createKeyCalls = %d", report, f.createKeyCalls)
	}
	if !f.called("GetUser") || len(f.attached) != 1 || f.attached[0] != fakePolicyARN {
		t.Fatalf("existing user must be looked up for its ARN; calls=%v attached=%v", f.calls, f.attached)
	}
}

// Second bucket, same account: the first bucket's policy is untouched and a
// second managed policy is attached alongside it — the reason for managed
// policies over the one inline policy the wizard used to replace.
func TestProvisionBackupUser_SecondBucketAccumulates(t *testing.T) {
	f := newFakeIAM().withPolicy(t, "first-bucket", "sentra/")
	f.userExists = true
	f.attached = []string{"arn:aws:iam::123456789012:policy/sentra-s3-backup-first-bucket"}
	cfg := backupUserCfg()
	cfg.Repo.S3.Bucket = "second-bucket"
	report, err := provision(f, cfg, okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PolicyCreated || report.PolicyName != "sentra-s3-backup-second-bucket" {
		t.Fatalf("report = %+v", report)
	}
	if len(f.attached) != 2 || f.attached[1] != "arn:aws:iam::123456789012:policy/sentra-s3-backup-second-bucket" {
		t.Fatalf("attached = %v", f.attached)
	}
	if first := f.policies["sentra-s3-backup-first-bucket"]; len(first) != 1 || first[0].doc != mustMarshalPolicy(t, BuildIAMPolicy("first-bucket", "sentra/")) {
		t.Fatalf("first bucket's policy was modified: %+v", first)
	}
}

// Rerun for the same repo: the stored document already says exactly what we
// would write, so no version is burned (five is the limit) — but the policy
// is still (re)attached, since attachment is what a rerun may need to repair.
func TestProvisionBackupUser_ExistingPolicySameDocumentReused(t *testing.T) {
	f := newFakeIAM().withPolicy(t, "example-bucket", "sentra/")
	f.userExists = true
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if report.PolicyCreated || report.PolicyUpdated || !report.PolicyAttached {
		t.Fatalf("report = %+v, want reused+attached", report)
	}
	if f.called("CreatePolicyVersion") || f.called("DeletePolicyVersion") {
		t.Fatalf("identical document must not create a version: %v", f.calls)
	}
	if len(f.attached) != 1 || f.attached[0] != fakePolicyARN {
		t.Fatalf("attached = %v", f.attached)
	}
}

// Same bucket, different prefix (two machines sharing a bucket): the new
// default version grants BOTH prefixes. Narrowing to the new prefix would
// silently revoke the first machine's backups — the bug this work removes.
func TestProvisionBackupUser_ExistingPolicyNewPrefixMergesAsNewVersion(t *testing.T) {
	f := newFakeIAM().withPolicy(t, "example-bucket", "desktop/")
	f.userExists = true
	cfg := backupUserCfg()
	cfg.Repo.S3.Prefix = "laptop/"
	report, err := provision(f, cfg, okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if report.PolicyCreated || !report.PolicyUpdated || !report.PolicyAttached {
		t.Fatalf("report = %+v, want updated+attached", report)
	}
	vs := f.policies["sentra-s3-backup-example-bucket"]
	if len(vs) != 2 || !vs[1].isDefault || vs[0].isDefault {
		t.Fatalf("versions = %+v, want v2 default", vs)
	}
	got, err := parsePolicyDocument(url.PathEscape(vs[1].doc))
	if err != nil {
		t.Fatal(err)
	}
	objects := statementBySid(t, got, "SentraRepositoryObjects")
	want := []string{"arn:aws:s3:::example-bucket/desktop/*", "arn:aws:s3:::example-bucket/laptop/*"}
	if len(objects.Resource) != 2 || objects.Resource[0] != want[0] || objects.Resource[1] != want[1] {
		t.Fatalf("new version objects = %v, want %v", objects.Resource, want)
	}
	if f.indexOf("CreatePolicyVersion") > f.indexOf("AttachUserPolicy") {
		t.Fatalf("attach must follow the version write: %v", f.calls)
	}
}

// At the five-version limit the oldest non-default version is deleted first;
// the default version is never a candidate.
func TestProvisionBackupUser_VersionLimitPrunesOldest(t *testing.T) {
	f := newFakeIAM()
	f.userExists = true
	name := "sentra-s3-backup-example-bucket"
	for i := 1; i <= 5; i++ {
		f.policies[name] = append(f.policies[name], fakePolicyVersion{
			id: fmt.Sprintf("v%d", i), doc: mustMarshalPolicy(t, BuildIAMPolicy("example-bucket", fmt.Sprintf("p%d/", i))),
			isDefault: i == 5, created: i,
		})
	}
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PolicyUpdated {
		t.Fatalf("report = %+v", report)
	}
	if len(f.deletedVersions) != 1 || f.deletedVersions[0] != "v1" {
		t.Fatalf("deleted versions = %v, want [v1]", f.deletedVersions)
	}
	vs := f.policies[name]
	if len(vs) != 5 || vs[4].id != "v6" || !vs[4].isDefault {
		t.Fatalf("versions after prune = %+v", vs)
	}
}

// A stored document that is not Sentra's shape cannot be merged; it is
// replaced by the canonical document rather than failing the step.
func TestProvisionBackupUser_UnparseableExistingDocumentReplaced(t *testing.T) {
	f := newFakeIAM()
	f.userExists = true
	f.policies["sentra-s3-backup-example-bucket"] = []fakePolicyVersion{{id: "v1", doc: `{"Version":"2012-10-17","Statement":{"Effect":"Allow"}}`, isDefault: true, created: 1}}
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if !report.PolicyUpdated {
		t.Fatalf("report = %+v", report)
	}
	vs := f.policies["sentra-s3-backup-example-bucket"]
	if len(vs) != 2 || vs[1].doc != mustMarshalPolicy(t, BuildIAMPolicy("example-bucket", "sentra/")) {
		t.Fatalf("versions = %+v", vs)
	}
}

// Migration of pre-managed-policy installs: the legacy inline policy is
// removed once the managed policy that covers it is attached — and only
// then, so the user never sits without a grant in between.
func TestProvisionBackupUser_LegacyInlinePolicyRemovedAfterAttach(t *testing.T) {
	f := newFakeIAM().withInlinePolicy(t, "example-bucket", "sentra/")
	f.userExists = true
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if !report.LegacyPolicyRemoved || f.inlineDoc != "" {
		t.Fatalf("report = %+v, inline = %q", report, f.inlineDoc)
	}
	if f.indexOf("DeleteUserPolicy") < f.indexOf("AttachUserPolicy") {
		t.Fatalf("inline policy deleted before the managed one was attached: %v", f.calls)
	}
}

// The inline policy may grant a DIFFERENT bucket (an install provisioned
// before managed policies, now running setup for a second bucket). Deleting
// it would revoke that bucket: it stays, and the run still succeeds.
func TestProvisionBackupUser_LegacyInlinePolicyForOtherBucketRetained(t *testing.T) {
	f := newFakeIAM().withInlinePolicy(t, "first-bucket", "sentra/")
	f.userExists = true
	report, err := provision(f, backupUserCfg(), okWriter)
	if err != nil {
		t.Fatal(err)
	}
	if report.LegacyPolicyRemoved || f.inlineDoc == "" || f.called("DeleteUserPolicy") {
		t.Fatalf("uncovered inline policy must be retained: report=%+v calls=%v", report, f.calls)
	}
}

// Cleanup is best-effort: the inline policy is inert residue once the managed
// grant is attached, so a denied read or delete must not turn a working
// provisioning into a warning (which would keep setup on expiring session
// credentials for nothing).
func TestProvisionBackupUser_LegacyCleanupFailureIsNotAnError(t *testing.T) {
	for _, op := range []string{"GetUserPolicy", "DeleteUserPolicy"} {
		t.Run(op, func(t *testing.T) {
			f := newFakeIAM().withInlinePolicy(t, "example-bucket", "sentra/").fail(op, accessDenied())
			f.userExists = true
			report, err := provision(f, backupUserCfg(), okWriter)
			if err != nil {
				t.Fatalf("cleanup failure must not fail provisioning: %v", err)
			}
			if report.LegacyPolicyRemoved || !report.PolicyAttached || f.createKeyCalls != 1 {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

func TestProvisionBackupUser_AccessDeniedClassifiedPerStep(t *testing.T) {
	tests := []struct {
		name  string
		f     *fakeIAM
		step  string
		exist bool
	}{
		{"create user", newFakeIAM().fail("CreateUser", accessDenied()), "iam:CreateUser", false},
		{"get user", newFakeIAM().fail("GetUser", accessDenied()), "iam:GetUser", true},
		{"create policy", newFakeIAM().fail("CreatePolicy", accessDenied()), "iam:CreatePolicy", false},
		{"list versions", newFakeIAM().withPolicy(t, "example-bucket", "old/").fail("ListPolicyVersions", accessDenied()), "iam:ListPolicyVersions", true},
		{"get version", newFakeIAM().withPolicy(t, "example-bucket", "old/").fail("GetPolicyVersion", accessDenied()), "iam:GetPolicyVersion", true},
		{"create version", newFakeIAM().withPolicy(t, "example-bucket", "old/").fail("CreatePolicyVersion", accessDenied()), "iam:CreatePolicyVersion", true},
		{"attach policy", newFakeIAM().fail("AttachUserPolicy", accessDenied()), "iam:AttachUserPolicy", false},
		{"create key", newFakeIAM().fail("CreateAccessKey", accessDenied()), "iam:CreateAccessKey", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.userExists = tc.exist
			_, err := provision(tc.f, backupUserCfg(), okWriter)
			var perr *BackupUserError
			if !errors.As(err, &perr) {
				t.Fatalf("err = %v, want *BackupUserError", err)
			}
			if !perr.AccessDenied || perr.Step != tc.step {
				t.Fatalf("classification = %+v, want AccessDenied at %s", perr, tc.step)
			}
			// Every policy-side failure happens before a key exists.
			if tc.step != "iam:CreateAccessKey" && tc.f.createKeyCalls != 0 {
				t.Fatalf("key minted despite %s failing", tc.step)
			}
		})
	}
}

func TestProvisionBackupUser_DeleteVersionDenied(t *testing.T) {
	f := newFakeIAM().fail("DeletePolicyVersion", accessDenied())
	f.userExists = true
	name := "sentra-s3-backup-example-bucket"
	for i := 1; i <= 5; i++ {
		f.policies[name] = append(f.policies[name], fakePolicyVersion{id: fmt.Sprintf("v%d", i), doc: mustMarshalPolicy(t, BuildIAMPolicy("example-bucket", fmt.Sprintf("p%d/", i))), isDefault: i == 5, created: i})
	}
	_, err := provision(f, backupUserCfg(), okWriter)
	var perr *BackupUserError
	if !errors.As(err, &perr) || !perr.AccessDenied || perr.Step != "iam:DeletePolicyVersion" {
		t.Fatalf("err = %v, want AccessDenied at iam:DeletePolicyVersion", err)
	}
}

func TestProvisionBackupUser_KeyLimit(t *testing.T) {
	f := newFakeIAM().fail("CreateAccessKey", &iamtypes.LimitExceededException{Message: aws.String("2 keys")})
	_, err := provision(f, backupUserCfg(), okWriter)
	var perr *BackupUserError
	if !errors.As(err, &perr) || !perr.KeyLimit || perr.PolicyLimit || perr.Step != "iam:CreateAccessKey" {
		t.Fatalf("err = %v, want KeyLimit at iam:CreateAccessKey", err)
	}
}

// LimitExceeded on attach is the ten-managed-policies-per-user quota, not
// the two-keys quota: it must not be reported as KeyLimit.
func TestProvisionBackupUser_PolicyLimit(t *testing.T) {
	f := newFakeIAM().fail("AttachUserPolicy", &iamtypes.LimitExceededException{Message: aws.String("10 policies")})
	_, err := provision(f, backupUserCfg(), okWriter)
	var perr *BackupUserError
	if !errors.As(err, &perr) || !perr.PolicyLimit || perr.KeyLimit || perr.Step != "iam:AttachUserPolicy" {
		t.Fatalf("err = %v, want PolicyLimit at iam:AttachUserPolicy", err)
	}
	if f.createKeyCalls != 0 {
		t.Fatal("no key may be minted when the policy could not be attached")
	}
}

// A quota on any other step is neither: those warnings would name the wrong fix.
func TestProvisionBackupUser_OtherLimitUnclassified(t *testing.T) {
	f := newFakeIAM().fail("CreatePolicy", &iamtypes.LimitExceededException{Message: aws.String("1500 policies")})
	_, err := provision(f, backupUserCfg(), okWriter)
	var perr *BackupUserError
	if !errors.As(err, &perr) || perr.KeyLimit || perr.PolicyLimit || perr.Step != "iam:CreatePolicy" {
		t.Fatalf("err = %v, want unclassified limit at iam:CreatePolicy", err)
	}
}

// The one ordering hazard: a key minted in AWS with nowhere to live on disk.
func TestProvisionBackupUser_WriteFailureDeletesKey(t *testing.T) {
	f := newFakeIAM()
	_, err := provision(f, backupUserCfg(), func(_, _, _, _ string) error { return errors.New("disk full") })
	var perr *BackupUserError
	if !errors.As(err, &perr) || perr.Step != "credentials" {
		t.Fatalf("err = %v, want credentials-step error", err)
	}
	if f.deletedKeyID != fakeKeyID {
		t.Fatalf("minted key must be deleted on write failure, deleted %q", f.deletedKeyID)
	}
	if perr.KeyOrphaned != "" {
		t.Fatalf("successful cleanup must not flag an orphan, got %q", perr.KeyOrphaned)
	}
}

func TestProvisionBackupUser_WriteFailureCleanupFailureFlagsOrphan(t *testing.T) {
	f := newFakeIAM().fail("DeleteAccessKey", errors.New("nope"))
	_, err := provision(f, backupUserCfg(), func(_, _, _, _ string) error { return errors.New("disk full") })
	var perr *BackupUserError
	if !errors.As(err, &perr) || perr.KeyOrphaned != fakeKeyID {
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
	f := newFakeIAM()
	_, err := provisionBackupUser(context.Background(), f, backupUserCfg(),
		BackupUserOptions{Profile: "sentra"}, path, WriteAWSCredentialsProfile)
	if !errors.Is(err, ErrCredentialsProfileExists) {
		t.Fatalf("err = %v, want ErrCredentialsProfileExists", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("IAM must not be called when the profile is taken: %v", f.calls)
	}
}

// The secret must never leak through the report or an error string — the
// dangerous condition, asserted on every failure path that follows minting.
func TestProvisionBackupUser_SecretNeverInReportOrError(t *testing.T) {
	f := newFakeIAM().fail("DeleteAccessKey", errors.New("nope"))
	report, err := provision(f, backupUserCfg(), func(_, _, _, _ string) error { return errors.New("disk full") })
	if err == nil {
		t.Fatal("expected a write failure")
	}
	if strings.Contains(err.Error(), fakeSecret) {
		t.Fatalf("secret leaked into error: %v", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", report), fakeSecret) {
		t.Fatalf("secret leaked into report: %+v", report)
	}
}

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
