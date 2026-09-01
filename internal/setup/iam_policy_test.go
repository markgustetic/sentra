package setup

import (
	"bytes"
	"slices"
	"strings"
	"testing"
)

func TestBucketAndObjectARN(t *testing.T) {
	if got := BucketARN("b"); got != "arn:aws:s3:::b" {
		t.Fatalf("BucketARN = %q", got)
	}
	if got := ObjectARN("b", ""); got != "arn:aws:s3:::b/*" {
		t.Fatalf("ObjectARN empty prefix = %q", got)
	}
	if got := ObjectARN("b", "sentra/"); got != "arn:aws:s3:::b/sentra/*" {
		t.Fatalf("ObjectARN with prefix = %q", got)
	}
}

func TestWriteIAMPolicyGolden(t *testing.T) {
	const want = `{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SentraSetupBucketControls",
      "Effect": "Allow",
      "Action": [
        "s3:CreateBucket",
        "s3:GetBucketPublicAccessBlock",
        "s3:GetEncryptionConfiguration",
        "s3:PutBucketPublicAccessBlock",
        "s3:PutEncryptionConfiguration"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket"
      ]
    },
    {
      "Sid": "SentraListBucket",
      "Effect": "Allow",
      "Action": [
        "s3:GetBucketLocation",
        "s3:ListBucket"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket"
      ]
    },
    {
      "Sid": "SentraRepositoryObjects",
      "Effect": "Allow",
      "Action": [
        "s3:DeleteObject",
        "s3:GetObject",
        "s3:PutObject"
      ],
      "Resource": [
        "arn:aws:s3:::example-bucket/sentra/*"
      ]
    }
  ]
}
`
	var buf bytes.Buffer
	if err := WriteIAMPolicy(&buf, "example-bucket", "sentra/"); err != nil {
		t.Fatalf("WriteIAMPolicy: %v", err)
	}
	if buf.String() != want {
		t.Fatalf("WriteIAMPolicy mismatch:\n got:\n%s\nwant:\n%s", buf.String(), want)
	}
}

// iamRequest models one AWS API call as IAM authorizes it: the IAM action
// name (which is not always the API name — GetBucketEncryption authorizes as
// s3:GetEncryptionConfiguration), the resource ARN, and the condition context
// keys the request carries. Keys a request type never sends (s3:prefix on
// HeadBucket) are simply absent from the map, mirroring the request context.
type iamRequest struct {
	action   string
	resource string
	context  map[string]string
}

// iamPolicyAllows simulates IAM evaluation of doc for req: some Allow
// statement must match action, resource, and every condition block. It models
// the rule that broke HeadBucket in production: a condition operator without
// an IfExists suffix evaluates to FALSE when its context key is absent from
// the request, so a conditioned statement can never authorize a request type
// that does not carry the key. Operators the simulator does not model are a
// fatal error rather than a silent pass, so extending the policy forces
// extending the simulation.
func iamPolicyAllows(t *testing.T, doc IAMPolicyDocument, req iamRequest) bool {
	t.Helper()
	for _, stmt := range doc.Statement {
		if stmt.Effect != "Allow" {
			continue
		}
		if !slices.Contains(stmt.Action, req.action) {
			continue
		}
		if !slices.ContainsFunc(stmt.Resource, func(pattern string) bool {
			return iamGlobMatch(pattern, req.resource)
		}) {
			continue
		}
		if !iamConditionHolds(t, stmt.Condition, req.context) {
			continue
		}
		return true
	}
	return false
}

func iamConditionHolds(t *testing.T, condition map[string]any, context map[string]string) bool {
	t.Helper()
	for operator, body := range condition {
		if operator != "StringLike" {
			t.Fatalf("IAM simulator does not model condition operator %q; extend it before using the operator in the policy", operator)
		}
		keys, ok := body.(map[string]any)
		if !ok {
			t.Fatalf("condition body for %q is %T, want map[string]any", operator, body)
		}
		for key, patterns := range keys {
			value, present := context[key]
			if !present {
				return false
			}
			globs, ok := patterns.([]string)
			if !ok {
				t.Fatalf("condition values for %q are %T, want []string", key, patterns)
			}
			if !slices.ContainsFunc(globs, func(pattern string) bool {
				return iamGlobMatch(pattern, value)
			}) {
				return false
			}
		}
	}
	return true
}

// iamGlobMatch implements IAM's * wildcard (zero or more characters); the
// policy uses no ? wildcards.
func iamGlobMatch(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}

