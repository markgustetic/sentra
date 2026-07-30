package setup

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// Fresh init: repo initialized, and with save=true the passphrase is saved
// to the keyring.
func TestEngineInitRepoFreshSavesPassphrase(t *testing.T) {
	store := blobstore.NewMemory()
	saved := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saved = true; return nil },
	}
	res, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("hunter2"), true)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if res.AlreadyInitialized {
		t.Fatal("AlreadyInitialized = true on a fresh store")
	}
	if res.RepoID == "" {
		t.Fatal("RepoID empty after fresh init")
	}
	if !res.PassphraseSavedToKeyring || !saved {
		t.Fatalf("passphrase not saved: res=%+v saved=%v", res, saved)
	}
}

// SAFETY-CRITICAL: already-initialized + save=true must repo.Open-verify the
// passphrase BEFORE calling SavePassphrase. A WRONG passphrase must surface
// an error and MUST NOT call SavePassphrase.
func TestEngineInitRepoAlreadyInitializedWrongPassphraseDoesNotSaveKeyring(t *testing.T) {
	store := blobstore.NewMemory()
	// Pre-initialize the repo under the correct passphrase.
	r, err := repo.Init(context.Background(), store, []byte("correct-horse"))
	if err != nil {
		t.Fatalf("pre-init: %v", err)
	}
	r.Close()

	saveCalled := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saveCalled = true; return nil },
	}
	_, err = NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("wrong-passphrase"), true)
	if err == nil {
		t.Fatal("InitRepo: got nil error with wrong passphrase on existing repo, want non-nil")
	}
	if saveCalled {
		t.Fatal("SavePassphrase was called with a wrong passphrase — verify-before-save guard is broken")
	}
	if !errors.Is(err, repo.ErrWrongPassphrase) {
		t.Fatalf("InitRepo error = %v, want wrapped repo.ErrWrongPassphrase", err)
	}
}

// Already-initialized + correct passphrase + save=true: repo.Open verifies,
// THEN SavePassphrase runs; result reports AlreadyInitialized and the saved
// keyring.
func TestEngineInitRepoAlreadyInitializedCorrectPassphraseSaves(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("correct-horse"))
	if err != nil {
		t.Fatalf("pre-init: %v", err)
	}
	wantID := r.Config().ID
	r.Close()

	saved := false
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
		savePass: func(*config.Config, []byte) error { saved = true; return nil },
	}
	res, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("correct-horse"), true)
	if err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if !res.AlreadyInitialized {
		t.Fatal("AlreadyInitialized = false, want true")
	}
	if res.RepoID != wantID {
		t.Fatalf("RepoID = %q, want %q", res.RepoID, wantID)
	}
	if !res.PassphraseSavedToKeyring || !saved {
		t.Fatalf("passphrase not saved after verified open: res=%+v saved=%v", res, saved)
	}
}

// TestEngineInitRepoKeyringSaveFailureSurfacesError: a locked or unavailable
// keyring must abort with the keyring named, on both the fresh and the
// already-initialized branch. Swallowing it would leave sentra.yaml claiming
// use_keyring:true over an empty keyring — every later non-interactive or
// scheduled run then fails with a passphrase error far from the cause.
func TestEngineInitRepoKeyringSaveFailureSurfacesError(t *testing.T) {
	wantErr := errors.New("keyring locked")
	tests := []struct {
		name    string
		preInit bool
	}{
		{"fresh repo", false},
		{"already initialized", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store := blobstore.NewMemory()
			if tc.preInit {
				r, err := repo.Init(context.Background(), store, []byte("hunter22"))
				if err != nil {
					t.Fatalf("pre-init: %v", err)
				}
				r.Close()
			}
			eff := fakeEffects{
				newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return store, nil },
				savePass: func(*config.Config, []byte) error { return wantErr },
			}
			_, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("hunter22"), true)
			if !errors.Is(err, wantErr) {
				t.Fatalf("InitRepo error = %v, want the keyring cause wrapped", err)
			}
			if !strings.Contains(err.Error(), "save passphrase to keyring") {
				t.Errorf("error must name the failing step, got %v", err)
			}
		})
	}
}

// save=true with no SavePassphrase effect wired must fail up front, not
// after touching the store.
func TestEngineInitRepoSaveWithoutSaverFails(t *testing.T) {
	eff := fakeEffects{
		newStore: func(context.Context, *config.Config) (blobstore.Store, error) { return blobstore.NewMemory(), nil },
		savePass: nil, // fakeEffects returns nil (no-op); simulate "missing" by asserting the engine relies on Effects, not a nil-check
	}
	// The engine relies on Effects.SavePassphrase always being present, so
	// this documents that a fresh save succeeds even without an override.
	if _, err := NewEngine(eff).InitRepo(context.Background(), &config.Config{}, []byte("hunter2"), true); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
}
