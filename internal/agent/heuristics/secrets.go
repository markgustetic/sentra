package heuristics

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/markgustetic/sentra/internal/walker"
)

// secretsScanCap is the per-file byte cap. Anything larger is skipped
// entirely (not scanned partially) — partial scans miss matches near
// the end of large files and create user confusion. 1 MiB is plenty
// for source files, configs, and plain-text manifests; bigger blobs
// are usually media or compiled artifacts where regex matches would
// be misleading anyway.
const secretsScanCap int64 = 1 << 20

// binarySniffSize is the prefix we read to detect binary content via
// the NUL-byte heuristic. 8 KiB matches what tools like `file(1)` use
// for the same job and is large enough to catch binary headers without
// being so large that we waste I/O on small files.
const binarySniffSize = 8 << 10

// previewMaxLen is the cap on the redacted preview returned in
// Finding.Details["preview"]. The preview is purely a UI hint
// ("here's roughly what context the match was in") — it must not
// contain the secret itself, so callers are limited to surrounding
// context only.
const previewMaxLen = 32

// dotEnvFilenames is the set of filenames that opt in to the looser
// generic-API-key pattern. The pattern matches "ALL_CAPS_NAME=long",
// which is far too noisy on arbitrary files (CI config dumps, build
// scripts) but exactly right inside .env-style configs.
var dotEnvFilenames = map[string]struct{}{
	".env":             {},
	".env.local":       {},
	".env.development": {},
	".env.production":  {},
	".env.test":        {},
	".env.staging":     {},
}

// secretPattern bundles a compiled regex with metadata used to build
// the eventual Finding. dotenvOnly gates the pattern to .env-style
// files (see dotEnvFilenames).
type secretPattern struct {
	name       string
	rx         *regexp.Regexp
	dotenvOnly bool
}

// secretPatterns is the in-memory pattern set, compiled once at
// package init. The list is intentionally small and high-confidence —
// adding fuzzier patterns inflates the false-positive rate, which is
// far worse than missing a secret (the agent's recall is stronger
// when the user trusts each finding).
var secretPatterns = compileSecretPatterns()

func compileSecretPatterns() []secretPattern {
	return []secretPattern{
		{
			name: "aws_access_key",
			// AKIA / ASIA / AGPA prefixes followed by 16 uppercase
			// alphanumerics. The trailing `\b` keeps us from matching
			// in the middle of a longer alphanumeric blob.
			rx: regexp.MustCompile(`\b(?:AKIA|ASIA|AGPA)[0-9A-Z]{16}\b`),
		},
		{
			name: "aws_secret_key",
			// "aws_secret_access_key" assignment followed by a 30+ char
			// base64-ish value. Case-insensitive on the key name to
			// match `AWS_SECRET_ACCESS_KEY` env-style and the lower-case
			// INI-style spelling.
			rx: regexp.MustCompile(`(?i)aws_secret_access_key\s*=\s*['"]?([A-Za-z0-9+/=]{30,})`),
		},
		{
			name: "github_pat",
			// Modern GitHub PAT format: gh{p,o,s,r,u}_<36 alphanumeric>.
			// Strict on the prefix character set to avoid matching
			// random `gh_...` strings.
			rx: regexp.MustCompile(`\bgh[psoru]_[A-Za-z0-9]{36}\b`),
		},
		{
			name: "private_key",
			// PEM/OpenSSH private-key headers. The optional algorithm
			// prefix (RSA/OPENSSH/EC/DSA/PGP) covers the common shapes
			// without needing one regex per format.
			rx: regexp.MustCompile(`-----BEGIN (?:RSA |OPENSSH |EC |DSA |PGP )?PRIVATE KEY-----`),
		},
		{
			name: "dotenv_generic_key",
			// Generic ALL_CAPS_NAME = "long_value" pattern. Only ever
			// fires inside .env-style files (gated below) because of
			// the high false-positive rate on arbitrary text.
			rx:         regexp.MustCompile(`[A-Z][A-Z0-9_]{7,}\s*=\s*['"]?[A-Za-z0-9+/=]{32,}`),
			dotenvOnly: true,
		},
	}
}

// Secrets is the secrets heuristic. NewSecrets returns a value-type
// instance because the heuristic is stateless — patterns are package-
// level — but the constructor keeps the API symmetrical with other
// heuristics that DO need state.
type Secrets struct{}

