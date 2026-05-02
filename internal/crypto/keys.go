package crypto

import (
	"crypto/rand"
	"fmt"
)

const (
	// SaltLen is the length in bytes of the KDF salt stored in the
	// encrypted repo config. 16 bytes (128 bits) is the design default
	// and matches Argon2id common practice.
	SaltLen = 16

	// RepoKeyLen is the length in bytes of the repo key. AES-256
	// requires 32 bytes.
	RepoKeyLen = KeyLen
)

// GenerateRepoKey returns a fresh random 32-byte AES-256 repo key from
// crypto/rand. The repo key encrypts every blob, manifest, and index;
// it is itself stored encrypted (wrapped) by a passphrase-derived KEK.
func GenerateRepoKey() ([]byte, error) {
	k := make([]byte, RepoKeyLen)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("crypto: generate repo key: %w", err)
	}
	return k, nil
}

// GenerateSalt returns a fresh random 16-byte salt suitable for
// Argon2id passphrase-to-KEK derivation. The salt is stored alongside
// the wrapped repo key in the encrypted repo config.
func GenerateSalt() ([]byte, error) {
	s := make([]byte, SaltLen)
	if _, err := rand.Read(s); err != nil {
		return nil, fmt.Errorf("crypto: generate salt: %w", err)
	}
	return s, nil
}
