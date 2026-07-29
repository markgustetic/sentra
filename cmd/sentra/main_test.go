package main

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/cli"
)

// TestIsUICommand_CoversEveryAltScreenCommand pins the rule that every
// command ending in the Bubbletea alt-screen is recognized by
// isUICommand, so ConfigureSlog discards logs instead of interleaving
// raw slog lines into the live screen buffer. The bug this guards:
// `local` launches the same TUI as `ui` but was invisible to the check,
// so `sentra local --log-level=info` corrupted the display.
func TestIsUICommand_CoversEveryAltScreenCommand(t *testing.T) {
	rootFlags := &cli.RootFlags{}
	root := cli.NewRootWithFlags("t", "t", "t", rootFlags)
	addProductionCommands(root, rootFlags, "t", "t")

	// Commands that launch the TUI (alt-screen) vs. plain-terminal ones.
	wantUI := map[string]bool{
		"ui":     true,
		"local":  true,
		"backup": false,
		"init":   false,
		"doctor": false,
	}

	find := func(name string) *cobra.Command {
		for _, c := range root.Commands() {
			if c.Name() == name {
				return c
			}
		}
		t.Fatalf("command %q not registered on root", name)
		return nil
	}

	if !isUICommand(root) {
		t.Errorf("isUICommand(bare root) = false, want true (bare sentra falls through to the TUI)")
	}
	for name, want := range wantUI {
		if got := isUICommand(find(name)); got != want {
			t.Errorf("isUICommand(%q) = %v, want %v", name, got, want)
		}
	}
}
