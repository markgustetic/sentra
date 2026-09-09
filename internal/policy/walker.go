package policy

import (
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/walker"
)

// BackupWalkerOptions resolves sentra.yaml's backup.* keys into the
// walker options every snapshot-taking surface must use — `backup`,
// `policy run`, the agent's walks, `sentra mcp` and both TUI runs — so
// an MCP-confirmed or TUI-driven backup walks exactly what the CLI one
// would. IgnoreFile is never left empty: the repo reads an all-zero
// walker.Options as "legacy defaults" and forces ExcludeCaches on, so a
// config with `ignore_file: ""` and an explicit `exclude_caches: false`
// would have its intent silently overridden. Seven sites once built the
// literal by hand; two forgot the fill-in and one dropped Concurrency.
//
// nil cfg returns the zero Options on purpose: with no config there is
// no operator intent to honor, and the repo's legacy defaults are the
// right walk.
func BackupWalkerOptions(cfg *config.Config) walker.Options {
	if cfg == nil {
		return walker.Options{}
	}
	opts := walker.Options{
		IgnoreFile:    cfg.Backup.IgnoreFile,
		ExcludeCaches: cfg.Backup.ExcludeCaches,
		Concurrency:   cfg.Backup.Concurrency,
	}
	if opts.IgnoreFile == "" {
		opts.IgnoreFile = ".sentraignore"
	}
	return opts
}
