package cli

import (
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/markgustetic/sentra/internal/ui"
)

func printSetupSummary(
	out io.Writer,
	cfgPath string,
	plan *SetupPlan,
	awsAuthReport *AWSAuthReport,
	awsReport *AWSPrepareReport,
	initResult *setupInitResult,
) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Success.Bold(true).Render("Sentra setup complete"))
	fmt.Fprintln(out, ui.Subtle.Render("Configuration"))
	fmt.Fprintf(out, "  config:   %s\n", cfgPath)
	fmt.Fprintf(out, "  storage:  %s\n", setupBackendLabel(plan.Backend))
	fmt.Fprintf(out, "  bucket:   %s\n", plan.Config.Repo.S3.Bucket)
	if plan.Config.Repo.S3.Prefix != "" {
		fmt.Fprintf(out, "  prefix:   %s\n", plan.Config.Repo.S3.Prefix)
	}
	if plan.Config.Repo.S3.Region != "" {
		fmt.Fprintf(out, "  region:   %s\n", plan.Config.Repo.S3.Region)
	}
	if plan.Config.Repo.S3.Profile != "" {
		fmt.Fprintf(out, "  profile:  %s\n", plan.Config.Repo.S3.Profile)
	}
	if plan.Config.Repo.S3.EndpointURL != "" {
		fmt.Fprintf(out, "  endpoint: %s\n", plan.Config.Repo.S3.EndpointURL)
	}
	if awsAuthReport != nil {
		fmt.Fprintln(out, ui.Subtle.Render("AWS authentication"))
		if awsAuthReport.AWSCLIInstalled {
			fmt.Fprintf(out, "  aws auth: aws cli installed with %s\n", awsAuthReport.AWSCLIManager)
		}
		if awsAuthReport.LoginRan {
			fmt.Fprintln(out, "  aws auth: browser login completed")
		}
		if awsAuthReport.SSOConfigureRan {
			fmt.Fprintln(out, "  aws auth: sso profile configured")
		}
		if awsAuthReport.SSOLoginRan {
			fmt.Fprintln(out, "  aws auth: sso login completed")
		} else if awsAuthReport.IdentityVerified {
			fmt.Fprintln(out, "  aws auth: identity verified")
		}
	}
	if awsReport != nil {
		fmt.Fprintln(out, ui.Subtle.Render("AWS bucket"))
		switch {
		case awsReport.BucketCreated:
			fmt.Fprintln(out, "  aws:      bucket created")
		case awsReport.BucketExisted:
			fmt.Fprintln(out, "  aws:      bucket verified")
		default:
			fmt.Fprintln(out, "  aws:      bucket checked")
		}
		if awsReport.PublicAccessBlocked {
			fmt.Fprintln(out, "  aws:      public access blocked")
		}
		if awsReport.DefaultEncryptionEnabled {
			fmt.Fprintln(out, "  aws:      default encryption enabled")
		}
	}
	if initResult != nil {
		fmt.Fprintln(out, ui.Subtle.Render("Repository"))
		if initResult.AlreadyInitialized {
			fmt.Fprintln(out, "  repo:     already initialized")
		} else {
			fmt.Fprintf(out, "  repo id:  %s\n", initResult.RepoID)
		}
		if initResult.PassphraseSavedToKeyring {
			fmt.Fprintln(out, "  pass:     saved to OS keyring")
		}
	} else {
		fmt.Fprintln(out, ui.Subtle.Render("Next"))
		fmt.Fprintln(out, "  Run `sentra init` when you are ready to initialize the encrypted repository.")
	}
}

func printSetupApplyHeader(out io.Writer, cfgPath string, plan *SetupPlan) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Primary.Render("Applying Sentra setup"))
	fmt.Fprintf(out, "  config:  %s\n", cfgPath)
	fmt.Fprintf(out, "  storage: %s\n", setupBackendLabel(plan.Backend))
	if plan.InitRepo {
		fmt.Fprintln(out, "  repo:    initialize after config")
		if plan.SavePassphrase {
			fmt.Fprintln(out, "  pass:    save to OS keyring after setup prompt")
		} else {
			fmt.Fprintln(out, "  pass:    prompt or configured passphrase source")
		}
	} else {
		fmt.Fprintln(out, "  repo:    config only")
	}
	fmt.Fprintln(out)
}

func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

func printSetupRepairContinue(out io.Writer, plan *SetupPlan) {
	if !plan.PrepareAWS {
		printSetupOK(out, "Continuing with config-only setup")
		return
	}
	fmt.Fprintf(out, "%s Retrying AWS setup with %s\n", ui.Subtle.Render("..."), setupAWSAuthMethodLabel(plan.AWSAuthMethod))
}

func setupBackendLabel(backend SetupBackend) string {
	switch backend {
	case SetupBackendAWS:
		return "AWS S3"
	case SetupBackendS3Compatible:
		return "S3-compatible or existing bucket"
	default:
		return string(backend)
	}
}

func setupAWSAuthMethodLabel(method SetupAWSAuthMethod) string {
	switch method {
	case SetupAWSAuthLogin:
		return "browser login"
	case SetupAWSAuthSSO:
		return "IAM Identity Center / SSO"
	case SetupAWSAuthExisting:
		return "existing credentials"
	case SetupAWSAuthSkip:
		return "config only"
	default:
		return string(method)
	}
}

func setupAWSPreparedLabel(report *AWSPrepareReport) string {
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

func validateSetupBucketName(bucket string) error {
	bucket = strings.TrimSpace(bucket)
	if len(bucket) < 3 || len(bucket) > 63 {
		return fmt.Errorf("repo.s3.bucket %q is invalid: S3 bucket names must be 3-63 characters", bucket)
	}
	if net.ParseIP(bucket) != nil {
		return fmt.Errorf("repo.s3.bucket %q is invalid: S3 bucket names cannot be formatted as IP addresses", bucket)
	}
	if bucket[0] == '-' || bucket[0] == '.' || bucket[len(bucket)-1] == '-' || bucket[len(bucket)-1] == '.' {
		return fmt.Errorf("repo.s3.bucket %q is invalid: bucket names must start and end with a lowercase letter or number", bucket)
	}
	prevDot := false
	for _, r := range bucket {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("repo.s3.bucket %q is invalid: use lowercase letters, numbers, dots, and hyphens only", bucket)
		}
		if r == '.' {
			if prevDot {
				return fmt.Errorf("repo.s3.bucket %q is invalid: bucket names cannot contain adjacent dots", bucket)
			}
			prevDot = true
			continue
		}
		if prevDot && r == '-' {
			return fmt.Errorf("repo.s3.bucket %q is invalid: dots cannot sit next to hyphens", bucket)
		}
		prevDot = false
	}
	if strings.Contains(bucket, "-.") {
		return fmt.Errorf("repo.s3.bucket %q is invalid: dots cannot sit next to hyphens", bucket)
	}
	return nil
}
