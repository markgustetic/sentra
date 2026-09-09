package policy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// TestRetentionFromConfig: every surface that plans retention must plan
// with the repo's pins, or a pinned snapshot in the drop set turns into
// ErrSnapshotPinned at DeleteSnapshot and fails the whole run after its
// backup succeeded. One constructor carries the config's four keep
// counts AND the pin set so no caller can build the policy without them.
func TestRetentionFromConfig(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("pw"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "f.txt"), []byte("body"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := r.CreateSnapshot(context.Background(), src, repo.SnapshotOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Pin(context.Background(), snap.ID); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.Retention.KeepLast, cfg.Retention.KeepDaily = 3, 2
	cfg.Retention.KeepWeekly, cfg.Retention.KeepMonthly = 1, 0
	got, err := RetentionFromConfig(context.Background(), r, &cfg)
	if err != nil {
		t.Fatalf("RetentionFromConfig: %v", err)
	}
	if got.KeepLast != 3 || got.KeepDaily != 2 || got.KeepWeekly != 1 || got.KeepMonthly != 0 {
		t.Errorf("keep counts not carried from config: %+v", got)
	}
	if _, pinned := got.Pinned[snap.ID]; !pinned {
		t.Errorf("Pinned missing the repo's pin %s: %v", snap.ID, got.Pinned)
	}

	// A nil config is how the TUI runs before a config is loaded: zero
	// keep counts, but the pins still apply.
	got, err = RetentionFromConfig(context.Background(), r, nil)
	if err != nil {
		t.Fatalf("RetentionFromConfig(nil cfg): %v", err)
	}
	if got.KeepLast != 0 || len(got.Pinned) != 1 {
		t.Errorf("nil cfg: want zero counts with pins, got %+v", got)
	}
}
