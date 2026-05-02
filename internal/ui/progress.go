package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/progress"
	tea "github.com/charmbracelet/bubbletea"
)

// ByteProgress wraps bubbles/progress with byte-formatted output. The
// underlying bubbles model owns the bar animation; this wrapper just
// translates byte counts to a percentage and composes a final render
// line that includes the human-readable done/total counts.
//
// ByteProgress is intended for two consumption modes:
//
//  1. Inline (non-tea): a CLI command repeatedly calls SetDone +
//     Render and prints the result, ignoring the returned tea.Cmd.
//  2. Tea program: a Bubble Tea model embeds *ByteProgress and
//     dispatches the returned tea.Cmd through its Update loop so
//     the bar animates smoothly between updates.
type ByteProgress struct {
	progress progress.Model
	total    int64
	done     int64
}

// NewByteProgress creates a progress component sized in bytes. A
// total of zero is allowed and is treated as "already complete";
// Render will report 100%.
func NewByteProgress(total int64) *ByteProgress {
	m := progress.New(progress.WithDefaultGradient())
	return &ByteProgress{
		progress: m,
		total:    total,
	}
}

// SetDone updates the done-bytes count. Returns a tea.Cmd that drives
// the bar's animation; inline (non-tea) callers may discard it.
//
// done is clamped to [0, total]. If total is zero, the bar is set to
// 100% regardless of done.
func (p *ByteProgress) SetDone(done int64) tea.Cmd {
	p.done = done
	return p.progress.SetPercent(p.percent())
}

// Render returns the formatted progress string. Layout:
//
//	<bar> 12.3 MiB / 50.4 MiB (24%)
//
// The percentage in the trailing parens is integer-rounded; the bar
// itself includes its own continuous percentage from bubbles. We use
// integer percent here so smoke tests have a stable substring.
func (p *ByteProgress) Render() string {
	pct := p.percent()
	bar := p.progress.ViewAs(pct)
	return fmt.Sprintf("%s %s / %s (%d%%)",
		bar,
		FormatBytes(p.done),
		FormatBytes(p.total),
		int(pct*100+0.5),
	)
}

// percent returns the fraction of work done in [0, 1]. Zero total is
// treated as complete (1.0) so the rendered output reads sensibly
// instead of NaN'ing through fmt.
func (p *ByteProgress) percent() float64 {
	if p.total <= 0 {
		return 1.0
	}
	if p.done >= p.total {
		return 1.0
	}
	if p.done <= 0 {
		return 0.0
	}
	return float64(p.done) / float64(p.total)
}

// FormatBytes returns a human-readable string for n bytes, using
// binary (IEC) units up to PiB. Values below 1 KiB are rendered as
// raw bytes; larger values use one decimal place.
//
// Negative values pass through with the sign preserved so callers
// (e.g. snapshot diffs showing freed space) keep accurate readouts.
func FormatBytes(n int64) string {
	const (
		kib = 1 << 10
		mib = 1 << 20
		gib = 1 << 30
		tib = 1 << 40
		pib = 1 << 50
	)

	abs := n
	sign := ""
	if n < 0 {
		abs = -n
		sign = "-"
	}

	switch {
	case abs < kib:
		// Under 1 KiB → bare-byte rendering. The sign is included via the
		// %d format verb on the original (signed) value to avoid manual
		// concatenation that would mishandle the int64 minimum.
		return fmt.Sprintf("%d B", n)
	case abs < mib:
		return fmt.Sprintf("%s%.1f KiB", sign, float64(abs)/kib)
	case abs < gib:
		return fmt.Sprintf("%s%.1f MiB", sign, float64(abs)/mib)
	case abs < tib:
		return fmt.Sprintf("%s%.1f GiB", sign, float64(abs)/gib)
	case abs < pib:
		return fmt.Sprintf("%s%.1f TiB", sign, float64(abs)/tib)
	default:
		return fmt.Sprintf("%s%.1f PiB", sign, float64(abs)/pib)
	}
}
