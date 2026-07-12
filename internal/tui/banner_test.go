package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// TestTitleRows locks the responsive threshold: the tall synthwave banner only
// engages at or above bannerMinHeight, and it is deliberately above the common
// 30-row test height so the existing geometry tests keep their one-line header
// and their contentH budget.
func TestTitleRows(t *testing.T) {
	cases := []struct {
		h    int
		want int
	}{
		{0, 1},  // pre-first-size
		{20, 1}, // 80x20 minimum — the overflow backstop runs here
		{30, 1}, // the common test height must stay one line
		{31, 1}, // just below the threshold
		{32, bannerRows},
		{60, bannerRows},
	}
	for _, tc := range cases {
		if got := titleRows(tc.h); got != tc.want {
			t.Errorf("titleRows(%d) = %d, want %d", tc.h, got, tc.want)
		}
	}
}

// TestSynthwaveBanner_Geometry: the banner is exactly bannerRows tall and, when
// centered to a width, every line is exactly that many cells — the property
// resize() relies on to keep the frame from over/underflowing.
func TestSynthwaveBanner_Geometry(t *testing.T) {
	const w = 80
	lines := strings.Split(synthwaveBanner(w, 0), "\n")
	if len(lines) != bannerRows {
		t.Fatalf("banner is %d lines, want %d", len(lines), bannerRows)
	}
	for i, ln := range lines {
		if got := lipgloss.Width(ln); got != w {
			t.Errorf("line %d width = %d, want %d (centered fill): %q", i, got, w, ln)
		}
	}
}

// TestSynthwaveBanner_CarriesTheScene: the banner must contain all three pieces
// of the synthwave logo — the sun, the large SENTRA wordmark, and the grid
// horizon — so a palette swap or a refactor that drops one is caught.
func TestSynthwaveBanner_CarriesTheScene(t *testing.T) {
	out := synthwaveBanner(80, 0)
	if !strings.Contains(out, "S E N T R A") {
		t.Errorf("banner is missing the SENTRA logotype:\n%s", out)
	}
	if !strings.Contains(out, bannerSunArt[0]) {
		t.Errorf("banner is missing the sun:\n%s", out)
	}
	if !strings.Contains(out, "│") {
		t.Errorf("banner is missing the grid horizon:\n%s", out)
	}
}

// TestSynthwaveBanner_PlainUnderAsciiProfile: the banner is color over fixed
// glyphs, so under the Ascii profile the unit tests run in it must emit no ANSI
// at all. And because the animation is COLOR only, the glyphs are identical
// across frames under Ascii — that frame-independence is what keeps the geometry
// deterministic and the golden stable regardless of the ambient clock.
func TestSynthwaveBanner_PlainUnderAsciiProfile(t *testing.T) {
	if strings.Contains(synthwaveBanner(80, 0), "\x1b") {
		t.Error("banner must strip to plain glyphs under the Ascii color profile")
	}
	if synthwaveBanner(80, 0) != synthwaveBanner(80, 7) {
		t.Error("under Ascii the banner shape must be frame-independent (animation is color only)")
	}
}

// TestSynthwaveBanner_AnimatesWithFrame: on a truecolor terminal the banner must
// actually move — consecutive ambient frames must render differently — or the
// animation is dead. The color profile is process-global; this test saves and
// restores it, and no test in the package runs in parallel.
func TestSynthwaveBanner_AnimatesWithFrame(t *testing.T) {
	prev := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	t.Cleanup(func() { lipgloss.SetColorProfile(prev) })

	// Compare frames a full slowdown apart, since bannerSlowdown holds each
	// step for several ticks (frame 0 and frame 1 share a tick by design).
	if synthwaveBanner(80, 0) == synthwaveBanner(80, bannerSlowdown) {
		t.Error("banner must animate: frames a slowdown apart must differ under truecolor")
	}
}
