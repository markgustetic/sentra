package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// The rule under test: an explicit flag wins and must name an existing
// file; a programmatic non-default value wins; only the untouched default
// falls through to discovery, which stays tolerant of "no file anywhere".
func TestResolveConfigPath(t *testing.T) {
	xdg := t.TempDir()
	chDir(t, t.TempDir()) // empty cwd: no ./sentra.yaml
	t.Setenv("XDG_CONFIG_HOME", xdg)
	home := filepath.Join(xdg, "sentra", "sentra.yaml")

	newCmd := func() *cobra.Command {
		cmd := &cobra.Command{Use: "x"}
		var cfgPath string
		cmd.Flags().StringVar(&cfgPath, "config", configFileName, "")
		return cmd
	}
	explicit := func(t *testing.T, path string) *cobra.Command {
		t.Helper()
		cmd := newCmd()
		if err := cmd.Flags().Set("config", path); err != nil {
			t.Fatal(err)
		}
		return cmd
	}

	t.Run("default resolves via discovery, tolerating no file", func(t *testing.T) {
		got, err := resolveConfigPath(newCmd(), configFileName)
		if err != nil {
			t.Fatalf("discovery must not require the file to exist: %v", err)
		}
		if got != home {
			t.Errorf("got %q, want %q", got, home)
		}
	})

	t.Run("explicit flag bypasses discovery even at the default value", func(t *testing.T) {
		if err := os.WriteFile(configFileName, []byte("repo:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(configFileName)
		got, err := resolveConfigPath(explicit(t, configFileName), configFileName)
		if err != nil {
			t.Fatal(err)
		}
		if got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})

	t.Run("explicit flag naming a missing file is an error naming it", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope", "sentra.yaml")
		_, err := resolveConfigPath(explicit(t, missing), missing)
		if err == nil {
			t.Fatal("want an error, got nil")
		}
		if !strings.Contains(err.Error(), missing) {
			t.Errorf("error must name %s: %v", missing, err)
		}
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("error must wrap os.ErrNotExist so callers can branch: %v", err)
		}
	})

	t.Run("launch variant accepts a missing explicit file for the wizard", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "nope", "sentra.yaml")
		if got := resolveConfigPathForLaunch(explicit(t, missing), missing); got != missing {
			t.Errorf("got %q, want %q", got, missing)
		}
	})

	t.Run("programmatic non-default value is left alone", func(t *testing.T) {
		cmd := &cobra.Command{Use: "local"} // no --config flag registered
		got, err := resolveConfigPath(cmd, ".sentra-local.yaml")
		if err != nil {
			t.Fatal(err)
		}
		if got != ".sentra-local.yaml" {
			t.Errorf("got %q, want .sentra-local.yaml", got)
		}
	})

	t.Run("cwd config wins over home", func(t *testing.T) {
		chDir(t, t.TempDir())
		if err := os.WriteFile("sentra.yaml", []byte("repo:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := resolveConfigPath(newCmd(), configFileName)
		if err != nil {
			t.Fatal(err)
		}
		if got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})
}
