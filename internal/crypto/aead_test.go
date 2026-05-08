package crypto

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
)

// fixedKey returns a deterministic 32-byte key for tests. Using a real
// repo key would require GenerateRepoKey, which lives in keys.go and is
// implemented in a later task; the AEAD layer doesn't care where the
// key bytes come from.
func fixedKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

func TestSealOpen_RoundTrip(t *testing.T) {
	key := fixedKey()
	plaintext := []byte("the quick brown fox jumps over the lazy dog")
	sealed, err := Seal(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, opened) {
		t.Fatalf("round-trip failed: got %q want %q", opened, plaintext)
	}
}

func TestSealOpen_EmptyPlaintext(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	opened, err := Open(key, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if len(opened) != 0 {
		t.Fatalf("expected empty plaintext, got %d bytes", len(opened))
	}
}

func TestSeal_DifferentNoncesEachCall(t *testing.T) {
	key := fixedKey()
	a, err := Seal(key, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Seal(key, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Fatal("nonces must be random per Seal call")
	}
}

func TestSeal_BlobFormatHeader(t *testing.T) {
	key := fixedKey()
	plaintext := []byte("hi")
	sealed, err := Seal(key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	// Format: [1 version][24 nonce][xchacha20poly1305-sealed] where
	// the sealed body is plaintext + 16 byte tag.
	wantLen := 1 + 24 + len(plaintext) + 16
	if len(sealed) != wantLen {
		t.Errorf("sealed length: got %d, want %d", len(sealed), wantLen)
	}
	if sealed[0] != BlobVersion {
		t.Errorf("version byte: got 0x%02x, want 0x%02x (BlobVersion)", sealed[0], BlobVersion)
	}
}

func TestOpen_RejectsTamperedCiphertext(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last byte of the AEAD tag.
	sealed[len(sealed)-1] ^= 0x01
	if _, err := Open(key, sealed); err == nil {
		t.Fatal("expected auth failure on tampered ciphertext")
	}
}

func TestOpen_RejectsTamperedNonce(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 24; i++ {
		t.Run("nonce_byte", func(t *testing.T) {
			tampered := append([]byte(nil), sealed...)
			tampered[i] ^= 0x01
			if _, err := Open(key, tampered); err == nil {
				t.Fatalf("expected auth failure when nonce byte %d is altered", i-1)
			}
		})
	}
}

func TestOpen_LegacyAESGCMV1(t *testing.T) {
	key := fixedKey()
	plaintext := []byte("legacy blob")
	sealed := sealLegacyAESGCMV1(t, key, plaintext)
	opened, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("open legacy v1: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("legacy round-trip: got %q want %q", opened, plaintext)
	}
}

func TestOpen_RejectsUnknownVersion(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Pick a version byte we definitely haven't allocated. 0xff
	// is reserved-by-convention as "future / unknown" so any open
	// must reject it.
	sealed[0] = 0xff
	if _, err := Open(key, sealed); err == nil {
		t.Fatal("expected error on unknown blob version")
	}
}

// TestOpen_RejectsVersionFlipOnV3 is the headline property of the
// v3 envelope: flipping the on-disk version byte invalidates the
// AEAD tag because the original byte is in the AD.
//
// We Seal under v3 (AD=[0x03]), flip to 0x02 (the v2 decoder is
// reachable and would expect AD=nil), and confirm Open rejects.
// Same for any other byte value — but 0x02 is the most adversarial
// because it routes to a real decoder rather than the unknown-
// version branch.
func TestOpen_RejectsVersionFlipOnV3(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if sealed[0] != BlobVersion {
		t.Fatalf("setup: expected v3 seal, got 0x%02x", sealed[0])
	}
	// Try every other version byte the dispatch knows about — none
	// must succeed.
	for _, fakeVersion := range []byte{blobVersionV1, blobVersionV2, 0xff} {
		t.Run(fmt.Sprintf("v=0x%02x", fakeVersion), func(t *testing.T) {
			tampered := append([]byte(nil), sealed...)
			tampered[0] = fakeVersion
			if _, err := Open(key, tampered); err == nil {
				t.Errorf("expected auth failure with version flipped to 0x%02x", fakeVersion)
			}
		})
	}
}

// TestOpen_LegacyXChaCha20PolyV2 confirms the backward-compat
// path for v2 blobs (XChaCha20-Poly1305 with AD=nil). Sealed
// repos that pre-date v3 must still decode under the new build
// — without this, an upgrade would silently brick every existing
// repo.
func TestOpen_LegacyXChaCha20PolyV2(t *testing.T) {
	key := fixedKey()
	plaintext := []byte("v2 legacy blob")
	sealed := sealLegacyXChaCha20PolyV2(t, key, plaintext)
	opened, err := Open(key, sealed)
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Errorf("v2 round-trip: got %q, want %q", opened, plaintext)
	}
}

func TestOpen_RejectsTooShort(t *testing.T) {
	key := fixedKey()
	cases := [][]byte{
		nil,
		{},
		{0x01},
		make([]byte, 1+23), // version + 23 of 24 nonce bytes
	}
	for i, sealed := range cases {
		if _, err := Open(key, sealed); err == nil {
			t.Errorf("case %d: expected error on truncated input", i)
		}
	}
}

func TestSeal_RejectsWrongKeyLen(t *testing.T) {
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		key := make([]byte, n)
		if _, err := Seal(key, []byte("x")); err == nil {
			t.Errorf("Seal(key len %d) should error", n)
		}
	}
}

func TestOpen_RejectsWrongKeyLen(t *testing.T) {
	good := fixedKey()
	sealed, err := Seal(good, []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range []int{0, 1, 16, 31, 33, 64} {
		key := make([]byte, n)
		if _, err := Open(key, sealed); err == nil {
			t.Errorf("Open(key len %d) should error", n)
		}
	}
}

func TestOpen_WrongKeyFails(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32)
	for i := range wrong {
		wrong[i] = byte(i + 1)
	}
	if _, err := Open(wrong, sealed); err == nil {
		t.Fatal("expected auth failure with wrong key")
	}
}

// sealLegacyXChaCha20PolyV2 produces a v2 blob (AD=nil) the way
// pre-v3 sentra builds did. Used by TestOpen_LegacyXChaCha20PolyV2
// to lock in the backward-compat reader.
func sealLegacyXChaCha20PolyV2(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 24)
	for i := range nonce {
		nonce[i] = byte(i + 7)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, blobVersionV2)
	out = append(out, nonce...)
	return aead.Seal(out, nonce, plaintext, nil) // AD=nil for v2
}

func sealLegacyAESGCMV1(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	nonce := make([]byte, 24)
	for i := range nonce {
		nonce[i] = byte(i + 1)
	}
	out := make([]byte, 0, 1+len(nonce)+len(plaintext)+gcm.Overhead())
	out = append(out, 0x01)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce[:gcm.NonceSize()], plaintext, nil)
}
