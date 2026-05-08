package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

// MACSize is the byte length of an HMAC-SHA256 tag — exactly 32
// bytes. Exposed as a constant so callers building wire formats
// can size buffers without importing crypto/sha256 themselves.
const MACSize = 32

// SubKeyLen is the byte length of a sub-key produced by DeriveSubKey.
// 32 bytes (256 bits) is the right size for HMAC-SHA256 and gives
// a comfortable security margin for any other symmetric primitive
// the caller might pair the sub-key with.
const SubKeyLen = 32

// DeriveSubKey produces a domain-separated 32-byte sub-key from
// masterKey using HKDF-Expand with SHA-256. The masterKey is
// expected to already be high-entropy (e.g., the output of
// DeriveKEK / Argon2id); the salt is empty per the HKDF "the
// master key is already pseudorandom" recipe.
//
// info is the domain separator: a short, fixed string identifying
// the sub-key's purpose. Different purposes MUST use different
// info strings so a sub-key compromise in one context can't
// affect the others. Convention in this codebase: lowercase
// hyphenated, ending in "/vN" so a future protocol change can
// rotate without invalidating existing on-disk artifacts.
//
// Examples:
//   - "sentra/config-mac/v1" — config blob authentication
//   - "sentra/manifest-sig/v1" — future per-snapshot signing
func DeriveSubKey(masterKey []byte, info string) ([]byte, error) {
	h := hkdf.New(sha256.New, masterKey, nil /*salt*/, []byte(info))
	out := make([]byte, SubKeyLen)
	if _, err := io.ReadFull(h, out); err != nil {
		return nil, fmt.Errorf("crypto: hkdf expand %q: %w", info, err)
	}
	return out, nil
}

// HMACSHA256 returns the HMAC-SHA256 tag over data, keyed with key.
// The output is exactly MACSize bytes. Pass the result of
// DeriveSubKey as key — using KEK directly would conflate the
// authentication and encryption purposes of the same secret,
// which is a well-known footgun.
func HMACSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

// VerifyHMACSHA256 returns true iff candidate equals the HMAC-SHA256
// of data under key. Comparison is constant-time so the caller
// doesn't have to remember to reach for hmac.Equal at the call
// site.
func VerifyHMACSHA256(key, data, candidate []byte) bool {
	expected := HMACSHA256(key, data)
	return hmac.Equal(expected, candidate)
}
