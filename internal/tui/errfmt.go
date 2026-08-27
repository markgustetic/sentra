package tui

import (
	"strings"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/ui"
)

// humanizeErr renders an error for an operator: when diag.Explain
// recognizes the cause, a plain-words summary (Danger) and fix lead, with
// the raw chain kept below in Muted — the summary is for the human, the
// chain is for the bug report. Unknown causes render the raw chain in
// Danger, exactly as error surfaces did before this helper existed.
//
// The result contains styled fragments: callers must write it as-is,
// never wrap it in another style (see the never-wrap-styled-strings
// invariant in CLAUDE.md).
func humanizeErr(err error) string {
	if err == nil {
		return ""
	}
	ex, ok := diag.Explain(err)
	if !ok {
		return ui.Danger.Render(err.Error())
	}
	var b strings.Builder
	b.WriteString(ui.Danger.Render(ex.Summary))
	if ex.Fix != "" {
		b.WriteString("\n")
		b.WriteString(ex.Fix)
	}
	b.WriteString("\n\n")
	b.WriteString(ui.Muted.Render(err.Error()))
	return b.String()
}
