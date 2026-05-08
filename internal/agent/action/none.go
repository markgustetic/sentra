package action

import (
	"context"
	"fmt"
)

// NoneHandler implements the explicit "no remediation" verb. The
// LLM emits this when a finding is worth surfacing to the operator
// but no automated action would help. The previous CLI dispatch
// switch had no case for "none" — it fell through to the
// "unknown action" error, even though "none" is documented as a
// valid verb. Registering it here closes that gap.
type NoneHandler struct{}

// Name returns the verb the LLM emits for this handler.
func (NoneHandler) Name() Action { return None }

// Description goes into the system prompt fragment.
func (NoneHandler) Description() string {
	return "explicit no-op; surface the finding to the operator with no automatic remediation"
}

// Apply emits one line acknowledging the finding and returns. No
// side effects beyond stdout. Severity and rationale come through
// from the recommendation so the operator-visible line carries
// context.
func (NoneHandler) Apply(
	ctx context.Context,
	env Env,
	id, target, severity, rationale string,
) error {
	suffix := ""
	if rationale != "" {
		suffix = " — " + rationale
	}
	fmt.Fprintf(env.Stdout, "  - %s: noted%s\n", id, suffix)
	return nil
}
