package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// parseRecommendations decodes the model's final text as a list of
// Recommendation. Real LLM output rarely matches the prompt exactly:
// models prepend prose, wrap arrays in code fences, return a single
// object instead of an array, or trail off with "Hope this helps!" —
// so the parser tries multiple shapes in order and accepts the first
// one that yields a valid Recommendation slice.
//
// Precedence (each step falls through to the next on failure):
//  1. Direct json.Unmarshal as []Recommendation.
//  2. Strip a single ``` … ``` (or ```json … ```) code fence and retry
//     direct array unmarshal.
//  3. Locate the substring from the first '[' to the last ']' and
//     unmarshal that as []Recommendation.
//  4. Try parsing the whole (or the fence-stripped) text as a single
//     Recommendation object and wrap it in a one-element slice.
//  5. Locate the substring from the first '{' to the last '}' and
//     parse that as a single Recommendation.
//
// If everything fails, returns an error wrapping a snippet of the
// actual response (first 200 chars) so logs and the CLI surface point
// the operator at what came back. Empty input is a clear error,
// distinct from the empty-array "nothing to recommend" case which
// step (1) handles cleanly.
func parseRecommendations(text string) ([]Recommendation, error) {
	original := text
	t := strings.TrimSpace(text)
	if t == "" {
		return nil, fmt.Errorf("empty response (got %q)", truncate(original, 200))
	}

	// Step 1: direct array.
	if recs, ok := tryUnmarshalArray(t); ok {
		return recs, nil
	}

	// Step 2: strip code fences (with or without language tag) and retry.
	stripped := stripFences(t)
	if stripped != t {
		if recs, ok := tryUnmarshalArray(stripped); ok {
			return recs, nil
		}
	}

	// Step 3: scan for the first '[' to the last ']' (handles prose
	// prefixes/suffixes around an inline array).
	if sub, ok := bracketSubstring(stripped, '[', ']'); ok {
		if recs, ok := tryUnmarshalArray(sub); ok {
			return recs, nil
		}
	}

	// Step 4: try a single Recommendation object — both on the
	// fence-stripped text and on a '{'-'}' substring scan.
	if rec, ok := tryUnmarshalObject(stripped); ok {
		return []Recommendation{rec}, nil
	}
	if sub, ok := bracketSubstring(stripped, '{', '}'); ok {
		if rec, ok := tryUnmarshalObject(sub); ok {
			return []Recommendation{rec}, nil
		}
	}

	return nil, fmt.Errorf("could not parse JSON recommendations from response (got %q)",
		truncate(original, 200))
}

// tryUnmarshalArray attempts to parse t as a JSON []Recommendation.
// Returns (recs, true) on success; (nil, false) on any failure. The
// boolean ok pattern avoids leaking parse errors up the chain — at
// each step we just want to know "did this shape work?".
func tryUnmarshalArray(t string) ([]Recommendation, bool) {
	var recs []Recommendation
	if err := json.Unmarshal([]byte(t), &recs); err != nil {
		return nil, false
	}
	return recs, true
}

// tryUnmarshalObject attempts to parse t as a single Recommendation.
// Returns (rec, true) on success; (Recommendation{}, false) otherwise.
func tryUnmarshalObject(t string) (Recommendation, bool) {
	var rec Recommendation
	if err := json.Unmarshal([]byte(t), &rec); err != nil {
		return Recommendation{}, false
	}
	return rec, true
}

// stripFences removes a single Markdown code-fence wrapper from t.
// Recognizes "```\n…\n```" and "```json\n…\n```" (and other language
// tags). If no leading fence is found, returns t unchanged so callers
// can detect "no fence stripping happened" by identity comparison.
func stripFences(t string) string {
	t = strings.TrimSpace(t)
	if !strings.HasPrefix(t, "```") {
		return t
	}
	// Drop everything up to and including the first newline (the
	// opening-fence line, optionally with a language tag).
	if nl := strings.IndexByte(t, '\n'); nl > 0 {
		t = t[nl+1:]
	} else {
		// Single-line "```json[…]```" pathology: just trim the
		// leading backticks.
		t = strings.TrimPrefix(t, "```")
	}
	// Drop the closing fence and anything after it.
	if end := strings.LastIndex(t, "```"); end >= 0 {
		t = t[:end]
	}
	return strings.TrimSpace(t)
}

// bracketSubstring returns the substring from the first occurrence
// of open to the last occurrence of close (both inclusive). Returns
// (s, true) when both delimiters were found in valid order, or
// ("", false) otherwise. Enables "salvage the JSON out of prose
// noise" without parsing the prose itself.
func bracketSubstring(t string, open, close byte) (string, bool) {
	start := strings.IndexByte(t, open)
	if start < 0 {
		return "", false
	}
	end := strings.LastIndexByte(t, close)
	if end < 0 || end <= start {
		return "", false
	}
	return t[start : end+1], true
}

// truncate clips s to at most n bytes (with an ellipsis), used for
// error context where we don't want to dump a multi-KB model response.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
