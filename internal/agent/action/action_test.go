package action

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestRegistry_Lookup covers the core registry contract: registered
// handlers are found, unregistered names return false. Locks the map
// semantics so a future refactor to e.g. a slice + linear scan
// preserves the public behavior.
func TestRegistry_Lookup(t *testing.T) {
	r := NewDefaultRegistry()
	for _, want := range []Action{PruneSnapshot, AddToIgnore, FlagSecret, None} {
		if _, ok := r.Lookup(want); !ok {
			t.Errorf("Lookup(%q) = _, false; want true", want)
		}
	}
	if h, ok := r.Lookup("definitely-not-an-action"); ok {
		t.Errorf("Lookup(unknown) = %v, true; want _, false", h)
	}
}

// TestRegistry_Names returns sorted, deduped, complete vocabulary.
// Used by callers (orchestrator, error messages) so the contract
// matters: stable order so prompt-fragment cache keys don't shift,
// every registered handler exposed exactly once.
func TestRegistry_Names(t *testing.T) {
	r := NewDefaultRegistry()
	got := r.Names()
	want := []Action{AddToIgnore, FlagSecret, None, PruneSnapshot} // alphabetical
	if len(got) != len(want) {
		t.Fatalf("Names: got %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Names[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
}

// TestRegistry_PromptFragment is a smoke test for the format the
// orchestrator splices into its system prompt. We don't lock the
// exact bytes — that would be brittle as descriptions evolve — but
// we do assert each registered verb appears, the verb appears
// quoted (so the LLM can copy it), and the fragment is multi-line.
func TestRegistry_PromptFragment(t *testing.T) {
	r := NewDefaultRegistry()
	frag := r.PromptFragment()
	for _, name := range r.Names() {
		quoted := `"` + string(name) + `"`
		if !strings.Contains(frag, quoted) {
			t.Errorf("prompt fragment missing %s:\n%s", quoted, frag)
		}
	}
	if !strings.Contains(frag, "\n") {
		t.Errorf("prompt fragment should be multi-line, got: %q", frag)
	}
}

// TestRegistry_DispatchUnknownAction surfaces a clear error when
// the model emits a verb the dispatcher can't find. The message
// must include the unknown verb so the operator can debug what
// went wrong.
func TestRegistry_DispatchUnknownAction(t *testing.T) {
	r := NewDefaultRegistry()
	err := r.Dispatch(context.Background(), Env{}, "totally_made_up", "id1", "tgt", "warn", "why")
	if err == nil {
		t.Fatal("expected error for unknown action, got nil")
	}
	if !strings.Contains(err.Error(), "totally_made_up") {
		t.Errorf("error %q should name the unknown verb", err.Error())
	}
}

// TestRegistry_DuplicatePanics asserts the documented contract that
// two handlers with the same Name panic at registration. A
// registration-time panic is the right failure mode: silent
// overwrite would mean the second registration's handler runs
// instead of the first, with no compile-time signal.
func TestRegistry_DuplicatePanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on duplicate Name, got none")
		}
	}()
	NewRegistry(NoneHandler{}, NoneHandler{}) // two of the same name
}

// TestNoneHandler_PrintsAndReturns confirms the no-op verb produces
// exactly one line of stdout per Apply and never errors.
func TestNoneHandler_PrintsAndReturns(t *testing.T) {
	buf := &bytes.Buffer{}
	env := Env{Stdout: buf}
	if err := (NoneHandler{}).Apply(context.Background(), env, "f1", "/some/path", "info", "see manifest"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "f1") {
		t.Errorf("output should include finding ID f1: %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("output should be exactly one line, got %d:\n%s",
			strings.Count(out, "\n"), out)
	}
}

// TestFlagSecretHandler_NotificationOnly verifies the safety rail:
// flag_secret never mutates state. We give it a nil Repo (so any
// repo-mutating attempt would panic loudly) and an Apply runs to
// completion.
func TestFlagSecretHandler_NotificationOnly(t *testing.T) {
	buf := &bytes.Buffer{}
	env := Env{Stdout: buf} // Repo is nil — handler must not touch it
	err := (FlagSecretHandler{}).Apply(context.Background(), env,
		"f9", "AKIA...", "critical", "found in env file")
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "AKIA") {
		t.Errorf("output should mention the target: %q", out)
	}
	// Severity should appear so the operator knows urgency.
	if !strings.Contains(out, "CRITICAL") {
		t.Errorf("output should surface the severity: %q", out)
	}
}

