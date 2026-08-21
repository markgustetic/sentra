package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// policiesDeps writes a sentra.yaml with two named policies to a temp dir
// and returns Deps wired with ConfigPath pointing at it (plus an optional
// repo for the RUN flow). The view hydrates by loading ConfigPath, mirror-
// ing how PruneView hydrates from the repo.
func policiesDeps(t *testing.T, r *repo.Repo) (Deps, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:    []string{"/data/alpha"},
		Tags:     []string{"nightly"},
		Schedule: config.PolicySchedule{Cadence: "daily", At: "03:00"},
		AfterBackup: config.PolicyAfterBackup{
			Check: true,
			Prune: "off",
		},
	}
	cfg.Policies["beta"] = config.PolicyConfig{
		Paths:    []string{"/data/beta"},
		Schedule: config.PolicySchedule{Cadence: "manual"},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Deps{Repo: r, Config: &cfg, ConfigPath: path}, path
}

func TestPoliciesView_HydratesFromConfigPath(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	if len(v.names) != 2 || v.names[0] != "alpha" || v.names[1] != "beta" {
		t.Fatalf("names = %v, want [alpha beta] (sorted)", v.names)
	}
	out := v.View()
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("picker must list both policies:\n%s", out)
	}
}

func TestPoliciesView_MissingConfigPathShowsPlaceholder(t *testing.T) {
	v := NewPoliciesView(Deps{})
	if v.loadErr == "" {
		t.Fatal("empty deps must set a load error")
	}
	if !strings.Contains(v.View(), "no config") {
		t.Errorf("view must surface the missing-config placeholder:\n%s", v.View())
	}
}

func TestPoliciesView_InlineDetailShowsSelectedPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Selection starts at index 0 (alpha); its schedule + tag render inline.
	out := v.View()
	if !strings.Contains(out, "daily@03:00") {
		t.Errorf("detail must show alpha's schedule shorthand:\n%s", out)
	}
	if !strings.Contains(out, "/data/alpha") {
		t.Errorf("detail must show alpha's path:\n%s", out)
	}
	// Down moves selection to beta; its manual schedule renders.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown})
	v = m.(PoliciesView)
	if v.selected != 1 {
		t.Fatalf("selected = %d, want 1 after down", v.selected)
	}
	if out := v.View(); !strings.Contains(out, "/data/beta") {
		t.Errorf("detail must follow selection to beta:\n%s", out)
	}
}

func TestPoliciesView_RemoveRequiresConfirm(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// Pressing 'd' pushes a simple ConfirmModal and does NOT touch the file.
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd == nil {
		t.Fatal("d must request a confirmation modal")
	}
	msg := cmd()
	push, ok := msg.(pushModalMsg)
	if !ok {
		t.Fatalf("expected pushModalMsg, got %#v", msg)
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("remove must use the simple ConfirmModal, got %T", push.modal)
	}
	// File is untouched: alpha still present.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; !ok {
		t.Fatal("remove must not delete before confirmation")
	}
}

func TestPoliciesView_RemoveConfirmedRewritesConfigAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	// selected == 0 == alpha. Arm the modal, then confirm.
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	v = m.(PoliciesView)
	m, cmd := v.Update(confirmedMsg{id: policyRemoveConfirmID})
	v = m.(PoliciesView)
	// The write is done synchronously in a plain tea.Cmd (no op guard).
	if cmd != nil {
		if _, ok := cmd().(startOpMsg); ok {
			t.Fatal("remove must NOT take the op guard (config-only edit)")
		}
	}
	// alpha is gone from disk and from the reloaded view.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Policies["alpha"]; ok {
		t.Fatal("confirmed remove must delete alpha from sentra.yaml")
	}
	if len(v.names) != 1 || v.names[0] != "beta" {
		t.Fatalf("view names after remove = %v, want [beta]", v.names)
	}
}

func TestPoliciesView_AddOpensInlineForm(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	if v.stage != policiesForm {
		t.Fatalf("stage = %v, want policiesForm after 'a'", v.stage)
	}
	if !strings.Contains(v.View(), "New policy") {
		t.Errorf("form view must show the new-policy header:\n%s", v.View())
	}
}

