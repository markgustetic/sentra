package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
)

func TestOperations_RendersHealthyReport(t *testing.T) {
	view := NewOperations(Deps{}).SetData(OperationsData{
		Report: repo.CheckReport{
			CheckedAt:       time.Date(2026, 6, 23, 12, 0, 0, 0, time.UTC),
			Snapshots:       3,
			Files:           12,
			ReferencedBlobs: 9,
			DataBlobs:       9,
		},
	}).View()

	for _, want := range []string{"healthy", "3 snapshots", "9 referenced"} {
		if !strings.Contains(strings.ToLower(view), want) {
			t.Fatalf("operations view missing %q:\n%s", want, view)
		}
	}
}

func TestOperations_RendersIssues(t *testing.T) {
	view := NewOperations(Deps{}).SetData(OperationsData{
		Report: repo.CheckReport{
			MissingBlobs: []repo.MissingBlob{{Key: "data/aa/missing", SnapshotID: "snap-x", Path: "a.txt"}},
			OrphanBlobs:  []repo.BlobIssue{{Key: "data/bb/orphan", Size: 10}},
			Lock:         &repo.LockReport{Present: true, Stale: true, Operation: "gc"},
		},
	}).View()

	for _, want := range []string{"failed", "missing blobs", "orphan blobs", "stale"} {
		if !strings.Contains(strings.ToLower(view), want) {
			t.Fatalf("operations issue view missing %q:\n%s", want, view)
		}
	}
}