// NewSecrets constructs a Secrets heuristic.
func NewSecrets() *Secrets { return &Secrets{} }

// Name is the registry-visible name of this heuristic.
func (s *Secrets) Name() string { return "secrets" }

// Run scans every walked file for high-confidence secret patterns.
// Skips:
//   - files larger than secretsScanCap
//   - files whose first binarySniffSize bytes contain a NUL (binary)
//
// Each match emits a critical finding. Findings never include the raw
// match value — Details["preview"] is a redacted excerpt.
func (s *Secrets) Run(ctx context.Context, in Input) ([]Finding, error) {
	var out []Finding
	for _, e := range in.Walked {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if e.Size > secretsScanCap {
			continue
		}
		findings, err := scanForSecrets(e)
		if err != nil {
			// Per-file errors (transient open failures, race with
			// deletion) shouldn't fail the whole scan. Skip the file
			// and move on; the agent caller can re-run if needed.
			continue
		}
		out = append(out, findings...)
	}
	return out, nil
}

// scanForSecrets opens entry, sniffs for binary, then runs the regex
// set line-by-line. The bufio.Scanner is line-oriented because we
// emit per-line findings — letting us include the line number in
// Details and bound preview length naturally at line boundaries.
func scanForSecrets(e walker.Entry) ([]Finding, error) {
	f, err := os.Open(e.AbsPath) //nolint:gosec // path is from walker.Entry, controlled
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Sniff the first chunk for binary content. Reading and then
	// re-seeking is simpler than trying to teach the scanner about
	// the lookahead.
	sniff := make([]byte, binarySniffSize)
	n, err := io.ReadFull(f, sniff)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	if bytes.IndexByte(sniff[:n], 0) >= 0 {
		// NUL byte in the head → treat as binary, skip.
		return nil, nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	base := filepath.Base(e.AbsPath)
	_, isDotEnv := dotEnvFilenames[base]

	var out []Finding
	scanner := bufio.NewScanner(f)
	// Allow lines up to scanCap; default 64KiB is too small for some
	// minified configs that legitimately fit single-line secrets.
	scanner.Buffer(make([]byte, 0, 64*1024), int(secretsScanCap))

	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		for _, p := range secretPatterns {
			if p.dotenvOnly && !isDotEnv {
				continue
			}
			loc := p.rx.FindStringIndex(line)
			if loc == nil {
				continue
			}
			out = append(out, Finding{
				ID:       makeFindingID("secrets", fmt.Sprintf("%s:%d:%s", e.AbsPath, lineNum, p.name)),
				Category: "secrets",
				Severity: SeverityCritical,
				Target:   e.AbsPath,
				Details: map[string]any{
					"pattern": p.name,
					"line":    lineNum,
					"preview": redactPreview(line, loc),
				},
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// redactPreview returns a short snippet of the matched line with the
// match itself replaced by `[REDACTED]`. We deliberately keep at most
// previewMaxLen characters of context so the user can see *which*
// line the match was on without seeing the secret.
//
// loc is [matchStart, matchEnd) into line, as returned by
// regexp.FindStringIndex.
func redactPreview(line string, loc []int) string {
	if loc == nil || len(loc) != 2 || loc[0] < 0 || loc[1] > len(line) {
		// Defensive: malformed location → fall back to a fixed redact
		// so we never accidentally return the raw line.
		return "[REDACTED]"
	}
	prefix := line[:loc[0]]
	suffix := line[loc[1]:]
	redacted := prefix + "[REDACTED]" + suffix
	// Trim the result to previewMaxLen chars centered roughly on the
	// `[REDACTED]` marker so the user sees a useful slice.
	const marker = "[REDACTED]"
	idx := strings.Index(redacted, marker)
	if idx < 0 {
		// Should never happen given the construction above.
		return "[REDACTED]"
	}
	// Take previewMaxLen chars total: half before, half after.
	half := previewMaxLen / 2
	start := idx - half
	if start < 0 {
		start = 0
	}
	end := start + previewMaxLen
	if end > len(redacted) {
		end = len(redacted)
		if end-previewMaxLen > 0 {
			start = end - previewMaxLen
		}
	}
	return redacted[start:end]
}
