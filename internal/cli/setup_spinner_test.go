package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestSetupProgressFallsBackToPlainOutput: the spinner outlived `sentra setup`
// — `sentra doctor` runs every probe through runSetupProgress. doctor_test.go
// asserts the success labels; this pins the part that only shows up off a TTY,
// where the animation must degrade to one plain line per step. Emitting \r into
// a pipe, a file, or a CI log would overwrite the previous line's text and
// corrupt the transcript an operator pasted into a bug report.
func TestSetupProgressFallsBackToPlainOutput(t *testing.T) {
	var out bytes.Buffer
	err := runSetupProgress(&out, "Preparing AWS S3 bucket", "AWS S3 bucket verified", func() error {
		return nil
	})
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"... Preparing AWS S3 bucket",
		"ok AWS S3 bucket verified",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\r") {
		t.Fatalf("non-terminal progress should not animate, got %q", got)
	}
}
