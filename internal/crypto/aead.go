package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	// BlobVersion is the wire format version Seal writes for new
	// blobs. Version 2 is XChaCha20-Poly1305 with a full 24-byte
	// nonce; Open still accepts legacy version 1 AES-GCM blobs for
	// existing repositories.
	BlobVersion byte = 0x02

	legacyAESGCMBlobVersion byte = 0x01

	// KeyLen is the required byte length of the symmetric key passed to
	// Seal and Open. XChaCha20-Poly1305 requires 32 bytes.
	KeyLen = 32

	// nonceSize is the number of random nonce bytes carried in each
	// sealed blob. XChaCha20-Poly1305 consumes all 24 bytes.
	nonceSize = chacha20poly1305.NonceSizeX

	// headerSize is the fixed-size prefix on every sealed blob:
	// one version byte + the full 24-byte XChaCha20 nonce.
	headerSize = 1 + nonceSize
)

// ErrInvalidKey is returned when the caller passes a key of the wrong
// length to Seal or Open.
var ErrInvalidKey = errors.New("crypto: key must be 32 bytes")

// ErrSealedTooShort is returned when Open is given input shorter than a
// valid header.
var ErrSealedTooShort = errors.New("crypto: sealed blob too short")

// Seal encrypts plaintext with key under XChaCha20-Poly1305 and returns the
// versioned blob layout used by sentra:
//
//	[1 byte version][24 byte random nonce][ciphertext + 16 byte tag]
//
// The nonce is generated with crypto/rand and every nonce byte is
// consumed by the AEAD.
func Seal(key, plaintext []byte) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, ErrInvalidKey
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new xchacha20poly1305: %w", err)
	}

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("crypto: read random nonce: %w", err)
	}

	out := make([]byte, 0, headerSize+len(plaintext)+aead.Overhead())
	out = append(out, BlobVersion)
	out = append(out, nonce...)
	// Append AEAD output (ciphertext || tag) directly onto out so we
	// avoid a second allocation.
	out = aead.Seal(out, nonce, plaintext, nil)
	return out, nil
}

// Open decrypts a blob produced by Seal. It validates the version byte,
// extracts the nonce, and verifies the AEAD tag. Any tampering with
// the version, nonce, ciphertext, or tag produces a non-nil error.
func Open(key, sealed []byte) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, ErrInvalidKey
	}
	if len(sealed) < headerSize {
		return nil, ErrSealedTooShort
	}
	switch sealed[0] {
	case BlobVersion:
		return openXChaCha20Poly1305(key, sealed)
	case legacyAESGCMBlobVersion:
		return openLegacyAESGCM(key, sealed)
	default:
		return nil, fmt.Errorf("crypto: unknown blob version 0x%02x", sealed[0])
	}
}

func openXChaCha20Poly1305(key, sealed []byte) ([]byte, error) {
	nonce := sealed[1:headerSize]
	ciphertext := sealed[headerSize:]

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new xchacha20poly1305: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}

func openLegacyAESGCM(key, sealed []byte) ([]byte, error) {
	nonce := sealed[1:headerSize]
	ciphertext := sealed[headerSize:]

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
