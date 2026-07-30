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

// AWSPreparedLabel is the one-line success label for the bucket-prep step.
func AWSPreparedLabel(report *AWSPrepareReport) string {
	switch {
	case report == nil:
		return "AWS S3 checked"
	case report.BucketCreated:
		return "AWS S3 bucket created"
	case report.BucketExisted:
		return "AWS S3 bucket verified"
	default:
		return "AWS S3 bucket checked"
	}
}

// SummaryLines returns the body content lines of the final setup summary,
// grouped by "Configuration", "AWS authentication", "AWS bucket", and either
// "Repository" or "Next". Section headers are emitted as plain strings so the
// caller can re-style the known headers; no ANSI escapes appear here.
func SummaryLines(cfgPath string, p Plan, auth *AWSAuthReport, prep *AWSPrepareReport, init *InitResult) []string {
	var lines []string
	add := func(s string) { lines = append(lines, s) }

	add("Configuration")
	add(fmt.Sprintf("  config:   %s", cfgPath))
	add(fmt.Sprintf("  storage:  %s", BackendLabel(p.Backend)))
	add(fmt.Sprintf("  bucket:   %s", p.Config.Repo.S3.Bucket))
	if p.Config.Repo.S3.Prefix != "" {
		add(fmt.Sprintf("  prefix:   %s", p.Config.Repo.S3.Prefix))
	}
	if p.Config.Repo.S3.Region != "" {
		add(fmt.Sprintf("  region:   %s", p.Config.Repo.S3.Region))
	}
	if p.Config.Repo.S3.Profile != "" {
		add(fmt.Sprintf("  profile:  %s", p.Config.Repo.S3.Profile))
	}
	if p.Config.Repo.S3.EndpointURL != "" {
		add(fmt.Sprintf("  endpoint: %s", p.Config.Repo.S3.EndpointURL))
	}

	if auth != nil {
		add("AWS authentication")
		if auth.AWSCLIInstalled {
			add(fmt.Sprintf("  aws auth: aws cli installed with %s", auth.AWSCLIManager))
		}
		if auth.LoginRan {
			add("  aws auth: browser login completed")
		}
		if auth.SSOConfigureRan {
			add("  aws auth: sso profile configured")
		}
		if auth.SSOLoginRan {
			add("  aws auth: sso login completed")
		} else if auth.IdentityVerified {
			add("  aws auth: identity verified")
		}
	}

	if prep != nil {
		add("AWS bucket")
		switch {
		case prep.BucketCreated:
			add("  aws:      bucket created")
		case prep.BucketExisted:
			add("  aws:      bucket verified")
		default:
			add("  aws:      bucket checked")
		}
		if prep.PublicAccessBlocked {
			add("  aws:      public access blocked")
		}
		if prep.DefaultEncryptionEnabled {
			add("  aws:      default encryption enabled")
		}
	}

	if init != nil {
		add("Repository")
		if init.AlreadyInitialized {
			add("  repo:     already initialized")
		} else {
			add(fmt.Sprintf("  repo id:  %s", init.RepoID))
		}
		if init.PassphraseSavedToKeyring {
			add("  pass:     saved to OS keyring")
		}
	} else {
		add("Next")
		add("  Run `sentra init` when you are ready to initialize the encrypted repository.")
	}
	return lines
}
