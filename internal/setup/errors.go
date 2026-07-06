package setup

import (
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
)

// WrapAWSSSOFlowError annotates a failed `aws configure sso` / `aws sso login`
// with the profile it ran for and the recovery paths, preserving the cause.
func WrapAWSSSOFlowError(command string, profile string, err error) error {
	profile = strings.TrimSpace(profile)
	profileLabel := "the default profile"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	return fmt.Errorf("%s did not complete for %s. Rerun `sentra setup` and choose IAM Identity Center / SSO again, choose Browser login, or choose Existing credentials after running `%s`: %w", command, profileLabel, configureCommand, err)
}

// WrapAWSPrepareError classifies a bucket-prep failure. Missing-credential
// causes get method-specific guidance; everything else is a plain prepare wrap.
// The credential test is substring matching on raw AWS SDK text (not
// errors.Is) because the SDK does not expose typed sentinels for these.
func WrapAWSPrepareError(cfg config.Config, method AWSAuthMethod, err error) error {
	if !IsAWSMissingCredentialsError(err) {
		return fmt.Errorf("prepare AWS S3: %w", err)
	}

	profile := strings.TrimSpace(cfg.Repo.S3.Profile)
	profileLabel := "the default AWS credential chain"
	configureCommand := "aws configure"
	if profile != "" && profile != "default" {
		profileLabel = "AWS profile " + profile
		configureCommand = "aws configure --profile " + profile
	}
	switch method {
	case AWSAuthLogin:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not available for %s after browser login. Rerun `sentra setup` and choose Browser login again, or configure non-browser credentials with `%s`: %w", profileLabel, configureCommand, err)
	case AWSAuthSSO:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not available for %s after the SSO flow. Rerun `sentra setup` and choose IAM Identity Center / SSO again, or configure non-SSO credentials with `%s`: %w", profileLabel, configureCommand, err)
	default:
		return fmt.Errorf("prepare AWS S3: AWS credentials were not found for %s. Configure them with `%s`, export AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, use role credentials, or rerun `sentra setup` and choose Browser login if you want Sentra to open an AWS sign-in flow: %w", profileLabel, configureCommand, err)
	}
}

// WrapAWSLoginFlowError annotates a failed `aws login` with its profile and the
// recovery paths, preserving the cause.
func WrapAWSLoginFlowError(profile string, err error) error {
	profile = strings.TrimSpace(profile)
	if profile == "" {
		profile = "default"
	}
	return fmt.Errorf("aws login did not complete for profile %s. Rerun `sentra setup` and choose Browser login again, choose IAM Identity Center / SSO, or choose Existing credentials after configuring a profile manually: %w", profile, err)
}

// IsAWSMissingCredentialsError reports whether err's text matches the AWS SDK's
// missing-credential phrasings. It is deliberately substring-based: the SDK
// returns these as plain messages, not wrapped sentinels, so errors.Is cannot
// classify them.
func IsAWSMissingCredentialsError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"failed to refresh cached credentials",
		"no ec2 imds role found",
		"no valid credential",
		"no credential provider",
		"credential providers",
		"ec2imds",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

// ErrorAdvice returns operator-facing recovery hints for an AWS setup failure.
// It classifies err by substring (raw AWS error text) and adds cfg context when
// available. Returns nil for a nil error and a single generic line when nothing
// else matched.
func ErrorAdvice(err error, cfg config.Config) []string {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	var advice []string
	add := func(line string) {
		for _, existing := range advice {
			if existing == line {
				return
			}
		}
		advice = append(advice, line)
	}

	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	region := strings.TrimSpace(cfg.Repo.S3.Region)
	profile := strings.TrimSpace(cfg.Repo.S3.Profile)
	switch {
	case bucket != "" && region != "" && profile != "":
		add(fmt.Sprintf("Using bucket %q in region %q with AWS profile %q.", bucket, region, profile))
	case bucket != "" && region != "":
		add(fmt.Sprintf("Using bucket %q in region %q with the default AWS credential chain.", bucket, region))
	case bucket != "":
		add(fmt.Sprintf("Using bucket %q.", bucket))
	}

	switch {
	case IsAWSMissingCredentialsError(err):
		add("Credentials are still unavailable. Choose Browser login/SSO again, or configure a working profile with `aws configure --profile <profile>`.")
	case strings.Contains(msg, "accessdenied") || strings.Contains(msg, "access denied") || strings.Contains(msg, "status code: 403") || strings.Contains(msg, "forbidden"):
		if strings.Contains(msg, "head bucket") {
			add("S3 HeadBucket can return AccessDenied when the bucket is owned by another account or when your identity lacks s3:ListBucket.")
			add("If this is a new bucket, choose a globally unique name; generic names like `sentra-test` are often already taken.")
			add("If this is your existing bucket, grant the selected identity s3:ListBucket on the bucket ARN.")
		}
		add("Use `sentra setup iam-policy --bucket <bucket> --prefix <prefix>` or the setup policy option to print the required IAM policy.")
	case strings.Contains(msg, "bucketalreadyexists"):
		add("That bucket name is already owned by another AWS account. Pick a globally unique bucket name and rerun setup.")
	case strings.Contains(msg, "bucketalreadyownedbyyou"):
		add("That bucket already belongs to you. Rerun setup with create/verify enabled, or choose verify-only if you do not want creation attempts.")
	case strings.Contains(msg, "permanentredirect") || strings.Contains(msg, "authorizationheadermalformed") || strings.Contains(msg, "wrong region"):
		add("The bucket appears to be in a different region. Check the bucket region in AWS, then rerun setup with that region.")
	case strings.Contains(msg, "invalidbucketname") || strings.Contains(msg, "invalid bucket"):
		add("Use a DNS-compatible S3 bucket name: lowercase letters, numbers, hyphens, and dots; 3-63 characters.")
	case strings.Contains(msg, "does not exist") || strings.Contains(msg, "nosuchbucket"):
		add("The bucket was not found. Allow setup to create it, create it manually, or choose a different bucket name.")
	}

	if len(advice) == 0 {
		add("You can edit the region/profile, switch sign-in methods, write config only, or cancel and rerun setup after fixing AWS.")
	}
	return advice
}
