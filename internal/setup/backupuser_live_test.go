//go:build live

package setup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
)

// TestLiveProvisionBackupUser provisions against the real account named by
// SENTRA_LIVE_ADMIN_PROFILE (a session with IAM rights) into a throwaway
// credentials file, verifies the key with STS through that file, then deletes
// the key. Requires: SENTRA_LIVE_ADMIN_PROFILE, SENTRA_LIVE_BUCKET.
func TestLiveProvisionBackupUser(t *testing.T) {
	admin := os.Getenv("SENTRA_LIVE_ADMIN_PROFILE")
	bucket := os.Getenv("SENTRA_LIVE_BUCKET")
	if admin == "" || bucket == "" {
		t.Skip("set SENTRA_LIVE_ADMIN_PROFILE and SENTRA_LIVE_BUCKET to run")
	}
	credsPath := filepath.Join(t.TempDir(), "credentials")
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", credsPath)

	cfg := backupUserCfg()
	cfg.Repo.S3.Bucket = bucket
	cfg.Repo.S3.Profile = admin
	ctx := context.Background()

	report, err := DefaultProvisionBackupUser(ctx, cfg, BackupUserOptions{Profile: "sentra-live-test"})
	if err != nil {
		t.Fatalf("provision: %v", err)
	}
	t.Cleanup(func() {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithSharedConfigProfile(admin), awsconfig.WithRegion(cfg.Repo.S3.Region))
		if err != nil {
			t.Logf("cleanup: load admin config: %v", err)
			return
		}
		if _, err := iam.NewFromConfig(awsCfg).DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{
			UserName: aws.String(BackupUserName), AccessKeyId: aws.String(report.AccessKeyID),
		}); err != nil {
			t.Errorf("cleanup: DELETE KEY %s BY HAND: %v", report.AccessKeyID, err)
		}
	})
	if !report.UserExisted {
		t.Fatalf("expected the existing sentra-backup user to be reused: %+v", report)
	}
	if !report.PolicyAttached || report.PolicyName != BackupUserPolicyNameFor(bucket) {
		t.Fatalf("expected the bucket's managed policy to be attached: %+v", report)
	}

	// Verify through the file the provisioner wrote, exactly as the engine does.
	verify := *cfg
	verify.Repo.S3.Profile = report.Profile
	eng := NewEngine(DefaultEffects())
	if err := eng.verifyIdentityWithRetry(ctx, &verify); err != nil {
		t.Fatalf("new key never verified: %v", err)
	}
}
