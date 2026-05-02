package ui

import (
	"strings"
	"testing"
)

// TestFormatBytes locks the human-readable formatting we expose for
// progress bars and snapshot summaries. Negative values pass through
// with the sign preserved so callers don't accidentally hide bugs by
// taking abs() at the format layer.
func TestFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1 << 20, "1.0 MiB"},
		{12897484, "12.3 MiB"}, // ~12.3 × 1 MiB
		{1 << 30, "1.0 GiB"},
		{1 << 40, "1.0 TiB"},
		{1 << 50, "1.0 PiB"},
		{-1024, "-1.0 KiB"},
		{-512, "-512 B"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			if got := FormatBytes(tc.in); got != tc.want {
				t.Fatalf("FormatBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestByteProgress_Render verifies that a constructed ByteProgress
// renders a string containing the formatted done/total values and a
// percentage. We don't assert the exact bar shape — bubbles owns
// that — just that our wrapper composes the known-good substrings.
func TestByteProgress_Render(t *testing.T) {
	const total = int64(100 * 1024 * 1024) // 100 MiB
	p := NewByteProgress(total)
	if p == nil {
		t.Fatal("NewByteProgress returned nil")
	}

	// Caller may be inline (non-tea), so the cmd is allowed to be nil.
	_ = p.SetDone(50 * 1024 * 1024)

	rendered := p.Render()
	if rendered == "" {
		t.Fatal("Render() returned empty string")
	}

	for _, want := range []string{"50.0 MiB", "100.0 MiB", "50%"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("Render() = %q, expected to contain %q", rendered, want)
		}
	}
}

// TestByteProgress_ZeroTotal protects against divide-by-zero panics
// when callers create a progress for an unknown-size operation that
// turns out to have zero work to do.
func TestByteProgress_ZeroTotal(t *testing.T) {
	p := NewByteProgress(0)
	if p == nil {
		t.Fatal("NewByteProgress(0) returned nil")
	}
	_ = p.SetDone(0)
	rendered := p.Render()
	if rendered == "" {
		t.Fatal("Render() returned empty string for zero total")
	}
	// 0 of 0 should report 100% complete, not panic and not NaN.
	if strings.Contains(rendered, "NaN") {
		t.Fatalf("Render() leaked NaN: %q", rendered)
	}
}

// TestByteProgress_OvershootClamps exercises a defensive case where
// SetDone is called with a value greater than total. The progress
// percentage should clamp at 100%, not exceed it or wrap around.
func TestByteProgress_OvershootClamps(t *testing.T) {
	p := NewByteProgress(1024)
	_ = p.SetDone(2048)
	rendered := p.Render()
	if !strings.Contains(rendered, "100%") {
		t.Fatalf("Render() = %q, expected to contain 100%% after overshoot", rendered)
	}
}
