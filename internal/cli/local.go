package cli

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/config"
)

// localConfigFileName is the dedicated config path `sentra local` launches
// against. It is deliberately NOT sentra.yaml so the dev-convenience flow never
// touches a user's real repository config; the first-run wizard writes here on
// completion.
const localConfigFileName = ".sentra-local.yaml"

// LocalDeps wires the side-effecting pieces of `sentra local`. UI is the same
// UIDeps the `ui` command uses; the command sets UI.SetupSeedConfig at run time
// so it must be passed in with SetupSeedConfig nil. EnsureMinIO is injected so
// tests exercise the command without touching docker or the network.
type LocalDeps struct {
	UI UIDeps

	// EnsureMinIO makes a local MinIO reachable before the UI launches: it
	// returns nil once MinIO answers, starts it (docker compose) if needed, and
	// returns a clear error when it can neither reach nor start it. Tests inject
	// a stub; production wires the docker/health probe in cmd/sentra.
	EnsureMinIO func(ctx context.Context) error
}

// NewLocal returns the cobra command for `sentra local`, a dev-convenience flow
// that boots a local MinIO, points Sentra at it, and opens the TUI with the
// first-run wizard pre-filled for MinIO.
//
// It never touches the real sentra.yaml: it launches against
// .sentra-local.yaml, seeds the wizard with the local MinIO S3 coordinates
// (endpoint http://localhost:9000, bucket sentra-test, region us-east-1), and
// exports the well-known minioadmin credentials — but only when the user has
// not already set AWS credentials, so a real environment is never clobbered.
func NewLocal(deps LocalDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "local",
		Short: "Run Sentra against a local MinIO for development",
		Long: "Start (or reuse) a local MinIO via docker compose, point Sentra at it, and\n" +
			"open the TUI with the first-run setup wizard pre-filled for MinIO.\n\n" +
			"This is a development convenience: it launches against .sentra-local.yaml\n" +
			"(never your real sentra.yaml) and, unless you have already set AWS\n" +
			"credentials, exports the default minioadmin credentials for the session.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLocal(cmd, deps)
		},
	}
	return cmd
}

// runLocal is the body of `sentra local`, pulled out for grep-ability and to
// keep the cobra closure shallow.
func runLocal(cmd *cobra.Command, deps LocalDeps) error {
	cmd.SilenceUsage = true

	if err := deps.EnsureMinIO(cmd.Context()); err != nil {
		return err
	}

	// Export the well-known MinIO credentials only when the user hasn't set
	// their own — the S3 client reads them via the AWS SDK's env provider. Never
	// clobber a real AWS credential a user may rely on.
	setEnvIfUnset("AWS_ACCESS_KEY_ID", "minioadmin")
	setEnvIfUnset("AWS_SECRET_ACCESS_KEY", "minioadmin")

	// Seed the first-run wizard with the local MinIO coordinates. These are
	// non-secret S3 coordinates only; the passphrase is collected by the wizard
	// and is never seeded. An endpoint_url + the exported credentials make
	// DefaultPlan infer the S3-compatible backend, so the wizard comes up with
	// no AWS provisioning steps.
	seed := &config.Config{}
	seed.Repo.S3.EndpointURL = "http://localhost:9000"
	seed.Repo.S3.Bucket = "sentra-test"
	seed.Repo.S3.Region = "us-east-1"

	ui := deps.UI
	ui.SetupSeedConfig = seed

	return runUI(cmd, ui, localConfigFileName)
}

// setEnvIfUnset sets key=value only when key is currently unset or empty,
// preserving any value the user already exported.
func setEnvIfUnset(key, value string) {
	if os.Getenv(key) == "" {
		_ = os.Setenv(key, value)
	}
}