func TestPoliciesView_AddConfirmedWritesPolicyAndReloads(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Type a name, tab to path, type a path.
	v = typeIntoPolicies(t, v, "gamma")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "/data/gamma")
	// Enter on the form arms the simple confirm modal.
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("form enter must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("add must use the simple ConfirmModal, got %T", push.modal)
	}
	// Confirm: config.Write happens, view reloads, gamma is present.
	m, _ = v.Update(confirmedMsg{id: policyAddConfirmID})
	v = m.(PoliciesView)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	p, ok := cfg.Policies["gamma"]
	if !ok || len(p.Paths) != 1 || p.Paths[0] != "/data/gamma" {
		t.Fatalf("gamma not written correctly: %+v", cfg.Policies["gamma"])
	}
	if v.stage != policiesList {
		t.Fatalf("stage after add = %v, want policiesList", v.stage)
	}
}

func TestPoliciesView_AddRejectsInvalidPolicy(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	// Name only, no path: policy.Validate rejects (needs >=1 path). Enter
	// must surface the error and NOT push a confirm modal.
	v = typeIntoPolicies(t, v, "noPaths")
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	if cmd != nil {
		t.Fatalf("invalid policy must not push a modal, got %#v", cmd())
	}
	if v.form.err == "" {
		t.Fatal("invalid policy must set a form error")
	}
}

func typeIntoPolicies(t *testing.T, v PoliciesView, s string) PoliciesView {
	t.Helper()
	for _, r := range s {
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		v = m.(PoliciesView)
	}
	return v
}

// policiesRunDeps builds a repo-backed Deps whose config has one policy
// pointing at a real seeded directory, with the given prune mode.
func policiesRunDeps(t *testing.T, prune string) (Deps, string, string) {
	t.Helper()
	r := newFlowRepo(t)
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Retention.KeepLast = 1
	cfg.Retention.KeepDaily = 0
	cfg.Retention.KeepWeekly = 0
	cfg.Retention.KeepMonthly = 0
	cfg.Policies["alpha"] = config.PolicyConfig{
		Paths:       []string{src},
		Schedule:    config.PolicySchedule{Cadence: "manual"},
		AfterBackup: config.PolicyAfterBackup{Check: true, Prune: prune},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// deps.Config must reflect the same file so retention limits are read.
	deps := Deps{Repo: r, Config: &cfg, ConfigPath: path}
	return deps, path, src
}

// TestPoliciesView_RunOffModeUsesSimpleConfirm: a policy with prune=off
// must gate RUN behind the SIMPLE confirm, then start the op guard.
func TestPoliciesView_RunOffModeUsesSimpleConfirm(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("r must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(ConfirmModal); !ok {
		t.Fatalf("prune=off must use the SIMPLE ConfirmModal, got %T", push.modal)
	}
}

// TestPoliciesView_RunApplyModeUsesTypedConfirm: prune=apply is
// destructive, so RUN must use the TYPED confirm.
func TestPoliciesView_RunApplyModeUsesTypedConfirm(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "apply")
	v := NewPoliciesView(deps)
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	push, ok := cmd().(pushModalMsg)
	if !ok {
		t.Fatalf("r must push a confirm modal, got %#v", cmd())
	}
	if _, ok := push.modal.(TypedConfirmModal); !ok {
		t.Fatalf("prune=apply must use the TYPED confirm, got %T", push.modal)
	}
}

