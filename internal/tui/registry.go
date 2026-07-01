package tui

import (
	"fmt"
	"strings"
)

// Command is one entry in the app-wide command registry: a navigable
// view (Phase 1) or an executable action (Phase 2). The registry is
// the single source of truth for both the sidebar and the palette, so
// the two can never drift.
type Command struct {
	// ID is the stable identifier ("dashboard", "prune"). Unique.
	ID string
	// Title is the human label shown in sidebar and palette.
	Title string
	// Category groups palette results ("Views", "Operations").
	Category string
	// Badge is a short live annotation rendered after the title in
	// the sidebar (e.g. agent findings count). Empty hides it.
	Badge string
}

// badgeMsg updates a command's badge. Views emit it from their Update
// (e.g. the agent view after a scan completes); App routes it to the
// registry so the sidebar repaints with the new count.
type badgeMsg struct {
	id    string
	badge string
}

// Registry holds the ordered command list. Not concurrency-safe by
// design: all mutation happens on the Bubbletea update loop.
type Registry struct {
	order []string
	byID  map[string]*Command
}

func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]*Command)}
}

// Add registers a command. Duplicate IDs are a programmer error —
// they'd silently shadow a sidebar entry — so Add panics.
func (r *Registry) Add(c Command) {
	if _, dup := r.byID[c.ID]; dup {
		panic(fmt.Sprintf("tui: duplicate command ID %q", c.ID))
	}
	r.order = append(r.order, c.ID)
	cc := c
	r.byID[c.ID] = &cc
}

// Commands returns the commands in registration order (copies).
func (r *Registry) Commands() []Command {
	out := make([]Command, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, *r.byID[id])
	}
	return out
}

// Filter returns commands whose Title matches query as a case-
// insensitive subsequence. Empty query matches everything. This is
// the palette's matcher — deliberately simple, no external dep.
func (r *Registry) Filter(query string) []Command {
	if query == "" {
		return r.Commands()
	}
	q := strings.ToLower(query)
	var out []Command
	for _, id := range r.order {
		c := r.byID[id]
		if isSubsequence(q, strings.ToLower(c.Title)) {
			out = append(out, *c)
		}
	}
	return out
}

// SetBadge updates a command's badge; unknown IDs are ignored (a view
// may emit a badge before registration in tests).
func (r *Registry) SetBadge(id, badge string) {
	if c, ok := r.byID[id]; ok {
		c.Badge = badge
	}
}

// isSubsequence reports whether every rune of needle appears in order
// within haystack.
func isSubsequence(needle, haystack string) bool {
	if needle == "" {
		return true
	}
	n := []rune(needle)
	i := 0
	for _, h := range haystack {
		if h == n[i] {
			i++
			if i == len(n) {
				return true
			}
		}
	}
	return false
}
