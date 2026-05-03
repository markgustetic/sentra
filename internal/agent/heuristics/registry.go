package heuristics

import (
	"context"
	"sort"

	"golang.org/x/sync/errgroup"
)

// Registry holds a set of heuristics and runs them concurrently over a
// shared Input. The zero Registry is unusable — call NewRegistry (or
// build one and Register manually) before Run.
//
// Registry is not safe for concurrent registration with concurrent
// Run; callers should set up the Registry once and then run it from
// any number of goroutines. Run itself is safe for concurrent calls
// because it doesn't mutate the heuristics slice.
type Registry struct {
	heuristics []Heuristic
}

// NewRegistry constructs a Registry pre-populated with the supplied
// heuristics. Variadic so call sites read naturally:
//
//	r := heuristics.NewRegistry(secrets.New(), large.New(...))
func NewRegistry(hs ...Heuristic) *Registry {
	r := &Registry{}
	r.heuristics = append(r.heuristics, hs...)
	return r
}

// Register adds h to the registry. Safe to call only before Run; not
// guarded by a mutex because the registry's intended usage is "build
// once, run many."
func (r *Registry) Register(h Heuristic) {
	r.heuristics = append(r.heuristics, h)
}

// Run invokes every registered heuristic concurrently using errgroup.
// Each heuristic's findings are collected, the source heuristic's
// Name() is stamped into Finding.Heuristic, and results are
// deduplicated by Finding.ID before return.
//
// The first non-nil error from any heuristic short-circuits the
// errgroup, cancels in-flight peers via the derived context, and is
// returned to the caller. Findings collected before the error are
// discarded — the caller should treat a Run error as "scan failed,"
// not "partial results."
//
// Output ordering is deterministic: findings are sorted by ID. That
// matters for golden tests in downstream packages that assert on the
// findings slice without having to sort it themselves.
func (r *Registry) Run(ctx context.Context, in Input) ([]Finding, error) {
	g, ctx := errgroup.WithContext(ctx)

	// Per-heuristic result slices, indexed by registration order. Using
	// a fixed-size slice instead of a channel keeps memory bounded and
	// avoids the "did all goroutines finish" sync dance.
	per := make([][]Finding, len(r.heuristics))

	for i, h := range r.heuristics {
		i, h := i, h // capture
		g.Go(func() error {
			out, err := h.Run(ctx, in)
			if err != nil {
				return err
			}
			// Stamp Heuristic on each finding from the source's Name().
			// Doing it here (registry-side) means individual heuristics
			// don't have to remember to set it — and can't accidentally
			// lie about which heuristic produced a finding.
			name := h.Name()
			for j := range out {
				out[j].Heuristic = name
			}
			per[i] = out
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Deduplicate by Finding.ID. Two heuristics may legitimately spot
	// the same condition (e.g. a large file that's also stale); we
	// keep the first one seen so dedup is order-stable given a stable
	// registration order. Sorting after dedup keeps the final slice
	// deterministic regardless of goroutine scheduling.
	seen := make(map[string]struct{})
	var all []Finding
	for _, group := range per {
		for _, f := range group {
			if _, dup := seen[f.ID]; dup {
				continue
			}
			seen[f.ID] = struct{}{}
			all = append(all, f)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	return all, nil
}
