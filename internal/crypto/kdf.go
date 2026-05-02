// Package crypto provides the symmetric primitives used by sentra:
// Argon2id passphrase-to-KEK derivation and AES-256-GCM blob sealing.
//
// The plaintext repo key (32 bytes, AES-256) is wrapped on disk with a
// KEK derived from the user's passphrase. Every blob is sealed with the
// repo key under AES-256-GCM with a fresh 24-byte random nonce; the
// first 12 bytes are passed to GCM and the remaining 12 are reserved as
// extra entropy for a future XChaCha20 swap.
package crypto

import "golang.org/x/crypto/argon2"

// KDFParams configures Argon2id key derivation.
//
// Memory is in KiB. Threads is the parallelism factor. KeyLen is the
// number of output bytes. Defaults come from DefaultKDFParams; the
// parameters are stored in the encrypted repo config so future bumps do
// not break existing repos.
type KDFParams struct {
	Time    uint32
	Memory  uint32 // KiB
	Threads uint8
	KeyLen  uint32
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
