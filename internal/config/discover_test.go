package config

import (
	"os"
	"path/filepath"
	"testing"
)

// touchDiscoverFile creates path (and its parents) with non-secret content.
func touchDiscoverFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("repo:\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The table exercises the precedence RULE, not one happy path: cwd wins
// when present, the XDG home is the fallback and the first-run write
// target, and a directory that merely shares the name does not count.
func TestDiscoverPath(t *testing.T) {
	tests := []struct {
		name    string
		arrange func(t *testing.T, cwd, xdg string)
		want    func(cwd, xdg string) string
	}{
		{
			name: "cwd config wins",
			arrange: func(t *testing.T, cwd, xdg string) {
				touchDiscoverFile(t, filepath.Join(cwd, "sentra.yaml"))
			},
			want: func(cwd, xdg string) string { return "sentra.yaml" },
		},
		{
			name:    "no cwd config falls back to XDG home",
			arrange: func(t *testing.T, cwd, xdg string) {},
			want: func(cwd, xdg string) string {
				return filepath.Join(xdg, "sentra", "sentra.yaml")
			},
		},
		{
			name: "both present cwd wins",
			arrange: func(t *testing.T, cwd, xdg string) {
				touchDiscoverFile(t, filepath.Join(cwd, "sentra.yaml"))
				touchDiscoverFile(t, filepath.Join(xdg, "sentra", "sentra.yaml"))
			},
			want: func(cwd, xdg string) string { return "sentra.yaml" },
		},
		{
			name: "directory named sentra.yaml does not count",
			arrange: func(t *testing.T, cwd, xdg string) {
				if err := os.Mkdir(filepath.Join(cwd, "sentra.yaml"), 0o755); err != nil {
					t.Fatal(err)
				}
			},
			want: func(cwd, xdg string) string {
				return filepath.Join(xdg, "sentra", "sentra.yaml")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cwd := t.TempDir()
			xdg := t.TempDir()
			t.Chdir(cwd)
			t.Setenv("XDG_CONFIG_HOME", xdg)
			tt.arrange(t, cwd, xdg)
			if got, want := DiscoverPath(), tt.want(cwd, xdg); got != want {
				t.Errorf("DiscoverPath() = %q, want %q", got, want)
			}
		})
	}
}

// Unset/empty XDG_CONFIG_HOME defaults to ~/.config (the gh-CLI
// convention). HOME is how os.UserHomeDir resolves on unix.
func TestDiscoverPath_DefaultsXDGToHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Chdir(t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".config", "sentra", "sentra.yaml")
	if got := DiscoverPath(); got != want {
		t.Errorf("DiscoverPath() = %q, want %q", got, want)
	}
}
