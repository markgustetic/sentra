package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// agentTestStubHeuristic is a deterministic heuristic for the CLI tests.
// Lives here because each test file gets its own _test stubs and we
// don't want to depend on internal types in another test file.
type agentTestStubHeuristic struct {
	name     string
	findings []heuristics.Finding
}

func (s *agentTestStubHeuristic) Name() string { return s.name }
func (s *agentTestStubHeuristic) Run(ctx context.Context, in heuristics.Input) ([]heuristics.Finding, error) {
	out := make([]heuristics.Finding, len(s.findings))
	copy(out, s.findings)
	return out, nil
}

// agentTestRetentionHeuristic emits a finding only when the
// orchestrator receives the retention policy loaded by the CLI.
type agentTestRetentionHeuristic struct {
	got heuristics.InputConfig
}

func (h *agentTestRetentionHeuristic) Name() string { return "retention_capture" }
func (h *agentTestRetentionHeuristic) Run(ctx context.Context, in heuristics.Input) ([]heuristics.Finding, error) {
	h.got = in.Config
	if in.Config.Retention.KeepLast != 1 ||
		in.Config.Retention.KeepDaily != 2 ||
		in.Config.Retention.KeepWeekly != 3 ||
		in.Config.Retention.KeepMonthly != 4 {
		return nil, nil
	}
	return []heuristics.Finding{{
		ID:       "retention-policy-seen",
		Category: "retention_drift",
		Severity: "info",
		Target:   "policy",
	}}, nil
}

// agentFixture builds an AgentDeps wired to a memory-backed repo with
// one snapshot, a fake LLM Provider, and one stub heuristic that emits
// the supplied findings.
//
// providerSteps is the script for the FakeProvider; pass a single step
// emitting "[]" for "no recommendations" or a single step emitting a
// JSON array of recs for the dry-run / apply tests.
func agentFixture(t *testing.T, providerSteps []llm.FakeStep, findings []heuristics.Finding) (AgentDeps, *blobstore.Memory, []string, *bytes.Buffer) {
	t.Helper()
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	defer r.Close()

	// Make a snapshot the agent can investigate (and that --apply can
	// prune in the prune_snapshot test).
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	s, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{Tag: "test"})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	ids := []string{s.ID}

	provider := &llm.FakeProvider{Steps: providerSteps}
	stubs := []heuristics.Heuristic{
		&agentTestStubHeuristic{name: "stub", findings: findings},
	}

	out := &bytes.Buffer{}
	deps := AgentDeps{
		RepoDeps: RepoDeps{
			NewStore: func(_ context.Context, _ *config.Config) (blobstore.Store, error) {
				return store, nil
			},
			Passphrase: func() ([]byte, error) { return []byte("hunter2"), nil },
			Stdout:     out,
		},
		Provider:   provider,
		Heuristics: stubs,
		Confirm:    func(string) (bool, error) { return true, nil },
	}
	return deps, store, ids, out
}

