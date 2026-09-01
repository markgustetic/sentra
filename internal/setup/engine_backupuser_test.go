package setup

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// newTestEngine builds an Engine whose sleeps are instant, so the retry
// loop's virtual clock runs without wall time.
func newTestEngine(eff Effects) *Engine {
	return &Engine{eff: eff, sleep: func(context.Context, time.Duration) error { return nil }}
}

func loginPlanWithBackupUser() Plan {
	p := awsPlan()
	p.AWSAuthMethod = AWSAuthLogin
	p.ProvisionBackupUser = true
	p.BackupUserProfile = "sentra"
	return p
}

func TestEngineBackupUser_GatedOffForExistingCredentials(t *testing.T) {
	called := false
	eff := fakeEffects{
		provisionBackupUser: func(context.Context, *config.Config, BackupUserOptions) (BackupUserReport, error) {
			called = true
			return BackupUserReport{}, nil
		},
	}
	p := loginPlanWithBackupUser()
	p.AWSAuthMethod = AWSAuthExisting
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if called || prep.BackupUser != nil {
		t.Fatalf("existing-credentials setup must never provision (called=%v, report=%+v)", called, prep.BackupUser)
	}
}

func TestEngineBackupUser_SuccessSwitchesProfileAfterVerify(t *testing.T) {
	var verifiedProfiles []string
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			verifiedProfiles = append(verifiedProfiles, cfg.Repo.S3.Profile)
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{UserName: BackupUserName, Profile: o.Profile, AccessKeyID: "AKIAX", CredentialsPath: "/c"}, nil
		},
	}
	p := loginPlanWithBackupUser()
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if prep.BackupUser == nil || !prep.BackupUser.ProfileSwitched || prep.BackupUser.Warning != "" {
		t.Fatalf("report = %+v, want switched with no warning", prep.BackupUser)
	}
	if p.Config.Repo.S3.Profile != "sentra" {
		t.Fatalf("plan profile = %q, want sentra", p.Config.Repo.S3.Profile)
	}
	// The verification after provisioning ran through the NEW profile.
	if last := verifiedProfiles[len(verifiedProfiles)-1]; last != "sentra" {
		t.Fatalf("last identity check used profile %q, want sentra (all: %v)", last, verifiedProfiles)
	}
}

func TestEngineBackupUser_BlankProfileDefaults(t *testing.T) {
	var gotProfile string
	eff := fakeEffects{
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			gotProfile = o.Profile
			return BackupUserReport{Profile: o.Profile}, nil
		},
	}
	p := loginPlanWithBackupUser()
	p.BackupUserProfile = "  "
	if _, _, err := newTestEngine(eff).PrepareAWS(context.Background(), &p); err != nil {
		t.Fatal(err)
	}
	if gotProfile != DefaultBackupUserProfile {
		t.Fatalf("profile passed = %q, want %q", gotProfile, DefaultBackupUserProfile)
	}
}

// Fresh IAM keys take seconds to propagate: two failures then success must
// still end in a switched profile.
func TestEngineBackupUser_VerifyRetriesThenSucceeds(t *testing.T) {
	calls := 0
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			if cfg.Repo.S3.Profile != "sentra" {
				return nil // the pre-provision session check
			}
			calls++
			if calls < 3 {
				return errors.New("InvalidClientTokenId")
			}
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{Profile: o.Profile}, nil
		},
	}
	p := loginPlanWithBackupUser()
	_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 3 || !prep.BackupUser.ProfileSwitched {
		t.Fatalf("calls = %d, switched = %v; want 3 and true", calls, prep.BackupUser.ProfileSwitched)
	}
}

