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
	// blobs.
	//
	//   v3: XChaCha20-Poly1305, 24-byte nonce, version byte bound
	//       into the AEAD's associated data. Sealed under
	//       []byte{0x03}, so flipping the on-disk version byte to
	//       any other value invalidates the tag.
	//
	// Open still accepts older versions for backward compatibility:
	//   v2: XChaCha20-Poly1305, 24-byte nonce, AD=nil.
	//   v1: AES-GCM, 12-byte nonce within a 24-byte nonce slot,
	//       AD=nil.
	//
	// All three share an identical wire layout (1 version + 24
	// nonce bytes + ciphertext + tag); the only difference is what
	// the AEAD authenticates.
	BlobVersion byte = 0x03

	// blobVersionV2 / blobVersionV1 are the legacy version bytes
	// the open dispatch recognizes for backward compatibility.
	// New blobs are NEVER written under these versions — Seal only
	// emits BlobVersion.
	blobVersionV2 byte = 0x02
	blobVersionV1 byte = 0x01

	// legacyAESGCMNonceSize is the AES-GCM nonce length in the
	// legacy v1 blob format. The on-disk header reserves the full
	// 24-byte XChaCha20 nonce slot for forward-compatibility, but
	// AES-GCM only consumes the first 12 bytes of that slot. Bytes
	// 12..24 of the stored nonce are unused.
	legacyAESGCMNonceSize = 12

	// KeyLen is the required byte length of the symmetric key
	// passed to Seal and Open. XChaCha20-Poly1305 requires 32 bytes.
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

// Seal encrypts plaintext with key under XChaCha20-Poly1305 and
// returns the v3 versioned blob layout used by sentra:
//
//	[1 byte version (0x03)][24 byte random nonce][ciphertext + 16 byte tag]
//
// The version byte is included as AEAD associated data, so any
// tamper of the on-disk version byte invalidates the tag — closing
// the future-downgrade path that v2 left open by sealing under
// AD=nil.
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
	// AD is the version byte: an attacker who flips the on-disk
	// version byte to 0x02 routes the open to a v2 decoder that
	// expects AD=nil; the tag was computed under AD=[0x03] so the
	// decrypt fails. Flipping to 0x01 routes to AES-GCM which
	// fails its own auth check too. So no version-byte flip can
	// be silently accepted on a v3 blob.
	out = aead.Seal(out, nonce, plaintext, []byte{BlobVersion})
	return out, nil
}

// Open decrypts a blob produced by Seal. It validates the version
// byte, extracts the nonce, and verifies the AEAD tag (with version-
// as-AD on v3, no AD on v2/v1). Any tampering with the version,
// nonce, ciphertext, or tag produces a non-nil error.
func Open(key, sealed []byte) ([]byte, error) {
	if len(key) != KeyLen {
		return nil, ErrInvalidKey
	}
	if len(sealed) < headerSize {
		return nil, ErrSealedTooShort
	}
	switch sealed[0] {
	case BlobVersion:
		// v3: AD = [version byte]. The version is part of what
		// the tag covers, so a flipped version invalidates the
		// blob.
		return openXChaCha20Poly1305(key, sealed, []byte{BlobVersion})
	case blobVersionV2:
		// v2: legacy XChaCha20-Poly1305 with AD=nil. Kept for
		// repos written by pre-v3 builds.
		return openXChaCha20Poly1305(key, sealed, nil)
	case blobVersionV1:
		return openLegacyAESGCM(key, sealed)
	default:
		return nil, fmt.Errorf("crypto: unknown blob version 0x%02x", sealed[0])
	}
}

// openXChaCha20Poly1305 is the shared decrypt path for v2 and v3
// XChaCha20-Poly1305 blobs. The associated-data argument is what
// distinguishes them: v3 passes []byte{BlobVersion}, v2 passes nil.
func openXChaCha20Poly1305(key, sealed, ad []byte) ([]byte, error) {
	nonce := sealed[1:headerSize]
	ciphertext := sealed[headerSize:]

	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new xchacha20poly1305: %w", err)
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, ad)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}

func openLegacyAESGCM(key, sealed []byte) ([]byte, error) {
	// AES-GCM only consumes the first 12 bytes of the 24-byte nonce
	// slot. The trailing 12 bytes of the slot are an artifact of the
	// shared on-disk header layout; they are not part of the nonce and
	// must not be passed to gcm.Open.
	nonce := sealed[1 : 1+legacyAESGCMNonceSize]
	ciphertext := sealed[headerSize:]

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("crypto: new cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("crypto: new gcm: %w", err)
	}
	if gcm.NonceSize() != legacyAESGCMNonceSize {
		// Defensive: the stdlib NonceSize for AES-GCM is hard-wired to
		// 12 bytes, but if a future Go release ever changed this we'd
		// silently misread legacy blobs. Surface the drift instead.
		return nil, fmt.Errorf("crypto: legacy AES-GCM nonce size mismatch: stdlib=%d, expected=%d",
			gcm.NonceSize(), legacyAESGCMNonceSize)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, fmt.Errorf("crypto: open: %w", err)
	}
	return plaintext, nil
}
