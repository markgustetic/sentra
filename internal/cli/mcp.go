package cli

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/mcpserver"
)

// MCPDeps wires `sentra mcp`. PassphraseFile mirrors the launch probe: a
// func reading the live --passphrase-file flag at run time.
type MCPDeps struct {
	RepoDeps
	Version        string
	PassphraseFile func() string
}

// NewMCP returns the cobra command for `sentra mcp`: a Model Context
// Protocol server over stdio, exposing metadata-only snapshot queries and
// two-phase (plan → token → confirm) backup/restore to MCP clients.
//
// stdin/stdout ARE the protocol channel, so the passphrase resolves
// non-interactively only (env / --passphrase-file / keyring) — a missing
// source is a startup error, never a prompt. Logs stay on stderr, which
// stdio MCP leaves free.
func NewMCP(deps MCPDeps) *cobra.Command {
	var cfgPath string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve this repository to MCP clients over stdio",
		Long: "Run a Model Context Protocol server on stdin/stdout so AI " +
			"assistants can query snapshots (metadata only — never file " +
			"contents) and drive confirm-gated backups and restores. " +
			"Point your MCP client at this command; the passphrase must " +
			"resolve non-interactively (SENTRA_PASSPHRASE, " +
			"--passphrase-file, or the OS keyring).",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runMCP(cmd, deps, cfgPath)
		},
	}
	cmd.Flags().StringVar(&cfgPath, "config", configFileName,
		"path to sentra.yaml (default: ./sentra.yaml, else ~/.config/sentra/sentra.yaml)")
	return cmd
}

func runMCP(cmd *cobra.Command, deps MCPDeps, cfgPath string) error {
	cmd.SilenceUsage = true
	cfgPath, err := resolveConfigPath(cmd, cfgPath)
	if err != nil {
		return err
	}

	passFile := ""
	if deps.PassphraseFile != nil {
		passFile = deps.PassphraseFile()
	}
	r, pass, _, err := openRepoForConfigNonInteractive(cmd, cfgPath, passFile, deps.NewStore)
	if err != nil {
		return err
	}
	defer crypto.Zeroize(pass)
	defer r.Close()

	srv := mcpserver.New(r, deps.Version)
	return srv.Run(cmd.Context(), &mcp.StdioTransport{})
}
