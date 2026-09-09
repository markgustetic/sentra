package repo

import (
	"os"
	"testing"
)

// TestMain switches Init to the cheap Argon2id parameters for the whole
// package: see UseFastKDFForTests for why that is safe and what it buys.
func TestMain(m *testing.M) {
	UseFastKDFForTests()
	os.Exit(m.Run())
}
