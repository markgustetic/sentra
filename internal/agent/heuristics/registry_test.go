package heuristics

import (
	"context"
	"errors"
	"testing"
)

// fakeHeuristic is a test double for verifying the registry's
// orchestration without needing a live walk/repo. It returns whatever
// findings/error it was constructed with.
type fakeHeuristic struct {
	name     string
	findings []Finding
	err      error
}

func (f *fakeHeuristic) Name() string { return f.name }
func (f *fakeHeuristic) Run(ctx context.Context, in Input) ([]Finding, error) {
	if f.err != nil {
		return nil, f.err
	}
	// Return a copy to mirror real heuristics, which build new slices.
	out := make([]Finding, len(f.findings))
	copy(out, f.findings)
	return out, nil
}

// TestRegistry_RunsAllHeuristics: two fakes each emit one finding; the
// registry merges both into one slice and stamps Finding.Heuristic
// from each source's Name().
func TestRegistry_RunsAllHeuristics(t *testing.T) {
	a := &fakeHeuristic{
		name: "fake_a",
		findings: []Finding{
			{ID: "id-a", Category: "cat_a", Severity: SeverityInfo, Target: "/a"},
		},
	}
	b := &fakeHeuristic{
		name: "fake_b",
		findings: []Finding{
			{ID: "id-b", Category: "cat_b", Severity: SeverityWarn, Target: "/b"},
		},
	}
	reg := NewRegistry(a, b)

	got, err := reg.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d findings, want 2: %+v", len(got), got)
	}
	// Findings are sorted by ID; "id-a" < "id-b".
	if got[0].ID != "id-a" || got[0].Heuristic != "fake_a" {
		t.Errorf("findings[0] = %+v, want id=id-a heuristic=fake_a", got[0])
	}
	if got[1].ID != "id-b" || got[1].Heuristic != "fake_b" {
		t.Errorf("findings[1] = %+v, want id=id-b heuristic=fake_b", got[1])
	}
}

// TestRegistry_DedupesFindings: two heuristics emit findings that
// share an ID. Only one survives the dedup pass.
func TestRegistry_DedupesFindings(t *testing.T) {
	dup := Finding{ID: "shared", Category: "x", Target: "/t"}
	a := &fakeHeuristic{name: "fake_a", findings: []Finding{dup}}
	b := &fakeHeuristic{name: "fake_b", findings: []Finding{dup}}
	reg := NewRegistry(a, b)

	got, err := reg.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d findings, want 1 (dedup): %+v", len(got), got)
	}
}

// TestRegistry_PropagatesHeuristicErrors: a fake that returns an error
// causes Run to return that error and discard partial results.
func TestRegistry_PropagatesHeuristicErrors(t *testing.T) {
	want := errors.New("boom")
	a := &fakeHeuristic{name: "fake_a", err: want}
	b := &fakeHeuristic{
		name:     "fake_b",
		findings: []Finding{{ID: "id-b", Target: "/b"}},
	}
	reg := NewRegistry(a, b)

	got, err := reg.Run(context.Background(), Input{})
	if !errors.Is(err, want) {
		t.Fatalf("Run err: got %v, want %v", err, want)
	}
	if got != nil {
		t.Errorf("expected nil findings on error, got %+v", got)
	}
}

// TestRegistry_RegisterAddsHeuristic: NewRegistry() + Register works
// the same as NewRegistry(h).
func TestRegistry_RegisterAddsHeuristic(t *testing.T) {
	a := &fakeHeuristic{
		name:     "fake_a",
		findings: []Finding{{ID: "id-a", Target: "/a"}},
	}
	reg := NewRegistry()
	reg.Register(a)
	got, err := reg.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 1 || got[0].ID != "id-a" {
		t.Fatalf("got %+v, want one finding id-a", got)
	}
}

// TestRegistry_EmptyRegistry: zero heuristics, zero findings, no error.
func TestRegistry_EmptyRegistry(t *testing.T) {
	reg := NewRegistry()
	got, err := reg.Run(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d findings, want 0", len(got))
	}
}
