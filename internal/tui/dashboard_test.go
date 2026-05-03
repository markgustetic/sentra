package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestDashboard_RendersRepoName locks in the repo name as the most-
// prominent label in the dashboard. The user opens `sentra ui` and
// the first thing they see should confirm WHICH repo they're looking
// at.
func TestDashboard_RendersRepoName(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "my-backups"})
	view := d.View()
	if !strings.Contains(view, "my-backups") {
		t.Errorf("dashboard view did not contain repo name: %s", view)
	}
}

// TestDashboard_RendersSnapshotCount verifies the count surfaced in
// the repo summary panel matches what the model was told. We hydrate
// the dashboard via SetData so tests don't need a live repo.
func TestDashboard_RendersSnapshotCount(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 42,
		TotalBytes:    1024 * 1024,
	})
	view := d.View()
	if !strings.Contains(view, "42") {
		t.Errorf("dashboard view did not contain snapshot count 42: %s", view)
	}
}

// TestDashboard_RendersLastSnapshot verifies the most-recent snapshot
// summary panel shows the snapshot ID and tag. A repo without
// snapshots takes the empty-repo branch (covered separately).
func TestDashboard_RendersLastSnapshot(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 1,
		LastSnap: &repo.SnapshotInfo{
			ID:        "snap-deadbeef",
			Tag:       "weekly",
			CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
		},
	})
	view := d.View()
	if !strings.Contains(view, "snap-deadbeef") {
		t.Errorf("dashboard view did not show last snapshot ID: %s", view)
	}
	if !strings.Contains(view, "weekly") {
		t.Errorf("dashboard view did not show last snapshot tag: %s", view)
	}
}

// TestDashboard_HandlesEmptyRepo asserts a dashboard with zero
// snapshots renders without panic and shows the user-facing
// empty-state copy ("no snapshots yet" or similar). The exact text
// can drift; we just check the panel acknowledges emptiness.
func TestDashboard_HandlesEmptyRepo(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "fresh"})
	view := d.View()
	if !strings.Contains(strings.ToLower(view), "no snapshots") {
		t.Errorf("empty repo did not render no-snapshots placeholder: %s", view)
	}
}

// TestDashboard_RendersAgentBadge surfaces pending recommendation
// counts. Zero recommendations is fine ("agent: 0"); any other count
// signals work the user can pick up.
func TestDashboard_RendersAgentBadge(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{RecCount: 7})
	view := d.View()
	if !strings.Contains(view, "7") {
		t.Errorf("dashboard view did not include rec count: %s", view)
	}
}
