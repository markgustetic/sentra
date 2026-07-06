package cli

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

const bucketExistsWaitTimeout = 2 * time.Minute

func headBucket(ctx context.Context, client *s3.Client, bucket string) error {
	_, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err != nil {
		return fmt.Errorf("head bucket %q (requires s3:ListBucket on %s): %w", bucket, s3BucketARN(bucket), err)
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
	return false, fmt.Errorf("create bucket %q (requires s3:CreateBucket on %s): %w", bucket, s3BucketARN(bucket), err)
}

func waitForBucketExists(ctx context.Context, client *s3.Client, bucket string) error {
	waiter := s3.NewBucketExistsWaiter(client)
	err := waiter.Wait(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)}, bucketExistsWaitTimeout)
	if err != nil {
		return fmt.Errorf("wait for bucket %q to exist (requires s3:ListBucket on %s): %w", bucket, s3BucketARN(bucket), err)
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
		return fmt.Errorf("block public access for bucket %q (requires s3:PutBucketPublicAccessBlock on %s): %w", bucket, s3BucketARN(bucket), err)
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
		return fmt.Errorf("enable default encryption for bucket %q (requires s3:PutBucketEncryption on %s): %w", bucket, s3BucketARN(bucket), err)
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

func s3BucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}

func s3ObjectARN(bucket string, prefix string) string {
	if prefix == "" {
		return s3BucketARN(bucket) + "/*"
	}
	return s3BucketARN(bucket) + "/" + prefix + "*"
}
