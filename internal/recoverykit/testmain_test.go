package recoverykit

import (
	"os"
	"testing"

	"github.com/markgustetic/sentra/internal/repo"
)

// TestMain switches repo.Init to cheap Argon2id parameters for the whole
// package: every repo these tests create pays the derivation at least
// twice, and under the race detector each costs about a second. See
// repo.UseFastKDFForTests for why that is safe.
func TestMain(m *testing.M) {
	repo.UseFastKDFForTests()
	os.Exit(m.Run())
}