// The dangerous condition: verification never succeeds. The profile must
// NOT switch (InitRepo would run on credentials that do not work), setup
// must still succeed, and the warning must say how to switch later.
func TestEngineBackupUser_VerifyTimeoutKeepsSessionProfile(t *testing.T) {
	var slept []time.Duration
	eff := fakeEffects{
		checkIdentity: func(_ context.Context, cfg *config.Config) error {
			if cfg.Repo.S3.Profile == "sentra" {
				return errors.New("InvalidClientTokenId")
			}
			return nil
		},
		provisionBackupUser: func(_ context.Context, _ *config.Config, o BackupUserOptions) (BackupUserReport, error) {
			return BackupUserReport{UserName: BackupUserName, Profile: o.Profile, CredentialsPath: "/c"}, nil
		},
	}
	eng := &Engine{eff: eff, sleep: func(_ context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}}
	p := loginPlanWithBackupUser()
	p.Config.Repo.S3.Profile = "login-session"
	_, prep, err := eng.PrepareAWS(context.Background(), &p)
	if err != nil {
		t.Fatalf("timeout must not fail setup: %v", err)
	}
	if p.Config.Repo.S3.Profile != "login-session" {
		t.Fatalf("profile switched despite unverified key: %q", p.Config.Repo.S3.Profile)
	}
	if prep.BackupUser.ProfileSwitched || prep.BackupUser.Warning == "" {
		t.Fatalf("report = %+v, want unswitched with warning", prep.BackupUser)
	}
	if !strings.Contains(prep.BackupUser.Warning, "repo.s3.profile") {
		t.Fatalf("warning must tell the operator how to switch later: %q", prep.BackupUser.Warning)
	}
	// Backoff 1,2,4,8,8 = 23s; a sixth 8s sleep would exceed 30s, so five sleeps.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second}
	if len(slept) != len(want) {
		t.Fatalf("sleeps = %v, want %v", slept, want)
	}
	for i := range want {
		if slept[i] != want[i] {
			t.Fatalf("sleeps = %v, want %v", slept, want)
		}
	}
}

func TestEngineBackupUser_ProvisionFailureWarnsAndContinues(t *testing.T) {
	tests := []struct {
		name string
		err  error
		// report is what the fake Effects returns alongside err — it must
		// mirror provisionBackupUser's real contract (Task 3): AccessKeyID is
		// non-empty if and only if a key was minted before the failure. That
		// is the signal backupUserWarning uses to tell "the key was deleted
		// again" (a mint-then-write-failure, cleaned up) apart from a
		// pre-mint refusal, so only the case that actually minted a key
		// carries an AccessKeyID here.
		report BackupUserReport
		want   string // substring of the warning
	}{
		{"access denied", &BackupUserError{Step: "iam:CreateUser", AccessDenied: true, Err: errors.New("x")}, BackupUserReport{UserName: BackupUserName}, "iam:CreateUser"},
		{"key limit", &BackupUserError{Step: "iam:CreateAccessKey", KeyLimit: true, Err: errors.New("x")}, BackupUserReport{UserName: BackupUserName}, "two access keys"},
		{"profile taken", &BackupUserError{Step: "credentials", Err: ErrCredentialsProfileExists}, BackupUserReport{UserName: BackupUserName}, "another profile name"},
		{"orphaned key", &BackupUserError{Step: "credentials", KeyOrphaned: "AKIAORPHAN", Err: errors.New("disk full")}, BackupUserReport{UserName: BackupUserName, AccessKeyID: "AKIAORPHAN"}, "AKIAORPHAN"},
		{"write failed, cleaned up", &BackupUserError{Step: "credentials", Err: errors.New("disk full")}, BackupUserReport{UserName: BackupUserName, AccessKeyID: "AKIAX"}, "deleted again"},
		{"unclassified", errors.New("boom"), BackupUserReport{UserName: BackupUserName}, "boom"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			eff := fakeEffects{
				provisionBackupUser: func(context.Context, *config.Config, BackupUserOptions) (BackupUserReport, error) {
					return tc.report, tc.err
				},
			}
			p := loginPlanWithBackupUser()
			p.Config.Repo.S3.Profile = "login-session"
			_, prep, err := newTestEngine(eff).PrepareAWS(context.Background(), &p)
			if err != nil {
				t.Fatalf("provisioning failure must not fail setup: %v", err)
			}
			if prep.BackupUser == nil || prep.BackupUser.ProfileSwitched {
				t.Fatalf("report = %+v", prep.BackupUser)
			}
			if !strings.Contains(prep.BackupUser.Warning, tc.want) {
				t.Fatalf("warning %q lacks %q", prep.BackupUser.Warning, tc.want)
			}
			if !strings.Contains(prep.BackupUser.Warning, "expire") {
				t.Fatalf("every warning must say the session credentials expire: %q", prep.BackupUser.Warning)
			}
			if p.Config.Repo.S3.Profile != "login-session" {
				t.Fatalf("profile must be untouched on failure, got %q", p.Config.Repo.S3.Profile)
			}
		})
	}
}
