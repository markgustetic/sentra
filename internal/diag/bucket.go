package diag

import (
	"fmt"
	"net"
	"strings"
)

// ValidateBucketName rejects S3 bucket names Sentra cannot use before any
// network call is made. The rules mirror AWS's general-purpose bucket
// naming constraints (3-63 chars, lowercase, no IP-shaped names, no
// adjacent dots, dots not adjacent to hyphens) so `sentra doctor` and the
// TUI Doctor view fail fast with a specific message instead of surfacing
// an opaque AWS 400.
func ValidateBucketName(bucket string) error {
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