// TestAgentScan_DryRun: a fake Provider emits one recommendation; the
// CLI prints it and does NOT apply it. The repo state is unchanged.
func TestAgentScan_DryRun(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"none","target":"/x","severity":"info","rationale":"FYI"}]`},
	}
	deps, store, ids, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	if !strings.Contains(got, "r1") {
		t.Errorf("expected recommendation id 'r1' in output, got %q", got)
	}
	if !strings.Contains(got, "FYI") {
		t.Errorf("expected rationale in output, got %q", got)
	}

	// Snapshot should still exist — dry-run does not apply.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ID != ids[0] {
		t.Errorf("snapshots changed unexpectedly in dry-run: %+v", snaps)
	}
}

// TestAgentScan_JSON: --json emits the recommendation array as JSON.
func TestAgentScan_JSON(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"none","target":"/x","severity":"info","rationale":"hi"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var recs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &recs); err != nil {
		t.Fatalf("unmarshal: %v\noutput: %s", err, out.String())
	}
	if len(recs) != 1 {
		t.Fatalf("got %d recommendations, want 1", len(recs))
	}
	got := recs[0]
	for _, k := range []string{"id", "action", "target", "severity", "rationale"} {
		if _, ok := got[k]; !ok {
			t.Errorf("rec missing %q: %v", k, got)
		}
	}
}

func TestAgentScan_LocalOnlySkipsProvider(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x/.env",
	}
	deps, _, _, out := agentFixture(t, nil, []heuristics.Finding{finding})
	provider := &llm.FakeProvider{} // no Steps; would error if called
	deps.Provider = provider

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--local-only", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(provider.Calls) != 0 {
		t.Fatalf("provider calls = %d, want 0", len(provider.Calls))
	}
	var recs []agent.Recommendation
	if err := json.Unmarshal(out.Bytes(), &recs); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(recs) != 1 || recs[0].Action != "flag_secret" {
		t.Fatalf("local-only recs = %+v, want flag_secret", recs)
	}
}

func TestAgentScan_CategoriesFilterLocalFindings(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	findings := []heuristics.Finding{
		{ID: "f1", Category: "secrets", Severity: "critical", Target: "/secret"},
		{ID: "f2", Category: "large_files", Severity: "warn", Target: "/large"},
	}
	deps, _, _, out := agentFixture(t, nil, findings)
	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--no-llm", "--categories", "secrets", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var recs []agent.Recommendation
	if err := json.Unmarshal(out.Bytes(), &recs); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(recs) != 1 || recs[0].Target != "/secret" {
		t.Fatalf("filtered recs = %+v, want only /secret", recs)
	}
}

func TestAgentScan_RootFlagFeedsFilesystemHeuristics(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	scanRoot := t.TempDir()
	secretPath := filepath.Join(scanRoot, ".env")
	if err := os.WriteFile(secretPath, []byte("AWS_ACCESS_KEY_ID=AKIA1234567890ABCDEF\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	deps, _, _, out := agentFixture(t, nil, nil)
	deps.Heuristics = []heuristics.Heuristic{heuristics.NewSecrets()}
	deps.Provider = &llm.FakeProvider{}

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--local-only", "--root", scanRoot, "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out.String(), secretPath) {
		t.Fatalf("expected local recommendation for %s, got %s", secretPath, out.String())
	}
}

// TestAgentScan_Apply: --apply --yes --allow-wipe dispatches a
// prune_snapshot recommendation that drops the only snapshot. The
// --allow-wipe flag is required because the action would empty the
// repo (see TestAgentScan_ApplyRefusesWipe for the safety rail).
func TestAgentScan_Apply(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "retention", Severity: "warn", Target: "snap-x",
	}
	deps, store, ids, out := agentFixture(t, nil /* set after we know IDs */, []heuristics.Finding{finding})

	// We needed the snapshot ID to script the recommendation, so build
	// the FakeProvider's response now and rewire the deps.
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"prune_snapshot","target":"` + ids[0] + `","severity":"warn","rationale":"old"}]`},
	}
	deps.Provider = &llm.FakeProvider{Steps: steps}

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes", "--allow-wipe"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Snapshot should be gone.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots after apply, got %d: %+v", len(snaps), snaps)
	}

	// Output should mention applied / deleted action.
	low := strings.ToLower(out.String())
	if !strings.Contains(low, "applied") && !strings.Contains(low, "deleted") && !strings.Contains(low, "prune") {
		t.Errorf("expected applied/deleted/prune in apply output, got %q", out.String())
	}
}

// TestAgentScan_Apply_AddToIgnore: a recommendation with action
// add_to_ignore appends the target to .sentraignore.
func TestAgentScan_Apply_AddToIgnore(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "cache_dirs", Severity: "warn", Target: "node_modules/",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"add_to_ignore","target":"node_modules/","severity":"warn","rationale":"cache"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// .sentraignore should exist and contain "node_modules/".
	body, err := os.ReadFile(".sentraignore")
	if err != nil {
		t.Fatalf("read .sentraignore: %v", err)
	}
	if !strings.Contains(string(body), "node_modules/") {
		t.Errorf(".sentraignore missing 'node_modules/': %q", body)
	}
}

// TestAgentScan_Apply_FlagSecret: flag_secret is a notification-only
// action; it does not modify the repo but should mention the path.
func TestAgentScan_Apply_FlagSecret(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x/.env",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"flag_secret","target":"/x/.env","severity":"critical","rationale":"please rotate"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "/x/.env") {
		t.Errorf("expected target in output, got %q", got)
	}
}

// TestAgentScan_NoFindings: when the heuristics emit nothing, the CLI
// should print an "all clear" message and exit zero. The Provider
// should not be called.
func TestAgentScan_NoFindings(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	provider := &llm.FakeProvider{} // would error on call
	deps, _, _, out := agentFixture(t, nil, nil)
	deps.Provider = provider

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.ToLower(out.String())
	if !strings.Contains(got, "no findings") && !strings.Contains(got, "all clear") {
		t.Errorf("expected no-findings hint in output, got %q", out.String())
	}
	if len(provider.Calls) != 0 {
		t.Errorf("Provider should not be called, got %d", len(provider.Calls))
	}
}

