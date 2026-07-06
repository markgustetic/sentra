package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/markgustetic/sentra/internal/config"
)

// WriteConfig writes p.Config to cfgPath. Headless port of the config.Write
// call at internal/cli/setup.go:294-298 (the "Writing"/"Config written"
// stdout lines are the driver's responsibility, not the engine's).
func (e *Engine) WriteConfig(cfgPath string, p *Plan) error {
	if err := config.Write(cfgPath, &p.Config); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return nil
}

// WriteDraft persists a non-secret setup draft next to cfgPath so an
// interrupted run can be resumed. Moved from writeSetupDraft
// (internal/cli/setup.go:397-403). config never serializes secrets, so the
// draft is safe to leave on disk.
func (e *Engine) WriteDraft(cfgPath string, cfg *config.Config) error {
	draftPath := e.DraftPath(cfgPath)
	if err := config.Write(draftPath, cfg); err != nil {
		return fmt.Errorf("write setup draft %s: %w", draftPath, err)
	}
	return nil
}

// RemoveDraft best-effort deletes the draft. Moved from removeSetupDraft
// (internal/cli/setup.go:405-411): a leftover non-secret draft is less
// harmful than turning a successful setup into a failure, so errors are
// swallowed.
func (e *Engine) RemoveDraft(cfgPath string) {
	if err := os.Remove(e.DraftPath(cfgPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// DraftPath returns the draft sibling of cfgPath. Moved from setupDraftPath
// (internal/cli/setup.go:413-417).
func (e *Engine) DraftPath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	base := filepath.Base(cfgPath)
	return filepath.Join(dir, "."+base+".setup-draft")
}
