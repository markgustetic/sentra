package setup

import (
	"encoding/json"
	"fmt"
	"io"
)

// IAMPolicyDocument is a least-privilege AWS IAM policy for Sentra, rendered as
// non-secret JSON for the operator to paste into AWS.
type IAMPolicyDocument struct {
	Version   string               `json:"Version"`
	Statement []IAMPolicyStatement `json:"Statement"`
}

// IAMPolicyStatement is one statement in an IAMPolicyDocument. Condition is
// part of the general IAM shape but BuildIAMPolicy never sets it — see
// BuildIAMPolicy for why the bucket statements must stay unconditioned. It is
// kept so the simulator in iam_policy_test.go evaluates any future condition
// against the calls Sentra actually makes instead of ignoring it.
type IAMPolicyStatement struct {
	Sid       string         `json:"Sid"`
	Effect    string         `json:"Effect"`
	Action    []string       `json:"Action"`
	Resource  []string       `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

// BucketARN returns the S3 ARN for the bucket itself (no object suffix).
func BucketARN(bucket string) string {
	return "arn:aws:s3:::" + bucket
}

// ObjectARN returns the S3 ARN pattern for the objects Sentra reads and writes.
// An empty prefix widens the pattern to the whole bucket; a prefix scopes it so
// the granted identity only touches Sentra's keys.
func ObjectARN(bucket string, prefix string) string {
	if prefix == "" {
		return BucketARN(bucket) + "/*"
	}
	return BucketARN(bucket) + "/" + prefix + "*"
}

// WriteIAMPolicy encodes BuildIAMPolicy(bucket, prefix) as indented JSON. It is
// the single rendering path so the CLI command and any TUI reuse the exact
// same document.
func WriteIAMPolicy(w io.Writer, bucket string, prefix string) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(BuildIAMPolicy(bucket, prefix)); err != nil {
		return fmt.Errorf("encode IAM policy: %w", err)
	}
	return nil
}

// BuildIAMPolicy assembles the three-statement least-privilege policy: bucket
// controls used during setup and doctor, bucket listing, and object CRUD on
// the repo keys.
//
// The bucket-level statements are deliberately unconditioned. HeadBucket —
// the doctor's reachability probe and setup's bucket-exists check and waiter
// — authorizes as s3:ListBucket, and its request context (like
// GetBucketLocation's) carries no s3:prefix key; IAM evaluates a condition on
// an absent key as false, so a StringLike s3:prefix condition here denies
// Sentra's own probes under Sentra's own recommended policy. The trade-off is
// that the identity can list every key NAME in the bucket; object reads and
// writes stay scoped to the prefix by SentraRepositoryObjects.
func BuildIAMPolicy(bucket string, prefix string) IAMPolicyDocument {
	bucketResource := BucketARN(bucket)
	objectResource := ObjectARN(bucket, prefix)
	return IAMPolicyDocument{
		Version: "2012-10-17",
		Statement: []IAMPolicyStatement{
			{
				Sid:    "SentraSetupBucketControls",
				Effect: "Allow",
				// The encryption APIs authorize under the
				// *EncryptionConfiguration action names, not their API names —
				// s3:GetBucketEncryption does not exist as an IAM action.
				Action: []string{
					"s3:CreateBucket",
					"s3:GetBucketPublicAccessBlock",
					"s3:GetEncryptionConfiguration",
					"s3:PutBucketPublicAccessBlock",
					"s3:PutEncryptionConfiguration",
				},
				Resource: []string{bucketResource},
			},
			{
				Sid:    "SentraListBucket",
				Effect: "Allow",
				Action: []string{
					"s3:GetBucketLocation",
					"s3:ListBucket",
				},
				Resource: []string{bucketResource},
			},
			{
				Sid:    "SentraRepositoryObjects",
				Effect: "Allow",
				Action: []string{
					"s3:DeleteObject",
					"s3:GetObject",
					"s3:PutObject",
				},
				Resource: []string{objectResource},
			},
		},
	}
}
