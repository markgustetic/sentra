package cli

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// AWSInspectReport is an alias for diag.AWSReport, preserved so existing
// DoctorDeps callers and tests keep compiling after the read-only AWS
// diagnostics moved to internal/diag.
type AWSInspectReport = diag.AWSReport

// DefaultAWSCheckSDKIdentity verifies credentials through the AWS SDK
// credential chain. Thin wrapper over diag.CheckSDKIdentity so the
// doctor's nil-fallback and setup's identity checker keep their names.
func DefaultAWSCheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return diag.CheckSDKIdentity(ctx, cfg)
}

// DefaultAWSInspect performs the read-only AWS checks for `sentra doctor`.
// Thin wrapper over diag.Inspect.
func DefaultAWSInspect(ctx context.Context, cfg *config.Config) (AWSInspectReport, error) {
	return diag.Inspect(ctx, cfg)
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
