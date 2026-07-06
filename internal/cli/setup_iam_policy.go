package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/setup"
)

// setupIAMPolicyDocument keeps its historical cli name as an alias of the
// setup engine's exported policy type. internal/cli/setup_test.go (the
// 1863-line behavior-preservation oracle, unchanged since before this
// refactor) unmarshals the `setup iam-policy` command's JSON output into
// setupIAMPolicyDocument directly, so the alias — not a cli-local struct —
// keeps that assertion compiling and semantically identical while the real
// type now lives in internal/setup. (setupIAMPolicyStatement is not aliased
// here because nothing in cli references it yet; Part 4 of the TUI Phase 3
// plan adds it alongside the rest of the thin-driver rewrite.)
type setupIAMPolicyDocument = setup.IAMPolicyDocument

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
			return setup.WriteIAMPolicy(out, bucket, prefix)
		},
	}
	cmd.Flags().StringVar(&bucket, "bucket", "", "S3 bucket name")
	cmd.Flags().StringVar(&prefix, "prefix", "sentra/", "S3 key prefix Sentra will use")
	return cmd
}
