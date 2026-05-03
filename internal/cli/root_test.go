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
	want := "1.2.3 (commit abc123, built 2026-01-01)"
	if !strings.Contains(got, want) {
		t.Errorf("expected version output to contain %q, got %q", want, got)
	}
}

// TestRoot_HasPassphraseFileFlag asserts the persistent --passphrase-file
// flag is registered on the root command. Without this flag, scripted
// callers can't supply the passphrase non-interactively without using
// the env var (which may have been blacklisted by reservedEnv).
func TestRoot_HasPassphraseFileFlag(t *testing.T) {
	cmd := NewRoot("v", "c", "d")
	flag := cmd.PersistentFlags().Lookup("passphrase-file")
	if flag == nil {
		t.Fatal("--passphrase-file persistent flag is not registered on the root command")
	}
}

// TestRoot_PassphraseFlagAccessibleFromSubcommands ensures subcommands
// can read the persistent flag — the practical wiring path that
// actually matters for the end-user contract.
func TestRoot_PassphraseFlagAccessibleFromSubcommands(t *testing.T) {
	flags := &RootFlags{}
	cmd := NewRootWithFlags("v", "c", "d", flags)
	cmd.SetArgs([]string{"--passphrase-file", "/tmp/path", "--version"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if flags.PassphraseFile != "/tmp/path" {
		t.Errorf("PassphraseFile: got %q, want /tmp/path", flags.PassphraseFile)
	}
}
