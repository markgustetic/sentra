package tui

import "testing"

func testRegistry() *Registry {
	r := NewRegistry()
	r.Add(Command{ID: "dashboard", Title: "Dashboard", Category: "Views"})
	r.Add(Command{ID: "snapshots", Title: "Snapshots", Category: "Views"})
	r.Add(Command{ID: "diff", Title: "Diff", Category: "Views"})
	return r
}

func TestRegistry_OrderIsInsertionOrder(t *testing.T) {
	r := testRegistry()
	cmds := r.Commands()
	if len(cmds) != 3 {
		t.Fatalf("len = %d, want 3", len(cmds))
	}
	if cmds[0].ID != "dashboard" || cmds[2].ID != "diff" {
		t.Fatalf("order not preserved: %+v", cmds)
	}
}

func TestRegistry_DuplicateIDPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic on duplicate command ID")
		}
	}()
	r := testRegistry()
	r.Add(Command{ID: "dashboard", Title: "Again"})
}

// TestRegistry_FuzzyFilter: the palette's matcher. Case-insensitive
// subsequence match over Title; empty query returns everything.
func TestRegistry_FuzzyFilter(t *testing.T) {
	r := testRegistry()
	if got := r.Filter(""); len(got) != 3 {
		t.Fatalf("empty query: got %d, want 3", len(got))
	}
	if got := r.Filter("dsh"); len(got) != 1 || got[0].ID != "dashboard" {
		t.Fatalf("subsequence 'dsh': got %+v", got)
	}
	if got := r.Filter("SNAP"); len(got) != 1 || got[0].ID != "snapshots" {
		t.Fatalf("case-insensitive 'SNAP': got %+v", got)
	}
	if got := r.Filter("zzz"); len(got) != 0 {
		t.Fatalf("no-match query: got %+v", got)
	}
}

func TestRegistry_SetBadge(t *testing.T) {
	r := testRegistry()
	r.SetBadge("snapshots", "142")
	for _, c := range r.Commands() {
		if c.ID == "snapshots" && c.Badge != "142" {
			t.Fatalf("badge not set: %+v", c)
		}
	}
}
