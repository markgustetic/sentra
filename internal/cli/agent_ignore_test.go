package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/agent/heuristics"
)

func TestWriteIgnoreAdviceJSON_NilIsEmptyArray(t *testing.T) {
	var out bytes.Buffer
	if err := writeIgnoreAdviceJSON(&out, nil); err != nil {
		t.Fatalf("writeIgnoreAdviceJSON: %v", err)
	}
	if out.String() != "[]\n" {
		t.Fatalf("output = %q, want empty JSON array", out.String())
	}
}

func TestWriteIgnoreAdviceJSON_RoundTripsAdvice(t *testing.T) {
	advice := []ignoreAdvice{
		{
			Pattern:  "node_modules/",
			Category: "cache_dirs",
			Target:   "node_modules",
			Reason:   "regenerable cache/build directory",
		},
		{
			Pattern:  "big.bin",
			Category: "large_files",
			Target:   "/repo/big.bin",
			Reason:   "large file; review whether it belongs in encrypted backups",
			Size:     123,
		},
	}

	var out bytes.Buffer
	if err := writeIgnoreAdviceJSON(&out, advice); err != nil {
		t.Fatalf("writeIgnoreAdviceJSON: %v", err)
	}
	var got []ignoreAdvice
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\n%s", err, out.String())
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Pattern != "node_modules/" || got[1].Size != 123 {
		t.Fatalf("decoded advice = %+v", got)
	}
}

func TestIgnorePatternForFinding(t *testing.T) {
	root := t.TempDir()
	insideLarge := filepath.Join(root, "dist", "bundle.bin")
	outsideLarge := filepath.Join(t.TempDir(), "video.mov")

	cases := []struct {
		name    string
		finding heuristics.Finding
		want    string
	}{
		{
			name:    "cache dir gains slash",
			finding: heuristics.Finding{Category: "cache_dirs", Target: "node_modules"},
			want:    "node_modules/",
		},
		{
			name:    "large file inside root becomes relative",
			finding: heuristics.Finding{Category: "large_files", Target: insideLarge},
			want:    "dist/bundle.bin",
		},
		{
			name:    "large file outside root stays absolute",
			finding: heuristics.Finding{Category: "large_files", Target: outsideLarge},
			want:    filepath.ToSlash(outsideLarge),
		},
		{
			name:    "blank target ignored",
			finding: heuristics.Finding{Category: "large_files", Target: "   "},
			want:    "",
		},
		{
			name:    "unknown category ignored",
			finding: heuristics.Finding{Category: "something_else", Target: "x"},
			want:    "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ignorePatternForFinding(root, tc.finding); got != tc.want {
				t.Fatalf("pattern = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindingSize(t *testing.T) {
	cases := []struct {
		name    string
		details map[string]any
		want    int64
	}{
		{"missing", nil, 0},
		{"int64", map[string]any{"size": int64(5)}, 5},
		{"int", map[string]any{"size": 6}, 6},
		{"float64", map[string]any{"size": float64(7)}, 7},
		{"wrong type", map[string]any{"size": "large"}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := findingSize(heuristics.Finding{Details: tc.details}); got != tc.want {
				t.Fatalf("findingSize = %d, want %d", got, tc.want)
			}
		})
	}
}
