package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// The rule under test: an explicit flag wins; a programmatic non-default
// value wins; only the untouched default falls through to discovery.
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

	t.Run("default resolves via discovery", func(t *testing.T) {
		if got := resolveConfigPath(newCmd(), configFileName); got != home {
			t.Errorf("got %q, want %q", got, home)
		}
	})

	t.Run("explicit flag bypasses discovery even at the default value", func(t *testing.T) {
		cmd := newCmd()
		if err := cmd.Flags().Set("config", configFileName); err != nil {
			t.Fatal(err)
		}
		if got := resolveConfigPath(cmd, configFileName); got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})

	t.Run("programmatic non-default value is left alone", func(t *testing.T) {
		cmd := &cobra.Command{Use: "local"} // no --config flag registered
		if got := resolveConfigPath(cmd, ".sentra-local.yaml"); got != ".sentra-local.yaml" {
			t.Errorf("got %q, want .sentra-local.yaml", got)
		}
	})

	t.Run("cwd config wins over home", func(t *testing.T) {
		chDir(t, t.TempDir())
		if err := os.WriteFile("sentra.yaml", []byte("repo:\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := resolveConfigPath(newCmd(), configFileName); got != configFileName {
			t.Errorf("got %q, want %q", got, configFileName)
		}
	})
}
