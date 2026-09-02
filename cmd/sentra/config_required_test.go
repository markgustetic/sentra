package main

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/cli"
)

// TestExplicitMissingConfig_FailsEveryCommand pins the rule over the whole
// production command tree: every command that takes --config must refuse a
// path that does not exist — naming it — and must not create it.
// config.Load tolerates a missing file (discovery and env-only loads depend
// on that), so the guard is applied per command in front of the load, and
// walking the real tree is what stops a new command from inheriting the
// tolerance unnoticed: a --config-bearing command with no row below fails
// this test until it is added.
//
// Exempt: ui and setup host the first-run wizard, the one flow that creates
// the file, so their --config may name a path that does not exist yet. init
// has no --config (it writes ./sentra.yaml only) and neither does local
// (.sentra-local.yaml is programmatic).
func TestExplicitMissingConfig_FailsEveryCommand(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	// Nothing below may reach a store: an inherited bucket override would
	// turn a regression into a network call instead of a clean failure.
	t.Setenv("SENTRA_REPO__S3__BUCKET", "")

	exempt := map[string]bool{"sentra ui": true, "sentra setup": true}
	// Positional arguments (and required flags) each command needs to get
	// past cobra's Args check and into its run body, keyed by CommandPath.
	rows := map[string][]string{
		"sentra backup":              {dir},
		"sentra backup plan":         {dir},
		"sentra backup apply":        {filepath.Join(dir, "plan.json")},
		"sentra snapshots":           {},
		"sentra ls":                  {"latest"},
		"sentra pin":                 {"latest"},
		"sentra unpin":               {"latest"},
		"sentra stats":               {},
		"sentra check":               {},
		"sentra doctor":              {},
		"sentra recovery-kit":        {},
		"sentra restore":             {"latest", filepath.Join(dir, "out")},
		"sentra diff":                {"a", "b"},
		"sentra prune":               {},
		"sentra policy add":          {"nightly", "--path", dir},
		"sentra policy list":         {},
		"sentra policy show":         {"nightly"},
		"sentra policy remove":       {"nightly"},
		"sentra policy run":          {"nightly"},
		"sentra schedule install":    {"nightly"},
		"sentra schedule status":     {"nightly"},
		"sentra schedule uninstall":  {"nightly"},
		"sentra password":            {},
		"sentra password forget":     {},
		"sentra mcp":                 {},
		"sentra agent scan":          {},
		"sentra agent advise-ignore": {dir},
	}

	newRoot := func() *cobra.Command {
		rootFlags := &cli.RootFlags{}
		root := cli.NewRootWithFlags("t", "t", "t", rootFlags)
		configureRootLogging(root, rootFlags)
		addProductionCommands(root, rootFlags, "t", "t")
		root.SetOut(io.Discard)
		root.SetErr(io.Discard)
		return root
	}

	// Every runnable command that can see a --config flag, its own or a
	// parent's persistent one.
	registered := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() && c.Flag("config") != nil {
			registered[c.CommandPath()] = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newRoot())

	for path := range rows {
		if !registered[path] {
			t.Errorf("stale row %q: no such --config command is registered", path)
		}
	}
	for path := range registered {
		if exempt[path] {
			continue
		}
		args, ok := rows[path]
		if !ok {
			t.Errorf("%q takes --config but has no row here — it must fail fast on a missing explicit path; add it", path)
			continue
		}
		t.Run(strings.TrimPrefix(path, "sentra "), func(t *testing.T) {
			missing := filepath.Join(dir, "nope", strings.ReplaceAll(path, " ", "_"), "sentra.yaml")
			argv := append(strings.Fields(strings.TrimPrefix(path, "sentra ")), args...)
			argv = append(argv, "--config", missing)

			root := newRoot()
			root.SetArgs(argv)
			err := root.Execute()
			if err == nil {
				t.Fatalf("%s --config %s: want an error, got nil", path, missing)
			}
			if !strings.Contains(err.Error(), missing) {
				t.Errorf("%s: error must name the missing path: %v", path, err)
			}
			if _, statErr := os.Stat(missing); statErr == nil {
				t.Errorf("%s created %s", path, missing)
			}
		})
	}
}
