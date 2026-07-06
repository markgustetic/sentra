package diag

import (
	"strings"
	"testing"
)

func TestValidateBucketName(t *testing.T) {
	tests := []struct {
		name       string
		bucket     string
		wantErr    bool
		wantSubstr string
	}{
		{name: "valid simple", bucket: "sentra-prod", wantErr: false},
		{name: "valid dotted", bucket: "my.sentra.bucket", wantErr: false},
		{name: "too short", bucket: "ab", wantErr: true, wantSubstr: "3-63"},
		{name: "too long", bucket: strings.Repeat("a", 64), wantErr: true, wantSubstr: "3-63"},
		{name: "uppercase rejected", bucket: "Bad_Bucket", wantErr: true, wantSubstr: "lowercase"},
		{name: "ip address rejected", bucket: "192.168.0.1", wantErr: true, wantSubstr: "IP addresses"},
		{name: "leading hyphen", bucket: "-nope", wantErr: true, wantSubstr: "start and end"},
		{name: "trailing dot", bucket: "nope.", wantErr: true, wantSubstr: "start and end"},
		{name: "adjacent dots", bucket: "a..b", wantErr: true, wantSubstr: "adjacent dots"},
		{name: "dot next to hyphen", bucket: "a.-b", wantErr: true, wantSubstr: "next to hyphens"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateBucketName(tt.bucket)
			if tt.wantErr && err == nil {
				t.Fatalf("ValidateBucketName(%q) = nil, want error", tt.bucket)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("ValidateBucketName(%q) = %v, want nil", tt.bucket, err)
			}
			if tt.wantErr && tt.wantSubstr != "" && !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Fatalf("ValidateBucketName(%q) error %q missing %q", tt.bucket, err.Error(), tt.wantSubstr)
			}
		})
	}
}

// TestAWSReportZeroValue guards the field set diag.AWSReport must expose so
// callers (cli wrapper + DoctorView) can rely on the exact shape.
func TestAWSReportZeroValue(t *testing.T) {
	r := AWSReport{
		BucketAccessible:          true,
		PublicAccessReadable:      true,
		PublicAccessBlocked:       true,
		DefaultEncryptionReadable: true,
		DefaultEncryptionEnabled:  true,
	}
	if !r.BucketAccessible || !r.PublicAccessReadable || !r.PublicAccessBlocked ||
		!r.DefaultEncryptionReadable || !r.DefaultEncryptionEnabled {
		t.Fatal("AWSReport fields did not round-trip")
	}
}
