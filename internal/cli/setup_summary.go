package cli

import (
	"fmt"
	"io"

	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/setup"
	"github.com/markgustetic/sentra/internal/ui"
)

func printSetupSummary(
	out io.Writer,
	cfgPath string,
	plan *SetupPlan,
	awsAuthReport *AWSAuthReport,
	awsReport *AWSPrepareReport,
	initResult *setupInitResult,
) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Success.Bold(true).Render("Sentra setup complete"))
	headers := map[string]bool{
		"Configuration":      true,
		"AWS authentication": true,
		"AWS bucket":         true,
		"Repository":         true,
		"Next":               true,
	}
	for _, line := range setup.SummaryLines(cfgPath, *plan, awsAuthReport, awsReport, initResult) {
		if headers[line] {
			fmt.Fprintln(out, ui.Subtle.Render(line))
			continue
		}
		fmt.Fprintln(out, line)
	}
}

func printSetupApplyHeader(out io.Writer, cfgPath string, plan *SetupPlan) {
	fmt.Fprintln(out)
	fmt.Fprintln(out, ui.Primary.Render("Applying Sentra setup"))
	fmt.Fprintf(out, "  config:  %s\n", cfgPath)
	fmt.Fprintf(out, "  storage: %s\n", setupBackendLabel(plan.Backend))
	if plan.InitRepo {
		fmt.Fprintln(out, "  repo:    initialize after config")
		if plan.SavePassphrase {
			fmt.Fprintln(out, "  pass:    save to OS keyring after setup prompt")
		} else {
			fmt.Fprintln(out, "  pass:    prompt or configured passphrase source")
		}
	} else {
		fmt.Fprintln(out, "  repo:    config only")
	}
	fmt.Fprintln(out)
}

func printSetupStep(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Subtle.Render("..."), label)
}

func printSetupOK(out io.Writer, label string) {
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("ok"), label)
}

func printSetupRepairContinue(out io.Writer, plan *SetupPlan) {
	if !plan.PrepareAWS {
		printSetupOK(out, "Continuing with config-only setup")
		return
	}
	fmt.Fprintf(out, "%s Retrying AWS setup with %s\n", ui.Subtle.Render("..."), setupAWSAuthMethodLabel(plan.AWSAuthMethod))
}

func setupBackendLabel(backend SetupBackend) string       { return setup.BackendLabel(backend) }
func setupAWSAuthMethodLabel(m SetupAWSAuthMethod) string { return setup.AWSAuthMethodLabel(m) }
func setupAWSPreparedLabel(r *AWSPrepareReport) string    { return setup.AWSPreparedLabel(r) }

func validateSetupBucketName(bucket string) error {
	return diag.ValidateBucketName(bucket)
}
