package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/recoverykit"
	"github.com/markgustetic/sentra/internal/ui"
)

// RecoveryKitDeps wires `sentra recovery-kit`.
type RecoveryKitDeps struct {
	RepoDeps
}

// NewRecoveryKit returns `sentra recovery-kit`.
func NewRecoveryKit(deps RecoveryKitDeps) *cobra.Command {
	var (
		cfgPath string
		outPath string
		asJSON  bool
	)
	cmd := &cobra.Command{
		Use:   "recovery-kit",
		Short: "Export non-secret repository recovery notes",
		Long: "Write a non-secret recovery kit containing repository identity, " +
			"storage location, latest snapshot, and copyable check/list/restore commands.",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runRecoveryKit(cmd, deps, cfgPath, outPath, asJSON)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (defaults to ./sentra.yaml)")
	cmd.Flags().StringVar(&outPath, "out", "",
		"write the kit to this path instead of stdout")
	cmd.Flags().BoolVar(&asJSON, "json", false,
		"emit JSON instead of Markdown")
	return cmd
}

func runRecoveryKit(cmd *cobra.Command, deps RecoveryKitDeps, cfgPath, outPath string, asJSON bool) error {
	cmd.SilenceUsage = true

	r, pass, cfg, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	kit, err := recoverykit.Build(cmd.Context(), r, cfg, cfgPath)
	if err != nil {
		return err
	}

	var body []byte
	if asJSON {
		body, err = recoverykit.MarshalJSON(kit)
	} else {
		body = []byte(recoverykit.RenderMarkdown(kit))
	}
	if err != nil {
		return err
	}

	out := cmdStdout(cmd, deps.Stdout)
	if outPath != "" {
		if err := os.WriteFile(outPath, body, 0o600); err != nil {
			return fmt.Errorf("write recovery kit: %w", err)
		}
		fmt.Fprintf(out, "%s %s\n", ui.Success.Render("Recovery kit written:"), outPath)
		return nil
	}
	_, err = out.Write(body)
	return err
}
