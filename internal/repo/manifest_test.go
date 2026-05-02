package repo

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestManifest_JSONRoundTrip_Full(t *testing.T) {
	mt := time.Date(2026, 5, 2, 15, 4, 5, 0, time.UTC)
	want := Manifest{
		Version:   ManifestVersion,
		ID:        "snap-20260502T150405Z-a1b2",
		CreatedAt: mt,
		Host:      "mark-mbp",
		Tag:       "weekly",
		Root:      "/Users/mark/Docs",
		Tree: []FileEntry{
			{
				Path:   "foo/bar.txt",
				Size:   1234,
				Mode:   os.FileMode(0o644),
				MTime:  mt,
				Chunks: []string{"abc123", "def456"},
			},
			{
				Path:   "baz.bin",
				Size:   42,
				Mode:   os.FileMode(0o600),
				MTime:  mt,
				Chunks: []string{"feedface"},
			},
		},
		Stats: SnapshotStats{Files: 2, Bytes: 1276, NewBytes: 1276},
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch:\n want=%+v\n got=%+v", want, got)
	}
}

func TestManifest_JSONRoundTrip_EmptyTagAndZeroStats(t *testing.T) {
	want := Manifest{
		Version:   ManifestVersion,
		ID:        "snap-20260502T000000Z-0000",
		CreatedAt: time.Unix(0, 0).UTC(),
		Host:      "h",
		Tag:       "", // empty
		Root:      "/",
		Tree:      []FileEntry{},
		Stats:     SnapshotStats{}, // zero
	}
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Manifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Tag != "" {
		t.Errorf("Tag round-trip lost: got %q", got.Tag)
	}
	if got.Stats != (SnapshotStats{}) {
		t.Errorf("Stats round-trip lost: got %+v", got.Stats)
	}
	if len(got.Tree) != 0 {
		t.Errorf("Tree round-trip lost: got len=%d", len(got.Tree))
	}
}

func TestManifest_VersionZeroParses(t *testing.T) {
	// Forward-compat: callers handle the version field, not unmarshal.
	// A manifest with Version: 0 must still parse without error.
	src := `{"version":0,"id":"x","created_at":"0001-01-01T00:00:00Z","host":"","root":"","tree":null,"stats":{"files":0,"bytes":0,"new_bytes":0}}`
	var got Manifest
	if err := json.Unmarshal([]byte(src), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 0 {
		t.Errorf("Version: got %d, want 0", got.Version)
	}
}

func TestManifest_TagOmittedWhenEmpty(t *testing.T) {
	// Phase 5 plan: Tag has `omitempty`; absent in JSON when empty.
	m := Manifest{Version: 1}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := string(b); contains(got, `"tag"`) {
		t.Errorf("expected 'tag' to be omitted, got %s", got)
	}
}

// contains is a small helper so we don't pull strings into this file
// for one assertion.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
