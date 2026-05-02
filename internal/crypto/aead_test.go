package crypto

import (
	"bytes"
	"testing"
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
	// Format: [1 version][24 nonce][gcm-sealed] where gcm-sealed is
	// plaintext + 16 byte tag.
	wantLen := 1 + 24 + len(plaintext) + 16
	if len(sealed) != wantLen {
		t.Errorf("sealed length: got %d, want %d", len(sealed), wantLen)
	}
	if sealed[0] != 0x01 {
		t.Errorf("version byte: got 0x%02x, want 0x01", sealed[0])
	}
}

func TestOpen_RejectsTamperedCiphertext(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	// Flip the last byte of the GCM tag.
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
	// Flip a byte inside the stored nonce (offset 1 = first nonce byte).
	sealed[1] ^= 0x01
	if _, err := Open(key, sealed); err == nil {
		t.Fatal("expected auth failure when nonce is altered")
	}
}

func TestOpen_RejectsUnknownVersion(t *testing.T) {
	key := fixedKey()
	sealed, err := Seal(key, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	sealed[0] = 0x02
	if _, err := Open(key, sealed); err == nil {
		t.Fatal("expected error on unknown blob version")
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