// TestPoliciesView_RunConfirmedTakesOpGuardAndSnapshots: confirming RUN
// emits a startOpMsg (the op guard) whose run creates a real snapshot.
func TestPoliciesView_RunConfirmedTakesOpGuardAndSnapshots(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	m, cmd := v.Update(confirmedMsg{id: policyRunConfirmID})
	v = m.(PoliciesView)
	if v.stage != policiesRunning {
		t.Fatalf("stage = %v, want policiesRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	var start startOpMsg
	var foundStart bool
	for _, msg := range msgs {
		if s, ok := msg.(startOpMsg); ok {
			start, foundStart = s, true
		}
	}
	if !foundStart {
		t.Fatalf("confirmed run must emit a startOpMsg, got %#v", msgs)
	}
	if start.name != "policy-run" {
		t.Fatalf("op name = %q, want policy-run", start.name)
	}
	// Run the op synchronously; it must create a snapshot and report done.
	res := start.run(context.Background())
	done, ok := res.(policyRunDoneMsg)
	if !ok {
		t.Fatalf("expected policyRunDoneMsg, got %#v", res)
	}
	if done.err != nil {
		t.Fatalf("run failed: %v", done.err)
	}
	if done.snapshots != 1 {
		t.Fatalf("snapshots = %d, want 1", done.snapshots)
	}
	snaps, err := deps.Repo.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("ListSnapshots = %v, %v", snaps, err)
	}
	// Delivering the result moves to the done stage.
	m, _ = v.Update(res)
	v = m.(PoliciesView)
	if v.stage != policiesRunDone {
		t.Fatalf("stage after result = %v, want policiesRunDone", v.stage)
	}
}

// TestPoliciesView_RunRejectedResetsToList: if the op guard rejects the
// start (another op running), the view must leave the running stage.
func TestPoliciesView_RunRejectedResetsToList(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "off")
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	m, _ = v.Update(confirmedMsg{id: policyRunConfirmID})
	v = m.(PoliciesView)
	m, _ = v.Update(opRejectedMsg{name: "policy-run"})
	v = m.(PoliciesView)
	if v.stage != policiesList {
		t.Fatalf("stage after rejection = %v, want policiesList", v.stage)
	}
	if v.notice == "" {
		t.Fatal("rejection must set a notice banner")
	}
}

// TestPoliciesView_RunRefusesInvalidPolicy: a policy with an unrecognized
// prune mode (a typo like "aply" or a stale hand-edit) must NOT arm the RUN
// modal. Because policyPruneModeOrOff only lowercases/trims, "aply" would
// slip past the apply check and get the SIMPLE confirm ("creates a snapshot"
// — no mention of deletion), yet runPolicyRetentionPrune's fall-through
// would then really delete. armRun must validate first (matching the CLI's
// runPolicy, which calls policycfg.Validate) and refuse — surfacing the
// error via notice instead of pushing a confirm.
func TestPoliciesView_RunRefusesInvalidPolicy(t *testing.T) {
	deps, _, _ := policiesRunDeps(t, "aply")
	v := NewPoliciesView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	v = m.(PoliciesView)
	if cmd != nil {
		if push, ok := cmd().(pushModalMsg); ok {
			t.Fatalf("invalid prune mode must not arm a RUN modal, got %T", push.modal)
		}
	}
	if v.stage == policiesRunning {
		t.Fatal("invalid policy must not enter the running stage")
	}
	if v.notice == "" {
		t.Fatal("invalid policy must surface a notice explaining the refusal")
	}
}

// TestRunPolicyRetentionPrune_UnknownModeIsFailClosed: even if an
// unrecognized mode reaches runPolicyRetentionPrune (defense in depth), it
// must be a no-op — never fall through to DeleteSnapshot+GC. With KeepLast=1
// and two snapshots, an "apply" would drop one; an unknown mode must delete
// nothing.
func TestRunPolicyRetentionPrune_UnknownModeIsFailClosed(t *testing.T) {
	r := newFlowRepo(t)
	seedTwoSnapshots(t, r)
	policy := repo.RetentionPolicy{KeepLast: 1}
	if err := runPolicyRetentionPrune(context.Background(), r, policy, "aply"); err != nil {
		t.Fatalf("unknown mode should be a no-op, got error: %v", err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 2 {
		t.Fatalf("unknown prune mode deleted snapshots: have %d, want 2 (fail-closed)", len(snaps))
	}
}

// TestPoliciesForm_FullFieldSet: the TUI add form carries the same
// policy shape as `policy add` — multiple comma-separated paths, tags,
// the post-backup check toggle, and the prune mode.
func TestPoliciesForm_FullFieldSet(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)

	v = typeIntoPolicies(t, v, "gamma")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → path
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "/data/one, /data/two")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → tags
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "nightly, offsite")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → schedule
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "daily@04:00")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → check toggle
	v = m.(PoliciesView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace}) // check on
	v = m.(PoliciesView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab}) // → prune mode
	v = m.(PoliciesView)
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeySpace}) // off → dry-run
	v = m.(PoliciesView)

	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	// The confirm's side effect is the config write, which is what the rest of
	// this test asserts against — the returned view is never inspected again.
	v.Update(confirmedMsg{id: policyAddConfirmID})

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p, ok := cfg.Policies["gamma"]
	if !ok {
		t.Fatalf("gamma not written: %+v", cfg.Policies)
	}
	if len(p.Paths) != 2 || p.Paths[0] != "/data/one" || p.Paths[1] != "/data/two" {
		t.Errorf("paths: %+v, want the two comma-separated entries", p.Paths)
	}
	if len(p.Tags) != 2 || p.Tags[0] != "nightly" || p.Tags[1] != "offsite" {
		t.Errorf("tags: %+v", p.Tags)
	}
	if p.Schedule.Cadence != "daily" || p.Schedule.At != "04:00" {
		t.Errorf("schedule: %+v", p.Schedule)
	}
	if !p.AfterBackup.Check || p.AfterBackup.Prune != "dry-run" {
		t.Errorf("after_backup: %+v", p.AfterBackup)
	}
}

