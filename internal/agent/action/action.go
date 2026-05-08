// Package action defines the vocabulary of remediations the Sentra
// agent can recommend, and a registry that maps each verb to a
// concrete side-effect.
//
// Adding a new action verb is now a single file:
//
//  1. Implement Handler in this package.
//  2. Add an instance to NewDefaultRegistry().
//
// The system prompt fragment the orchestrator builds is generated
// from the registered handlers' Description() methods, so the LLM's
// vocabulary is guaranteed to match the dispatcher's vocabulary by
// construction. Before this package, the action set was defined in
// three places — the CLI's dispatch switch, the orchestrator's
// system-prompt template string, and the comment on Recommendation
// — and a new verb meant a coordinated edit across all three with
// no compile-time guard against drift.
package action

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/markgustetic/sentra/internal/repo"
)

// Action is the verb the LLM emits in a Recommendation. Typed as a
// string so it round-trips through JSON unchanged; the constants
// below are the canonical set known to a default Sentra build.
type Action string

const (
	// PruneSnapshot deletes a snapshot (target=snapshot ID) and runs
	// GC over the remaining manifests to reclaim chunks that were
	// only referenced by the now-deleted manifest.
	PruneSnapshot Action = "prune_snapshot"

	// AddToIgnore appends target (a gitignore-style glob) to the
	// repo's .sentraignore so future backups skip it.
	AddToIgnore Action = "add_to_ignore"

	// FlagSecret is notification-only: the operator is told a secret
	// was found and asked to rotate it. Sentra never holds the cloud
	// credentials needed to rotate the secret itself, so automation
	// stops at flagging.
	FlagSecret Action = "flag_secret"

	// None is the explicit "no remediation needed" verb. The model
	// emits it when a finding deserves operator attention but no
	// automated step would help. The dispatcher recognizes this and
	// emits a single line of stdout rather than erroring on it.
	None Action = "none"
)

// Env is the side-effect surface Apply receives. Each field is the
// minimum a handler currently needs; new fields land here as new
// actions need them, not retroactively across all handlers.
type Env struct {
	// Repo is the open repository. Required for actions that mutate
	// the repo (PruneSnapshot, future verbs). May be nil for actions
	// that don't touch repo state — handlers that need it must check
	// and surface a clear error.
	Repo *repo.Repo

	// Stdout receives operator-visible progress messages. Each
	// handler is expected to emit one line per Apply ("  - ID:
	// <result>\n"); the dispatcher prints nothing of its own, so the
	// per-action message is what the user sees.
	Stdout io.Writer

	// Cwd is the working directory for filesystem-side actions
	// (e.g. AddToIgnore writing .sentraignore relative to it). Empty
	// string falls back to "." so tests can leave it unset.
	Cwd string

	// FormatBytes is an optional formatter for byte counts. Wired
	// to ui.FormatBytes by the production dispatcher; nil falls
	// back to a plain "%d bytes" printer so the action package
	// doesn't have to import internal/ui (which transitively pulls
	// in lipgloss + bubbles).
	FormatBytes func(int64) string
}

// formatBytes is the internal helper handlers use to format byte
// counts; falls back to a plain decimal renderer when the operator
// didn't wire a custom formatter.
func (e Env) formatBytes(n int64) string {
	if e.FormatBytes != nil {
		return e.FormatBytes(n)
	}
	return fmt.Sprintf("%d bytes", n)
}

// Handler is the contract every registered action satisfies. Apply
// is called only after the operator has approved the recommendation
// (the CLI's per-recommendation prompt happens before Dispatch); a
// Handler must NOT re-prompt.
type Handler interface {
	// Name is the verb the LLM emits.
	Name() Action

	// Description is a one-line human summary used to build the
	// system prompt fragment. Format: imperative + when-to-use.
	// Keep <=80 chars so the prompt stays scannable.
	Description() string

	// Apply executes the side-effect. id is the finding ID for
	// operator-visible output; target is the action's argument (a
	// snapshot ID for PruneSnapshot, a glob for AddToIgnore, ...).
	// severity and rationale are passed through for handlers that
	// want to format messaging on them.
	Apply(ctx context.Context, env Env, id, target, severity, rationale string) error
}

// Registry holds the registered handlers keyed by Action. The zero
// Registry is unusable — call NewRegistry or NewDefaultRegistry to
// construct one with at least one handler.
type Registry struct {
	handlers map[Action]Handler
}

// NewRegistry builds a Registry with the supplied handlers. Two
// handlers with the same Name panic at registration — the action
// vocabulary must be unambiguous, and a duplicate is a programmer
// error caught at startup, not a runtime branch.
func NewRegistry(handlers ...Handler) *Registry {
	r := &Registry{handlers: make(map[Action]Handler, len(handlers))}
	for _, h := range handlers {
		if _, dup := r.handlers[h.Name()]; dup {
			panic(fmt.Sprintf("action: duplicate handler for %q", h.Name()))
		}
		r.handlers[h.Name()] = h
	}
	return r
}

// NewDefaultRegistry builds the production action set: prune_snapshot,
// add_to_ignore, flag_secret, none. Adding a new verb to the default
// build means appending one line here.
func NewDefaultRegistry() *Registry {
	return NewRegistry(
		PruneSnapshotHandler{},
		AddToIgnoreHandler{},
		FlagSecretHandler{},
		NoneHandler{},
	)
}

// Lookup returns the handler for a, or (nil, false) when the action
// isn't registered. Callers should treat false as a programmer or
// model error, not a successful no-op.
func (r *Registry) Lookup(a Action) (Handler, bool) {
	h, ok := r.handlers[a]
	return h, ok
}

// Names returns every registered action verb in alphabetical order.
// Used by the orchestrator to surface the full vocabulary in error
// messages and validation without coupling to internal map order.
func (r *Registry) Names() []Action {
	out := make([]Action, 0, len(r.handlers))
	for n := range r.handlers {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// PromptFragment returns the system-prompt-ready list of action
// verbs and their descriptions. The output is substituted into the
// orchestrator's system prompt template so the LLM sees the same
// vocabulary the dispatcher recognizes.
//
// Example output (one verb per line):
//
//	"prune_snapshot": delete a snapshot ...
//	"add_to_ignore": append a glob to .sentraignore ...
func (r *Registry) PromptFragment() string {
	names := r.Names()
	var b strings.Builder
	for i, n := range names {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "  - %q: %s", n, r.handlers[n].Description())
	}
	return b.String()
}

// Dispatch looks up the handler for action and calls Apply. Unknown
// actions return a clear error mentioning the unknown verb so the
// operator can see what the model produced. The id, target, severity,
// and rationale fields are passed through verbatim for the handler
// to format into operator-visible output.
func (r *Registry) Dispatch(
	ctx context.Context,
	env Env,
	a Action,
	id, target, severity, rationale string,
) error {
	h, ok := r.Lookup(a)
	if !ok {
		return fmt.Errorf("action: unknown verb %q", a)
	}
	return h.Apply(ctx, env, id, target, severity, rationale)
}
