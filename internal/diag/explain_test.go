package diag

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials/ssocreds"
	"github.com/aws/smithy-go"
)

// chain wraps cause the way a launch-path open surfaces it, so every case
// proves Explain sees through sentra's own %w nesting — not just a bare
// SDK error.
func chain(cause error) error {
	return fmt.Errorf("open repo: %w", fmt.Errorf("repo: get config: %w",
		fmt.Errorf("blobstore/s3: get %q: %w", "config", cause)))
}

// apiErr builds the shape the S3 client returns for a service-side code.
func apiErr(code string) error {
	return fmt.Errorf("operation error S3: GetObject: %w",
		&smithy.GenericAPIError{Code: code, Message: code})
}

// timeoutNetErr is a minimal net.Error whose only signal is Timeout() —
// the shape socket and HTTP deadlines surface as.
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "i/o timeout" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return false }

func TestExplain_KnownCauses(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantSummary string // substring of Explanation.Summary
		wantFix     string // substring of Explanation.Fix ("" = just non-empty)
	}{
		{
			// The exact failure that motivated Explain: an expired AWS
			// browser-login session. The SDK's logincreds provider returns
			// this as plain text (no error type survives), so the substring
			// match is the only available hook — this case pins it.
			name: "browser login session expired",
			err: chain(fmt.Errorf("operation error S3: GetObject, get identity: get credentials: failed to refresh cached credentials, create oauth2 token: %w",
				errors.New("login session has expired, please reauthenticate"))),
			wantSummary: "login session has expired",
			wantFix:     "sign in",
		},
		{
			name: "browser login password changed",
			err: chain(errors.New(
				"login session password has changed, please reauthenticate")),
			wantSummary: "password has changed",
			wantFix:     "sign in",
		},
		{
			name:        "sso token invalid (typed)",
			err:         chain(&ssocreds.InvalidTokenError{}),
			wantSummary: "SSO session",
			wantFix:     "aws sso login",
		},
		{
			name:        "sso refresh grant rejected",
			err:         chain(apiErr("InvalidGrantException")),
			wantSummary: "SSO session",
			wantFix:     "aws sso login",
		},
		{
			name:        "temporary credentials expired",
			err:         chain(apiErr("ExpiredToken")),
			wantSummary: "credentials have expired",
			wantFix:     "",
		},
		{
			name:        "temporary credentials expired (long code)",
			err:         chain(apiErr("ExpiredTokenException")),
			wantSummary: "credentials have expired",
			wantFix:     "",
		},
		{
			name:        "no credentials anywhere in the chain",
			err:         chain(errors.New("failed to retrieve credentials: no EC2 IMDS role found")),
			wantSummary: "no AWS credentials",
			wantFix:     "",
		},
		{
			name:        "access key rejected",
			err:         chain(apiErr("InvalidAccessKeyId")),
			wantSummary: "access keys",
			wantFix:     "",
		},
		{
			name:        "secret key wrong",
			err:         chain(apiErr("SignatureDoesNotMatch")),
			wantSummary: "access keys",
			wantFix:     "",
		},
		{
			name:        "access denied",
			err:         chain(apiErr("AccessDenied")),
			wantSummary: "denied access",
			wantFix:     "",
		},
		{
			name:        "bucket missing",
			err:         chain(apiErr("NoSuchBucket")),
			wantSummary: "bucket was not found",
			wantFix:     "",
		},
		{
			name:        "endpoint hostname unresolvable",
			err:         chain(&net.DNSError{Err: "no such host", Name: "s3.amazonaws.com", IsNotFound: true}),
			wantSummary: "could not be resolved",
			wantFix:     "",
		},
		{
			name:        "context deadline",
			err:         chain(context.DeadlineExceeded),
			wantSummary: "timed out",
			wantFix:     "",
		},
		{
			name:        "socket timeout",
			err:         chain(timeoutNetErr{}),
			wantSummary: "timed out",
			wantFix:     "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := Explain(tt.err)
			if !ok {
				t.Fatalf("Explain(%v) not recognized, want %q", tt.err, tt.wantSummary)
			}
			if !strings.Contains(got.Summary, tt.wantSummary) {
				t.Errorf("Summary %q missing %q", got.Summary, tt.wantSummary)
			}
			if got.Fix == "" {
				t.Errorf("Fix empty for %s — every known cause must say what to do", tt.name)
			}
			if tt.wantFix != "" && !strings.Contains(got.Fix, tt.wantFix) {
				t.Errorf("Fix %q missing %q", got.Fix, tt.wantFix)
			}
		})
	}

	t.Run("endpoint refused the connection", func(t *testing.T) {
		err := chain(&net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED})
		got, ok := Explain(err)
		if !ok {
			t.Fatalf("Explain(%v) not recognized", err)
		}
		if !strings.Contains(got.Summary, "refused the connection") {
			t.Errorf("Summary %q missing %q", got.Summary, "refused the connection")
		}
		if got.Fix == "" {
			t.Error("Fix empty — every known cause must say what to do")
		}
	})
}

// The contract's other half: anything Explain does not positively
// recognize falls through, so callers keep showing the raw chain instead
// of a wrong guess.
func TestExplain_UnknownFallsThrough(t *testing.T) {
	for _, err := range []error{
		nil,
		errors.New("open repo: something novel"),
		chain(errors.New("operation error S3: GetObject: api error Teapot: I'm a teapot")),
	} {
		if got, ok := Explain(err); ok {
			t.Errorf("Explain(%v) = %+v, want unrecognized", err, got)
		}
	}
}
