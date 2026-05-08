package ui

import (
	"testing"

	"github.com/markgustetic/sentra/internal/progress"
)

func TestByteProgress_SatisfiesProgressReporter(t *testing.T) {
	var _ progress.Reporter = (*ByteProgress)(nil)

	p := NewByteProgress(1024)
	p.Total(2048) // override
	p.Add(512)

	if p.total != 2048 {
		t.Errorf("Total didn't update: got %d", p.total)
	}
	if p.done != 512 {
		t.Errorf("Add didn't update done: got %d", p.done)
	}
}
