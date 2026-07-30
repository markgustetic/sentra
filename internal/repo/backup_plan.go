package repo

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/walker"
)

// BackupPlanVersion is the JSON schema version for reviewable backup
// plan files. Apply rejects unknown versions rather than guessing.
const BackupPlanVersion = 1

// BackupPlan is the reviewable file produced by `sentra backup plan`
// and consumed by `sentra backup apply`. It records the exact file set
// and metadata that apply must see before it writes a snapshot.
type BackupPlan struct {
	Version   int               `json:"version"`
	CreatedAt time.Time         `json:"created_at"`
	Root      string            `json:"root"`
	Tag       string            `json:"tag,omitempty"`
	Options   BackupPlanOptions `json:"options"`
	Stats     BackupPlanStats   `json:"stats"`
	Files     []BackupPlanFile  `json:"files"`
}

// BackupPlanStats is the aggregate size summary shown in the plan
// file before any encryption or upload work has happened.
type BackupPlanStats struct {
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
}

// BackupPlanOptions is the reviewable subset of walker.Options that
// affects which files enter the plan. Concurrency is deliberately
// omitted because it is an execution detail, not part of the reviewed
// file set.
type BackupPlanOptions struct {
	IgnoreFile    string `json:"ignore_file"`
	ExcludeCaches bool   `json:"exclude_caches"`
}

// BackupPlanFile is one file entry in a BackupPlan. Mode is stored as
// a four-digit octal permission string so the JSON is easy to review.
type BackupPlanFile struct {
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	Mode  string    `json:"mode"`
	MTime time.Time `json:"mtime"`
}

// PlanSnapshot walks root with the same include/exclude rules as
// CreateSnapshot and returns a deterministic, reviewable plan. It does
// not require an opened repo because it performs no encryption or
// blobstore writes.
func PlanSnapshot(ctx context.Context, root string, opts SnapshotOptions) (BackupPlan, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return BackupPlan{}, fmt.Errorf("repo: abs root: %w", err)
	}
	absRoot = filepath.Clean(absRoot)

	walkerOpts := resolveWalkerOptions(opts.Walker)
	plan := BackupPlan{
		Version:   BackupPlanVersion,
		CreatedAt: time.Now().UTC(),
		Root:      absRoot,
		Tag:       opts.Tag,
		Options:   backupPlanOptionsFromWalker(walkerOpts),
		Stats:     BackupPlanStats{},
		Files:     make([]BackupPlanFile, 0),
	}

	entries, err := collectPlanEntries(ctx, absRoot, plan.Options.toWalkerOptions())
	if err != nil {
		return BackupPlan{}, err
	}
	plan.Files = make([]BackupPlanFile, 0, len(entries))
	for _, e := range entries {
		plan.Files = append(plan.Files, backupPlanFileFromEntry(e))
		plan.Stats.Files++
		plan.Stats.Bytes += e.Size
	}
	return plan, nil
}

// CreateSnapshotFromPlan validates that the current tree still matches
// plan, then chunks/encrypts/uploads the reviewed file set and writes a
// snapshot manifest. The snapshot tag comes from the plan, not opts,
// because the tag is part of what the operator reviewed.
func (r *Repo) CreateSnapshotFromPlan(ctx context.Context, plan BackupPlan, opts SnapshotOptions) (SnapshotInfo, error) {
	if err := validateBackupPlanShape(plan); err != nil {
		return SnapshotInfo{}, err
	}
	entries, err := validatePlanAgainstDisk(ctx, plan)
	if err != nil {
		return SnapshotInfo{}, err
	}

	// Same advisory lock as CreateSnapshot — apply paths are
	// snapshot-creating operations and must serialize against GC.
	// Local var is `heldLock` (not `lockInfo`) because `lockInfo` is
	// now the unexported type name in this package; reusing it as a
	// local would shadow the type.
	heldLock, err := acquireLock(ctx, r.store, "snapshot-apply")
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer releaseLock(ctx, r.store, heldLock)

	repoKey, err := r.keyOrErr()
	if err != nil {
		return SnapshotInfo{}, err
	}
	defer crypto.Zeroize(repoKey)

	// Local var name avoids shadowing the imported `progress` package.
	reporter := opts.Progress
	if reporter == nil {
		reporter = progress.NopReporter{}
	}
	reporter.Total(plan.Stats.Bytes)

	state := &snapState{}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return SnapshotInfo{}, err
		}
		fe, newBytes, err := r.captureFile(ctx, repoKey, e, reporter)
		if err != nil {
			return SnapshotInfo{}, err
		}
		if fe == nil {
			return SnapshotInfo{}, fmt.Errorf("repo: plan file %q vanished during apply", e.RelPath)
		}
		state.add(*fe, newBytes)
	}

	// Dirs and symlinks are captured fresh at apply time rather than
	// carried in the plan: the plan's review surface is file CONTENT
	// (what gets read and uploaded). Dirs and symlinks upload nothing,
	// so re-walking them here keeps apply-created snapshots at the
	// same tree fidelity as direct backups without changing what the
	// operator reviewed.
	wopts := plan.Options.toWalkerOptions()
	wopts.IncludeNonRegular = true
	if err := walker.Walk(ctx, plan.Root, wopts, func(e walker.Entry) error {
		if e.Kind == walker.KindFile {
			return nil
		}
		state.add(entryFromNonRegular(e), 0)
		return nil
	}); err != nil {
		return SnapshotInfo{}, fmt.Errorf("repo: walk plan root for dirs/symlinks: %w", err)
	}

	return r.finishSnapshot(ctx, repoKey, plan.Root, plan.Tag, state)
}

