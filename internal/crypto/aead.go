package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
)

const (
	// BlobVersion is the wire format version of a sealed blob. Bumped
	// whenever the on-disk layout changes; Open refuses unknown values.
	BlobVersion byte = 0x01

	// KeyLen is the required byte length of the symmetric key passed to
	// Seal and Open. AES-256 requires 32 bytes.
	KeyLen = 32

	// NonceSize is the number of random nonce bytes carried in each
	// sealed blob. AES-GCM consumes only the first 12 bytes; the
	// remaining 12 are reserved for a future XChaCha20 swap (which
	// uses 24-byte nonces) and cost one extra dozen bytes per blob.
	NonceSize = 24

	// HeaderSize is the fixed-size prefix on every sealed blob:
	// one version byte + the full 24-byte nonce.
	HeaderSize = 1 + NonceSize
)

// ErrInvalidKey is returned when the caller passes a key of the wrong
// length to Seal or Open.
var ErrInvalidKey = errors.New("crypto: key must be 32 bytes")

// ErrSealedTooShort is returned when Open is given input shorter than a
// valid header.
var ErrSealedTooShort = errors.New("crypto: sealed blob too short")

// Seal encrypts plaintext with key under AES-256-GCM and returns the
// versioned blob layout used by sentra:
//
//	[1 byte version][24 byte random nonce][AES-GCM ciphertext + 16 byte tag]
//
// The nonce is generated with crypto/rand. AES-GCM consumes only the
// first 12 bytes of the stored nonce; see the package doc comment for
// why we carry 24.
func Seal(key, plaintext []byte) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, ErrInvalidKey
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}

	nonce := make([]byte, NonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: read random nonce: %w", err)
	}

	out := make([]byte, 0, HeaderSize+len(plaintext)+gcm.Overhead())
	out = append(out, BlobVersion)
	out = append(out, nonce...)
	// Append GCM output (ciphertext || tag) directly onto out so we
	// avoid a second allocation.
	out = gcm.Seal(out, nonce[:gcm.NonceSize()], plaintext, nil)
	return out, nil
}

// Open decrypts a blob produced by Seal. It validates the version byte,
// extracts the nonce, and verifies the AES-GCM tag. Any tampering with
// the version, nonce, ciphertext, or tag produces a non-nil error.
func Open(key, sealed []byte) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, ErrInvalidKey
	}
	if len(sealed) < HeaderSize {
		return nil, ErrSealedTooShort
	}
	if sealed[0] != BlobVersion {
		return nil, fmt.Errorf("crypto: unknown blob version 0x%02x", sealed[0])
	}
	nonce := sealed[1:HeaderSize]
	ciphertext := sealed[HeaderSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce[:gcm.NonceSize()], ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}
