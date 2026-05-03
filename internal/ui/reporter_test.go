package ui

import (
	"sync"
	"testing"
)

func TestNopReporter_DiscardsCalls(t *testing.T) {
	var r ProgressReporter = NopReporter{}
	// Just confirm the calls don't panic; there's nothing observable.
	r.Total(1024)
	r.Add(256)
}

func TestRecordingReporter_TracksEvents(t *testing.T) {
	r := &RecordingReporter{}
	r.Total(1000)
	r.Add(100)
	r.Add(250)
	total, done, events := r.Snapshot()
	if total != 1000 {
		t.Errorf("total: got %d, want 1000", total)
	}
	if done != 350 {
		t.Errorf("done: got %d, want 350", done)
	}
	if events != 3 {
		t.Errorf("events: got %d, want 3", events)
	}
}

func TestRecordingReporter_ConcurrentSafe(t *testing.T) {
	r := &RecordingReporter{}
	r.Total(0)

	const workers = 8
	const perWorker = 100
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				r.Add(1)
			}
		}()
	}
	wg.Wait()

	_, done, _ := r.Snapshot()
	if done != workers*perWorker {
		t.Errorf("done: got %d, want %d", done, workers*perWorker)
	}
}

func TestByteProgress_SatisfiesProgressReporter(t *testing.T) {
	var _ ProgressReporter = (*ByteProgress)(nil)

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