func backupPlanOptionsFromWalker(opts walker.Options) BackupPlanOptions {
	ignoreFile := opts.IgnoreFile
	if ignoreFile == "" {
		ignoreFile = ".sentraignore"
	}
	return BackupPlanOptions{
		IgnoreFile:    ignoreFile,
		ExcludeCaches: opts.ExcludeCaches,
	}
}

func (o BackupPlanOptions) toWalkerOptions() walker.Options {
	ignoreFile := o.IgnoreFile
	if ignoreFile == "" {
		ignoreFile = ".sentraignore"
	}
	return walker.Options{
		IgnoreFile:    ignoreFile,
		ExcludeCaches: o.ExcludeCaches,
	}
}

func backupPlanFileFromEntry(e walker.Entry) BackupPlanFile {
	return BackupPlanFile{
		Path:  e.RelPath,
		Size:  e.Size,
		Mode:  fmt.Sprintf("%04o", e.Mode.Perm()),
		MTime: e.MTime.UTC(),
	}
}

func collectPlanEntries(ctx context.Context, absRoot string, opts walker.Options) ([]walker.Entry, error) {
	var entries []walker.Entry
	var mu sync.Mutex
	err := walker.Walk(ctx, absRoot, opts, func(e walker.Entry) error {
		mu.Lock()
		entries = append(entries, e)
		mu.Unlock()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("repo: plan walk: %w", err)
	}
	slices.SortFunc(entries, func(a, b walker.Entry) int { return cmp.Compare(a.RelPath, b.RelPath) })
	return entries, nil
}

func validateBackupPlanShape(plan BackupPlan) error {
	if plan.Version != BackupPlanVersion {
		return fmt.Errorf("repo: unsupported backup plan version %d", plan.Version)
	}
	if plan.Root == "" {
		return fmt.Errorf("repo: backup plan root is empty")
	}
	if !filepath.IsAbs(plan.Root) {
		return fmt.Errorf("repo: backup plan root %q is not absolute", plan.Root)
	}
	if plan.Stats.Files != len(plan.Files) {
		return fmt.Errorf("repo: backup plan stats/files mismatch: stats=%d files=%d",
			plan.Stats.Files, len(plan.Files))
	}
	var bytes int64
	seen := make(map[string]bool, len(plan.Files))
	for _, f := range plan.Files {
		if f.Path == "" || filepath.IsAbs(f.Path) || strings.HasPrefix(f.Path, "/") {
			return fmt.Errorf("repo: invalid backup plan path %q", f.Path)
		}
		if _, err := safeJoinPath(plan.Root, f.Path, "backup root"); err != nil {
			return err
		}
		if seen[f.Path] {
			return fmt.Errorf("repo: duplicate backup plan path %q", f.Path)
		}
		seen[f.Path] = true
		if f.Size < 0 {
			return fmt.Errorf("repo: backup plan path %q has negative size", f.Path)
		}
		if _, err := parsePlanMode(f.Mode); err != nil {
			return fmt.Errorf("repo: backup plan path %q mode: %w", f.Path, err)
		}
		bytes += f.Size
	}
	if plan.Stats.Bytes != bytes {
		return fmt.Errorf("repo: backup plan stats/bytes mismatch: stats=%d files=%d",
			plan.Stats.Bytes, bytes)
	}
	return nil
}

func validatePlanAgainstDisk(ctx context.Context, plan BackupPlan) ([]walker.Entry, error) {
	current, err := collectPlanEntries(ctx, plan.Root, plan.Options.toWalkerOptions())
	if err != nil {
		return nil, err
	}
	if len(current) != len(plan.Files) {
		return nil, fmt.Errorf("repo: backup plan drift: expected %d files, found %d",
			len(plan.Files), len(current))
	}

	currentByPath := make(map[string]walker.Entry, len(current))
	for _, e := range current {
		currentByPath[e.RelPath] = e
	}

	out := make([]walker.Entry, 0, len(plan.Files))
	for _, planned := range plan.Files {
		e, ok := currentByPath[planned.Path]
		if !ok {
			return nil, fmt.Errorf("repo: backup plan drift: %q is missing", planned.Path)
		}
		if err := comparePlannedFile(planned, e); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

func comparePlannedFile(planned BackupPlanFile, current walker.Entry) error {
	if planned.Size != current.Size {
		return fmt.Errorf("repo: backup plan drift: %q size changed from %d to %d",
			planned.Path, planned.Size, current.Size)
	}
	mode := fmt.Sprintf("%04o", current.Mode.Perm())
	if planned.Mode != mode {
		return fmt.Errorf("repo: backup plan drift: %q mode changed from %s to %s",
			planned.Path, planned.Mode, mode)
	}
	if !planned.MTime.Equal(current.MTime) {
		return fmt.Errorf("repo: backup plan drift: %q mtime changed from %s to %s",
			planned.Path, planned.MTime.UTC().Format(time.RFC3339Nano), current.MTime.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// (safePlanPath was the predecessor of safeJoinPath in path.go.
// See path.go for the consolidated implementation shared between
// backup-plan validation and restore.)

func parsePlanMode(mode string) (os.FileMode, error) {
	n, err := strconv.ParseUint(mode, 8, 32)
	if err != nil {
		return 0, err
	}
	if n > 0o777 {
		return 0, fmt.Errorf("permission %q out of range", mode)
	}
	return os.FileMode(n), nil
}
