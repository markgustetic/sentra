package setup

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

const (
	// backupUserVerifyTimeout bounds the wait for a freshly minted IAM key
	// to become usable. Propagation is usually a few seconds; 30s is the
	// point past which the operator is better served by a warning.
	backupUserVerifyTimeout = 30 * time.Second
	// backupUserVerifyMaxBackoff caps the doubling: 1s, 2s, 4s, 8s, 8s, …
	backupUserVerifyMaxBackoff = 8 * time.Second
)

// provisionBackupUser is the post-bucket-prep stage of PrepareAWS: create
// the scoped user through the Effects seam, then switch the plan's profile
// to it — but ONLY after the new identity verifies. Until then InitRepo
// would run on a key AWS has not finished propagating, so an unverified
// switch fails toward "setup works right now": the session profile stays.
//
// Never returns an error. Provisioning is hardening, and a working setup on
// session credentials beats no setup; every failure becomes Warning.
func (e *Engine) provisionBackupUser(ctx context.Context, p *Plan) *BackupUserReport {
	profile := strings.TrimSpace(p.BackupUserProfile)
	if profile == "" {
		profile = DefaultBackupUserProfile
	}
	report, err := e.eff.ProvisionBackupUser(ctx, &p.Config, BackupUserOptions{Profile: profile})
	if err != nil {
		// report.AccessKeyID is non-empty iff a key was minted before the
		// failure (provisionBackupUser's contract, internal/setup/backupuser.go)
		// — that is what tells backupUserWarning whether "the key was
		// deleted again" is true of this failure.
		report.Warning = backupUserWarning(err, report.AccessKeyID != "")
		return &report
	}

	previous := p.Config.Repo.S3.Profile
	p.Config.Repo.S3.Profile = report.Profile
	if err := e.verifyIdentityWithRetry(ctx, &p.Config); err != nil {
		p.Config.Repo.S3.Profile = previous
		report.Warning = fmt.Sprintf(
			"backup user %s was created and its key saved to profile %q in %s, but the new credentials did not verify within %s (%v). "+
				"Setup continues on the signed-in session, which expires. Once the key is active, set repo.s3.profile: %q in sentra.yaml.",
			report.UserName, report.Profile, report.CredentialsPath, backupUserVerifyTimeout, err, report.Profile)
		return &report
	}
	report.ProfileSwitched = true
	return &report
}

// verifyIdentityWithRetry runs CheckAWSSDKIdentity until it passes or the
// backoff schedule would exceed backupUserVerifyTimeout of VIRTUAL time —
// summed sleep requests, not wall clock — so the schedule is deterministic
// under an instant test sleep. Any error retries: the SDK reports a
// not-yet-propagated key several different ways.
func (e *Engine) verifyIdentityWithRetry(ctx context.Context, cfg *config.Config) error {
	var elapsed time.Duration
	backoff := time.Second
	for {
		err := e.eff.CheckAWSSDKIdentity(ctx, cfg)
		if err == nil {
			return nil
		}
		if elapsed+backoff > backupUserVerifyTimeout {
			return err
		}
		if serr := e.sleep(ctx, backoff); serr != nil {
			return serr
		}
		elapsed += backoff
		if backoff < backupUserVerifyMaxBackoff {
			backoff *= 2
		}
	}
}

// backupUserWarning renders a provisioning failure as operator guidance.
// Every message ends by naming the consequence — the session credentials
// expire — because that is the fact the step existed to prevent.
//
// minted discriminates the generic "credentials" step failure: a pre-check
// refusal (profile taken, or named "default") never touched IAM, so nothing
// was deleted; a post-mint write failure did mint a key and then deleted it
// again as cleanup. The caller passes report.AccessKeyID != "" — the mint
// contract from provisionBackupUser (internal/setup/backupuser.go): that
// field is set the moment CreateAccessKey succeeds, before the write that
// can fail, so its presence is exactly "a key was minted".
func backupUserWarning(err error, minted bool) string {
	const tail = " Setup continues on the signed-in session, which expires; see docs/QUICKSTART.md to create the backup user later."
	var perr *BackupUserError
	if !errors.As(err, &perr) {
		return "backup user not created: " + err.Error() + "." + tail
	}
	switch {
	case perr.AccessDenied:
		return fmt.Sprintf("backup user not created: the signed-in identity is not allowed to perform %s. Ask an administrator for that permission, or create the user by hand with `sentra setup iam-policy`.", perr.Step) + tail
	case perr.KeyLimit:
		return fmt.Sprintf("backup user %s already has two access keys; remove one in IAM and rerun setup.", BackupUserName) + tail
	case perr.KeyOrphaned != "":
		return fmt.Sprintf("backup user key %s was created but could not be saved (%v), and deleting it failed — delete that key in IAM.", perr.KeyOrphaned, perr.Err) + tail
	case perr.Step == "credentials" && errors.Is(perr.Err, ErrCredentialsProfileExists):
		return fmt.Sprintf("backup user key not saved: %v — choose another profile name and rerun setup.", perr.Err) + tail
	case perr.Step == "credentials" && errors.Is(perr.Err, ErrBackupUserProfileDefault):
		return fmt.Sprintf("backup user key not saved: %v — choose another profile name and rerun setup.", perr.Err) + tail
	case perr.Step == "credentials" && minted:
		return fmt.Sprintf("backup user key could not be saved (%v); the key was deleted again.", perr.Err) + tail
	case perr.Step == "credentials":
		return fmt.Sprintf("backup user key not saved: %v", perr.Err) + tail
	default:
		return fmt.Sprintf("backup user not created: %s failed: %v.", perr.Step, perr.Err) + tail
	}
}
