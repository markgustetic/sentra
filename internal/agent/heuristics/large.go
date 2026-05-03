package heuristics

import "context"

// DefaultLargeFileBytes is the default threshold for the large_files
// heuristic. 100 MiB matches the design doc's "files > threshold
// (default 100 MiB)" line and is the right default for backup-style
// repos: anything bigger is almost always media or build output that
// users want to know about.
const DefaultLargeFileBytes int64 = 100 << 20

// LargeFiles flags walked files whose size exceeds the configured
// threshold. The heuristic is purely metadata-driven — we never open
// the file — so it's effectively free.
type LargeFiles struct{}

// NewLargeFiles constructs a LargeFiles heuristic.
func NewLargeFiles() *LargeFiles { return &LargeFiles{} }

// Name is the registry-visible name of this heuristic.
func (l *LargeFiles) Name() string { return "large_files" }

// Run emits one warn finding per walked file larger than the
// configured threshold. Threshold falls back to DefaultLargeFileBytes
// when InputConfig.LargeFileBytes is zero.
//
// The predicate is strictly greater than (>): a file at exactly the
// threshold is not flagged. This matches the doc's "> threshold"
// wording and gives users a clean knob ("flag anything bigger than
// this").
func (l *LargeFiles) Run(ctx context.Context, in Input) ([]Finding, error) {
	threshold := in.Config.LargeFileBytes
	if threshold <= 0 {
		threshold = DefaultLargeFileBytes
	}
	var out []Finding
	for _, e := range in.Walked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Size <= threshold {
			continue
		}
		out = append(out, Finding{
			ID:       makeFindingID("large_files", e.AbsPath),
			Category: "large_files",
			Severity: SeverityWarn,
			Target:   e.AbsPath,
			Details: map[string]any{
				"size": e.Size,
			},
		})
	}
	return out, nil
}
