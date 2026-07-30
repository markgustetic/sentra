package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/ui"
)

// emptyDash renders an empty string as "-" for compact command summaries and
// tables.
func emptyDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// printSetupStep and printSetupOK are the CLI's step/success line printers.
// They outlived `sentra setup`'s huh wizard — setup_spinner.go (whose only
// caller is doctor.go) and doctor.go itself still use them for progress
// output — so they live here with the other output helpers rather than in a
// setup-specific file.
func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

// validateSetupBucketName is shared by doctor, `setup iam-policy`, and the TUI
// wizard's details stage, so it is not setup-command-specific.
func validateSetupBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
