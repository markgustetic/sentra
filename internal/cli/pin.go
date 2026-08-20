package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/ui"
)

// PinDeps wires the side-effecting pieces of `sentra pin` / `unpin`.
type PinDeps struct {
	RepoDeps
}

// NewPin returns the cobra command for `sentra pin <snapshot>`: mark a
// snapshot as protected so retention never drops it and deletion
// refuses it until unpinned.
func NewPin(deps PinDeps) *cobra.Command {
	return newPinCommand(deps, true)
}

// NewUnpin returns the cobra command for `sentra unpin <snapshot>`.
func NewUnpin(deps PinDeps) *cobra.Command {
	return newPinCommand(deps, false)
}

func newPinCommand(deps PinDeps, pin bool) *cobra.Command {
	var cfgPath string
	verb, short := "pin", "Protect a snapshot from prune and deletion"
	if !pin {
		verb, short = "unpin", "Remove a snapshot's prune/deletion protection"
	}
	cmd := &cobra.Command{
		Use:   verb + " <snapshot>",
		Short: short,
		Long: short + ". The snapshot may be a full ID, \"latest\", a unique " +
			"prefix, or the trailing hex from `sentra snapshots`.",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPin(cmd, deps, args[0], cfgPath, pin)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

func runPin(cmd *cobra.Command, deps PinDeps, ref, cfgPath string, pin bool) error {
	cmd.SilenceUsage = true
	cfgPath = resolveConfigPath(cmd, cfgPath)

	r, pass, _, err := openRepoForConfig(cmd, cfgPath, deps.RepoDeps)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	id, err := r.ResolveSnapshotID(cmd.Context(), ref)
	if err != nil {
		return err
	}
	out := cmdStdout(cmd, deps.Stdout)
	if pin {
		if err := r.Pin(cmd.Context(), id); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s %s\n", ui.Success.Render("Pinned"), id)
		return nil
	}
	if err := r.Unpin(cmd.Context(), id); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s\n", ui.Success.Render("Unpinned"), id)
	return nil
}
