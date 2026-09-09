package repo

import (
	"testing"

	"github.com/markgustetic/sentra/internal/crypto"
)

// initKDF returns the Argon2id parameters Init records into a new repo's
// config. Production code never reassigns it; only UseFastKDFForTests
// does, and only inside a test binary.
//
// It is the single seam because it is the only place a parameter choice
// is MADE: Open, Passwd, and every later derivation read the params back
// from meta/config, so a repo initialised cheaply stays cheap for the
// rest of its life without any other code knowing.
var initKDF = crypto.DefaultKDFParams

// fastKDFParams is the cheapest Argon2id configuration Validate accepts:
// one pass over the 4 MiB floor on a single lane. The default (three
// passes over 64 MiB) is a brute-force cost paid once per unlock; under
// the race detector it costs about a second per derivation, and a
// suite that inits a repo per test pays it hundreds of times. Nothing a
// test asserts depends on the derivation being expensive — the tamper
// tests distinguish params by VALUE, not by cost.
var fastKDFParams = crypto.KDFParams{Time: 1, Memory: crypto.MinMemoryKiB, Threads: 1, KeyLen: 32}

// UseFastKDFForTests makes Init record fastKDFParams instead of the
// production default for the rest of the process. It panics outside a
// `go test` binary so a stray call can never weaken a real repository:
// the params land in meta/config, so a production Init with them would
// permanently cap the attacker's cost at the Validate floor.
//
// Call it once from a package's TestMain; every test package that
// creates repos does. It is exported because internal/cli, internal/tui
// and friends init repos through this package and pay the same cost.
func UseFastKDFForTests() {
	if !testing.Testing() {
		panic("repo.UseFastKDFForTests called outside a test binary")
	}
	initKDF = func() crypto.KDFParams { return fastKDFParams }
}
