package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/ui"
)

// printSetupStep and printSetupOK outlived `sentra setup`'s own output — the
// launcher prints nothing and the TUI wizard renders its own checklist. They
// survive because `sentra doctor` reports every probe through them, directly
// and via the non-animated branch of setup_spinner.go's progress steps.
func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

func validateSetupBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
