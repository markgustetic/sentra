package cli

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// AWSInspectReport summarizes read-only AWS bucket diagnostics.
type AWSInspectReport struct {
	BucketAccessible          bool
	PublicAccessReadable      bool
	PublicAccessBlocked       bool
	DefaultEncryptionReadable bool
	DefaultEncryptionEnabled  bool
}

// DefaultAWSCheckSDKIdentity verifies credentials through the AWS SDK
// credential chain Sentra will use for S3.
func DefaultAWSCheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	awsCfg, err := loadSetupAWSConfig(ctx, cfg)
	if err != nil {
		return err
	}
	client := sts.NewFromConfig(awsCfg)
	if _, err := client.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		return fmt.Errorf("verify AWS identity: %w", err)
	}
	return nil
}

// DefaultAWSInspect performs read-only AWS checks for `sentra doctor`.
func DefaultAWSInspect(ctx context.Context, cfg *config.Config) (AWSInspectReport, error) {
	awsCfg, err := loadSetupAWSConfig(ctx, cfg)
	if err != nil {
		return AWSInspectReport{}, err
	}
	client := s3.NewFromConfig(awsCfg)
	bucket := cfg.Repo.S3.Bucket
	report := AWSInspectReport{}
	if err := headBucket(ctx, client, bucket); err != nil {
		return AWSInspectReport{}, err
	}
	report.BucketAccessible = true

	readPublic, blocked, err := getBucketPublicAccessBlock(ctx, client, bucket)
	if err != nil {
		return AWSInspectReport{}, err
	}
	report.PublicAccessReadable = readPublic
	report.PublicAccessBlocked = blocked

	readEncryption, encrypted, err := getBucketDefaultEncryption(ctx, client, bucket)
	if err != nil {
		return AWSInspectReport{}, err
	}
	report.DefaultEncryptionReadable = readEncryption
	report.DefaultEncryptionEnabled = encrypted
	return report, nil
}

// DefaultAWSPrepare performs the deterministic AWS S3 setup work chosen
// in the wizard. It intentionally does not create or manage IAM users.
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	if cfg.Repo.S3.Region == "" {
		return AWSPrepareReport{}, fmt.Errorf("repo.s3.region is required for AWS setup")
	}

	store, err := blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:  cfg.Repo.S3.Bucket,
		Prefix:  cfg.Repo.S3.Prefix,
		Region:  cfg.Repo.S3.Region,
		Profile: cfg.Repo.S3.Profile,
	})
	if err != nil {
		return AWSPrepareReport{}, err
	}
	client := store.Client()
	bucket := cfg.Repo.S3.Bucket
	report := AWSPrepareReport{}

	if err := headBucket(ctx, client, bucket); err == nil {
		report.BucketExisted = true
	} else if isS3BucketMissing(err) {
		if !opts.CreateBucket {
			return AWSPrepareReport{}, fmt.Errorf("bucket %q does not exist", bucket)
		}
		created, err := createBucket(ctx, client, bucket, cfg.Repo.S3.Region)
		if err != nil {
			return AWSPrepareReport{}, err
		}
		if created {
			report.BucketCreated = true
		} else {
			report.BucketExisted = true
		}
		if err := waitForBucketExists(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
	} else {
		return AWSPrepareReport{}, err
	}

	if opts.BlockPublicAccess {
		if err := blockBucketPublicAccess(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.PublicAccessBlocked = true
	}
	if opts.DefaultEncryption {
		if err := enableBucketDefaultEncryption(ctx, client, bucket); err != nil {
			return AWSPrepareReport{}, err
		}
		report.DefaultEncryptionEnabled = true
	}
	return report, nil
}
