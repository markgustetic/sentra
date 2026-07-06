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
// omitted from the JSON when nil so a prefix-less policy stays clean.
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
// controls used during setup, list access scoped by prefix, and object CRUD on
// the repo keys. The prefix condition is only attached when a prefix is set.
func BuildIAMPolicy(bucket string, prefix string) IAMPolicyDocument {
	bucketResource := BucketARN(bucket)
	objectResource := ObjectARN(bucket, prefix)
	listStatement := IAMPolicyStatement{
		Sid:    "SentraListBucket",
		Effect: "Allow",
		Action: []string{
			"s3:GetBucketLocation",
			"s3:ListBucket",
		},
		Resource: []string{bucketResource},
	}
	if prefix != "" {
		listStatement.Condition = map[string]any{
			"StringLike": map[string]any{
				"s3:prefix": []string{prefix + "*"},
			},
		}
	}
	return IAMPolicyDocument{
		Version: "2012-10-17",
		Statement: []IAMPolicyStatement{
			{
				Sid:    "SentraSetupBucketControls",
				Effect: "Allow",
				Action: []string{
					"s3:CreateBucket",
					"s3:GetBucketEncryption",
					"s3:GetBucketPublicAccessBlock",
					"s3:PutBucketEncryption",
					"s3:PutBucketPublicAccessBlock",
				},
				Resource: []string{bucketResource},
			},
			listStatement,
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
