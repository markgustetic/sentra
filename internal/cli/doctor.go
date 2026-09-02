package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// ErrDoctorFailed is returned after a doctor report is written when one or
// more required checks failed.
var ErrDoctorFailed = errors.New("sentra doctor failed")

// DoctorDeps wires read-only diagnostics for `sentra doctor`.
type DoctorDeps struct {
	CheckAWSSDKIdentity  func(ctx context.Context, cfg *config.Config) error
	InspectAWS           func(ctx context.Context, cfg *config.Config) (AWSInspectReport, error)
	NewStore             func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)
	Stdout               io.Writer
}

// NewDoctor returns the read-only setup and repository diagnostic command.
func NewDoctor(deps DoctorDeps) *cobra.Command {
	var cfgPath string
	var skipRepo bool
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check Sentra setup without changing anything",
		Long: "Validate sentra.yaml, verify AWS identity and bucket access for AWS S3, " +
			"inspect bucket public-access and encryption settings, and check the encrypted repository when possible.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd, deps, cfgPath, skipRepo, asJSON)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	cmd.Flags().BoolVar(&skipRepo, "skip-repo", false,
		"skip encrypted repository open/check")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit a summary schema {status, issues} instead of probe prose")
	return cmd
}

func runDoctor(cmd *cobra.Command, deps DoctorDeps, cfgPath string, skipRepo, asJSON bool) error {
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}
	stdout := cmdStdout(cmd, deps.Stdout)
	out := stdout
	if asJSON {
		// Probe prose would corrupt the JSON document; the summary
		// schema carries the outcome and the exit code carries failure.
		out = io.Discard
	}
	fmt.Fprintln(out, ui.Primary.Render("Sentra doctor"))

	failures := 0
	cfg, err := config.Load(cfgPath)
	if err != nil {
		printSetupFail(out, "Config load failed")
		return fmt.Errorf("load config: %w", err)
	}
	// Two possible config locations exist since discovery; naming the
	// one actually loaded is doctor's answer to "which config am I on?".
	absCfg := cfgPath
	if p, err := filepath.Abs(cfgPath); err == nil {
		absCfg = p
	}
	printSetupOK(out, fmt.Sprintf("Config loaded (%s)", absCfg))

	bucketOK := false
	if cfg.Repo.S3.Bucket == "" {
		printSetupFail(out, "repo.s3.bucket is missing")
		failures++
	} else if err := validateSetupBucketName(cfg.Repo.S3.Bucket); err != nil {
		printSetupFail(out, err.Error())
		failures++
	} else {
		bucketOK = true
		printSetupOK(out, "Bucket name valid")
	}

	if bucketOK && cfg.Repo.S3.EndpointURL == "" {
		if cfg.Repo.S3.Region == "" {
			printSetupWarn(out, "AWS region not set; relying on AWS SDK defaults")
		}
		failures += runDoctorAWS(cmd.Context(), deps, cfg, out)
	} else if bucketOK && cfg.Repo.S3.EndpointURL != "" {
		printSetupOK(out, "S3-compatible endpoint configured")
	}

	if skipRepo {
		printSetupWarn(out, "Repository check skipped")
	} else {
		failures += runDoctorRepo(cmd.Context(), deps, cfg, out)
	}

	if asJSON {
		status := "healthy"
		if failures > 0 {
			status = "failed"
		}
		if err := encodeJSON(stdout, doctorJSONReport{Status: status, Issues: failures}); err != nil {
			return err
		}
		if failures > 0 {
			return fmt.Errorf("%w: %d issue(s)", ErrDoctorFailed, failures)
		}
		return nil
	}
	if failures > 0 {
		fmt.Fprintf(out, "%s Doctor found %d issue(s)\n", ui.Danger.Render("error"), failures)
		return fmt.Errorf("%w: %d issue(s)", ErrDoctorFailed, failures)
	}
	fmt.Fprintln(out, ui.Success.Render("Doctor: healthy"))
	return nil
}

// doctorJSONReport is the summary schema for `doctor --json`. Probe
// detail stays on the text surface; automation needs the verdict.
type doctorJSONReport struct {
	Status string `json:"status"`
	Issues int    `json:"issues"`
}

func runDoctorAWS(ctx context.Context, deps DoctorDeps, cfg *config.Config, out io.Writer) int {
	failures := 0
	checkIdentity := deps.CheckAWSSDKIdentity
	if checkIdentity == nil {
		checkIdentity = DefaultAWSCheckSDKIdentity
	}
	if err := runSetupProgress(out, "Checking AWS identity", "AWS identity verified", func() error {
		return checkIdentity(ctx, cfg)
	}); err != nil {
		printSetupFail(out, err.Error())
		failures++
		return failures
	}

	inspect := deps.InspectAWS
	if inspect == nil {
		inspect = DefaultAWSInspect
	}
	var report AWSInspectReport
	if err := runSetupProgress(out, "Inspecting AWS S3 bucket", "AWS S3 bucket inspected", func() error {
		var inspectErr error
		report, inspectErr = inspect(ctx, cfg)
		return inspectErr
	}); err != nil {
		printSetupFail(out, err.Error())
		failures++
		return failures
	}
	if report.BucketAccessible {
		printSetupOK(out, "Bucket is accessible")
	}
	if report.PublicAccessReadable && report.PublicAccessBlocked {
		printSetupOK(out, "Bucket public access is blocked")
	} else if report.PublicAccessReadable {
		printSetupWarn(out, "Bucket public access block is not fully enabled")
	}
	if report.DefaultEncryptionReadable && report.DefaultEncryptionEnabled {
		printSetupOK(out, "Bucket default encryption is enabled")
	} else if report.DefaultEncryptionReadable {
		printSetupWarn(out, "Bucket default encryption is not enabled")
	}
	return failures
}

func runDoctorRepo(ctx context.Context, deps DoctorDeps, cfg *config.Config, out io.Writer) int {
	if deps.NewStore == nil {
		printSetupWarn(out, "Repository check skipped: store factory is not configured")
		return 0
	}
	if deps.PassphraseWithConfig == nil {
		printSetupWarn(out, "Repository check skipped: passphrase resolver is not configured")
		return 0
	}
	store, err := deps.NewStore(ctx, cfg)
	if err != nil {
		printSetupFail(out, "Open blobstore failed: "+err.Error())
		return 1
	}
	pass, err := deps.PassphraseWithConfig(cfg)
	if err != nil {
		printSetupWarn(out, "Repository check skipped: passphrase unavailable")
		return 0
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Open(ctx, store, pass)
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			printSetupWarn(out, "Repository is not initialized yet")
			return 0
		}
		printSetupFail(out, "Open repository failed: "+err.Error())
		return 1
	}
	defer r.Close()

	report, err := r.Check(ctx, repo.CheckOptions{StaleLockAfter: 24 * time.Hour})
	if err != nil {
		printSetupFail(out, "Repository check failed: "+err.Error())
		return 1
	}
	if !report.Healthy() {
		printSetupFail(out, "Repository integrity check failed")
		return 1
	}
	printSetupOK(out, "Repository check healthy")
	return 0
}

func printSetupWarn(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Warn.Render("warn"), label)
}

func printSetupFail(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Danger.Render("error"), label)
}
