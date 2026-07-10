package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

// uiFrameInterval is the chrome's repaint cadence. Slower than the splash's
// reveal (60ms): the steady-state glow only needs to breathe, not animate
// smoothly, and a gentler rate keeps a long-lived TUI from spinning the CPU.
const uiFrameInterval = 120 * time.Millisecond

// uiFrameMsg advances the ambient animation clock. It is self-sustaining: Init
// arms the first tick and each frame re-arms the next, so the chrome breathes
// for the whole session.
type uiFrameMsg struct{}

func uiTick() tea.Cmd {
	return tea.Tick(uiFrameInterval, func(time.Time) tea.Msg { return uiFrameMsg{} })
}

// Breathing ramps — each sweeps a hue from bright to brighter and back, so
// cycling them reads as a pulse rather than a hard blink. Sized so one lap at
// uiFrameInterval lasts roughly a second.
var (
	animBrand  = []string{"#FF5AD6", "#FF6BDD", "#FF86E6", "#FFA0EF", "#FF86E6", "#FF6BDD"}                       // title brand
	animFocus  = []string{"#22DBFF", "#3FE3FF", "#5CEBFF", "#79F0FF", "#93F4FF", "#79F0FF", "#5CEBFF", "#3FE3FF"} // focused border (cyan)
	animIdle   = []string{"#8A5BEA", "#9A68F2", "#AD7CFC", "#9A68F2"}                                             // unfocused border (purple)
	animActive = []string{"#FF6BDD", "#FF86E6", "#E79BFF", "#FF86E6"}                                             // active nav item
)

// animColor samples a ramp at the current frame, wrapping so the pulse loops.
func animColor(ramp []string, frame int) lipgloss.Color {
	n := len(ramp)
	return lipgloss.Color(ramp[((frame%n)+n)%n])
}
