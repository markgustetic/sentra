package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/agent/tools"
)

// systemPrompt is the agent's role prompt. It describes Sentra, the
// agent's job, and — crucially — the safety rails: never request file
// contents, always emit recommendations as a JSON array.
//
// We rebuild this on every Scan rather than storing it on the Agent
// struct because the available tools depend on the runner, and the
// action vocabulary depends on the registry — both injectable. The
// two %s placeholders are filled with: (1) the tool-list fragment
// produced by formatToolsForPrompt, (2) the action-list fragment
// produced by action.Registry.PromptFragment. Generating from the
// live registry guarantees the LLM is told about exactly the verbs
// the dispatcher knows how to handle.
const systemPromptTemplate = `You are the Sentra repository auditor. Sentra is an encrypted, ` +
	`versioned S3 backup tool. Your job is to triage local heuristic findings ` +
	`(secrets, large files, stale paths, retention drift, ...) and emit a JSON ` +
	`array of recommendations the operator can review.

Tools available (read-only metadata; you must NEVER request file contents):
%s

Hard rules:
- You never see file contents. Don't ask for them; the tools won't return them.
- Use the tools to investigate findings before recommending action. Be parsimonious — most findings need 0-1 tool calls.
- Final response MUST be a single JSON array of Recommendation objects:
  [{"id":"...","action":"...","target":"...","severity":"...","rationale":"..."}]
  Action must be one of:
%s
  Severity is one of: "info", "warn", "critical".
- If there is nothing to do, respond with "[]".
- Do NOT include prose outside the JSON array. The array IS your response.`

// buildInitialMessage formats the heuristic findings as a JSON array
// for the model's initial user-turn input. JSON keeps the parse cost
// low for the model and lines up with the recommendation output shape.
func buildInitialMessage(findings []heuristics.Finding) (string, error) {
	out, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Heuristic findings (%d total):\n\n%s\n\nReview these and emit recommendations as a JSON array.",
		len(findings), string(out)), nil
}

func filterFindingsByCategory(findings []heuristics.Finding, categories []string) []heuristics.Finding {
	allowed := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		category = strings.TrimSpace(strings.ToLower(category))
		if category == "" {
			continue
		}
		allowed[category] = struct{}{}
	}
	if len(allowed) == 0 {
		return findings
	}
	out := make([]heuristics.Finding, 0, len(findings))
	for _, finding := range findings {
		category := strings.ToLower(finding.Category)
		heuristic := strings.ToLower(finding.Heuristic)
		if _, ok := allowed[category]; ok {
			out = append(out, finding)
			continue
		}
		if _, ok := allowed[heuristic]; ok {
			out = append(out, finding)
		}
	}
	return out
}

func localRecommendations(findings []heuristics.Finding) []Recommendation {
	recs := make([]Recommendation, 0, len(findings))
	for _, finding := range findings {
		action := localActionForFinding(finding)
		severity := finding.Severity
		if severity == "" {
			severity = heuristics.SeverityInfo
		}
		category := finding.Category
		if category == "" {
			category = finding.Heuristic
		}
		recs = append(recs, Recommendation{
			ID:        "local-" + finding.ID,
			Action:    action,
			Target:    finding.Target,
			Severity:  severity,
			Rationale: fmt.Sprintf("Local heuristic %q reported this target.", category),
		})
	}
	return recs
}

func localActionForFinding(finding heuristics.Finding) string {
	switch finding.Category {
	case "secrets":
		return "flag_secret"
	case "cache_dirs", "large_files":
		return "add_to_ignore"
	case "retention_drift":
		return "prune_snapshot"
	default:
		return "none"
	}
}

// formatToolsForPrompt renders the toolset into a human-readable list
// for the system prompt. Each line is "- name: description" so the LLM
// has both the name (for tool-use) and a one-liner of intent.
func formatToolsForPrompt(ts []tools.Tool) string {
	var sb strings.Builder
	for _, t := range ts {
		fmt.Fprintf(&sb, "- %s: %s\n", t.Name, t.Description)
	}
	return strings.TrimRight(sb.String(), "\n")
}
