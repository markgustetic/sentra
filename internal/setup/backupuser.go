package setup

import (
	"context"
	"errors"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// iamAPI is the slice of the IAM client the provisioner uses. *iam.Client
// satisfies it; tests pass a fake. Every call the provisioner makes is on
// this surface and nothing else, which is what keeps every failure path
// unit-testable without an AWS account.
type iamAPI interface {
	CreateUser(ctx context.Context, in *iam.CreateUserInput, opts ...func(*iam.Options)) (*iam.CreateUserOutput, error)
	GetUser(ctx context.Context, in *iam.GetUserInput, opts ...func(*iam.Options)) (*iam.GetUserOutput, error)
	CreatePolicy(ctx context.Context, in *iam.CreatePolicyInput, opts ...func(*iam.Options)) (*iam.CreatePolicyOutput, error)
	ListPolicyVersions(ctx context.Context, in *iam.ListPolicyVersionsInput, opts ...func(*iam.Options)) (*iam.ListPolicyVersionsOutput, error)
	GetPolicyVersion(ctx context.Context, in *iam.GetPolicyVersionInput, opts ...func(*iam.Options)) (*iam.GetPolicyVersionOutput, error)
	CreatePolicyVersion(ctx context.Context, in *iam.CreatePolicyVersionInput, opts ...func(*iam.Options)) (*iam.CreatePolicyVersionOutput, error)
	DeletePolicyVersion(ctx context.Context, in *iam.DeletePolicyVersionInput, opts ...func(*iam.Options)) (*iam.DeletePolicyVersionOutput, error)
	AttachUserPolicy(ctx context.Context, in *iam.AttachUserPolicyInput, opts ...func(*iam.Options)) (*iam.AttachUserPolicyOutput, error)
	GetUserPolicy(ctx context.Context, in *iam.GetUserPolicyInput, opts ...func(*iam.Options)) (*iam.GetUserPolicyOutput, error)
	DeleteUserPolicy(ctx context.Context, in *iam.DeleteUserPolicyInput, opts ...func(*iam.Options)) (*iam.DeleteUserPolicyOutput, error)
	CreateAccessKey(ctx context.Context, in *iam.CreateAccessKeyInput, opts ...func(*iam.Options)) (*iam.CreateAccessKeyOutput, error)
	DeleteAccessKey(ctx context.Context, in *iam.DeleteAccessKeyInput, opts ...func(*iam.Options)) (*iam.DeleteAccessKeyOutput, error)
}

// credentialsWriter is WriteAWSCredentialsProfile's shape, injected so the
// provisioner's write-failure path can be exercised without touching disk.
type credentialsWriter func(path, profile, accessKeyID, secret string) error

// BackupUserError classifies a provisioning failure so the engine can write
// a warning that names the fix. Step is the IAM action or "credentials".
// KeyLimit and PolicyLimit are the two quotas an operator can clear by hand
// (two access keys per user; ten managed policies per user). KeyOrphaned is
// the access key ID left behind when a post-mint failure's cleanup also
// failed — the one outcome an operator must act on by hand.
type BackupUserError struct {
	Step         string
	AccessDenied bool
	KeyLimit     bool
	PolicyLimit  bool
	KeyOrphaned  string
	Err          error
}

func (e *BackupUserError) Error() string { return e.Step + ": " + e.Err.Error() }
func (e *BackupUserError) Unwrap() error { return e.Err }

// DefaultProvisionBackupUser is the production Effects driver: it authenticates
// with the credential chain cfg currently names (the session that just signed
// in) via diag.LoadAWSConfig — the same loader the identity check used, so
// "which credentials" can never differ between the check and the mutation —
// then creates the scoped user and stores its key.
func DefaultProvisionBackupUser(ctx context.Context, cfg *config.Config, opts BackupUserOptions) (BackupUserReport, error) {
	if cfg == nil {
		return BackupUserReport{}, errors.New("provision backup user: nil config")
	}
	path, err := AWSCredentialsPath()
	if err != nil {
		return BackupUserReport{}, err
	}
	awsCfg, err := diag.LoadAWSConfig(ctx, cfg)
	if err != nil {
		return BackupUserReport{}, err
	}
	return provisionBackupUser(ctx, iam.NewFromConfig(awsCfg), cfg, opts, path, WriteAWSCredentialsProfile)
}

// provisionBackupUser is the ordered body: pre-check the profile, create (or
// reuse) the user, ensure and attach this bucket's managed policy, retire a
// legacy inline policy the managed one now covers, mint a key, write it.
// Every IAM mutation that can fail happens before the key exists, so a
// policy-side failure never leaves a live secret behind. The secret itself
// exists only between the mint and the write and is never returned. A write
// failure deletes the just-minted key so no live secret is left homeless; if
// that cleanup fails too, the key ID is reported as orphaned.
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

	created, err := client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(BackupUserName)})
	var userARN string
	switch {
	case err == nil:
		report.UserCreated = true
		if created != nil && created.User != nil {
			userARN = aws.ToString(created.User.Arn)
		}
	case isIAMEntityExists(err):
		report.UserExisted = true
	default:
		return report, classifyIAMError("iam:CreateUser", err)
	}
	if userARN == "" {
		// An existing user's ARN — the account and partition the policy ARN
		// is derived from — is only available by lookup.
		got, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(BackupUserName)})
		if err != nil {
			return report, classifyIAMError("iam:GetUser", err)
		}
		if got == nil || got.User == nil || aws.ToString(got.User.Arn) == "" {
			return report, &BackupUserError{Step: "iam:GetUser", Err: errors.New("iam returned no user ARN")}
		}
		userARN = aws.ToString(got.User.Arn)
	}

	report.PolicyName = BackupUserPolicyNameFor(cfg.Repo.S3.Bucket)
	policy, err := ensureBackupPolicy(ctx, client, userARN, cfg.Repo.S3.Bucket, cfg.Repo.S3.Prefix)
	if err != nil {
		return report, err
	}
	report.PolicyCreated, report.PolicyUpdated = policy.created, policy.updated
	// AttachUserPolicy is idempotent: a rerun re-attaches without error, and
	// that is the repair a rerun may exist to make.
	if _, err := client.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{
		UserName:  aws.String(BackupUserName),
		PolicyArn: aws.String(policy.arn),
	}); err != nil {
		return report, classifyIAMError("iam:AttachUserPolicy", err)
	}
	report.PolicyAttached = true
	report.LegacyPolicyRemoved = removeLegacyInlinePolicy(ctx, client, policy.doc)

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

// classifyIAMError maps the actionable IAM failures. AccessDenied arrives as
// a generic API error (code only) and applies to any step. A quota
// (LimitExceeded) is only actionable when the warning can name what to
// remove, so it is bound to the step: two keys per user on CreateAccessKey,
// ten managed policies per user on AttachUserPolicy. Any other step's quota
// (managed policies per account, document size) falls through unclassified
// and is reported verbatim with its step.
func classifyIAMError(step string, err error) *BackupUserError {
	e := &BackupUserError{Step: step, Err: err}
	limit := false
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "AccessDenied", "AccessDeniedException":
			e.AccessDenied = true
		case "LimitExceeded", "LimitExceededException":
			limit = true
		}
	}
	var typed *iamtypes.LimitExceededException
	if errors.As(err, &typed) {
		limit = true
	}
	switch {
	case limit && step == "iam:CreateAccessKey":
		e.KeyLimit = true
	case limit && step == "iam:AttachUserPolicy":
		e.PolicyLimit = true
	}
	return e
}
