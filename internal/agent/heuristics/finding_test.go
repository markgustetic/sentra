package heuristics

import "testing"

// TestFindingID_Stable verifies that the same (category, target) pair
// produces the same ID across calls. The LLM agent in Phase 11
// references findings by ID across runs, so any drift here would
// break that contract.
func TestFindingID_Stable(t *testing.T) {
	a := makeFindingID("secrets", "/tmp/.env")
	b := makeFindingID("secrets", "/tmp/.env")
	if a != b {
		t.Fatalf("makeFindingID not stable: %q vs %q", a, b)
	}
	if got, want := len(a), 16; got != want {
		t.Fatalf("ID length: got %d, want %d", got, want)
	}
}

// TestFindingID_DifferentInputs verifies that different inputs produce
// different IDs. This is a sanity check, not a collision-resistance
// proof — but if two obviously-different findings collide here, our
// hash inputs are wrong (e.g. forgot the separator).
func TestFindingID_DifferentInputs(t *testing.T) {
	cases := []struct {
		cat, target string
	}{
		{"secrets", "/tmp/a"},
		{"secrets", "/tmp/b"},
		{"large_files", "/tmp/a"},
		{"large_files", "/tmp/b"},
	}
	seen := make(map[string]struct{}, len(cases))
	for _, c := range cases {
		id := makeFindingID(c.cat, c.target)
		if _, dup := seen[id]; dup {
			t.Fatalf("collision on (%q, %q): id=%q", c.cat, c.target, id)
		}
		seen[id] = struct{}{}
	}
}

// TestFindingID_SeparatorMatters verifies the "|" separator does its
// job: ("ab", "cd") and ("a", "bcd") must produce different IDs.
// Without the separator, both would hash the concatenation "abcd".
func TestFindingID_SeparatorMatters(t *testing.T) {
	a := makeFindingID("ab", "cd")
	b := makeFindingID("a", "bcd")
	if a == b {
		t.Fatalf("separator not effective: both produce %q", a)
	}
}
