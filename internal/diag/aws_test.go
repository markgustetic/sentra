package diag

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/smithy-go"

	"github.com/markgustetic/sentra/internal/config"
)

// awsEnv points the SDK's default chain at a hermetic fake: static
// credentials, no shared config files, and every service call routed to
// the given test server (AWS_ENDPOINT_URL is honored by the config
// loader). Nothing here can reach real AWS.
func awsEnv(t *testing.T, endpoint string) *config.Config {
	t.Helper()
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")
	t.Setenv("AWS_SESSION_TOKEN", "")
	t.Setenv("AWS_CONFIG_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_SHARED_CREDENTIALS_FILE", filepath.Join(t.TempDir(), "none"))
	t.Setenv("AWS_ENDPOINT_URL", endpoint)
	t.Setenv("AWS_EC2_METADATA_DISABLED", "true")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "probe-bucket"
	cfg.Repo.S3.Region = "us-east-1"
	return &cfg
}

func s3Error(w http.ResponseWriter, status int, code string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `<?xml version="1.0"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, code)
}

func TestCheckSDKIdentity_Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/test</Arn><Account>123456789012</Account><UserId>AIDATEST</UserId></GetCallerIdentityResult>
  <ResponseMetadata><RequestId>req-1</RequestId></ResponseMetadata>
</GetCallerIdentityResponse>`)
	}))
	defer srv.Close()
	if err := CheckSDKIdentity(context.Background(), awsEnv(t, srv.URL)); err != nil {
		t.Fatalf("CheckSDKIdentity: %v", err)
	}
}

func TestCheckSDKIdentity_WrapsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s3Error(w, http.StatusForbidden, "InvalidClientTokenId")
	}))
	defer srv.Close()
	err := CheckSDKIdentity(context.Background(), awsEnv(t, srv.URL))
	if err == nil {
		t.Fatal("expected an error from a 403 STS response")
	}
	if !strings.Contains(err.Error(), "verify AWS identity") {
		t.Fatalf("error not wrapped with the probe's context: %v", err)
	}
}

// Inspect's happy path: bucket reachable, public access fully blocked,
// default encryption on — all three read flags true.
func TestInspect_ReportsBucketState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case q.Has("publicAccessBlock"):
			fmt.Fprint(w, `<PublicAccessBlockConfiguration><BlockPublicAcls>true</BlockPublicAcls><IgnorePublicAcls>true</IgnorePublicAcls><BlockPublicPolicy>true</BlockPublicPolicy><RestrictPublicBuckets>true</RestrictPublicBuckets></PublicAccessBlockConfiguration>`)
		case q.Has("encryption"):
			fmt.Fprint(w, `<ServerSideEncryptionConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Rule><ApplyServerSideEncryptionByDefault><SSEAlgorithm>aws:kms</SSEAlgorithm></ApplyServerSideEncryptionByDefault></Rule></ServerSideEncryptionConfiguration>`)
		default:
			s3Error(w, http.StatusBadRequest, "UnexpectedCall")
		}
	}))
	defer srv.Close()

	report, err := Inspect(context.Background(), awsEnv(t, srv.URL))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	want := AWSReport{
		BucketAccessible:          true,
		PublicAccessReadable:      true,
		PublicAccessBlocked:       true,
		DefaultEncryptionReadable: true,
		DefaultEncryptionEnabled:  true,
	}
	if report != want {
		t.Fatalf("report = %+v, want %+v", report, want)
	}
}

// The two "configuration not found" codes are states, not failures: the
// probe read successfully and the answer is "off".
func TestInspect_MissingConfigsReadAsOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case q.Has("publicAccessBlock"):
			s3Error(w, http.StatusNotFound, "NoSuchPublicAccessBlockConfiguration")
		case q.Has("encryption"):
			s3Error(w, http.StatusNotFound, "ServerSideEncryptionConfigurationNotFoundError")
		default:
			s3Error(w, http.StatusBadRequest, "UnexpectedCall")
		}
	}))
	defer srv.Close()

	report, err := Inspect(context.Background(), awsEnv(t, srv.URL))
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !report.BucketAccessible || !report.PublicAccessReadable || !report.DefaultEncryptionReadable {
		t.Fatalf("missing configs must still read successfully: %+v", report)
	}
	if report.PublicAccessBlocked || report.DefaultEncryptionEnabled {
		t.Fatalf("missing configs must read as OFF: %+v", report)
	}
}

// A denied HeadBucket surfaces the permission the operator must grant.
func TestInspect_HeadBucketDeniedNamesPermission(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s3Error(w, http.StatusForbidden, "AccessDenied")
	}))
	defer srv.Close()

	_, err := Inspect(context.Background(), awsEnv(t, srv.URL))
	if err == nil {
		t.Fatal("expected an error from a denied HeadBucket")
	}
	if !strings.Contains(err.Error(), "s3:ListBucket") || !strings.Contains(err.Error(), "arn:aws:s3:::probe-bucket") {
		t.Fatalf("error must name the permission and bucket ARN: %v", err)
	}
}

func TestIsAWSAPIErrCode(t *testing.T) {
	wrapped := fmt.Errorf("op: %w", &smithy.GenericAPIError{Code: "NoSuchBucket"})
	if !isAWSAPIErrCode(wrapped, "AccessDenied", "NoSuchBucket") {
		t.Error("wrapped APIError with a listed code must match")
	}
	if isAWSAPIErrCode(wrapped, "AccessDenied") {
		t.Error("code not in the list must not match")
	}
	if isAWSAPIErrCode(errors.New("plain"), "AccessDenied") {
		t.Error("non-API errors must not match")
	}
}

func TestBucketARN(t *testing.T) {
	if got := bucketARN("b"); got != "arn:aws:s3:::b" {
		t.Fatalf("bucketARN = %q", got)
	}
}
