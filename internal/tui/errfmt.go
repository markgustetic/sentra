package tui

import (
	"strings"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/ui"
)

// humanizeErr renders an error for an operator: when diag.Explain
// recognizes the cause, ONLY the plain-words summary (Danger) and fix
// render — the raw chain is deliberately hidden, because for a known cause
// it adds noise, not information. The chain is still recoverable: the CLI
// surface prints errors verbatim by contract, and any cause Explain does
// not recognize renders its raw chain in Danger exactly as error surfaces
// did before this helper existed.
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
	return b.String()
}
