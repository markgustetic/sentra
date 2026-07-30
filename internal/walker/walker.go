package walker

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// EntryKind distinguishes what an Entry describes. The zero value is
// KindFile so pre-existing callers that never look at the field keep
// their exact semantics (Walk only emits the other kinds behind the
// Options.IncludeNonRegular opt-in).
type EntryKind int

const (
	KindFile EntryKind = iota
	KindDir
	KindSymlink
)

// Entry is the metadata for one filesystem object emitted by Walk. It
// is the minimum a downstream chunker / encryption pipeline needs to
// do its job: where the object lives on disk, what to call it relative
// to the snapshot root, and the stat fields needed for change
// detection.
type Entry struct {
	Kind    EntryKind
	AbsPath string
	RelPath string
	// Size is the file's byte length. Zero for dirs and symlinks —
	// neither contributes content bytes to a snapshot.
	Size int64
	// Mode is the lstat mode. The bits the manifest cares about are
	// the permission bits (Mode.Perm()); type bits are carried by
	// Kind instead.
	Mode  os.FileMode
	MTime time.Time
	// LinkTarget is the symlink's target exactly as stored on disk
	// (os.Readlink output — relative or absolute, never resolved).
	// Empty for every other kind.
	LinkTarget string
}

// Options tunes the walk. Zero-value Options runs with sensible
// defaults: ".sentraignore" as the ignore filename, no cache-tag
// honoring, and one worker per logical CPU.
type Options struct {
	// IgnoreFile is the basename (or relative path) of the ignore
	// file at the walk root. Empty means ".sentraignore".
	IgnoreFile string

	// ExcludeCaches honors the CACHEDIR.TAG convention: a directory
	// containing a CACHEDIR.TAG file whose first line carries the
	// canonical signature is skipped entirely.
	ExcludeCaches bool

	// Concurrency is the number of worker goroutines that stat and
	// invoke fn. Zero means GOMAXPROCS, which is the right default
	// for stat-bound workloads on modern disks.
	Concurrency int

	// IncludeNonRegular additionally emits KindDir entries for every
	// directory under root (the root itself excluded) and KindSymlink
	// entries carrying LinkTarget. Neither is followed. Off by
	// default so file-only consumers (backup plans, agent heuristics)
	// keep their exact historical behavior; snapshot capture opts in
	// for filesystem fidelity.
	IncludeNonRegular bool
}

// defaultIgnoreFile is the filename used when Options.IgnoreFile is
// empty. Pulled out as a constant so it's grep-able from elsewhere.
const defaultIgnoreFile = ".sentraignore"

// cachedirSignature is the spec-mandated first-line magic for a
// CACHEDIR.TAG file. Anything else, even if the filename matches,
// is not a real cache marker and the directory is walked normally.
//
// Spec: https://bford.info/cachedir/
const cachedirSignature = "Signature: 8a477f597d28d172789f06886806bc55"

