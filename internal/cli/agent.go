package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/llm"
	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
	"github.com/markgustetic/sentra/internal/walker"
)

// AgentDeps wires the side-effecting pieces of `sentra agent scan` so
// tests can inject a memory store, a fake LLM Provider, and stub
// heuristics. Production fills these from main.go; tests drop in
// scripted alternatives so a Scan run is fully deterministic and
// never makes a real LLM call.
//
// Provider and Heuristics are required for the scan flow itself;
// NewStore + Passphrase open the repo; Confirm is the per-recommendation
// approval prompt for --apply (skipped under --yes).
type AgentDeps struct {
	// NewStore opens the blobstore the repo lives in. Same shape as
	// every other command's deps.
	NewStore func(ctx context.Context, cfg *config.Config) (blobstore.Store, error)

	// Passphrase returns the bytes used to unwrap the repo key.
	Passphrase func() ([]byte, error)

	// PassphraseWithConfig is the config-aware production resolver.
	// When set, it takes precedence over Passphrase so --config paths
	// feed keyring/user selection instead of hardcoded sentra.yaml.
	PassphraseWithConfig func(cfg *config.Config) ([]byte, error)

	// Stdout receives the user-facing summary table / JSON.
	Stdout io.Writer

	// Actions is the registry of action verbs the orchestrator's
	// system prompt will list and the --apply path will dispatch
	// through. Production wires action.NewDefaultRegistry; tests
	// can inject a smaller registry. Nil falls back to
	// NewDefaultRegistry so a tests using zero-value AgentDeps still
	// works.
	Actions *action.Registry

	// Provider is the LLM provider passed through to the agent. Tests
	// use llm.FakeProvider; main wires the Anthropic client.
	Provider llm.Provider

	// ProviderForConfig builds the provider from the loaded command
	// config. When set, it takes precedence over Provider so --config
	// controls agent.model in production.
	ProviderForConfig func(cfg *config.Config) llm.Provider

	// Heuristics is the slice of heuristics the agent will run. Tests
	// pass deterministic stubs; main wires the production secrets /
	// large_files / etc. set.
	Heuristics []heuristics.Heuristic

	// Confirm prompts the user with a question and returns the answer.
	// Production wires this to huh.NewConfirm; tests inject a stub.
	// Skipped entirely when --yes is passed.
	Confirm func(prompt string) (bool, error)
}

// agentFlags captures the CLI flags for `sentra agent scan`. Pulled
// into a struct so the cobra wiring stays compact and the runner
// reads from a single explicit input.
type agentFlags struct {
	apply        bool
	asJSON       bool
	yes          bool
	cfgPath      string
	root         string
	localOnly    bool
	noLLM        bool
	categories   []string
	maxToolCalls int
	// allowWipe is the safety rail equivalent to `prune --all`: when an
	// LLM recommends pruning every remaining snapshot, we refuse to
	// apply unless the user explicitly opts in. Default false so the
	// "scripted apply quietly wipes the repo" footgun is unreachable.
	allowWipe bool
}

// NewAgent returns the cobra command for the `agent` parent. It owns
// no business logic itself; only registers `scan` as a subcommand.
// Future agent subcommands (e.g. `agent eval`) belong here.
func NewAgent(deps AgentDeps) *cobra.Command {
	cmd := &cobra.Command{
		Use:           "agent",
		Short:         "Repository auditor (heuristics + LLM)",
		Long:          "The agent runs local heuristics over the repo and an LLM-powered triage loop, emitting recommendations the operator can review or apply.",
		SilenceUsage:  true,
		SilenceErrors: false,
	}
	cmd.AddCommand(NewAgentScan(deps))
	cmd.AddCommand(NewAgentAdviseIgnore(deps))
	return cmd
}

