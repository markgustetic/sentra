# Contributing

Sentra is a personal project; issues and small PRs are welcome, large
features are best discussed in an issue first.

## Development

- Go 1.27+. `just build` / `just test` / `just check` (the full local gate:
  build, race tests, vet, lint, vuln scan, tidy/fmt/diff checks).
- Integration tests need Docker: `just integration`.
- `just local` runs the TUI against a local MinIO — the easiest way to try
  changes end to end.

## Ground rules

- TDD: the failing test comes first. Table-driven where it fits.
- The invariants in [CLAUDE.md](CLAUDE.md) and the per-command contract in
  [AGENTS.md](AGENTS.md) are load-bearing — a PR that breaks one needs a
  very good story.
- Every mutating capability lands as a CLI verb (the machine/recovery
  surface) — TUI-only features are a defect by contract.
