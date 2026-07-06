package setup

import (
	"bytes"
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
        "s3:GetBucketEncryption",
        "s3:GetBucketPublicAccessBlock",
        "s3:PutBucketEncryption",
        "s3:PutBucketPublicAccessBlock"
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
      ],
      "Condition": {
        "StringLike": {
          "s3:prefix": [
            "sentra/*"
          ]
        }
      }
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

func TestBuildIAMPolicyNoPrefixOmitsCondition(t *testing.T) {
	doc := BuildIAMPolicy("example-bucket", "")
	for _, s := range doc.Statement {
		if s.Sid == "SentraListBucket" && s.Condition != nil {
			t.Fatalf("expected no Condition when prefix is empty, got %v", s.Condition)
		}
		if s.Sid == "SentraRepositoryObjects" && s.Resource[0] != "arn:aws:s3:::example-bucket/*" {
			t.Fatalf("object resource = %q, want /*", s.Resource[0])
		}
	}
}