// TestPoliciesForm_ReplaceGuardPreservesHooks: adding a policy whose
// name exists must NOT silently overwrite — it pushes a replace
// confirm, and confirming preserves the existing policy's
// config-authored hooks, exactly like `policy add --replace`.
func TestPoliciesForm_ReplaceGuardPreservesHooks(t *testing.T) {
	deps, path := policiesDeps(t, nil)
	// Give alpha a hand-authored hook the form can't express.
	if err := config.Update(path, func(cfg *config.Config) error {
		p := cfg.Policies["alpha"]
		p.Hooks = config.PolicyHooks{OnFailureWebhookEnv: "SENTRA_ALERT_URL"}
		cfg.Policies["alpha"] = p
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "alpha")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
	v = m.(PoliciesView)
	v = typeIntoPolicies(t, v, "/data/alpha-new")
	m, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(PoliciesView)
	m, _ = v.Update(confirmedMsg{id: policyAddConfirmID})
	v = m.(PoliciesView)

	// The write must NOT have happened yet — a replace confirm is up.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Policies["alpha"].Paths[0] == "/data/alpha-new" {
		t.Fatal("existing policy overwritten without the replace confirm")
	}

	// As above: the replace confirm's side effect is the config write, and the
	// returned view is never inspected again.
	v.Update(confirmedMsg{id: policyReplaceConfirmID})
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.Policies["alpha"]
	if len(p.Paths) != 1 || p.Paths[0] != "/data/alpha-new" {
		t.Fatalf("replace did not apply: %+v", p.Paths)
	}
	if p.Hooks.OnFailureWebhookEnv != "SENTRA_ALERT_URL" {
		t.Errorf("replace dropped the config-authored hooks: %+v", p.Hooks)
	}
}

