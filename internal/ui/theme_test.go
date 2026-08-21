package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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

// TestTheme_ShellStylesRenderContent: the new shell styles must wrap
// content without losing it. Colors are profile-dependent (stripped in
// tests); we assert content survives and structural styles differ.
func TestTheme_ShellStylesRenderContent(t *testing.T) {
	for name, st := range map[string]lipgloss.Style{
		"TitleBar":      TitleBar,
		"SidebarItem":   SidebarItem,
		"SidebarActive": SidebarActive,
		"StatusBar":     StatusBar,
		"ModalBox":      ModalBox,
		"PanelFocused":  PanelFocused,
	} {
		if got := st.Render("content"); !strings.Contains(got, "content") {
			t.Errorf("%s.Render dropped content: %q", name, got)
		}
	}
}

// TestTheme_ActiveAndInactiveSidebarDiffer: active items carry the
// "▍" accent marker so selection is visible even with colors stripped.
func TestTheme_ActiveAndInactiveSidebarDiffer(t *testing.T) {
	active := SidebarActive.Render("Dashboard")
	inactive := SidebarItem.Render("Dashboard")
	if active == inactive {
		t.Fatal("active and inactive sidebar styles render identically")
	}
}

// TestTheme_AdaptiveColorsDeclared: semantic styles must use adaptive
// colors so light terminals stay readable (spec requirement).
func TestTheme_AdaptiveColorsDeclared(t *testing.T) {
	// AccentPink etc. are the palette constants the styles derive from.
	for name, c := range map[string]lipgloss.AdaptiveColor{
		"AccentViolet": AccentViolet,
		"AccentPink":   AccentPink,
		"AccentAqua":   AccentAqua,
	} {
		if c.Dark == "" || c.Light == "" {
			t.Errorf("%s missing a variant: %+v", name, c)
		}
	}
}

// TestSelectRowMarkerAndAlignment pins the selection contract: a selected row
// carries the "▍" marker so the selection survives when colors are stripped
// (NO_COLOR, a pipe, a 2-color terminal), and both states occupy the same
// number of cells so the label column never shifts as the cursor moves.
func TestSelectRowMarkerAndAlignment(t *testing.T) {
	selected := SelectRow(true, "S3 bucket")
	plain := SelectRow(false, "S3 bucket")

	if !strings.Contains(selected, "▍") {
		t.Errorf("selected row lost its marker: %q", selected)
	}
	if strings.Contains(plain, "▍") {
		t.Errorf("unselected row must not carry the marker: %q", plain)
	}
	if got, want := lipgloss.Width(selected), lipgloss.Width(plain); got != want {
		t.Errorf("row width shifts with selection: selected=%d unselected=%d", got, want)
	}
}

// TestSelectRowDoesNotNestStyles guards the bug this replaced: callers used to
// wrap an already-styled string, whose inner ANSI reset terminated the outer
// style mid-line. SelectRow takes a plain label; help text is appended by the
// caller outside the styled span.
func TestSelectRowDoesNotNestStyles(t *testing.T) {
	if got := SelectRow(true, "label"); !strings.HasSuffix(stripANSI(got), "label") {
		t.Errorf("SelectRow should end with the bare label, got %q", stripANSI(got))
	}
}

// TestFieldBox_RendersRoundedFrame asserts that FieldBox applies a rounded
// border frame to its content.
func TestFieldBox_RendersRoundedFrame(t *testing.T) {
	out := FieldBox.Render("hello")
	for _, want := range []string{"╭", "╰", "hello"} {
		if !strings.Contains(out, want) {
			t.Errorf("FieldBox output missing %q:\n%s", want, out)
		}
	}
}

func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		switch {
		case r == 0x1b:
			inEsc = true
		case inEsc && r == 'm':
			inEsc = false
		case !inEsc:
			b.WriteRune(r)
		}
	}
	return b.String()
}