// NewAgentScan returns the cobra command for `sentra agent scan`.
// Without --apply the command is dry-run: it prints a styled table of
// recommendations and exits. With --apply, each recommendation goes
// through the per-action handler map after a Confirm prompt
// (skippable with --yes).
//
// Flags:
//   - --apply              actually execute approved recommendations
//   - --json               emit recommendations as JSON instead of a table
//   - --yes                skip the confirm prompt under --apply
//   - --root <path>        filesystem root to scan (default ".")
//   - --local-only         convert heuristic findings without calling the LLM
//   - --no-llm             alias for --local-only
//   - --categories a,b     only triage matching finding categories/heuristics
//   - --config <path>      sentra.yaml path (default ./sentra.yaml)
//   - --max-tool-calls N   override the orchestrator's tool-call budget
func NewAgentScan(deps AgentDeps) *cobra.Command {
	flags := &agentFlags{}
	cmd := &cobra.Command{
		Use:           "scan",
		Short:         "Run the repository audit",
		Long:          "Run the heuristic + LLM audit over the repo and print recommendations. Default is dry-run; pass --apply to execute approved actions.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgentScan(cmd, deps, flags)
		},
	}
	cmd.Flags().BoolVar(&flags.apply, "apply", false,
		"execute approved recommendations after interactive confirm (default: dry-run)")
	cmd.Flags().BoolVar(&flags.asJSON, "json", false,
		"emit recommendations as a JSON array instead of a styled table")
	cmd.Flags().BoolVar(&flags.yes, "yes", false,
		"skip the per-recommendation confirm prompt (use with --apply for scripts)")
	cmd.Flags().StringVar(&flags.root, "root", ".",
		"filesystem root to scan for local heuristics")
	cmd.Flags().BoolVar(&flags.localOnly, "local-only", false,
		"skip the LLM and convert local heuristic findings directly")
	cmd.Flags().BoolVar(&flags.noLLM, "no-llm", false,
		"alias for --local-only")
	cmd.Flags().StringSliceVar(&flags.categories, "categories", nil,
		"only triage matching categories or heuristic names (comma-separated)")
	cmd.Flags().StringVar(&flags.cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().IntVar(&flags.maxToolCalls, "max-tool-calls", 0,
		"cap on LLM tool calls per scan (0 means use the agent default)")
	cmd.Flags().BoolVar(&flags.allowWipe, "allow-wipe", false,
		"required when prune_snapshot recommendations would empty the repo (safety rail equivalent to prune --all)")
	return cmd
}

