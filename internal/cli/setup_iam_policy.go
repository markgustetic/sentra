package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
)

type setupIAMPolicyDocument struct {
	Version   string                    `json:"Version"`
	Statement []setupIAMPolicyStatement `json:"Statement"`
}

type setupIAMPolicyStatement struct {
	Sid       string         `json:"Sid"`
	Effect    string         `json:"Effect"`
	Action    []string       `json:"Action"`
	Resource  []string       `json:"Resource"`
	Condition map[string]any `json:"Condition,omitempty"`
}

func newSetupIAMPolicy(out io.Writer) *cobra.Command {
	var bucket string
	var prefix string
	cmd := &cobra.Command{
		Use:   "iam-policy",
		Short: "Print a least-privilege AWS IAM policy for Sentra",
		Long: "Print non-secret IAM JSON for the selected S3 bucket and prefix. " +
			"The policy covers setup checks plus normal backup, restore, check, sync, and prune operations.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if out == nil {
				out = cmd.OutOrStdout()
			}
			bucket = strings.TrimSpace(bucket)
			prefix = strings.TrimSpace(prefix)
			if bucket == "" {
				return fmt.Errorf("--bucket is required")
			}
			if err := validateSetupBucketName(bucket); err != nil {
				return err
			}
			return writeSetupIAMPolicy(out, bucket, prefix)
		},
	}
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&prefix, "prefix", "sentra/", "S3 key prefix Sentra will use")
	return cmd
}

func writeSetupIAMPolicy(out io.Writer, bucket string, prefix string) error {
	policy := buildSetupIAMPolicy(bucket, prefix)
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(policy); err != nil {
		return fmt.Errorf("encode IAM policy: %w", err)
	}
	return nil
}

func buildSetupIAMPolicy(bucket string, prefix string) setupIAMPolicyDocument {
	bucketResource := s3BucketARN(bucket)
	objectResource := s3ObjectARN(bucket, prefix)
	listStatement := setupIAMPolicyStatement{
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
	return setupIAMPolicyDocument{
		Version: "2012-10-17",
		Statement: []setupIAMPolicyStatement{
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