// TestAgentScan_ConfirmDecline: --apply without --yes, with a Confirm
// that returns false, should NOT execute the action.
func TestAgentScan_ConfirmDecline(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "retention", Severity: "warn", Target: "snap-x",
	}
	deps, store, ids, out := agentFixture(t, nil, []heuristics.Finding{finding})
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"prune_snapshot","target":"` + ids[0] + `","severity":"warn","rationale":"old"}]`},
	}
	deps.Provider = &llm.FakeProvider{Steps: steps}
	deps.Confirm = func(string) (bool, error) { return false, nil }

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	// Snapshot must still exist.
	r, err := repo.Open(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()
	snaps, _ := r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Errorf("declined confirm should leave snapshot intact, got %d", len(snaps))
	}
}

// TestAgent_RegisteredOnRoot: `sentra agent scan` is reachable from
// the root command's tree.
func TestAgent_RegisteredOnRoot(t *testing.T) {
	deps := AgentDeps{
		RepoDeps: RepoDeps{
			NewStore:   func(context.Context, *config.Config) (blobstore.Store, error) { return blobstore.NewMemory(), nil },
			Passphrase: func() ([]byte, error) { return []byte("h"), nil },
			Stdout:     io.Discard,
		},
		Provider:   &llm.FakeProvider{},
		Heuristics: nil,
		Confirm:    func(string) (bool, error) { return true, nil },
	}
	root := NewRoot("v", "c", "d")
	root.AddCommand(NewAgent(deps))
	var agentCmd *struct{ ok bool }
	for _, c := range root.Commands() {
		if c.Name() == "agent" {
			agentCmd = &struct{ ok bool }{ok: true}
			// Verify scan subcommand exists.
			scanFound := false
			for _, sub := range c.Commands() {
				if sub.Name() == "scan" {
					scanFound = true
					break
				}
			}
			if !scanFound {
				t.Errorf("agent has no scan subcommand")
			}
		}
	}
	if agentCmd == nil {
		t.Fatal("agent command not registered on root")
	}
}

func TestAgentAdviseIgnore_SuggestsCacheAndLargeFiles(t *testing.T) {
	chDir(t, t.TempDir())

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatalf("mkdir cache: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("module"), 0o600); err != nil {
		t.Fatalf("write cache file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "big.bin"), bytes.Repeat([]byte("x"), 64), 0o600); err != nil {
		t.Fatalf("write large file: %v", err)
	}

	out := &bytes.Buffer{}
	cmd := NewAgent(AgentDeps{RepoDeps: RepoDeps{Stdout: out}})
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"advise-ignore", root, "--large-file-bytes", "10"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	got := out.String()
	for _, want := range []string{"node_modules/", "big.bin"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in ignore advice output, got %q", want, got)
		}
	}
}

// TestAgentScan_ApplyRefusesWipe: a single-snapshot repo whose only
// recommendation is prune_snapshot must NOT be wiped by the apply path
// without --allow-wipe. The CLI should refuse, surface a clear error
// mentioning --allow-wipe, and leave the snapshot intact.
//
// Without this guard, --apply --yes on an LLM that recommends pruning
// every snapshot silently empties the repo — same footgun the prune
// CLI plugs with --all.
func TestAgentScan_ApplyRefusesWipe(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "retention", Severity: "warn", Target: "snap-x",
	}
	deps, store, ids, out := agentFixture(t, nil, []heuristics.Finding{finding})
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"prune_snapshot","target":"` + ids[0] + `","severity":"warn","rationale":"old"}]`},
	}
	deps.Provider = &llm.FakeProvider{Steps: steps}

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error refusing to wipe the repo without --allow-wipe")
	}
	if !strings.Contains(err.Error(), "allow-wipe") {
		t.Errorf("expected error to mention --allow-wipe, got %v", err)
	}

	// Snapshot must still exist.
	r, oerr := repo.Open(context.Background(), store, []byte("hunter2"))
	if oerr != nil {
		t.Fatalf("open: %v", oerr)
	}
	defer r.Close()
	snaps, lerr := r.ListSnapshots(context.Background())
	if lerr != nil {
		t.Fatalf("list: %v", lerr)
	}
	if len(snaps) != 1 {
		t.Errorf("expected snapshot to survive refused wipe, got %d snapshots", len(snaps))
	}
}

