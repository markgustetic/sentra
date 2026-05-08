package action

import (
	"context"
	"fmt"
	"strings"
)

// FlagSecretHandler implements the flag_secret verb: notification-
// only. Sentra never holds the cloud-provider credentials needed to
// rotate a leaked secret automatically, so the handler emits a loud
// stdout line and returns. The operator is responsible for the
// rotation.
//
// This is structurally important — it locks in the design's safety
// rail: if a future contributor wants automated rotation, they'd
// have to add a brand-new handler (with a brand-new verb) rather
// than silently extending this one.
type FlagSecretHandler struct{}

// Name returns the verb the LLM emits for this handler.
func (FlagSecretHandler) Name() Action { return FlagSecret }

// Description goes into the system prompt fragment.
func (FlagSecretHandler) Description() string {
	return "notify the operator that a credential at target was found and must be rotated by hand"
}

// Apply prints a one-line notification. Severity is interpolated
// into the message so the operator sees how urgent the agent
// thought the find was.
func (FlagSecretHandler) Apply(
	ctx context.Context,
	env Env,
	id, target, severity, rationale string,
) error {
	// We deliberately don't use any styling primitives here so the
	// action package stays free of UI dependencies. The dispatcher
	// is welcome to wrap stdout in a styled Writer if it wants
	// colorized output.
	tag := "FLAGGED"
	if severity != "" {
		tag = "FLAGGED [" + strings.ToUpper(severity) + "]"
	}
	fmt.Fprintf(env.Stdout, "  - %s: %s — rotate the credential at %s\n",
		id, tag, target)
	return nil
}
