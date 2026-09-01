package setup

import (
	"fmt"
	"strings"
)

// ReviewText renders the non-secret setup plan shown before any AWS or repo
// side effects run. Behavior-preserving port of the deleted CLI wizard's
// review text; the trailing no-secrets assertion is load-bearing and must not
// be removed.
func ReviewText(cfgPath string, p Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Config: %s\n", cfgPath)
	fmt.Fprintf(&b, "Storage: %s\n", BackendLabel(p.Backend))
	fmt.Fprintf(&b, "Bucket: %s\n", emptyDash(p.Config.Repo.S3.Bucket))
	if p.Config.Repo.S3.Prefix != "" {
		fmt.Fprintf(&b, "Prefix: %s\n", p.Config.Repo.S3.Prefix)
	}
	if p.Config.Repo.S3.Region != "" {
		fmt.Fprintf(&b, "Region: %s\n", p.Config.Repo.S3.Region)
	}
	if p.Config.Repo.S3.Profile != "" {
		fmt.Fprintf(&b, "Profile: %s\n", p.Config.Repo.S3.Profile)
	}
	if p.Config.Repo.S3.EndpointURL != "" {
		fmt.Fprintf(&b, "Endpoint: %s\n", p.Config.Repo.S3.EndpointURL)
	}
	if p.PrepareAWS {
		fmt.Fprintf(&b, "AWS sign-in: %s\n", AWSAuthMethodLabel(p.AWSAuthMethod))
		fmt.Fprintf(&b, "Create missing bucket: %t\n", p.CreateBucket)
		fmt.Fprintf(&b, "Block public access: %t\n", p.BlockPublicAccess)
		fmt.Fprintf(&b, "Enable default encryption: %t\n", p.DefaultEncryption)
		if line := backupUserPlanLine(p); line != "" {
			fmt.Fprintf(&b, "%s\n", line)
		}
	} else {
		fmt.Fprintln(&b, "AWS setup: skipped")
	}
	if p.InitRepo {
		fmt.Fprintln(&b, "Repository: initialize after config")
		fmt.Fprintf(&b, "Passphrase: %s\n", passphrasePlanLine(p))
	} else {
		fmt.Fprintln(&b, "Repository: config only")
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "No passphrases, AWS credentials, salts, wrapped keys, or MAC material are written to the config.")
	return b.String()
}

// passphrasePlanLine describes where the repository passphrase comes from.
// When a non-interactive source already answered, the wizard never showed its
// passphrase stage, so this line is the only place the operator learns which
// secret the repository is about to be initialized under — naming it is what
// makes a mismatch visible now instead of at the first failed decrypt. It
// renders the source LABEL only; the secret never reaches this package's
// output.
func passphrasePlanLine(p Plan) string {
	switch {
	case p.PassphraseSource != "" && p.SavePassphrase:
		return "read from " + p.PassphraseSource + ", saved to OS keyring after repo initialization"
	case p.PassphraseSource != "":
		return "read from " + p.PassphraseSource
	case p.SavePassphrase:
		return "save to OS keyring after repo initialization"
	default:
		return "prompted or read from --passphrase-file or SENTRA_PASSPHRASE"
	}
}

func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// BackendLabel is the human-readable storage-backend name.
func BackendLabel(b Backend) string {
	switch b {
	case BackendAWS:
		return "AWS S3"
	case BackendS3Compatible:
		return "S3-compatible or existing bucket"
	default:
		return string(b)
	}
}

// AWSAuthMethodLabel is the human-readable sign-in method name.
func AWSAuthMethodLabel(m AWSAuthMethod) string {
	switch m {
	case AWSAuthLogin:
		return "browser login"
	case AWSAuthSSO:
		return "IAM Identity Center / SSO"
	case AWSAuthExisting:
		return "existing credentials"
	case AWSAuthSkip:
		return "config only"
	default:
		return string(m)
	}
}

// backupUserPlanLine says what the provisioning stage will do, in the same
// voice as the other plan lines. It names the profile (a section header,
// not a secret) so the operator learns where the key will land before it
// exists. Methods that never provision get no line at all — "skipped"
// would imply a choice they were never offered.
func backupUserPlanLine(p Plan) string {
	m := ResolveAWSAuthMethod(&p)
	if m != AWSAuthLogin && m != AWSAuthSSO {
		return ""
	}
	if !p.ProvisionBackupUser {
		if existing := strings.TrimSpace(p.ProvisionedBackupUserProfile); existing != "" {
			// A retry after a late failure: the user was created and verified
			// on the previous attempt. "skipped" would send the operator back to
			// switch it on again, straight into the profile-exists refusal.
			return fmt.Sprintf("Backup user: %s already created, keys in ~/.aws/credentials [%s]", BackupUserName, existing)
		}
		return "Backup user: skipped"
	}
	profile := strings.TrimSpace(p.BackupUserProfile)
	if profile == "" {
		profile = DefaultBackupUserProfile
	}
	return fmt.Sprintf("Backup user: create %s, keys → ~/.aws/credentials [%s]", BackupUserName, profile)
}
