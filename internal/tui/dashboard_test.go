package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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

// --- new-section helpers -------------------------------------------------

// TestShortBytes locks the compact byte format used by the dense panels and the
// snapshots table: single-letter units, one decimal below ten, none at/above.
func TestShortBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0B"},
		{999, "999B"},
		{1024, "1.0K"},
		{1536, "1.5K"},
		{10 * 1024, "10K"},
		{128 << 20, "128M"},
		{8<<20 + 512<<10, "8.5M"},
		{1 << 30, "1.0G"},
		{3 << 40, "3.0T"},
	}
	for _, tc := range cases {
		if got := shortBytes(tc.in); got != tc.want {
			t.Errorf("shortBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTagBreakdown groups snapshots by tag (empty → "(untagged)"), sums bytes
// and counts, and orders by total size descending — what the tags panel needs.
func TestTagBreakdown(t *testing.T) {
	mib := func(n int64) int64 { return n << 20 }
	snaps := []repo.SnapshotInfo{
		{Tag: "nightly", Stats: repo.SnapshotStats{Bytes: mib(96)}},
		{Tag: "nightly", Stats: repo.SnapshotStats{Bytes: mib(94)}},
		{Tag: "weekly", Stats: repo.SnapshotStats{Bytes: mib(61)}},
		{Tag: "", Stats: repo.SnapshotStats{Bytes: mib(58)}},
	}
	got := tagBreakdown(snaps)
	if len(got) != 3 {
		t.Fatalf("want 3 tag groups, got %d: %+v", len(got), got)
	}
	if got[0].tag != "nightly" || got[0].count != 2 || got[0].bytes != mib(190) {
		t.Errorf("first group = %+v, want nightly/2/190MiB", got[0])
	}
	if got[1].tag != "weekly" || got[1].bytes != mib(61) {
		t.Errorf("second group = %+v, want weekly/61MiB", got[1])
	}
	if got[2].tag != "(untagged)" || got[2].bytes != mib(58) {
		t.Errorf("third group = %+v, want (untagged)/58MiB", got[2])
	}
}

// TestComputeDashLayout locks the responsive budget: which sections appear at a
// given content height, and that the pieces always sum to exactly that height
// (so the dashboard never over/underflows the content pane).
func TestComputeDashLayout(t *testing.T) {
	cases := []struct {
		availH     int
		wantStatsB bool
		wantTable  bool
	}{
		{16, false, false}, // 80x20 floor: hero + one stats row only
		{18, false, false},
		{19, true, false}, // room for the second stats row
		{24, true, false},
		{25, true, true}, // room for the snapshots table
		{40, true, true},
	}
	for _, tc := range cases {
		lo := computeDashLayout(tc.availH)
		if lo.showStatsB != tc.wantStatsB {
			t.Errorf("availH %d: showStatsB = %v, want %v", tc.availH, lo.showStatsB, tc.wantStatsB)
		}
		if (lo.table > 0) != tc.wantTable {
			t.Errorf("availH %d: table shown = %v (h=%d), want %v", tc.availH, lo.table > 0, lo.table, tc.wantTable)
		}
		// The pieces must tile the height exactly.
		statsRows := 1
		if lo.showStatsB {
			statsRows = 2
		}
		sum := lo.hero + statsRows*dashStatsBlock + lo.table
		if sum != tc.availH {
			t.Errorf("availH %d: hero(%d)+stats(%d)+table(%d) = %d, want %d",
				tc.availH, lo.hero, statsRows*dashStatsBlock, lo.table, sum, tc.availH)
		}
		if lo.hero < 5 {
			t.Errorf("availH %d: hero %d below the minimum 5", tc.availH, lo.hero)
		}
	}
}

// TestDashboard_ShowsAllSectionsWhenTall: a tall terminal must render the full
// btop-style layout — the tags and retention panels and the recent-snapshots
// table, not just the hero + first stats row.
func TestDashboard_ShowsAllSectionsWhenTall(t *testing.T) {
	day := func(n int) time.Time { return time.Date(2026, 6, n, 3, 30, 0, 0, time.UTC) }
	snaps := []repo.SnapshotInfo{
		{ID: "snap-newest", CreatedAt: day(9), Tag: "nightly", Stats: repo.SnapshotStats{Files: 100, Bytes: 96 << 20, NewBytes: 3 << 20}},
		{ID: "snap-older", CreatedAt: day(5), Tag: "weekly", Stats: repo.SnapshotStats{Files: 90, Bytes: 61 << 20, NewBytes: 12 << 20}},
	}
	data := DashboardData{SnapshotCount: 2, LastSnap: &snaps[0], Snaps: snaps,
		TotalBytes: (96 + 61) << 20, UploadedBytes: 15 << 20}
	d := NewDashboard(Deps{RepoName: "x"}) // nil Config → retention shows defaults
	m, _ := d.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	d = m.(Dashboard).SetData(data)

	view := d.View()
	for _, want := range []string{
		"tags", "nightly", "weekly", // tags panel
		"retention", "keep last", // retention panel
		"created", "snap-newest", // snapshots table (header + a row)
	} {
		if !strings.Contains(view, want) {
			t.Errorf("tall dashboard is missing %q:\n%s", want, view)
		}
	}
}

// TestDashboard_SectionsHiddenWhenShort: at the 80x20 floor the dashboard shows
// only the hero and the first stats row — the extra sections must not appear (or
// they would overflow the content pane).
func TestDashboard_SectionsHiddenWhenShort(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	d = d.SetData(DashboardData{SnapshotCount: 1, TotalBytes: 1 << 20, UploadedBytes: 1 << 19,
		Snaps: []repo.SnapshotInfo{{Tag: "nightly", Stats: repo.SnapshotStats{Bytes: 1 << 20}}}})
	// No WindowSizeMsg → the min-terminal fallback (availH 16).
	view := d.View()
	if strings.Contains(view, "retention") {
		t.Errorf("retention panel must be hidden at the minimum height:\n%s", view)
	}
	if strings.Contains(view, "created ") { // the table header column
		t.Errorf("snapshots table must be hidden at the minimum height:\n%s", view)
	}
}

// TestDashboard_TableShowsOverflow: when more snapshots exist than table rows,
// the last row must be a "… N more" marker rather than a silent truncation.
func TestDashboard_TableShowsOverflow(t *testing.T) {
	var snaps []repo.SnapshotInfo
	for i := range 30 {
		snaps = append(snaps, repo.SnapshotInfo{
			ID: "s", CreatedAt: time.Date(2026, 6, 1, 0, 0, i, 0, time.UTC),
			Tag: "t", Stats: repo.SnapshotStats{Bytes: 1 << 20}})
	}
	data := DashboardData{SnapshotCount: 30, LastSnap: &snaps[0], Snaps: snaps,
		TotalBytes: 30 << 20, UploadedBytes: 10 << 20}
	d := NewDashboard(Deps{RepoName: "x"})
	m, _ := d.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	d = m.(Dashboard).SetData(data)
	if view := d.View(); !strings.Contains(view, "more") {
		t.Errorf("a table with more snapshots than rows must show a '… N more' marker:\n%s", view)
	}
}

// TestDashboard_StorageDetail: the storage panel must surface the dedup ratio,
// the average snapshot size, and the largest snapshot.
func TestDashboard_StorageDetail(t *testing.T) {
	d := NewDashboard(Deps{RepoName: "x"})
	m, _ := d.Update(tea.WindowSizeMsg{Width: 100, Height: 20}) // wide enough not to truncate the detail line
	d = m.(Dashboard).SetData(DashboardData{
		SnapshotCount: 2, TotalBytes: 200 << 20, UploadedBytes: 50 << 20,
		Snaps: []repo.SnapshotInfo{
			{Stats: repo.SnapshotStats{Bytes: 128 << 20}},
			{Stats: repo.SnapshotStats{Bytes: 72 << 20}},
		},
	})
	view := d.View()
	for _, want := range []string{"4.0×", "avg 100M", "max 128M"} {
		if !strings.Contains(view, want) {
			t.Errorf("storage detail missing %q:\n%s", want, view)
		}
	}
}

// TestDashboard_TilesHeightExactly is the layout backstop: the dashboard body
// must be EXACTLY the content height at every size, populated or empty. The App
// pads short content but does not clip tall content, so a body one row too tall
// overflows the frame. It regresses a real off-by-one at the heights where the
// hero graph lands on its minimum (availH 19 and 25).
func TestDashboard_TilesHeightExactly(t *testing.T) {
	populated := DashboardData{
		SnapshotCount: 2, TotalBytes: 157 << 20, UploadedBytes: 15 << 20,
		LastSnap: &repo.SnapshotInfo{ID: "a", Tag: "n", Stats: repo.SnapshotStats{Files: 10, Bytes: 96 << 20}},
		Snaps: []repo.SnapshotInfo{
			{ID: "a", CreatedAt: time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC), Tag: "n", Stats: repo.SnapshotStats{Files: 10, Bytes: 96 << 20, NewBytes: 3 << 20}},
			{ID: "b", CreatedAt: time.Date(2026, 6, 5, 0, 0, 0, 0, time.UTC), Tag: "w", Stats: repo.SnapshotStats{Files: 9, Bytes: 61 << 20, NewBytes: 12 << 20}},
		},
	}
	for _, data := range []DashboardData{populated, {}} {
		for h := 16; h <= 42; h++ {
			d := NewDashboard(Deps{RepoName: "x"})
			m, _ := d.Update(tea.WindowSizeMsg{Width: 100, Height: h})
			d = m.(Dashboard).SetData(data)
			if got := len(strings.Split(d.View(), "\n")); got != h {
				t.Errorf("availH=%d (empty=%v): View has %d lines, want exactly %d",
					h, data.SnapshotCount == 0, got, h)
			}
		}
	}
}

// TestDashboard_PeriodicRefresh: Init arms the refresh tick; a tick loads
// asynchronously and re-arms; the delivered data is adopted (preserving the
// agent RecCount, which comes from a scan, not ListSnapshots).
func TestDashboard_PeriodicRefresh(t *testing.T) {
	r := newFlowRepo(t)
	d := NewDashboard(Deps{Repo: r})
	if d.Init() == nil {
		t.Error("Init must arm the refresh tick")
	}

	// A tick emits work (the async load + the re-armed tick).
	if _, cmd := d.Update(dashboardTickMsg{}); cmd == nil {
		t.Error("a tick must emit a load + re-arm command")
	}

	// dashLoadCmd hydrates from the repo off-loop.
	seedTaggedSnaps(t, r, "a", "b")
	msg, ok := dashLoadCmd(Deps{Repo: r})().(dashboardDataMsg)
	if !ok || msg.data.SnapshotCount != 2 {
		t.Fatalf("dashLoadCmd should hydrate 2 snapshots, got %+v", msg)
	}

	// Delivering data adopts it but keeps the agent finding count.
	d = d.SetData(DashboardData{RecCount: 5})
	m, _ := d.Update(dashboardDataMsg{data: DashboardData{SnapshotCount: 9}})
	d = m.(Dashboard)
	if d.data.SnapshotCount != 9 {
		t.Errorf("data not adopted: count = %d, want 9", d.data.SnapshotCount)
	}
	if d.data.RecCount != 5 {
		t.Errorf("refresh must preserve RecCount: got %d, want 5", d.data.RecCount)
	}
}
