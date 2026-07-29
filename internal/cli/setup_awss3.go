package cli

import (
	"context"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
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