// TestBuildIAMPolicyAuthorizesSentraOperations proves the generated policy is
// satisfiable by every AWS call Sentra itself makes — the doctor's probes
// (internal/diag/aws.go: HeadBucket, GetPublicAccessBlock,
// GetBucketEncryption), setup's prepare steps (internal/setup/awsprepare.go:
// HeadBucket probe and exists-waiter, CreateBucket, PutPublicAccessBlock,
// PutBucketEncryption), and the blobstore's repo operations — while still
// denying object access outside the prefix. This is the regression gate for
// two shipped bugs: a s3:prefix condition on the ListBucket statement (whose
// context key HeadBucket and GetBucketLocation never carry) and the
// s3:*BucketEncryption action names (the real IAM actions are
// s3:GetEncryptionConfiguration / s3:PutEncryptionConfiguration).
func TestBuildIAMPolicyAuthorizesSentraOperations(t *testing.T) {
	const bucket = "arn:aws:s3:::example-bucket"
	doc := BuildIAMPolicy("example-bucket", "sentra/")
	tests := []struct {
		name  string
		req   iamRequest
		allow bool
	}{
		{
			name:  "HeadBucket probe (doctor bucketExists, setup probe and waiter)",
			req:   iamRequest{action: "s3:ListBucket", resource: bucket},
			allow: true,
		},
		{
			name:  "GetBucketLocation (no s3:prefix context key either)",
			req:   iamRequest{action: "s3:GetBucketLocation", resource: bucket},
			allow: true,
		},
		{
			name:  "setup CreateBucket",
			req:   iamRequest{action: "s3:CreateBucket", resource: bucket},
			allow: true,
		},
		{
			name:  "doctor GetPublicAccessBlock",
			req:   iamRequest{action: "s3:GetBucketPublicAccessBlock", resource: bucket},
			allow: true,
		},
		{
			name:  "setup PutPublicAccessBlock",
			req:   iamRequest{action: "s3:PutBucketPublicAccessBlock", resource: bucket},
			allow: true,
		},
		{
			name:  "doctor GetBucketEncryption authorizes as s3:GetEncryptionConfiguration",
			req:   iamRequest{action: "s3:GetEncryptionConfiguration", resource: bucket},
			allow: true,
		},
		{
			name:  "setup PutBucketEncryption authorizes as s3:PutEncryptionConfiguration",
			req:   iamRequest{action: "s3:PutEncryptionConfiguration", resource: bucket},
			allow: true,
		},
		{
			name: "blobstore List under the repo prefix",
			req: iamRequest{
				action:   "s3:ListBucket",
				resource: bucket,
				context:  map[string]string{"s3:prefix": "sentra/meta/snapshots/"},
			},
			allow: true,
		},
		{
			name:  "blobstore Get under the repo prefix",
			req:   iamRequest{action: "s3:GetObject", resource: bucket + "/sentra/blobs/ab/cdef"},
			allow: true,
		},
		{
			name:  "blobstore Put under the repo prefix",
			req:   iamRequest{action: "s3:PutObject", resource: bucket + "/sentra/meta/lock"},
			allow: true,
		},
		{
			name:  "blobstore Delete under the repo prefix",
			req:   iamRequest{action: "s3:DeleteObject", resource: bucket + "/sentra/blobs/ab/cdef"},
			allow: true,
		},
		{
			name:  "object read outside the prefix stays denied",
			req:   iamRequest{action: "s3:GetObject", resource: bucket + "/other/secrets.txt"},
			allow: false,
		},
		{
			name:  "object write outside the prefix stays denied",
			req:   iamRequest{action: "s3:PutObject", resource: bucket + "/planted"},
			allow: false,
		},
		{
			name:  "DeleteBucket stays denied",
			req:   iamRequest{action: "s3:DeleteBucket", resource: bucket},
			allow: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := iamPolicyAllows(t, doc, tt.req); got != tt.allow {
				t.Fatalf("policy allows %s on %s (context %v) = %v, want %v",
					tt.req.action, tt.req.resource, tt.req.context, got, tt.allow)
			}
		})
	}
}

// TestBuildIAMPolicyNeverEmitsConditions: bucket-level statements must stay
// unconditioned for every prefix, because HeadBucket and GetBucketLocation
// carry no s3:prefix context key and IAM evaluates a condition on an absent
// key as false (see TestBuildIAMPolicyAuthorizesSentraOperations). An empty
// prefix instead widens the object resource to the whole bucket.
func TestBuildIAMPolicyNeverEmitsConditions(t *testing.T) {
	for _, prefix := range []string{"", "sentra/"} {
		doc := BuildIAMPolicy("example-bucket", prefix)
		for _, s := range doc.Statement {
			if s.Condition != nil {
				t.Fatalf("prefix %q: statement %s has Condition %v; conditions on bucket statements deny Sentra's own HeadBucket probes", prefix, s.Sid, s.Condition)
			}
		}
	}
	doc := BuildIAMPolicy("example-bucket", "")
	for _, s := range doc.Statement {
		if s.Sid == "SentraRepositoryObjects" && s.Resource[0] != "arn:aws:s3:::example-bucket/*" {
			t.Fatalf("object resource = %q, want /*", s.Resource[0])
		}
	}
}
