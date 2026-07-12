package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestRenderGraph_TruecolorGradient exercises the flourish path the Ascii test
// profile never reaches: on a truecolor terminal the braille graph must carry
// the btop vertical gradient. We assert the exact endpoint colors land — the
// top row in the hot stop (#FF6BDD → 255;107;221), the bottom row in the cool
// stop (#5CEBFF → 92;235;255) — which proves both that truecolor SGR is emitted
// and that it varies across rows rather than being one flat tint.
//
// The color profile is global process state; this test saves and restores it,
// and no test in the package runs in parallel.
func TestRenderGraph_TruecolorGradient(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	out := renderGraph([]int64{1, 2, 3, 4, 5, 6}, 12, 4)
	if !strings.Contains(out, "38;2;255;107;221") {
		t.Errorf("top graph row must use the hot gradient stop (#FF6BDD): %q", out)
	}
	if !strings.Contains(out, "38;2;92;235;255") {
		t.Errorf("bottom graph row must use the cool gradient stop (#5CEBFF): %q", out)
	}
}

// TestStyledGauge_TruecolorMeter is the meter's half of the same guarantee:
// on truecolor the filled cells carry the aqua→pink meter gradient, starting
// at aqua (#5CEBFF → 92;235;255).
func TestStyledGauge_TruecolorMeter(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	out := styledGauge(0.8, 10)
	if !strings.Contains(out, "38;2;92;235;255") {
		t.Errorf("meter fill must start at the aqua gradient stop (#5CEBFF): %q", out)
	}
	if !strings.Contains(out, "█") {
		t.Errorf("meter must still carry the fill glyph: %q", out)
	}
}

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

// TestDashboard_RendersDedupSavings: the repo panel must show what dedup +
// compression actually bought — stored (uploaded) bytes against logical bytes,
// as a percentage and a gauge whose meaning survives NO_COLOR (glyph fill,
// not color).
func TestDashboard_RendersDedupSavings(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 4,
		TotalBytes:    1000,
		UploadedBytes: 250,
	})
	view := d.View()
	if !strings.Contains(view, "75% saved") {
		t.Errorf("dashboard must show the dedup savings percentage: %s", view)
	}
	if !strings.Contains(view, "█") || !strings.Contains(view, "░") {
		t.Errorf("dashboard must render the savings gauge glyphs: %s", view)
	}
}

// TestDashboard_SavingsClampWhenUploadExceedsLogical: tiny repos can upload
// MORE than their logical size (AEAD sealing + zstd framing overhead beats
// dedup on small files). That must render as 0% saved, never a negative
// percentage or a panic.
func TestDashboard_SavingsClampWhenUploadExceedsLogical(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 1,
		TotalBytes:    100,
		UploadedBytes: 150,
	})
	view := d.View()
	if !strings.Contains(view, "0% saved") {
		t.Errorf("overhead-dominated repo must clamp to 0%% saved: %s", view)
	}
	if strings.Contains(view, "-") && strings.Contains(view, "% saved") &&
		strings.Contains(view, "-50% saved") {
		t.Errorf("savings must never go negative: %s", view)
	}
}

// TestDashboard_RendersTimelineSparkline: the timeline panel must graph real
// per-snapshot sizes (newest-first input, drawn oldest→newest), state the
// backup cadence, and show the covered date span. The v1 "coming soon"
// placeholder must be gone.
func TestDashboard_RendersTimelineSparkline(t *testing.T) {
	day := func(n int) time.Time {
		return time.Date(2026, 5, n, 12, 0, 0, 0, time.UTC)
	}
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 3,
		Snaps: []repo.SnapshotInfo{ // newest-first, as ListSnapshots returns
			{ID: "c", CreatedAt: day(5), Stats: repo.SnapshotStats{Bytes: 900}},
			{ID: "b", CreatedAt: day(3), Stats: repo.SnapshotStats{Bytes: 450}},
			{ID: "a", CreatedAt: day(1), Stats: repo.SnapshotStats{Bytes: 100}},
		},
	})
	view := d.View()
	if strings.Contains(view, "coming soon") {
		t.Fatalf("timeline placeholder must be replaced by the real graph: %s", view)
	}
	// The braille area graph fills from the baseline, so a full-height cell
	// (⣿) must appear somewhere in the rendered hero.
	if !strings.ContainsRune(view, '⣿') {
		t.Errorf("timeline must render the braille area graph: %s", view)
	}
	if !strings.Contains(view, "3 backups") {
		t.Errorf("timeline must count the backups it graphs: %s", view)
	}
	if !strings.Contains(view, "~2d apart") {
		t.Errorf("timeline must state the average cadence: %s", view)
	}
	if !strings.Contains(view, "2026-05-01 → 2026-05-05") {
		t.Errorf("timeline must show its date span: %s", view)
	}
}

