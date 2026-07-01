package action

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"context"
)

// ignoreFileName is the conventional name for sentra's per-repo
// ignore file. Mirrored from the repo / walker docs; encoded here
// so AddToIgnore writes to a consistent path regardless of how the
// CLI was invoked.
const ignoreFileName = ".sentraignore"

// AddToIgnoreHandler implements the add_to_ignore verb: append a
// gitignore-style glob to .sentraignore so future backups skip the
// matched files. The handler is idempotent — repeated runs against
// the same target produce a single entry, not duplicates.
type AddToIgnoreHandler struct{}

// Name returns the verb the LLM emits for this handler.
func (AddToIgnoreHandler) Name() Action { return AddToIgnore }

// Description goes into the system prompt fragment.
func (AddToIgnoreHandler) Description() string {
	return "append a gitignore-style glob to .sentraignore so future backups skip it"
}

// Apply writes a single line to .sentraignore. Write order:
//
//  1. Trim whitespace from target. Empty target is a soft skip
//     (logged but not an error) — the model occasionally emits a
//     blank target when it has nothing concrete to suggest.
//  2. Read the existing patterns to dedupe. A repeat invocation
//     against the same pattern emits "already in" and returns nil.
//  3. Append target to the file (creating it with 0600 if missing).
//     A leading newline is added when the existing file is non-
//     empty without a trailing newline so two appends never end
//     up on the same line.
func (AddToIgnoreHandler) Apply(
	ctx context.Context,
	env Env,
	id, target, _, _ string,
) error {
	target = strings.TrimSpace(target)
	if target == "" {
		fmt.Fprintf(env.Stdout, "  - %s: empty target, skipped\n", id)
		return nil
	}
	// A single verb must write exactly one pattern line. Reject a target
	// with an embedded CR/LF: otherwise one suggestion would inject
	// multiple .sentraignore lines — including a "!"-negation that
	// silently re-includes a file the operator meant to exclude — and
	// the whole-string dedup would fail to catch the individual lines.
	if strings.ContainsAny(target, "\r\n") {
		return fmt.Errorf("%s target %q contains a newline; a pattern must be a single line", ignoreFileName, target)
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd = "."
	}
	path := filepath.Join(cwd, ignoreFileName)

	existing, err := readIgnorePatterns(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", ignoreFileName, err)
	}
	if _, dup := existing[target]; dup {
		fmt.Fprintf(env.Stdout, "  - %s: %q already in %s, skipped\n",
			id, target, ignoreFileName)
		return nil
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open %s: %w", ignoreFileName, err)
	}
	defer f.Close()

	// Add a leading newline if the file already exists and is non-
	// empty without a trailing newline. Cheap check: stat the file.
	// Worst case we add an unnecessary blank line; not worth a more
	// complex fix.
	if info, statErr := f.Stat(); statErr == nil && info.Size() > 0 {
		if _, err := f.WriteString("\n"); err != nil {
			return fmt.Errorf("write %s: %w", ignoreFileName, err)
		}
	}
	if _, err := f.WriteString(target + "\n"); err != nil {
		return fmt.Errorf("write %s: %w", ignoreFileName, err)
	}
	fmt.Fprintf(env.Stdout, "  - %s: added %q to %s\n", id, target, ignoreFileName)
	return nil
}

// readIgnorePatterns reads path and returns the set of trimmed
// non-comment, non-blank lines as map keys. Lifted from the
// previous cli/agent.go helper of the same name; staying in this
// package keeps the AddToIgnore behavior self-contained.
func readIgnorePatterns(path string) (map[string]struct{}, error) {
	out := make(map[string]struct{})
	body, err := os.ReadFile(path) //nolint:gosec // path is computed from env.Cwd + a fixed name
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return out, nil
		}
		return nil, err
	}
	for _, line := range strings.Split(string(body), "\n") {
		// Strip CR for CRLF, then trim whitespace.
		clean := strings.TrimSpace(strings.TrimRight(line, "\r"))
		if clean == "" || strings.HasPrefix(clean, "#") {
			continue
		}
		out[clean] = struct{}{}
	}
	return out, nil
}
