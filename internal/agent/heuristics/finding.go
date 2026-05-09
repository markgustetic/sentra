// Package heuristics implements local rules that scan a walked tree,
// snapshot history, and live-blob set, producing structured Finding
// records the LLM agent can later triage.
//
// Heuristics are intentionally narrow and side-effect free: each one
// reads from Input, returns []Finding (and possibly an error), never
// mutates the repo. Findings carry paths/sizes/types/mtimes and small
// structured Details, but never file *contents* — keeping the LLM
// provider on a strict no-content diet.
package heuristics

import (
	"context"
	"crypto/sha1" //nolint:gosec // not a security primitive; stable IDs only
	"encoding/hex"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/walker"
)

// Severity values used across all heuristics. Stringly-typed because
// the LLM tool contract surfaces them as plain strings anyway; using
// a typed constant here adds friction without help.
const (
	SeverityInfo     = "info"
	SeverityWarn     = "warn"
	SeverityCritical = "critical"
)

// Finding is one structured signal emitted by a Heuristic. ID is a
// stable hash over Category+Target so downstream consumers (the LLM
// `inspect_finding(id)` tool) get the same handle across runs.
//
// Heuristic is populated by Registry.Run from the source heuristic's
// Name(); individual heuristics should leave it zero — the registry
// owns it.
//
// Details is intentionally typed as map[string]any so each heuristic
// can attach whatever structured context makes sense (size, mtime,
// pattern name, line number, ...) without forcing a sum type. The
// LLM tools marshal it to JSON.
type Finding struct {
	ID        string         `json:"id"`
	Category  string         `json:"category"`
	Severity  string         `json:"severity"`
	Target    string         `json:"target"`
	Details   map[string]any `json:"details,omitempty"`
	Heuristic string         `json:"heuristic"`
}

// Heuristic is the contract every rule implements. Run is expected to
// honor ctx cancellation and return promptly when ctx.Err() is set.
//
// Concurrency: Registry.Run calls Run from multiple goroutines (one
// per heuristic), but each individual Heuristic's Run is invoked once
// per registry call — implementations don't need to be reentrant, but
// they MUST be safe to call concurrently with other heuristics over
// the same Input (since Input is shared by reference).
type Heuristic interface {
	Name() string
	Run(ctx context.Context, in Input) ([]Finding, error)
}

// Input bundles every read-only signal a heuristic might want to look
// at. Heuristics pick the fields they care about and ignore the rest;
// the registry passes the same Input to every heuristic.
//
// LiveBlobs is keyed by the chunk-key form ("data/<aa>/<hex>") to
// match what blobstore.List returns and what GC computes — that lets
// orphan_blobs do a direct map lookup against the store listing
// without a translation step.
type Input struct {
	Walked    []walker.Entry
	Snapshots []repo.SnapshotInfo
	LiveBlobs map[string]struct{}
	Repo      *repo.Repo
	Config    InputConfig
}

// InputConfig carries thresholds and policy that individual heuristics
// consult. Zero values are NOT valid defaults for every field — the
// registry caller is expected to populate them (the CLI sources them
// from the loaded sentra.yaml). Each heuristic that needs a default
// falls back to a documented constant when the field is zero.
type InputConfig struct {
	// LargeFileBytes is the byte threshold above which the large_files
	// heuristic flags a file. Zero falls back to DefaultLargeFileBytes.
	LargeFileBytes int64

	// StaleDays is the age threshold (in days) past which the
	// stale_paths heuristic flags a file. Zero falls back to
	// DefaultStaleDays.
	StaleDays int

	// Retention is the policy fed to repo.PlanRetention by the
	// retention_drift heuristic. The zero RetentionPolicy keeps every
	// snapshot, which means retention_drift never fires — a sensible
	// no-op when the user hasn't configured retention.
	Retention repo.RetentionPolicy

	// IgnoreCacheDirNames optionally overrides the built-in cache-name
	// list used by cache_dirs. Empty means "use the defaults".
	IgnoreCacheDirNames []string
}

// makeFindingID returns a stable 16-hex-char ID derived from
// category+target. Stability matters because the LLM tools keep
// findings keyed by ID across runs (so a user can refer to "finding
// abc123" in conversation and the agent can look it up).
//
// SHA-1 is fine here — this is a stable hash, not a security
// primitive. Truncating to 16 hex chars (64 bits) keeps IDs short for
// CLI display while staying collision-resistant for the small N of
// findings a single repo produces.
func makeFindingID(category, target string) string {
	h := sha1.New() //nolint:gosec // stable hash, not security
	h.Write([]byte(category))
	h.Write([]byte("|"))
	h.Write([]byte(target))
	sum := h.Sum(nil)
	return hex.EncodeToString(sum)[:16]
}
