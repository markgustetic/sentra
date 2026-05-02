// Package walker provides filesystem traversal with .sentraignore
// support and a concurrent stat pipeline. It is the producer that
// feeds the snapshot path: emit a stream of file metadata, skip what
// the user has marked ignored, and bail cleanly when the caller
// cancels the context.
package walker

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"
)

// Matcher decides whether a repo-relative path is excluded by the
// project's .sentraignore. Patterns follow gitignore syntax: literal
// globs, "**" recursion, leading "!" to negate, trailing "/" to
// scope to directories.
//
// A nil *Matcher is valid and matches nothing — this lets callers
// avoid threading a "no ignore file" sentinel through the walk.
type Matcher struct {
	gi *gitignore.GitIgnore
}

// NewMatcher compiles patterns into a Matcher. Empty / nil input
// produces a matcher that never matches.
func NewMatcher(patterns []string) *Matcher {
	if len(patterns) == 0 {
		return &Matcher{}
	}
	return &Matcher{gi: gitignore.CompileIgnoreLines(patterns...)}
}

// Match reports whether path is ignored. path is forward-slash,
// repo-relative, no leading "/". The caller is responsible for
// normalizing OS-native paths before calling Match.
func (m *Matcher) Match(path string) bool {
	if m == nil || m.gi == nil {
		return false
	}
	return m.gi.MatchesPath(path)
}

// LoadIgnoreFile reads patterns from path (one per line, "#" comments
// and blank lines allowed, CRLF tolerated) and returns a Matcher.
//
// A missing file is not an error: a project without a .sentraignore
// should walk happily, so we return an empty Matcher. Any other I/O
// failure (permissions, EISDIR) is surfaced.
func LoadIgnoreFile(path string) (*Matcher, error) {
	// We do the read-and-split ourselves rather than calling the
	// library's CompileIgnoreFile for two reasons:
	//   1. The library's "missing file" error is not fs.ErrNotExist
	//      and we want a clean errors.Is check.
	//   2. CRLF stripping. The library handles "\n" splits; a CRLF
	//      file leaves a trailing "\r" on every pattern, which then
	//      fails to match. Stripping here keeps the contract in our
	//      own code.
	bs, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Matcher{}, nil
		}
		return nil, fmt.Errorf("walker: read ignore file %q: %w", path, err)
	}

	// Strip CR before splitting so Windows line endings don't smuggle
	// an extra byte into every pattern. ReplaceAll is cheap on bytes
	// we read once.
	text := strings.ReplaceAll(string(bs), "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	lines := strings.Split(text, "\n")

	return NewMatcher(lines), nil
}
