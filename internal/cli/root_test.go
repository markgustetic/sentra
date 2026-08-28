package cli

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/debug"
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

// TestRoot_HasLogFlags asserts the three logging persistent flags are
// registered. Operators need them for cron / CI runs to capture
// structured events.
func TestRoot_HasLogFlags(t *testing.T) {
	cmd := NewRoot("v", "c", "d")
	for _, name := range []string{"log-level", "log-format", "log-file"} {
		if cmd.PersistentFlags().Lookup(name) == nil {
			t.Errorf("--%s persistent flag is not registered", name)
		}
	}
}

// TestConfigureSlog_FileBackend writes a log line to a real file via
// the configured handler and confirms it lands. This exercises the
// open-and-redirect path operators rely on for cron runs.
func TestConfigureSlog_FileBackend(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.log")
	flags := &RootFlags{
		LogLevel:  "info",
		LogFormat: "text",
		LogFile:   path,
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	cleanup, err := ConfigureSlog(flags, false)
	if err != nil {
		t.Fatalf("ConfigureSlog: %v", err)
	}
	slog.Info("hello from test", "k", "v")
	if err := cleanup(); err != nil {
		t.Errorf("cleanup: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(got), "hello from test") {
		t.Errorf("log file missing message: %q", got)
	}
	if !strings.Contains(string(got), "k=v") {
		t.Errorf("log file missing structured field: %q", got)
	}
}

// TestConfigureSlog_TUIDiscardsLogs confirms that tuiMode=true with
// no LogFile silences output — the alt-screen would otherwise be
// corrupted by interleaved log lines. We verify by setting up a level
// that would normally print, then capturing stderr (via a test pipe)
// and confirming nothing arrives.
func TestConfigureSlog_TUIDiscardsLogs(t *testing.T) {
	flags := &RootFlags{
		LogLevel:  "debug", // would print everything if not discarded
		LogFormat: "text",
		LogFile:   "", // no file → discard in TUI mode
	}
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })

	// Capture stderr by replacing it with a pipe.
	origStderr := os.Stderr
	rPipe, wPipe, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("pipe: %v", perr)
	}
	os.Stderr = wPipe
	t.Cleanup(func() { os.Stderr = origStderr })

	cleanup, err := ConfigureSlog(flags, true) // tuiMode=true
	if err != nil {
		t.Fatalf("ConfigureSlog: %v", err)
	}
	slog.Info("should be discarded")
	_ = cleanup()
	_ = wPipe.Close()

	buf := &bytes.Buffer{}
	_, _ = buf.ReadFrom(rPipe)
	if buf.Len() != 0 {
		t.Errorf("TUI mode wrote to stderr: %q", buf.String())
	}
}

// TestParseLogLevel covers each parseable level plus the unknown
// fallback (warn). Locks in the contract that a typo doesn't crash
// the CLI — at most it silences info-level events.
func TestParseLogLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
		"":        slog.LevelWarn, // empty falls back to warn
		"garbage": slog.LevelWarn,
	}
	for in, want := range cases {
		t.Run("input="+in, func(t *testing.T) {
			if got := parseLogLevel(in); got != want {
				t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
			}
		})
	}
}

// ResolveBuildVersion fills the goreleaser ldflags' place when the binary
// came from `go install`: the module version (or VCS revision/time) from
// the embedded build info replaces the "dev/none/unknown" placeholders, so
// `sentra version` identifies the build on the ONLY install path that
// works without a release pipeline.
func TestResolveBuildVersion_FallsBackToBuildInfo(t *testing.T) {
	bi := &debug.BuildInfo{}
	bi.Main.Version = "v0.1.2"
	bi.Settings = []debug.BuildSetting{
		{Key: "vcs.revision", Value: "abcdef1234567890"},
		{Key: "vcs.time", Value: "2026-08-28T12:00:00Z"},
	}
	v, c, d := ResolveBuildVersion("dev", "none", "unknown", func() (*debug.BuildInfo, bool) { return bi, true })
	if v != "v0.1.2" {
		t.Errorf("version = %q, want v0.1.2", v)
	}
	if c != "abcdef123456" {
		t.Errorf("commit = %q, want short revision", c)
	}
	if d != "2026-08-28T12:00:00Z" {
		t.Errorf("date = %q", d)
	}
}

// Explicit ldflags (a real release build) always win — build info must
// never override what goreleaser stamped.
func TestResolveBuildVersion_LdflagsWin(t *testing.T) {
	v, c, d := ResolveBuildVersion("v1.0.0", "cafe", "2026-01-01", func() (*debug.BuildInfo, bool) {
		t.Fatal("build info must not be consulted when ldflags are set")
		return nil, false
	})
	if v != "v1.0.0" || c != "cafe" || d != "2026-01-01" {
		t.Errorf("ldflags overridden: %q %q %q", v, c, d)
	}
}

// (devel) module versions keep "dev" but still pick up the VCS revision.
func TestResolveBuildVersion_DevelKeepsDevWithRevision(t *testing.T) {
	bi := &debug.BuildInfo{}
	bi.Main.Version = "(devel)"
	bi.Settings = []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789ab"}}
	v, c, _ := ResolveBuildVersion("dev", "none", "unknown", func() (*debug.BuildInfo, bool) { return bi, true })
	if v != "dev" {
		t.Errorf("version = %q, want dev for (devel)", v)
	}
	if c != "0123456789ab" {
		t.Errorf("commit = %q, want revision", c)
	}
}
