package ui

import (
	"strings"
	"testing"
)

// TestSeverity_KnownLevels asserts that Severity returns the expected
// style for each known severity level. We compare via Render("x") so
// the test detects drift in the underlying lipgloss styling (color
// codes, bold, etc.) without coupling to internal Style fields.
func TestSeverity_KnownLevels(t *testing.T) {
	const probe = "x"

	cases := []struct {
		level string
		want  string
	}{
		{"info", Subtle.Render(probe)},
		{"warn", Warn.Render(probe)},
		{"critical", Danger.Bold(true).Render(probe)},
		{"unknown-bogus-level", Muted.Render(probe)},
		{"", Muted.Render(probe)},
	}

	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			got := Severity(tc.level).Render(probe)
			if got != tc.want {
				t.Fatalf("Severity(%q).Render(%q) = %q, want %q",
					tc.level, probe, got, tc.want)
			}
		})
	}
}

// TestStyles_RenderText asserts that every exported style renders
// non-empty output containing the input text. We don't try to verify
// the exact escape codes — that's brittle without snapshot tooling —
// just that the style applies cleanly and the original text survives.
func TestStyles_RenderText(t *testing.T) {
	const probe = "sentinel-text"

	styles := map[string]struct {
		render func(...string) string
	}{
		"Primary": {Primary.Render},
		"Success": {Success.Render},
		"Warn":    {Warn.Render},
		"Danger":  {Danger.Render},
		"Muted":   {Muted.Render},
		"Subtle":  {Subtle.Render},
		"Panel":   {Panel.Render},
	}

	for name, s := range styles {
		t.Run(name, func(t *testing.T) {
			got := s.render(probe)
			if got == "" {
				t.Fatalf("%s.Render(%q) returned empty string", name, probe)
			}
			if !strings.Contains(got, probe) {
				t.Fatalf("%s.Render(%q) = %q, expected to contain %q",
					name, probe, got, probe)
			}
		})
	}
}