func TestPruneSnapshotHandler_RequiresRepo(t *testing.T) {
	err := (PruneSnapshotHandler{}).Apply(context.Background(), Env{Stdout: &bytes.Buffer{}},
		"r1", "snapshot-id", "warn", "")
	if err == nil {
		t.Fatal("expected error for missing repo")
	}
	if !strings.Contains(err.Error(), "no repo") {
		t.Fatalf("error = %v, want no repo context", err)
	}
}

func TestPruneSnapshotHandler_PrunesSnapshotAndRunsGC(t *testing.T) {
	ctx := context.Background()
	r, ids := newActionTestRepo(t, []string{"first", "second"})

	buf := &bytes.Buffer{}
	env := Env{
		Repo:        r,
		Stdout:      buf,
		FormatBytes: func(n int64) string { return fmt.Sprintf("%dB", n) },
	}
	if err := (PruneSnapshotHandler{}).Apply(ctx, env, "rec-1", ids[0], "warn", "old"); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ID != ids[1] {
		t.Fatalf("snapshots after prune = %+v, want only %s", snaps, ids[1])
	}
	out := buf.String()
	for _, want := range []string{"rec-1", ids[0], "pruned", "reclaimed"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
}

func TestPruneSnapshotHandler_AllowsLastSnapshotDelete(t *testing.T) {
	ctx := context.Background()
	r, ids := newActionTestRepo(t, []string{"only"})

	buf := &bytes.Buffer{}
	env := Env{Repo: r, Stdout: buf}
	if err := (PruneSnapshotHandler{}).Apply(ctx, env, "rec-last", ids[0], "warn", "old"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatalf("list snapshots: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("snapshots after last prune = %+v, want none", snaps)
	}
	out := buf.String()
	if !strings.Contains(out, "last snapshot") {
		t.Fatalf("output should explain last-snapshot GC skip: %q", out)
	}
}

func TestPruneSnapshotHandler_WrapsDeleteError(t *testing.T) {
	ctx := context.Background()
	r, _ := newActionTestRepo(t, []string{"only"})

	err := (PruneSnapshotHandler{}).Apply(ctx, Env{Repo: r, Stdout: &bytes.Buffer{}},
		"rec-missing", "20000102T030405Z-missing", "warn", "")
	if err == nil {
		t.Fatal("expected delete error for missing snapshot")
	}
	if !strings.Contains(err.Error(), "delete snapshot") {
		t.Fatalf("error = %v, want delete snapshot wrapper", err)
	}
}

// TestAddToIgnoreHandler_AppendsAndDedupes covers the idempotent
// write contract: a fresh target lands on disk, a repeat target
// is skipped without writing, and the file uses LF line endings
// regardless of pre-existing content.
func TestAddToIgnoreHandler_AppendsAndDedupes(t *testing.T) {
	dir := t.TempDir()
	env := Env{Stdout: &bytes.Buffer{}, Cwd: dir}
	h := AddToIgnoreHandler{}

	if err := h.Apply(context.Background(), env, "f1", "*.tmp", "warn", ""); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := h.Apply(context.Background(), env, "f2", "*.tmp", "warn", ""); err != nil {
		t.Fatalf("second Apply (duplicate): %v", err)
	}
	if err := h.Apply(context.Background(), env, "f3", "node_modules/", "warn", ""); err != nil {
		t.Fatalf("third Apply: %v", err)
	}

	body, err := os.ReadFile(filepath.Join(dir, ignoreFileName))
	if err != nil {
		t.Fatalf("read .sentraignore: %v", err)
	}
	got := string(body)
	want := []string{"*.tmp", "node_modules/"}
	for _, pat := range want {
		if !strings.Contains(got, pat) {
			t.Errorf("file should contain %q: %q", pat, got)
		}
	}
	if strings.Count(got, "*.tmp") != 1 {
		t.Errorf("dedupe failed: *.tmp appears %d times in %q",
			strings.Count(got, "*.tmp"), got)
	}
}

// TestAddToIgnoreHandler_SkipsEmptyTarget catches the "model emits
// blank target" edge case. The handler logs and returns nil rather
// than corrupting .sentraignore with an empty pattern.
func TestAddToIgnoreHandler_SkipsEmptyTarget(t *testing.T) {
	dir := t.TempDir()
	buf := &bytes.Buffer{}
	env := Env{Stdout: buf, Cwd: dir}
	if err := (AddToIgnoreHandler{}).Apply(context.Background(), env, "f1", "   ", "warn", ""); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(buf.String(), "empty target") {
		t.Errorf("stdout should explain skip: %q", buf.String())
	}
	// The file should not exist (or should be empty).
	if body, err := os.ReadFile(filepath.Join(dir, ignoreFileName)); err == nil && len(body) > 0 {
		t.Errorf("blank target should not write to file; got %q", body)
	}
}

// TestAddToIgnoreHandler_RejectsEmbeddedNewline: a single verb must
// produce a single pattern line. An LLM-supplied target with an embedded
// newline would otherwise inject extra .sentraignore lines — including a
// "!"-negation that silently re-includes a file the operator meant to
// exclude — and defeat the per-line dedup.
func TestAddToIgnoreHandler_RejectsEmbeddedNewline(t *testing.T) {
	dir := t.TempDir()
	env := Env{Stdout: &bytes.Buffer{}, Cwd: dir}

	target := "build/\n!build/keep.bin"
	err := (AddToIgnoreHandler{}).Apply(context.Background(), env, "rec-x", target, "warn", "why")
	if err == nil {
		t.Fatal("expected Apply to reject a target with an embedded newline")
	}

	// No injected pattern line may have reached the file.
	body, _ := os.ReadFile(filepath.Join(dir, ignoreFileName))
	if strings.Contains(string(body), "!build/keep.bin") {
		t.Fatalf(".sentraignore contains an injected negation line: %q", string(body))
	}
}

// TestEnv_FormatBytesFallback covers the optional formatter: when
// none is supplied, the default fallback produces a sane string
// (used by handler stdout when ui isn't wired up, e.g. in tests
// that don't bring in the CLI).
func TestEnv_FormatBytesFallback(t *testing.T) {
	env := Env{}
	if got := env.formatBytes(1024); got != "1024 bytes" {
		t.Errorf("default formatter: got %q, want %q", got, "1024 bytes")
	}
	custom := func(n int64) string { return fmt.Sprintf("formatted-%d", n) }
	env = Env{FormatBytes: custom}
	if got := env.formatBytes(42); got != "formatted-42" {
		t.Errorf("custom formatter not used: got %q", got)
	}
}

// errorHandler is a Handler that returns a sentinel error. Used by
// dispatch tests below to confirm the registry's error-passthrough
// contract.
type errorHandler struct {
	name Action
	err  error
}

func (e errorHandler) Name() Action      { return e.name }
func (errorHandler) Description() string { return "test handler" }
func (e errorHandler) Apply(ctx context.Context, _ Env, _, _, _, _ string) error {
	return e.err
}

// TestRegistry_DispatchPropagatesError makes the error-handling
// contract explicit: a handler's error is returned verbatim (no
// wrapping) so callers' errors.Is checks work against any sentinel
// the handler chose to expose.
func TestRegistry_DispatchPropagatesError(t *testing.T) {
	sentinel := errors.New("custom-handler-error")
	r := NewRegistry(errorHandler{name: "test_action", err: sentinel})
	err := r.Dispatch(context.Background(), Env{}, "test_action", "id", "target", "info", "")
	if !errors.Is(err, sentinel) {
		t.Errorf("Dispatch did not propagate handler error: got %v", err)
	}
}

func newActionTestRepo(t *testing.T, contents []string) (*repo.Repo, []string) {
	t.Helper()
	ctx := context.Background()
	r, err := repo.Init(ctx, blobstore.NewMemory(), []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo init: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })

	ids := make([]string, 0, len(contents))
	for i, body := range contents {
		root := t.TempDir()
		path := filepath.Join(root, fmt.Sprintf("file-%d.txt", i))
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write fixture: %v", err)
		}
		snap, err := r.CreateSnapshot(ctx, root, repo.SnapshotOptions{Tag: fmt.Sprintf("snap-%d", i)})
		if err != nil {
			t.Fatalf("create snapshot: %v", err)
		}
		ids = append(ids, snap.ID)
	}
	return r, ids
}
