package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/setup"
)

// setupIAMPolicyDocument/Statement keep their historical cli names as aliases
// of the setup engine's exported policy types so the oracle can json.Unmarshal
// into them. internal/cli/setup_test.go (the 1863-line behavior-preservation
// oracle, unchanged) unmarshals the `setup iam-policy` command's JSON output
// into setupIAMPolicyDocument directly, so the alias — not a cli-local struct —
// keeps that assertion compiling and semantically identical while the real
// type now lives in internal/setup.
type setupIAMPolicyDocument = setup.IAMPolicyDocument

type setupIAMPolicyStatement = setup.IAMPolicyStatement

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
			out = cmdStdout(cmd, out)
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
	return setup.WriteIAMPolicy(out, bucket, prefix)
}

func buildSetupIAMPolicy(bucket string, prefix string) setupIAMPolicyDocument {
	return setup.BuildIAMPolicy(bucket, prefix)
}