// TestDashboard_TimelineEmptyState: a fresh repo still renders the timeline
// panel, with copy instead of an empty graph.
func TestDashboard_TimelineEmptyState(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "fresh"})
	view := d.View()
	if !strings.Contains(view, "activity") {
		t.Errorf("activity graph panel must render on an empty repo: %s", view)
	}
	if !strings.Contains(view, "no backups yet") {
		t.Errorf("empty repo must show empty-state copy: %s", view)
	}
	if strings.Contains(view, "0 backups") {
		t.Errorf("empty repo should show empty-state copy, not a zero count: %s", view)
	}
}

// TestDashboard_LastSnapShowsUploadDelta: the last-snapshot panel must show
// how much data that snapshot actually pushed (NewBytes) — the "how big was
// this delta" number — alongside the file count.
func TestDashboard_LastSnapShowsUploadDelta(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{
		SnapshotCount: 1,
		LastSnap: &repo.SnapshotInfo{
			ID:        "snap-deadbeef",
			CreatedAt: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC),
			Stats: repo.SnapshotStats{
				Files:    128,
				Bytes:    5 << 20,
				NewBytes: 2 << 20,
			},
		},
	})
	view := d.View()
	if !strings.Contains(view, "128 files") {
		t.Errorf("last-snapshot panel must show the file count: %s", view)
	}
	if !strings.Contains(view, "+2.0 MiB") {
		t.Errorf("last-snapshot panel must show the uploaded delta: %s", view)
	}
}

// TestDashboard_HydratePopulatesStats: hydration from a live repo must fill
// the new stat fields (per-snapshot series and uploaded-byte sum), not just
// the v1 aggregates — otherwise the sparkline and savings gauge silently
// render their empty states against a populated repo.
func TestDashboard_HydratePopulatesStats(t *testing.T) {
	r := newFlowRepo(t)
	seedTaggedSnaps(t, r, "a", "b")
	d := NewDashboard(Deps{Repo: r})
	if len(d.data.Snaps) != 2 {
		t.Errorf("hydrate must retain the snapshot series: want 2, got %d", len(d.data.Snaps))
	}
	if d.data.UploadedBytes <= 0 {
		t.Errorf("hydrate must sum uploaded (NewBytes) across snapshots, got %d", d.data.UploadedBytes)
	}
}

// TestAvgSnapshotInterval: cadence is span/(n-1) over the newest-first series.
// Fewer than two snapshots have no interval and must return zero.
func TestAvgSnapshotInterval(t *testing.T) {
	at := func(h int) time.Time {
		return time.Date(2026, 5, 1, h, 0, 0, 0, time.UTC)
	}
	newestFirst := []repo.SnapshotInfo{
		{CreatedAt: at(10)}, {CreatedAt: at(4)}, {CreatedAt: at(0)},
	}
	if got := avgSnapshotInterval(newestFirst); got != 5*time.Hour {
		t.Errorf("avg interval: want 5h, got %v", got)
	}
	if got := avgSnapshotInterval(newestFirst[:1]); got != 0 {
		t.Errorf("single snapshot has no interval: want 0, got %v", got)
	}
	if got := avgSnapshotInterval(nil); got != 0 {
		t.Errorf("empty series has no interval: want 0, got %v", got)
	}
}

// TestFormatApproxDuration: one coarse unit, matched to how operators talk
// about backup cadence ("about every 26 hours"), never "26h3m12s".
func TestFormatApproxDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{30 * time.Second, "<1m"},
		{90 * time.Second, "1m"},
		{45 * time.Minute, "45m"},
		{26 * time.Hour, "26h"},
		{49 * time.Hour, "2d"},
		{30 * 24 * time.Hour, "30d"},
	}
	for _, tc := range cases {
		if got := formatApproxDuration(tc.in); got != tc.want {
			t.Errorf("formatApproxDuration(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestDashboard_RefreshesAfterOpCompletes: the dashboard is hydrated once at
// launch, so a backup taken in-session must still update its counts. It must
// also preserve the agent recommendation count (which comes from a scan, not
// ListSnapshots) rather than zeroing it on refresh.
func TestDashboard_RefreshesAfterOpCompletes(t *testing.T) {
	r := newFlowRepo(t)
	d := NewDashboard(Deps{Repo: r}) // empty repo
	if d.data.SnapshotCount != 0 {
		t.Fatalf("precondition: want 0 snapshots, got %d", d.data.SnapshotCount)
	}
	d = d.SetData(DashboardData{RecCount: 3}) // a prior scan populated recs

	seedTaggedSnaps(t, r, "nightly")

	m, _ := d.Update(backupDoneMsg{})
	d = m.(Dashboard)
	if d.data.SnapshotCount != 1 {
		t.Fatalf("dashboard must refresh its snapshot count after an op: want 1, got %d", d.data.SnapshotCount)
	}
	if d.data.LastSnap == nil {
		t.Error("dashboard must show the last snapshot after refresh")
	}
	if d.data.RecCount != 3 {
		t.Errorf("refresh must preserve the agent rec count: want 3, got %d", d.data.RecCount)
	}
}