// TestAgentScan_ApplyAllowsWipeWithFlag: with --allow-wipe explicitly
// passed, the same single-snapshot apply succeeds and the repo is
// wiped. Confirms the safety rail is opt-out, not unconditional.
func TestAgentScan_ApplyAllowsWipeWithFlag(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "retention", Severity: "warn", Target: "snap-x",
	}
	deps, store, ids, out := agentFixture(t, nil, []heuristics.Finding{finding})
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"prune_snapshot","target":"` + ids[0] + `","severity":"warn","rationale":"old"}]`},
	}
	deps.Provider = &llm.FakeProvider{Steps: steps}

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes", "--allow-wipe"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("expected success with --allow-wipe, got %v", err)
	}

	r, oerr := repo.Open(context.Background(), store, []byte("hunter2"))
	if oerr != nil {
		t.Fatalf("open: %v", oerr)
	}
	defer r.Close()
	snaps, lerr := r.ListSnapshots(context.Background())
	if lerr != nil {
		t.Fatalf("list: %v", lerr)
	}
	if len(snaps) != 0 {
		t.Errorf("expected 0 snapshots after --allow-wipe apply, got %d", len(snaps))
	}
}

// TestAgentScan_AddToIgnore_AppendsNewPattern: starting from no
// .sentraignore (or an empty one), an add_to_ignore action writes
// the target as a single line.
func TestAgentScan_AddToIgnore_AppendsNewPattern(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "cache_dirs", Severity: "warn", Target: "build/",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"add_to_ignore","target":"build/","severity":"warn","rationale":"build artifacts"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(".sentraignore")
	if err != nil {
		t.Fatalf("read .sentraignore: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(got) != 1 || got[0] != "build/" {
		t.Errorf(".sentraignore should contain exactly 'build/', got %q", body)
	}
}

// TestAgentScan_AddToIgnore_SkipsExistingPattern: when the target
// pattern is ALREADY in the file, the action must NOT append a
// duplicate. Repeated runs over the same recommendation must converge.
func TestAgentScan_AddToIgnore_SkipsExistingPattern(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	// Pre-populate .sentraignore with the exact pattern the action
	// will recommend, so the test exercises the dedupe path.
	if err := os.WriteFile(".sentraignore", []byte("node_modules/\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	finding := heuristics.Finding{
		ID: "f1", Category: "cache_dirs", Severity: "warn", Target: "node_modules/",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"add_to_ignore","target":"node_modules/","severity":"warn","rationale":"cache"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(".sentraignore")
	if err != nil {
		t.Fatalf("read .sentraignore: %v", err)
	}
	// Count non-empty trimmed lines that match the target.
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(line) == "node_modules/" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'node_modules/' line, got %d. Full body: %q", count, body)
	}
}

// TestAgentScan_AddToIgnore_HonorsTrailingWhitespace: existing entries
// may have trailing whitespace, CRLF line endings, or pure spaces;
// after trimming, an exact-pattern match is still a no-op.
func TestAgentScan_AddToIgnore_HonorsTrailingWhitespace(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	// Write the existing entry with trailing whitespace and a CRLF
	// line ending. The dedupe logic must trim before comparing.
	if err := os.WriteFile(".sentraignore", []byte("node_modules/   \r\n"), 0o600); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	finding := heuristics.Finding{
		ID: "f1", Category: "cache_dirs", Severity: "warn", Target: "node_modules/",
	}
	steps := []llm.FakeStep{
		{Text: `[{"id":"r1","action":"add_to_ignore","target":"node_modules/","severity":"warn","rationale":"cache"}]`},
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--apply", "--yes"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	body, err := os.ReadFile(".sentraignore")
	if err != nil {
		t.Fatalf("read .sentraignore: %v", err)
	}
	count := 0
	for _, line := range strings.Split(string(body), "\n") {
		if strings.TrimSpace(strings.TrimRight(line, "\r")) == "node_modules/" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly one 'node_modules/' line after dedupe of whitespace-padded existing entry, got %d. Body: %q",
			count, body)
	}
}

// TestAgentScan_BudgetExhausted_Error: the orchestrator surfaces
// ErrBudgetExhausted; the CLI should propagate it as an error rather
// than silently exiting clean.
func TestAgentScan_BudgetExhausted_Error(t *testing.T) {
	chDir(t, t.TempDir())
	writeBackupConfigFile(t, ".")

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x",
	}
	// Provider keeps asking for tools forever.
	steps := make([]llm.FakeStep, 20)
	for i := range steps {
		steps[i] = llm.FakeStep{
			ToolCalls: []llm.ToolCall{
				{ID: "loop", Name: "inspect_finding", Input: map[string]any{"id": "f1"}},
			},
		}
	}
	deps, _, _, out := agentFixture(t, steps, []heuristics.Finding{finding})

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	// Pass a tighter budget via flag so the test runs quickly.
	cmd.SetArgs([]string{"scan", "--max-tool-calls", "2"})

	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for budget exhaustion")
	}
	// Sanity: the underlying agent error is preserved (errors.Is
	// reaches through fmt.Errorf wrapping).
	if !errors.Is(err, agent.ErrBudgetExhausted) {
		t.Errorf("expected ErrBudgetExhausted, got %v", err)
	}
}

// TestAgentScan_PassesRetentionConfigToHeuristics verifies the CLI
// threads sentra.yaml's retention policy into the agent orchestrator.
// Without that wiring, the retention_drift heuristic silently no-ops
// regardless of the user's configured policy.
func TestAgentScan_PassesRetentionConfigToHeuristics(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	body := `repo:
  s3:
    bucket: ignored
