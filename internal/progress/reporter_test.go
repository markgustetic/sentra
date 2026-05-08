package progress

import (
	"sync"
	"testing"
)

func TestNopReporter_DiscardsCalls(t *testing.T) {
	var r Reporter = NopReporter{}
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

// TestRecordingReporter_AsReporter is a compile-time assertion that
// *RecordingReporter implements Reporter. Test code in other packages
// type-asserts via the interface, so making this explicit means any
// future change to the Reporter interface fails the test rather than
// silently breaking downstream callers.
func TestRecordingReporter_AsReporter(t *testing.T) {
	var _ Reporter = (*RecordingReporter)(nil)
	var _ Reporter = NopReporter{}
}
