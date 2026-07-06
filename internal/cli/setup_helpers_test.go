package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// writeAWSConfig writes body as a fake AWS CLI config file and returns its
// path. It lived in setup_awscli_test.go before the AWS-CLI parser and its
// coverage moved to internal/setup; internal/cli/setup_test.go (the
// 1863-line behavior-preservation oracle) still calls it directly at :104, so
// it stays here rather than being deleted with the rest of that file.
func writeAWSConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write aws config: %v", err)
	}
	return path
}
