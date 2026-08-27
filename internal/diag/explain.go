package diag

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"syscall"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

// Explanation is an operator-readable reading of a low-level failure:
// what went wrong in plain words, and what to do about it. It never
// replaces the raw error — callers keep the chain visible (muted, below)
// because that is what goes into a bug report.
type Explanation struct {
	Summary string
	Fix     string
}

// Explain maps known failure chains — expired AWS sessions, missing or
// rejected credentials, missing buckets, unreachable endpoints — to an
// Explanation. It returns ok=false for anything it does not positively
// recognize, so callers fall back to the raw error instead of a wrong
// guess.
//
// Matching is typed (errors.As / errors.Is) wherever the SDK gives us a
// type, and pinned substrings where it does not: the logincreds provider
// discards its typed AccessDeniedException and returns plain fmt.Errorf
// text ("login session has expired, please reauthenticate"), so for those
// causes the message text is the only hook. Each pinned string has a test
// so an SDK reword fails loudly instead of silently unmapping.
func Explain(err error) (Explanation, bool) {
	if err == nil {
		return Explanation{}, false
	}

	// AWS session/credential causes, most specific first.
	var ssoErr *ssocreds.InvalidTokenError
	if errors.As(err, &ssoErr) {
		return Explanation{
			Summary: "your AWS SSO session has expired or is invalid",
			Fix:     "run aws sso login, then retry",
		}, true
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "login session has expired") {
		return Explanation{
			Summary: "your AWS login session has expired",
			Fix:     "sign in to AWS again, then retry",
		}, true
	}
	if strings.Contains(msg, "login session password has changed") {
		return Explanation{
			Summary: "your AWS login password has changed",
			Fix:     "sign in to AWS again, then retry",
		}, true
	}

	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "InvalidGrantException", "UnauthorizedException":
			return Explanation{
				Summary: "your AWS SSO session has expired or is invalid",
				Fix:     "run aws sso login, then retry",
			}, true
		case "ExpiredToken", "ExpiredTokenException", "TokenRefreshRequired":
			return Explanation{
				Summary: "your AWS credentials have expired",
				Fix:     "sign in to AWS again, then retry",
			}, true
		case "InvalidAccessKeyId", "SignatureDoesNotMatch":
			return Explanation{
				Summary: "the storage provider rejected your access keys",
				Fix:     "check the access key and secret this repository is configured with",
			}, true
		case "AccessDenied", "AccessDeniedException":
			return Explanation{
				Summary: "the storage provider denied access",
				Fix:     "check this identity's permissions on the bucket",
			}, true
		case "NoSuchBucket":
			return Explanation{
				Summary: "the bucket was not found",
				Fix:     "check the bucket name in your config, or create the bucket",
			}, true
		}
	}

	// Network causes. DNS before the generic timeout check: a DNS
	// timeout satisfies both, and the resolution reading is the more
	// actionable one.
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return Explanation{
			Summary: "the storage endpoint's hostname could not be resolved",
			Fix:     "check your network connection and the endpoint URL",
		}, true
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return Explanation{
			Summary: "the storage endpoint refused the connection",
			Fix:     "check that the server is running and the endpoint URL is right",
		}, true
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, os.ErrDeadlineExceeded) ||
		(errors.As(err, &netErr) && netErr.Timeout()) {
		return Explanation{
			Summary: "timed out talking to the storage provider",
			Fix:     "check your network connection and retry",
		}, true
	}

	// Credential chain exhausted without producing credentials at all.
	// Same pinned-substring situation as the login errors above; the
	// needles mirror setup.IsAWSMissingCredentialsError's.
	for _, needle := range []string{
		"no ec2 imds role found",
		"no valid credential",
		"no credential provider",
	} {
		if strings.Contains(msg, needle) {
			return Explanation{
				Summary: "no AWS credentials were found",
				Fix:     "configure credentials (aws configure or aws sso login), then retry",
			}, true
		}
	}

	return Explanation{}, false
}