// TestPoliciesRun_ExecutesHooks: a TUI policy run executes the same
// hooks the CLI run does — a before hook lands its output in the
// snapshot, and a failing before hook aborts the run and fires
// on_failure. Skipping hooks would make TUI runs back up different
// data than CLI runs of the same policy.
func TestPoliciesRun_ExecutesHooks(t *testing.T) {
	r := newFlowRepo(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "failed.marker")

	path := filepath.Join(dir, "sentra.yaml")
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "b"
	cfg.Policies["hooked"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks: config.PolicyHooks{
			Before: "echo dumped > " + filepath.Join(src, "dump.txt"),
		},
	}
	cfg.Policies["failing"] = config.PolicyConfig{
		Paths:    []string{src},
		Schedule: config.PolicySchedule{Cadence: "manual"},
		Hooks: config.PolicyHooks{
			Before:    "exit 7",
			OnFailure: "touch " + marker,
		},
	}
	if err := config.Write(path, &cfg); err != nil {
		t.Fatal(err)
	}
	deps := Deps{Repo: r, Config: &cfg, ConfigPath: path}

	runPolicyByName := func(name string) policyRunDoneMsg {
		t.Helper()
		v := NewPoliciesView(deps)
		for i, n := range v.names {
			if n == name {
				v.selected = i
			}
		}
		m, cmd := v.startRun()
		_ = m
		var start startOpMsg
		for _, msg := range execCmds(t, cmd) {
			if s, ok := msg.(startOpMsg); ok {
				start = s
			}
		}
		if start.run == nil {
			t.Fatal("startRun emitted no op")
		}
		done, ok := start.run(context.Background()).(policyRunDoneMsg)
		if !ok {
			t.Fatal("op did not return policyRunDoneMsg")
		}
		return done
	}

	if done := runPolicyByName("hooked"); done.err != nil {
		t.Fatalf("hooked run: %v", done.err)
	}
	snaps, err := r.ListSnapshots(context.Background())
	if err != nil || len(snaps) != 1 {
		t.Fatalf("snapshots: %v err=%v", snaps, err)
	}
	man, err := r.LoadSnapshot(context.Background(), snaps[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, fe := range man.Tree {
		if fe.Path == "dump.txt" {
			found = true
		}
	}
	if !found {
		t.Error("before-hook output missing from the TUI-run snapshot")
	}

	if done := runPolicyByName("failing"); done.err == nil {
		t.Fatal("failing before hook must fail the TUI run")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("on_failure hook did not run from the TUI path: %v", err)
	}
	snaps, _ = r.ListSnapshots(context.Background())
	if len(snaps) != 1 {
		t.Errorf("aborted run must not snapshot; got %d", len(snaps))
	}
}

// TestPolicies_ExactlyOneBoxAndItFollowsFocus mirrors the brief's canonical
// shape for the add form's four text fields. The form only exists once the
// operator presses 'a' — that first-focus keypress (which runs
// newPolicyForm's name.Focus() at policies.go:708) is this view's
// activation path, so its returned cmd must schedule the blink.
func TestPolicies_ExactlyOneBoxAndItFollowsFocus(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)

	m, entryCmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)
	if v.stage != policiesForm {
		t.Fatalf("stage = %v, want policiesForm", v.stage)
	}
	assertBlinkCmd(t, entryCmd)

	base := v
	base.form.name.Blur()
	base.form.path.Blur()
	base.form.tags.Blur()
	base.form.schedule.Blur()
	n := boxCount(base.View())

	if got := boxCount(v.View()); got != n+1 {
		t.Fatalf("name focused: boxCount = %d, want %d (+1 over blurred)", got, n+1)
	}

	tabbed, cmd := v.Update(tea.KeyMsg{Type: tea.KeyTab}) // name -> path
	tv := tabbed.(PoliciesView)
	if got := boxCount(tv.View()); got != n+1 {
		t.Fatalf("box count changed on tab (got %d, want %d) — box must follow focus, one at a time", got, n+1)
	}
	assertBlinkCmd(t, cmd)

	tv.form.path.Cursor.BlinkSpeed = time.Millisecond
	tick := tv.form.path.Cursor.BlinkCmd()
	if _, tickCmd := tv.Update(tick()); tickCmd == nil {
		t.Fatal("blink tick not routed to the newly focused path field")
	}
}

// TestPolicies_RoutesBlinkTicksToNameField exercises the switch's other
// arm: a tick reaches name while it holds focus (the state right after the
// form opens).
func TestPolicies_RoutesBlinkTicksToNameField(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)

	v.form.name.Cursor.BlinkSpeed = time.Millisecond
	tick := v.form.name.Cursor.BlinkCmd()
	if _, cmd := v.Update(tick()); cmd == nil {
		t.Fatal("blink tick not routed to the focused name field")
	}
}

// TestPolicies_NoBoxWhenToggleFocused: tabbing past all four text fields
// onto the check/prune toggles must drop the box — neither toggle is a text
// field.
func TestPolicies_NoBoxWhenToggleFocused(t *testing.T) {
	deps, _ := policiesDeps(t, nil)
	v := NewPoliciesView(deps)
	m, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	v = m.(PoliciesView)

	base := v
	base.form.name.Blur()
	base.form.path.Blur()
	base.form.tags.Blur()
	base.form.schedule.Blur()
	n := boxCount(base.View())

	for i := 0; i < 4; i++ { // name -> path -> tags -> schedule -> check
		m, _ = v.Update(tea.KeyMsg{Type: tea.KeyTab})
		v = m.(PoliciesView)
	}
	if v.form.focus != 4 {
		t.Fatalf("form.focus = %d, want 4 (check)", v.form.focus)
	}
	if got := boxCount(v.View()); got != n {
		t.Fatalf("toggle focused: boxCount = %d, want %d (no box on a non-text field)", got, n)
	}
}