// runAgentScan is the body of `sentra agent scan`. Pulled out for
// grep-ability and to keep cobra closures shallow.
func runAgentScan(cmd *cobra.Command, deps AgentDeps, flags *agentFlags) error {
	cmd.SilenceUsage = true

	cfg, err := config.Load(flags.cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	store, err := deps.NewStore(cmd.Context(), cfg)
	if err != nil {
		return fmt.Errorf("open blobstore: %w", err)
	}
	pass, err := resolvePassphrase(deps.Passphrase, deps.PassphraseWithConfig, cfg)
	if err != nil {
		return fmt.Errorf("resolve passphrase: %w", err)
	}
	defer crypto.Zeroize(pass)

	r, err := repo.Open(cmd.Context(), store, pass)
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	defer r.Close()

	registry := heuristics.NewRegistry(deps.Heuristics...)

	agentCfg := agent.Config{
		MaxFindingsToLLM: cfg.Agent.MaxFindingsToLLM,
		Model:            cfg.Agent.Model,
		InputConfig: heuristics.InputConfig{
			Retention: repo.RetentionPolicy{
				KeepLast:    cfg.Retention.KeepLast,
				KeepDaily:   cfg.Retention.KeepDaily,
				KeepWeekly:  cfg.Retention.KeepWeekly,
				KeepMonthly: cfg.Retention.KeepMonthly,
			},
		},
		LocalOnly:  flags.localOnly || flags.noLLM,
		Categories: flags.categories,
	}
	walkerOpts := walker.Options{
		IgnoreFile:    cfg.Backup.IgnoreFile,
		ExcludeCaches: cfg.Backup.ExcludeCaches,
	}
	normalizeBackupWalkerOptions(&walkerOpts)
	agentCfg.Walker = walkerOpts
	if flags.maxToolCalls > 0 {
		agentCfg.MaxToolCalls = flags.maxToolCalls
	}

	// Resolve the action registry once and share it between the
	// orchestrator (system prompt builder) and the --apply
	// dispatcher. They MUST agree on the vocabulary — the LLM is
	// told what verbs are valid, the dispatcher executes those
	// verbs. Sharing the same instance makes drift impossible.
	actions := deps.Actions
	if actions == nil {
		actions = action.NewDefaultRegistry()
	}

	provider := deps.Provider
	if deps.ProviderForConfig != nil {
		provider = deps.ProviderForConfig(cfg)
	}

	a := &agent.Agent{
		Repo:       r,
		Heuristics: registry,
		Provider:   provider,
		Config:     agentCfg,
		Actions:    actions,
	}

	out := deps.Stdout
	if out == nil {
		out = cmd.OutOrStdout()
	}

	// Drain the stream channel into stderr (or discard) in the
	// background so the model's reasoning text doesn't get lost. We
	// don't surface it on stdout because stdout is reserved for the
	// recommendations payload (table or JSON), which downstream pipes
	// might consume.
	stream := make(chan string, 32)
	streamDone := make(chan struct{})
	go func() {
		defer close(streamDone)
		for tok := range stream {
			// Streaming tokens are best-effort; if the user redirected
			// stderr away we silently drop. The point is to surface
			// thinking *interactively*, not to log every byte.
			fmt.Fprint(cmd.ErrOrStderr(), tok)
		}
	}()

	recs, scanErr := a.Scan(cmd.Context(), flags.root, stream)
	close(stream)
	<-streamDone

	if scanErr != nil {
		// ErrBudgetExhausted is informative; surface verbatim. Other
		// errors are surfaced as-is. We do NOT swallow the error so the
		// CLI's exit code reflects failure.
		return fmt.Errorf("agent scan: %w", scanErr)
	}

	if flags.asJSON {
		// JSON path is a clean stream: no styled prelude, no trailing
		// hints. Consumers piping into jq or another parser would
		// otherwise choke on the Re-run hint.
		if err := writeRecsJSON(out, recs); err != nil {
			return err
		}
		// Apply path still runs in JSON mode, but we don't print the
		// styled re-run hint either way.
		if !flags.apply {
			return nil
		}
	} else {
		if err := writeRecsTable(out, recs); err != nil {
			return err
		}
		if !flags.apply {
			if len(recs) > 0 {
				fmt.Fprintln(out, ui.Subtle.Render("Re-run with --apply to execute approved actions."))
			}
			return nil
		}
	}

	// --apply path: walk recommendations, confirm each (unless --yes),
	// dispatch through the action handler map. Errors on any single
	// action are surfaced but do NOT abort the rest of the loop —
	// users frequently want partial application. Pass the same
	// `actions` registry the orchestrator's system prompt was built
	// from so the dispatch loop and the LLM agree on vocabulary
	// (instead of having the loop re-derive its own copy on every
	// iteration with a duplicated nil-fallback).
	return applyRecommendations(cmd.Context(), r, recs, deps, flags, out, actions)
}

// writeRecsTable emits a styled lipgloss table of recommendations. An
// empty slice still prints the header row so the user sees the same
// frame regardless of contents.
func writeRecsTable(w io.Writer, recs []agent.Recommendation) error {
	headers := []string{"ID", "Severity", "Action", "Target", "Rationale"}
	rows := make([][]string, 0, len(recs))
	for _, r := range recs {
		rows = append(rows, []string{
			r.ID,
			ui.Severity(r.Severity).Render(emptyDash(r.Severity)),
			emptyDash(r.Action),
			emptyDash(r.Target),
			truncateRationale(r.Rationale, 60),
		})
	}
	if _, err := fmt.Fprintln(w, ui.RenderTable(headers, rows)); err != nil {
		return fmt.Errorf("write table: %w", err)
	}
	if len(recs) == 0 {
		fmt.Fprintln(w, ui.Subtle.Render("No findings — all clear."))
	}
	return nil
}

// writeRecsJSON emits the recommendations as a JSON array. An empty
// slice emits `[]` so consumers can iterate without a nil check.
func writeRecsJSON(w io.Writer, recs []agent.Recommendation) error {
	out := recs
	if out == nil {
		out = []agent.Recommendation{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("encode json: %w", err)
	}
	return nil
}

// applyRecommendations walks the recommendation list, prompting the
// user (skipped with --yes) and dispatching approved actions through
// the action handler map. Errors on individual actions are reported
// but don't abort the loop.
//
// Safety rail: tracks the remaining-snapshot count across the apply
// loop so a sequence of prune_snapshot actions can't silently empty
// the repo. If a prune would drop the last snapshot AND --allow-wipe
// is not set, the action is refused with a clear error pointing at
// the flag (mirrors `prune --all`'s contract).
func applyRecommendations(
	ctx context.Context,
	r *repo.Repo,
	recs []agent.Recommendation,
	deps AgentDeps,
	flags *agentFlags,
	out io.Writer,
	actions *action.Registry,
) error {
	if len(recs) == 0 {
		fmt.Fprintln(out, ui.Subtle.Render("No recommendations to apply."))
		return nil
	}

	// Snapshot count baseline: count what's currently in the repo. We
	// decrement as prune_snapshot actions succeed; the wipe-guard
	// triggers when the next prune would drive the count to zero.
	currentSnaps, err := r.ListSnapshots(ctx)
	if err != nil {
		return fmt.Errorf("list snapshots: %w", err)
	}
	remaining := len(currentSnaps)

	applied, declined, errs := 0, 0, 0
	for _, rec := range recs {
		// "none" is the model's "FYI, nothing to do here" — skip the
		// confirm and the apply but acknowledge it in the output.
		if rec.Action == "none" {
			fmt.Fprintf(out, "  - %s (%s): noted, nothing to apply\n", rec.ID, ui.Severity(rec.Severity).Render(rec.Severity))
			continue
		}

		// Per-recommendation confirm. --yes short-circuits this so
		// scripted runs (e.g. cron) don't deadlock on stdin.
		approved := flags.yes
		if !approved {
			prompt := fmt.Sprintf("Apply %s on %q?\n  rationale: %s",
				rec.Action, rec.Target, rec.Rationale)
			ok, err := deps.Confirm(prompt)
			if err != nil {
				return fmt.Errorf("confirm: %w", err)
			}
			approved = ok
		}
		if !approved {
			fmt.Fprintf(out, "  - %s: declined\n", rec.ID)
			declined++
			continue
		}

		// Wipe guard: an APPROVED prune that would empty the repo is
		// refused unless --allow-wipe was explicitly passed. Placing
		// the check AFTER confirm means a declined prune doesn't trip
		// the rail (the user already said no, no wipe is happening) —
		// only an approved action that crosses the safety line fails
		// the run. Mirrors `prune --all` semantics.
		if rec.Action == "prune_snapshot" && remaining-1 <= 0 && !flags.allowWipe {
			return fmt.Errorf(
				"refusing to apply %s on %q: this would prune every snapshot in the repo; pass --allow-wipe to confirm",
				rec.Action, rec.Target)
		}

		if err := dispatchAction(ctx, actions, r, rec, out); err != nil {
			fmt.Fprintf(out, "  - %s: %s\n", rec.ID, ui.Danger.Render("error: "+err.Error()))
			errs++
			continue
		}
		applied++
		// On a successful prune, decrement the live counter so the
		// guard fires correctly on the next prune action. Other
		// actions don't affect the snapshot count.
		if rec.Action == "prune_snapshot" {
			remaining--
		}
	}

	fmt.Fprintln(out, ui.Success.Render("Apply complete"))
	fmt.Fprintf(out, "  applied:  %d\n", applied)
	fmt.Fprintf(out, "  declined: %d\n", declined)
	fmt.Fprintf(out, "  errors:   %d\n", errs)
	return nil
}

// dispatchAction maps a recommendation's Action to a side-effect via
// the action.Registry. Unknown actions surface as errors so the
// model's vocabulary can't silently fail to do anything.
//
// The handlers themselves live in internal/agent/action — adding a
// new action verb is a single file there + one line in
// action.NewDefaultRegistry. The CLI's role is only to construct the
// Env (formatter for byte counts, working directory) and dispatch.
func dispatchAction(
	ctx context.Context,
	registry *action.Registry,
	r *repo.Repo,
	rec agent.Recommendation,
	out io.Writer,
) error {
	cwd, _ := os.Getwd() // failure → handler falls back to "."
	env := action.Env{
		Repo:        r,
		Stdout:      out,
		Cwd:         cwd,
		FormatBytes: ui.FormatBytes,
	}
	return registry.Dispatch(ctx, env, action.Action(rec.Action),
		rec.ID, rec.Target, rec.Severity, rec.Rationale)
}

// truncateRationale shortens long rationale text for table display.
// The full text is still in the JSON output (and the model's stream).
func truncateRationale(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}

// (HuhAgentConfirm now lives in confirm.go alongside the other two
// production confirm callbacks; their bodies were identical except
// for the affirmative/negative label pair.)
