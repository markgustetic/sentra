package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
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
