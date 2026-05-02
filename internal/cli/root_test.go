package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRoot_Version(t *testing.T) {
	buf := &bytes.Buffer{}
	cmd := NewRoot("1.2.3", "abc123", "2026-01-01")
	cmd.SetOut(buf)
	cmd.SetArgs([]string{"--version"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "1.2.3") {
		t.Errorf("expected version in output, got %q", got)
	}
}
