// Package diag holds read-only environment diagnostics shared by the
// `sentra doctor` CLI command and the TUI Doctor view. It imports only
// the AWS SDK and internal/config so both internal/cli and internal/tui
// can depend on it without an import cycle.
package diag

import (
	"context"
	"errors"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

// AWSReport summarizes read-only AWS bucket diagnostics. It mirrors the
// former cli.AWSInspectReport field-for-field so the cli wrapper can
// expose it as a type alias.
type AWSReport struct {
	BucketAccessible          bool
	PublicAccessReadable      bool
	PublicAccessBlocked       bool
	DefaultEncryptionReadable bool
	DefaultEncryptionEnabled  bool
}

// CheckSDKIdentity verifies credentials through the AWS SDK credential
// chain Sentra will use for S3. Read-only: it calls sts:GetCallerIdentity
// and nothing else, so it never mutates the account.
func CheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return err
	}
	client := sts.NewFromConfig(awsCfg)
	if _, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("verify AWS identity: %w", err)
	}
	return nil
}

// Inspect performs the read-only AWS checks for `sentra doctor` and the
// TUI Doctor view: bucket reachability, public-access-block state, and
// default-encryption state. It issues only Head/Get calls — never a Put —
// so it is safe to run against a production bucket.
func Inspect(ctx context.Context, cfg *config.Config) (AWSReport, error) {
	awsCfg, err := loadAWSConfig(ctx, cfg)
	if err != nil {
		return AWSReport{}, err
	}
	client := s3.NewFromConfig(awsCfg)
	bucket := cfg.Repo.S3.Bucket
	report := AWSReport{}
	if err := headBucket(ctx, client, bucket); err != nil {
		return AWSReport{}, err
	}
	report.BucketAccessible = true

	readPublic, blocked, err := getBucketPublicAccessBlock(ctx, client, bucket)
	if err != nil {
		return AWSReport{}, err
	}
	report.PublicAccessReadable = readPublic
	report.PublicAccessBlocked = blocked

	readEncryption, encrypted, err := getBucketDefaultEncryption(ctx, client, bucket)
	if err != nil {
		return AWSReport{}, err
	}
	report.DefaultEncryptionReadable = readEncryption
	report.DefaultEncryptionEnabled = encrypted
	return report, nil
}

func loadAWSConfig(ctx context.Context, cfg *config.Config) (aws.Config, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg != nil {
		if cfg.Repo.S3.Region != "" {
			loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Repo.S3.Region))
		}
		if cfg.Repo.S3.Profile != "" {
			loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.Repo.S3.Profile))
		}
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return aws.Config{}, fmt.Errorf("load AWS config: %w", err)
	}
	return awsCfg, nil
}

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, bucketARN(bucket), err)
	}
	return nil
}

func getBucketPublicAccessBlock(ctx context.Context, client *s3.Client, bucket string) (bool, bool, error) {
	out, err := client.GetPublicAccessBlock(ctx, &s3.GetPublicAccessBlockInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isAWSAPIErrCode(err, "NoSuchPublicAccessBlockConfiguration", "NoSuchPublicAccessBlock") {
			return true, false, nil
		}
		return false, false, fmt.Errorf("inspect public access block for bucket %q (requires s3:GetBucketPublicAccessBlock on %s): %w", bucket, bucketARN(bucket), err)
	}
	cfg := out.PublicAccessBlockConfiguration
	blocked := cfg != nil &&
		aws.ToBool(cfg.BlockPublicAcls) &&
		aws.ToBool(cfg.IgnorePublicAcls) &&
		aws.ToBool(cfg.BlockPublicPolicy) &&
		aws.ToBool(cfg.RestrictPublicBuckets)
	return true, blocked, nil
}

func getBucketDefaultEncryption(ctx context.Context, client *s3.Client, bucket string) (bool, bool, error) {
	out, err := client.GetBucketEncryption(ctx, &s3.GetBucketEncryptionInput{Bucket: aws.String(bucket)})
	if err != nil {
		if isAWSAPIErrCode(err, "ServerSideEncryptionConfigurationNotFoundError") {
			return true, false, nil
		}
		return false, false, fmt.Errorf("inspect default encryption for bucket %q (requires s3:GetBucketEncryption on %s): %w", bucket, bucketARN(bucket), err)
	}
	cfg := out.ServerSideEncryptionConfiguration
	return true, cfg != nil && len(cfg.Rules) > 0, nil
}

func isAWSAPIErrCode(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

func bucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}