retention:
  keep_last: 1
  keep_daily: 2
  keep_weekly: 3
  keep_monthly: 4
`
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte(body), 0o600); err != nil {
		t.Fatalf("write sentra.yaml: %v", err)
	}

	provider := &llm.FakeProvider{Steps: []llm.FakeStep{{Text: "[]"}}}
	deps, _, _, out := agentFixture(t, nil, nil)
	capture := &agentTestRetentionHeuristic{}
	deps.Heuristics = []heuristics.Heuristic{capture}
	deps.Provider = provider

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(provider.Calls) != 1 {
		t.Fatalf("Provider calls: got %d, want 1 (retention finding should reach the LLM)", len(provider.Calls))
	}
	if capture.got.Retention.KeepLast != 1 ||
		capture.got.Retention.KeepDaily != 2 ||
		capture.got.Retention.KeepWeekly != 3 ||
		capture.got.Retention.KeepMonthly != 4 {
		t.Fatalf("heuristic config retention = %+v, want 1/2/3/4", capture.got.Retention)
	}
}

// TestAgentScan_UsesConfigPathForPassphraseAndProvider guards the
// production wiring bug where passphrase resolution and provider
// construction read hardcoded sentra.yaml instead of the --config file
// already loaded by the command.
func TestAgentScan_UsesConfigPathForPassphraseAndProvider(t *testing.T) {
	dir := t.TempDir()
	chDir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, "sentra.yaml"), []byte("repo:\n  s3:\n    bucket: wrong\nagent:\n  model: wrong-model\n"), 0o600); err != nil {
		t.Fatalf("write default config: %v", err)
	}
	alt := filepath.Join(dir, "alt.yaml")
	if err := os.WriteFile(alt, []byte("repo:\n  s3:\n    bucket: alt-bucket\nagent:\n  model: alt-model\n"), 0o600); err != nil {
		t.Fatalf("write alt config: %v", err)
	}

	finding := heuristics.Finding{
		ID: "f1", Category: "secrets", Severity: "critical", Target: "/x",
	}
	deps, _, _, out := agentFixture(t, nil, []heuristics.Finding{finding})
	deps.Passphrase = func() ([]byte, error) {
		return nil, errors.New("legacy passphrase callback should not be used")
	}

	var passphraseBucket string
	deps.PassphraseWithConfig = func(cfg *config.Config) ([]byte, error) {
		passphraseBucket = cfg.Repo.S3.Bucket
		return []byte("hunter2"), nil
	}

	provider := &llm.FakeProvider{Steps: []llm.FakeStep{{Text: "[]"}}}
	var providerModel string
	deps.Provider = &llm.FakeProvider{}
	deps.ProviderForConfig = func(cfg *config.Config) llm.Provider {
		providerModel = cfg.Agent.Model
		return provider
	}

	cmd := NewAgent(deps)
	cmd.SetOut(out)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"scan", "--config", alt})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if passphraseBucket != "alt-bucket" {
		t.Fatalf("passphrase resolver saw bucket %q, want alt-bucket", passphraseBucket)
	}
	if providerModel != "alt-model" {
		t.Fatalf("provider factory saw model %q, want alt-model", providerModel)
	}
	if len(provider.Calls) != 1 {
		t.Fatalf("provider calls: got %d, want 1", len(provider.Calls))
	}
}
