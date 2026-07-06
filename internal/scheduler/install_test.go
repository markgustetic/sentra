package scheduler

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

func TestInstallStatusUninstall_RoundTrip(t *testing.T) {
	home := t.TempDir()
	paths, err := PathsFor("linux", home, "home")
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}

	// Not installed before Install.
	installed, err := Installed(paths)
	if err != nil {
		t.Fatalf("Installed (pre): %v", err)
	}
	if installed {
		t.Fatal("Installed = true before any files written")
	}

	files, err := Render(paths, "/usr/bin/sentra", filepath.Join(home, "sentra.yaml"), "home",
		config.PolicySchedule{Cadence: "hourly"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if err := Install(files); err != nil {
		t.Fatalf("Install: %v", err)
	}

	installed, err = Installed(paths)
	if err != nil {
		t.Fatalf("Installed (post): %v", err)
	}
	if !installed {
		t.Fatal("Installed = false after Install")
	}
	for _, p := range paths.Files {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file %s perm = %o, want 600", p, perm)
		}
	}

	if err := Uninstall(paths); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	installed, err = Installed(paths)
	if err != nil {
		t.Fatalf("Installed (after uninstall): %v", err)
	}
	if installed {
		t.Fatal("Installed = true after Uninstall")
	}
	// Uninstall is idempotent: a second call with files already gone is fine.
	if err := Uninstall(paths); err != nil {
		t.Fatalf("second Uninstall: %v", err)
	}
}

func TestInstalled_PartialIsNotInstalled(t *testing.T) {
	home := t.TempDir()
	paths, err := PathsFor("linux", home, "home") // 2 files
	if err != nil {
		t.Fatalf("PathsFor: %v", err)
	}
	// Write only the first file; Installed must report false.
	if err := os.MkdirAll(filepath.Dir(paths.Files[0]), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.Files[0], []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	installed, err := Installed(paths)
	if err != nil {
		t.Fatalf("Installed: %v", err)
	}
	if installed {
		t.Fatal("Installed = true with only one of two files present")
	}
}
