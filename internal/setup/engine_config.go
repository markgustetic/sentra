package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/markgustetic/sentra/internal/config"
)

// WriteConfig writes p.Config to cfgPath. Progress output is the driver's
// responsibility, not the engine's — the TUI wizard renders its own checklist.
func (e *Engine) WriteConfig(cfgPath string, p *Plan) error {
	if err := config.Write(cfgPath, &p.Config); err != nil {
		return fmt.Errorf("write %s: %w", cfgPath, err)
	}
	return nil
}

// WriteDraft persists a non-secret setup draft next to cfgPath so an
// interrupted run can be resumed: the wizard writes it before provisioning and
// RemoveDraft clears it only on success, so a draft left on disk is the record
// of a run that failed partway. cli.loadSetupDraft is the reader — it pre-fills
// the wizard on the next launch when no real config exists. config never
// serializes secrets, so the draft is safe to leave on disk.
func (e *Engine) WriteDraft(cfgPath string, cfg *config.Config) error {
	draftPath := e.DraftPath(cfgPath)
	if err := config.Write(draftPath, cfg); err != nil {
		return fmt.Errorf("write setup draft %s: %w", draftPath, err)
	}
	return nil
}

// RemoveDraft best-effort deletes the draft. A leftover non-secret draft is
// less harmful than turning a successful setup into a failure, so errors are
// swallowed.
func (e *Engine) RemoveDraft(cfgPath string) {
	if err := os.Remove(e.DraftPath(cfgPath)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return
	}
}

// DraftPath returns the draft sibling of cfgPath: a dotfile named after the
// config so one directory can hold drafts for several config paths.
func (e *Engine) DraftPath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	base := filepath.Base(cfgPath)
	return filepath.Join(dir, "."+base+".setup-draft")
}
