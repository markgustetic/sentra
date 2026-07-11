package tui

import (
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

// The shell chrome breathes: a low-rate clock advances animFrame, and the title
// brand, the focused panel border, and the active nav item cycle their neon
// through the ramps below. It is the splash's living-neon idea carried into the
// steady-state UI — but confined to CHROME, never data. A snapshot list or a
// byte count must stay still and legible; only the frame around it glows.
//
// Every animated value is COLOR. Under the Ascii profile (tests, NO_COLOR, a
// pipe) styling is stripped, so the animation simply vanishes and the frame is
// static and deterministic — which is what keeps goldens and geometry tests
// stable and gives motion-sensitive/NO_COLOR users a still interface for free.

// uiFrameInterval is the chrome's repaint cadence. Deliberately slow — much
// slower than the splash's reveal (60ms): the steady-state glow only needs to
// breathe gently, not flash, and a gentle rate keeps a long-lived TUI from
// spinning the CPU. Paired with the narrow ramps below, one lap reads as a slow
// pulse, not a blink.
const uiFrameInterval = 240 * time.Millisecond

// uiFrameMsg advances the ambient animation clock. It is self-sustaining: Init
// arms the first tick and each frame re-arms the next, so the chrome breathes
// for the whole session.
type uiFrameMsg struct{}

func uiTick() tea.Cmd {
	return tea.Tick(uiFrameInterval, func(time.Time) tea.Msg { return uiFrameMsg{} })
}

// Breathing ramps — each oscillates gently around its base color, a small
// luminance swing so cycling reads as a soft shimmer rather than a flash.
// Narrowed deliberately: the wide bright→dim sweeps this replaced read as
// flashing in a long-lived app. Sized so one lap at uiFrameInterval is ~1s.
var (
	animBrand  = []string{"#F462D4", "#FF6BDD", "#FF7EE2", "#FF6BDD"} // title brand (gentle magenta breath)
	animFocus  = []string{"#4FE2FF", "#5CEBFF", "#72EFFF", "#5CEBFF"} // focused border (gentle cyan)
	animIdle   = []string{"#9163EE", "#9A68F2", "#A276F5", "#9A68F2"} // unfocused border (gentle purple)
	animActive = []string{"#F462D4", "#FF6BDD", "#FF7EE2", "#FF6BDD"} // active nav item (gentle magenta)
)

// animColor samples a ramp at the current frame, wrapping so the pulse loops.
func animColor(ramp []string, frame int) lipgloss.Color {
	n := len(ramp)
	return lipgloss.Color(ramp[((frame%n)+n)%n])
}

// deepSpaceSGR is the near-black indigo (#0D0221 → r13 g2 b33) ground the whole
// TUI floats its neon on, to match the preview mockup.
const deepSpaceSGR = "48;2;13;2;33"

// paintBackground fills every cell of a rendered TUI frame with the deep-space
// ground, preserving each run's foreground and weight and leaving any explicit
// background (a panel, a selection, the splash) untouched. It re-emits the
// frame's SGR runs with the ground as the DEFAULT background, so a cell that had
// no background before now sits on deep space.
//
// It lives here — a post-process on the finished frame — rather than in the
// styles, because the content styles are shared with the CLI's printed output; a
// background baked into them would box every printed word. Painting only in the
// App's View keeps CLI output clean. It is also a no-op unless the terminal is
// truecolor, so tests (Ascii profile), NO_COLOR, and pipes see the frame exactly
// as before, and 256-color terminals fall back to the plain neon.
//
// Only zero-width SGR escapes are rewritten; every visible byte is preserved, so
// line widths (and the overflow guarantees) are unchanged.
func paintBackground(frame string) string {
	if lipgloss.ColorProfile() != termenv.TrueColor {
		return frame
	}
	var out strings.Builder
	out.Grow(len(frame) + len(frame)/4)
	fg, bg := "", ""
	bold := false
	writeRun := func(text string) {
		if text == "" {
			return
		}
		params := make([]string, 0, 3)
		if bold {
			params = append(params, "1")
		}
		if fg != "" {
			params = append(params, "38;2;"+fg)
		}
		if bg != "" {
			params = append(params, "48;2;"+bg)
		} else {
			params = append(params, deepSpaceSGR)
		}
		out.WriteString("\x1b[")
		out.WriteString(strings.Join(params, ";"))
		out.WriteByte('m')
		out.WriteString(text)
		out.WriteString("\x1b[0m")
	}
	i := 0
	for i < len(frame) {
		switch c := frame[i]; {
		case c == '\n':
			// A terminal line starts fresh (lipgloss resets before each newline).
			fg, bg, bold = "", "", false
			out.WriteByte('\n')
			i++
		case c == 0x1b && i+1 < len(frame) && frame[i+1] == '[':
			j := i + 2
			for j < len(frame) && frame[j] != 'm' {
				j++
			}
			parts := strings.Split(frame[i+2:j], ";")
			for k := 0; k < len(parts); k++ {
				switch parts[k] {
				case "0", "":
					fg, bg, bold = "", "", false
				case "1":
					bold = true
				case "22":
					bold = false
				case "39":
					fg = ""
				case "49":
					bg = ""
				case "38":
					if k+4 < len(parts) && parts[k+1] == "2" {
						fg = parts[k+2] + ";" + parts[k+3] + ";" + parts[k+4]
						k += 4
					}
				case "48":
					if k+4 < len(parts) && parts[k+1] == "2" {
						bg = parts[k+2] + ";" + parts[k+3] + ";" + parts[k+4]
						k += 4
					}
				}
			}
			i = j + 1
		default:
			start := i
			for i < len(frame) && frame[i] != 0x1b && frame[i] != '\n' {
				i++
			}
			writeRun(frame[start:i])
		}
	}
	return out.String()
}
