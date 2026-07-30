package setup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

const bucketExistsWaitTimeout = 2 * time.Minute

// DefaultAWSPrepare performs the deterministic AWS S3 setup work chosen in
// the wizard. It intentionally does not create or manage IAM users. Moved
// verbatim from the old internal/cli/setup_awss3.go (that file has since
// been trimmed to identity-check and inspect wrappers only); it must build
// its own *S3 store because it needs the concrete *s3.Client (Store does not
// expose Client()), so it cannot use Effects.NewStore.
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

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, BucketARN(bucket), err)
	}
	return nil
}

func createBucket(ctx context.Context, client *s3.Client, bucket, region string) (bool, error) {
	input := &s3.CreateBucketInput{Bucket: aws.String(bucket)}
	if region != "" && region != "us-east-1" {
		input.CreateBucketConfiguration = &types.CreateBucketConfiguration{
			LocationConstraint: types.BucketLocationConstraint(region),
		}
	}
	_, err := client.CreateBucket(ctx, input)
	if err == nil {
		return true, nil
	}
	if isBucketAlreadyOwned(err) {
		return false, nil
	}
	return false, fmt.Errorf("create bucket %q (requires s3:CreateBucket on %s): %w", bucket, BucketARN(bucket), err)
}

func waitForBucketExists(ctx context.Context, client *s3.Client, bucket string) error {
	waiter := s3.NewBucketExistsWaiter(client)
	err := waiter.Wait(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}, bucketExistsWaitTimeout)
	if err != nil {
		return fmt.Errorf("wait for bucket %q to exist (requires s3:ListBucket on %s): %w", bucket, BucketARN(bucket), err)
	}
	return nil
}

func blockBucketPublicAccess(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(bucket),
		PublicAccessBlockConfiguration: &types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("block public access for bucket %q (requires s3:PutBucketPublicAccessBlock on %s): %w", bucket, BucketARN(bucket), err)
	}
	return nil
}

func enableBucketDefaultEncryption(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.PutBucketEncryption(ctx, &s3.PutBucketEncryptionInput{
		Bucket: aws.String(bucket),
		ServerSideEncryptionConfiguration: &types.ServerSideEncryptionConfiguration{
			Rules: []types.ServerSideEncryptionRule{
				{
					ApplyServerSideEncryptionByDefault: &types.ServerSideEncryptionByDefault{
						SSEAlgorithm: types.ServerSideEncryptionAes256,
					},
				},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("enable default encryption for bucket %q (requires s3:PutBucketEncryption on %s): %w", bucket, BucketARN(bucket), err)
	}
	return nil
}

func isS3BucketMissing(err error) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.ErrorCode() {
	case "NotFound", "NoSuchBucket", "404":
		return true
	default:
		return false
	}
}

func isBucketAlreadyOwned(err error) bool {
	var apiErr smithy.APIError
	return errors.As(err, &apiErr) && apiErr.ErrorCode() == "BucketAlreadyOwnedByYou"
}
