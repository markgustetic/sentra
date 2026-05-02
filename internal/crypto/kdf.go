// Package crypto provides the symmetric primitives used by sentra:
// Argon2id passphrase-to-KEK derivation and AES-256-GCM blob sealing.
//
// The plaintext repo key (32 bytes, AES-256) is wrapped on disk with a
// KEK derived from the user's passphrase. Every blob is sealed with the
// repo key under AES-256-GCM with a fresh 24-byte random nonce; the
// first 12 bytes are passed to GCM and the remaining 12 are reserved as
// extra entropy for a future XChaCha20 swap.
package crypto

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
)

// KDFParams configures Argon2id key derivation.
//
// Defaults come from DefaultKDFParams; the parameters are stored in the
// encrypted repo config so future bumps do not break existing repos.
type KDFParams struct {
	// Time is the number of passes Argon2id makes over memory.
	// Higher values increase the cost of brute-force attacks linearly.
	Time uint32
	// Memory is the working-set size in KiB. 64 MiB is the design
	// default. Watch the unit: setting this to "64*1024*1024" instead
	// of "64*1024" requests 64 GiB and will OOM the process.
	Memory uint32
	// Threads is the parallelism factor (lanes). Increasing this
	// reduces wall-clock time on the legitimate side without
	// proportionally raising attacker cost.
	Threads uint8
	// KeyLen is the output length in bytes. 32 (AES-256) for sentra.
	KeyLen uint32
}

// DefaultKDFParams returns the parameters used at sentra init time:
// Argon2id, time=3, memory=64 MiB, parallelism=4, 32-byte output.
func DefaultKDFParams() KDFParams {
	return KDFParams{Time: 3, Memory: 64 * 1024, Threads: 4, KeyLen: 32}
}

// DeriveKEK runs Argon2id over (passphrase, salt) using p and returns
// the resulting key. The output length is p.KeyLen.
func DeriveKEK(passphrase, salt []byte, p KDFParams) []byte {
	return argon2.IDKey(passphrase, salt, p.Time, p.Memory, p.Threads, p.KeyLen)
}

// Validate checks that the parameters are within sane bounds. Loaded
// configs are run through this to prevent a corrupted on-disk config
// from triggering pathological allocations.
func (p KDFParams) Validate() error {
	if p.Time == 0 {
		return errors.New("crypto: KDFParams.Time must be > 0")
	}
	if p.Threads == 0 {
		return errors.New("crypto: KDFParams.Threads must be > 0")
	}
	if p.KeyLen != 32 {
		return fmt.Errorf("crypto: KDFParams.KeyLen must be 32, got %d", p.KeyLen)
	}
	if p.Memory == 0 || p.Memory > 1<<24 { // ceiling 16 GiB in KiB
		return fmt.Errorf("crypto: KDFParams.Memory out of range: %d KiB", p.Memory)
	}
	return nil
}
