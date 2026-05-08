package cli

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// RootFlags collects the values of persistent flags that subcommand
// bodies need to read at runtime. Wired through Deps so production and
// tests share the same plumbing — no globals.
//
// Today this only carries --passphrase-file; future cross-cutting flags
// (a config-path override, a verbosity toggle) belong here too.
type RootFlags struct {
	// PassphraseFile is the path passed via --passphrase-file. Empty
	// means "no file source"; the resolver will fall through to the
	// next priority (env / keyring / prompt).
	PassphraseFile string

	// LogLevel selects the slog filter level: "debug", "info",
	// "warn", "error". Default "warn" keeps stderr quiet during
	// normal use; cron/CI runners can set --log-level=info to capture
	// completion summaries and retry events.
	LogLevel string

	// LogFormat selects the slog handler: "text" (the default,
	// human-readable) or "json" (machine-parseable, intended for
	// piping into a log aggregator).
	LogFormat string

	// LogFile redirects slog output to a file. Empty (default) sends
	// logs to stderr — except in TUI mode (`sentra ui` or bare
	// `sentra`) where stderr would corrupt the alt-screen, so the
	// TUI silently discards logs unless LogFile is set.
	LogFile string
}

// NewRoot returns the root cobra command without exposing the flags
// struct. Useful for tests / callers that don't care about reading
// persistent flag values back. The flags are still registered (and
// parsed normally), they're just not surfaced to the caller.
func NewRoot(version, commit, date string) *cobra.Command {
	return NewRootWithFlags(version, commit, date, &RootFlags{})
}

// NewRootWithFlags is the wiring point for production: pass in a
// pointer to a RootFlags and the persistent flag values get written
// into it as cobra parses argv. Subcommand bodies capture the same
// pointer via their Deps and read the live values at run time.
//
// Persistent flags:
//   - --passphrase-file <path>   sourced first by the passphrase resolver
func NewRootWithFlags(version, commit, date string, flags *RootFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "sentra",
		Short:   "Encrypted versioned S3 backups with an agentic sidekick",
		Long:    "Sentra backs up directories to S3 as encrypted, versioned snapshots and runs a hybrid heuristics+LLM agent that audits the repo.",
		Version: fmt.Sprintf("%s (commit %s, built %s)", version, commit, date),
	}
	cmd.SetVersionTemplate("{{.Version}}\n")
	cmd.PersistentFlags().StringVar(&flags.PassphraseFile, "passphrase-file", "",
		"path to a file containing the repository passphrase (overrides SENTRA_PASSPHRASE)")
	cmd.PersistentFlags().StringVar(&flags.LogLevel, "log-level", "warn",
		"log level: debug, info, warn, error")
	cmd.PersistentFlags().StringVar(&flags.LogFormat, "log-format", "text",
		"log format: text or json")
	cmd.PersistentFlags().StringVar(&flags.LogFile, "log-file", "",
		"log output file (default: stderr; TUI mode discards logs unless this is set)")
	return cmd
}

// ConfigureSlog builds and installs a slog.Default logger from the
// parsed root flags. Returns a cleanup func that closes any opened
// log file (no-op when LogFile is empty). main.go calls this once at
// startup; subcommand bodies use slog.Info/Warn/Error directly through
// slog.Default().
//
// Behavior:
//   - LogLevel: parsed case-insensitively; unknown levels fall back
//     to warn rather than panic, so a typo doesn't crash the CLI.
//   - LogFormat: text or json; anything else falls back to text.
//   - LogFile: opened append-only with 0600. Errors are returned;
//     callers should fall back to stderr or fail the command.
//   - tuiMode: when true and LogFile is empty, logs go to io.Discard
//     so the TUI's alt-screen stays clean. Setting --log-file
//     overrides this — operators running the TUI on cron can still
//     capture diagnostics by writing to a file.
func ConfigureSlog(flags *RootFlags, tuiMode bool) (cleanup func() error, err error) {
	level := parseLogLevel(flags.LogLevel)

	var w io.Writer = os.Stderr
	cleanup = func() error { return nil }
	if flags.LogFile != "" {
		f, ferr := os.OpenFile(flags.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if ferr != nil {
			return cleanup, fmt.Errorf("open log file %s: %w", flags.LogFile, ferr)
		}
		w = f
		cleanup = f.Close
	} else if tuiMode {
		// No log file in TUI mode — silence so the alt-screen isn't
		// trashed by interleaved log lines.
		w = io.Discard
	}

	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	switch strings.ToLower(flags.LogFormat) {
	case "json":
		h = slog.NewJSONHandler(w, opts)
	default:
		h = slog.NewTextHandler(w, opts)
	}
	slog.SetDefault(slog.New(h))
	return cleanup, nil
}

// parseLogLevel maps a string to slog.Level. Unknown values default
// to warn — a typo in --log-level shouldn't be fatal.
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "info":
		return slog.LevelInfo
	case "error":
		return slog.LevelError
	default:
		return slog.LevelWarn
	}
}