// Walk visits every regular file under root, calling fn for each
// non-ignored entry. fn is called concurrently from up to N goroutines
// (Options.Concurrency); callers MUST be safe for concurrent calls.
//
// Walk returns the first non-nil error from fn, or any I/O error from
// the walk itself. ctx cancellation is respected: a cancel during the
// walk surfaces as ctx.Err() (typically context.Canceled).
//
// Symlinks are not followed (treated as non-regular and silently
// skipped). Future: symlink policy.
func Walk(ctx context.Context, root string, opts Options, fn func(Entry) error) error {
	if opts.IgnoreFile == "" {
		opts.IgnoreFile = defaultIgnoreFile
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = runtime.GOMAXPROCS(0)
	}

	matcher, err := LoadIgnoreFile(filepath.Join(root, opts.IgnoreFile))
	if err != nil {
		return fmt.Errorf("walker: load ignore file: %w", err)
	}

	// Buffered enough to avoid back-pressuring the (single-threaded)
	// directory walker on every emit. A small multiple of worker
	// count is plenty: fn is generally faster than directory I/O on
	// a backup workload, but during a stat-heavy stage (e.g. cold
	// cache) the workers may briefly stall.
	jobs := make(chan string, opts.Concurrency*4)

	g, ctx := errgroup.WithContext(ctx)

	// Worker pool: each worker pulls a path off the channel, stats
	// it, and calls fn. The first non-nil error from fn (or stat)
	// cancels ctx, which causes the producer to stop emitting.
	for i := 0; i < opts.Concurrency; i++ {
		g.Go(func() error {
			for absPath := range jobs {
				// Re-check ctx between jobs so a cancel from a
				// peer worker takes effect promptly even if the
				// channel still has buffered work.
				if err := ctx.Err(); err != nil {
					return err
				}
				rel, err := filepath.Rel(root, absPath)
				if err != nil {
					return fmt.Errorf("walker: rel %q: %w", absPath, err)
				}
				rel = filepath.ToSlash(rel)
				info, err := os.Lstat(absPath)
				if err != nil {
					// File can vanish between WalkDir and Lstat
					// on a live tree; report the error rather
					// than silently dropping the entry.
					return fmt.Errorf("walker: lstat %q: %w", absPath, err)
				}
				// Belt-and-braces: WalkDir.d.Type already filters
				// non-regular entries, but a TOCTOU race could
				// still hand us a now-symlink. Skip silently.
				// Future: symlink policy.
				if !info.Mode().IsRegular() {
					continue
				}
				e := Entry{
					AbsPath: absPath,
					RelPath: rel,
					Size:    info.Size(),
					Mode:    info.Mode(),
					MTime:   info.ModTime(),
				}
				if err := fn(e); err != nil {
					return err
				}
			}
			return nil
		})
	}

	// Producer: single goroutine doing the directory walk. Closes
	// the channel when done, which lets workers drain and exit
	// cleanly. Any error from filepath.WalkDir is returned and the
	// errgroup will cancel the workers via ctx.
	g.Go(func() error {
		defer close(jobs)

		walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Honor cancellation between directory steps.
			if cerr := ctx.Err(); cerr != nil {
				return cerr
			}

			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return fmt.Errorf("walker: rel %q: %w", path, relErr)
			}
			relSlash := filepath.ToSlash(rel)

			if d.IsDir() {
				// Ignore-match on the directory short-circuits the
				// whole subtree. This is how `node_modules/` gets
				// skipped without statting every file inside.
				// We use the trailing-slash form so a directory-
				// only pattern like "build/" matches.
				if relSlash != "." && matcher.Match(relSlash+"/") {
					return fs.SkipDir
				}
				if opts.ExcludeCaches && isCacheDir(path) {
					return fs.SkipDir
				}
				if opts.IncludeNonRegular && relSlash != "." {
					// Emit the directory itself so empty dirs and
					// dir modes survive a snapshot. fn must be
					// concurrency-safe anyway (worker pool), so
					// calling it from the producer is fine.
					if err := emitNonRegular(d, KindDir, path, relSlash, "", fn); err != nil {
						return err
					}
				}
				return nil
			}

			// Skip ignored paths *before* stat-ing them. The match
			// operates on the slash-form rel path: cheap.
			if matcher.Match(relSlash) {
				return nil
			}

			// Non-regular entries: symlinks are emitted (with their
			// target, never followed) behind the opt-in; everything
			// else — devices, FIFOs, sockets — is dropped.
			if !d.Type().IsRegular() {
				if opts.IncludeNonRegular && d.Type()&fs.ModeSymlink != 0 {
					target, err := os.Readlink(path)
					if err != nil {
						if errors.Is(err, fs.ErrNotExist) {
							return nil // vanished mid-walk; skip like a vanished file
						}
						return fmt.Errorf("walker: readlink %q: %w", path, err)
					}
					if err := emitNonRegular(d, KindSymlink, path, relSlash, target, fn); err != nil {
						return err
					}
				}
				return nil
			}

			select {
			case <-ctx.Done():
				return ctx.Err()
			case jobs <- path:
			}
			return nil
		})
		// fs.SkipAll / SkipDir at the root would surface here as
		// nil; any other error (including ctx.Err) is real.
		if walkErr != nil && !errors.Is(walkErr, fs.SkipAll) {
			return walkErr
		}
		return nil
	})

	return g.Wait()
}

// emitNonRegular builds and emits a KindDir or KindSymlink Entry from
// the producer goroutine. A vanished entry (Info returning not-exist)
// is skipped silently, matching the vanished-file semantics elsewhere.
func emitNonRegular(d fs.DirEntry, kind EntryKind, absPath, relSlash, linkTarget string, fn func(Entry) error) error {
	info, err := d.Info()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("walker: info %q: %w", absPath, err)
	}
	return fn(Entry{
		Kind:       kind,
		AbsPath:    absPath,
		RelPath:    relSlash,
		Mode:       info.Mode(),
		MTime:      info.ModTime(),
		LinkTarget: linkTarget,
	})
}

// isCacheDir reports whether dir contains a CACHEDIR.TAG file whose
// first line is the spec signature. Errors (missing tag, unreadable)
// fail closed: we return false and let the walk descend normally.
func isCacheDir(dir string) bool {
	tag := filepath.Join(dir, "CACHEDIR.TAG")
	f, err := os.Open(tag) //nolint:gosec // dir is from filepath.WalkDir, not user input
	if err != nil {
		return false
	}
	defer f.Close()

	// The spec (https://bford.info/cachedir/) requires the file to
	// start with the 43-byte signature. Trailing content on the same
	// line is allowed (e.g. comma-separated comment fields some tools
	// append), so we use HasPrefix rather than equality. A 256-byte
	// buffer is generous: the canonical first line is 49 bytes
	// including the newline.
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 256), 256)
	if !scanner.Scan() {
		return false
	}
	first := strings.TrimRight(scanner.Text(), "\r")
	return strings.HasPrefix(first, cachedirSignature)
}
