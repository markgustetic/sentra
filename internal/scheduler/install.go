package scheduler

import (
	"fmt"
	"os"
	"path/filepath"
)

// Install writes every rendered file to disk, creating parent dirs (0o755)
// and writing bodies at 0o600. It is the sole mutating helper; callers gate
// it behind explicit confirmation. The files hold no secrets — only the
// executable path, the --config path, and the schedule spec — so 0o600 is a
// defense-in-depth choice, not a secrecy requirement.
func Install(files map[string]string) error {
	for path, body := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("create scheduler dir %s: %w", filepath.Dir(path), err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			return fmt.Errorf("write scheduler file %s: %w", path, err)
		}
	}
	return nil
}

// Installed reports whether every file in paths.Files exists. A missing file
// yields false; any other stat error is returned so callers surface it rather
// than silently reporting "not installed".
func Installed(paths Paths) (bool, error) {
	installed := true
	for _, path := range paths.Files {
		if _, err := os.Stat(path); err != nil {
			if os.IsNotExist(err) {
				installed = false
				continue
			}
			return false, fmt.Errorf("stat scheduler file %s: %w", path, err)
		}
	}
	return installed, nil
}

// Uninstall removes every file in paths.Files, tolerating already-absent
// files so a re-run (or an OS that never had all files) is a no-op.
func Uninstall(paths Paths) error {
	for _, path := range paths.Files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove scheduler file %s: %w", path, err)
		}
	}
	return nil
}
