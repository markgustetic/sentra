package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/setup"
)

// AWSInspectReport is an alias for diag.AWSReport, preserved so existing
// DoctorDeps callers and tests keep compiling after the read-only AWS
// diagnostics moved to internal/diag.
type AWSInspectReport = diag.AWSReport

// DefaultAWSCheckSDKIdentity verifies credentials through the AWS SDK
// credential chain. Thin wrapper over diag.CheckSDKIdentity so the
// doctor's nil-fallback and setup's identity checker keep their names.
func DefaultAWSCheckSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return diag.CheckSDKIdentity(ctx, cfg)
}

// DefaultAWSInspect performs the read-only AWS checks for `sentra doctor`.
// Thin wrapper over diag.Inspect.
func DefaultAWSInspect(ctx context.Context, cfg *config.Config) (AWSInspectReport, error) {
	return diag.Inspect(ctx, cfg)
}

// DefaultAWSPrepare performs the deterministic AWS S3 setup work chosen
// in the wizard. It intentionally does not create or manage IAM users.
// Thin alias over setup.DefaultAWSPrepare so callers referencing the cli
// name (SetupDeps.PrepareAWS default, oracle tests) keep compiling.
func DefaultAWSPrepare(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	return setup.DefaultAWSPrepare(ctx, cfg, opts)
}
