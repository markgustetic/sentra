package setup

import (
	"context"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// Effects is the side-effecting seam of the setup engine. Its method set
// mirrors the func fields of the former cli.SetupDeps
// (internal/cli/setup.go:80-97) so the cli driver and the TUI wizard can
// share one sequencing engine. Tests inject a fake Effects; production uses
// DefaultEffects.
type Effects interface {
	// EnsureAWSCLI verifies the AWS CLI is installed, optionally installing
	// it via the confirmed package-manager plan (brew). confirm is nil in
	// the TUI, which handles a missing CLI with an ErrorAdvice modal.
	EnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error)
	AWSLogin(ctx context.Context, profile string, region string) error
	CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error)
	AWSConfigureSSO(ctx context.Context, profile string) error
	AWSSSOLogin(ctx context.Context, profile string) error
	// CheckAWSSDKIdentity verifies credentials through the SDK credential
	// chain; delegates to diag.CheckSDKIdentity.
	CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error
	// PrepareAWS performs the deterministic bucket-side setup work.
	PrepareAWS(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error)
	NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error)
	// SavePassphrase persists the passphrase to the OS keyring. The engine
	// only ever calls this AFTER repo init or a verified repo.Open.
	SavePassphrase(cfg *config.Config, passphrase []byte) error
}

// defaultEffects is the production Effects. Each method delegates to the
// Default* driver already moved into package setup (awscli.go); CheckAWSSDKIdentity
// delegates to diag.CheckSDKIdentity and PrepareAWS to DefaultAWSPrepare.
type defaultEffects struct{}

// DefaultEffects returns the production side-effecting seam.
func DefaultEffects() Effects { return defaultEffects{} }

func (defaultEffects) EnsureAWSCLI(ctx context.Context, confirm AWSCLIInstallConfirm) (AWSCLIInstallReport, error) {
	return DefaultEnsureAWSCLI(ctx, confirm)
}

func (defaultEffects) AWSLogin(ctx context.Context, profile string, region string) error {
	return DefaultAWSLogin(ctx, profile, region)
}

func (defaultEffects) CheckAWSSSOConfigured(ctx context.Context, profile string) (bool, error) {
	return DefaultAWSSSOConfigured(ctx, profile)
}

func (defaultEffects) AWSConfigureSSO(ctx context.Context, profile string) error {
	return DefaultAWSConfigureSSO(ctx, profile)
}

func (defaultEffects) AWSSSOLogin(ctx context.Context, profile string) error {
	return DefaultAWSSSOLogin(ctx, profile)
}

func (defaultEffects) CheckAWSSDKIdentity(ctx context.Context, cfg *config.Config) error {
	return diag.CheckSDKIdentity(ctx, cfg)
}

func (defaultEffects) PrepareAWS(ctx context.Context, cfg *config.Config, opts AWSPrepareOptions) (AWSPrepareReport, error) {
	return DefaultAWSPrepare(ctx, cfg, opts)
}

func (defaultEffects) NewStore(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	return blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:      cfg.Repo.S3.Bucket,
		Prefix:      cfg.Repo.S3.Prefix,
		Region:      cfg.Repo.S3.Region,
		Profile:     cfg.Repo.S3.Profile,
		EndpointURL: cfg.Repo.S3.EndpointURL,
	})
}

func (defaultEffects) SavePassphrase(cfg *config.Config, passphrase []byte) error {
	return config.StoreKeyringPassphrase(config.KeyringOptionsForConfig(cfg), passphrase)
}
