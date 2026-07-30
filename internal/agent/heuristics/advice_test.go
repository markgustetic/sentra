package heuristics

import (
	"path/filepath"
	"testing"
)

func TestIgnorePatternForFinding(t *testing.T) {
	root := t.TempDir()
	insideLarge := filepath.Join(root, "dist", "bundle.bin")
	outsideLarge := filepath.Join(t.TempDir(), "video.mov")

	cases := []struct {
		name    string
		finding Finding
		want    string
	}{
		{
			name:    "cache dir gains slash",
			finding: Finding{Category: "cache_dirs", Target: "node_modules"},
			want:    "node_modules/",
		},
		{
			name:    "large file inside root becomes relative",
			finding: Finding{Category: "large_files", Target: insideLarge},
			want:    "dist/bundle.bin",
		},
		{
			name:    "large file outside root stays absolute",
			finding: Finding{Category: "large_files", Target: outsideLarge},
			want:    filepath.ToSlash(outsideLarge),
		},
		{
			name:    "blank target ignored",
			finding: Finding{Category: "large_files", Target: "   "},
			want:    "",
		},
		{
			name:    "unknown category ignored",
			finding: Finding{Category: "something_else", Target: "x"},
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
			if got := findingSize(Finding{Details: tc.details}); got != tc.want {
				t.Fatalf("findingSize = %d, want %d", got, tc.want)
			}
		})
	}
}
