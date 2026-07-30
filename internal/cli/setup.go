package cli

import (
	"github.com/spf13/cobra"
)

// NewSetup returns the cobra command for `sentra setup`. It is a thin launcher
// for the TUI setup wizard: the wizard drives setup.Engine directly, so a
// second huh-based wizard here would be a duplicate of the same flow against
// the same engine.
//
// The command forces the wizard even when sentra.yaml already exists, which
// makes reconfiguring a normal supported flow. The wizard's review stage is
// the confirmation gate for the overwrite, so there is no --force flag.
func NewSetup(deps UIDeps) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Run the guided Sentra setup wizard",
		Long: "Open the guided terminal wizard for configuring Sentra. The wizard " +
			"can sign in with AWS CLI browser login, run AWS SSO profile setup when " +
			"selected, verify credentials, prepare an AWS S3 bucket, write " +
			"sentra.yaml, and initialize the encrypted repository in one flow. " +
			"Re-running it over an existing config reconfigures in place.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd, deps, cfgPath, true)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.AddCommand(newSetupIAMPolicy(deps.Stdout))
	return cmd
}
