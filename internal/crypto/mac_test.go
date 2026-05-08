package crypto

import (
	"bytes"
	"testing"
)

// TestDeriveSubKey_Deterministic locks in the contract that the
// same (masterKey, info) pair always derives the same sub-key.
// Without this the Open path can't verify a MAC the Init path
// computed.
func TestDeriveSubKey_Deterministic(t *testing.T) {
	master := bytes.Repeat([]byte{0xab}, 32)
	a, err := DeriveSubKey(master, "sentra/config-mac/v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveSubKey(master, "sentra/config-mac/v1")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("DeriveSubKey is not deterministic: %x vs %x", a, b)
	}
	if len(a) != SubKeyLen {
		t.Errorf("output length: got %d, want %d", len(a), SubKeyLen)
	}
}

// TestDeriveSubKey_DomainSeparation is the central security
// property: the same masterKey with two different info strings
// must produce two different sub-keys. Otherwise a sub-key
// compromise in one context can affect the others.
func TestDeriveSubKey_DomainSeparation(t *testing.T) {
	master := bytes.Repeat([]byte{0xcd}, 32)
	a, err := DeriveSubKey(master, "sentra/config-mac/v1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DeriveSubKey(master, "sentra/manifest-sig/v1")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a, b) {
		t.Errorf("domain separation failed: same sub-key for different info")
	}
}

// TestHMAC_RoundTrip locks in the basic compute-then-verify path.
// Different keys / different data must NOT verify against the
// reference tag.
func TestHMAC_RoundTrip(t *testing.T) {
	key := bytes.Repeat([]byte{0xef}, 32)
	data := []byte("the quick brown fox")

	tag := HMACSHA256(key, data)
	if len(tag) != MACSize {
		t.Errorf("tag length: got %d, want %d", len(tag), MACSize)
	}
	if !VerifyHMACSHA256(key, data, tag) {
		t.Error("VerifyHMACSHA256 rejected its own output")
	}

	wrongKey := bytes.Repeat([]byte{0x00}, 32)
	if VerifyHMACSHA256(wrongKey, data, tag) {
		t.Error("verify must reject tag computed under a different key")
	}

	wrongData := []byte("the lazy dog")
	if VerifyHMACSHA256(key, wrongData, tag) {
		t.Error("verify must reject tag for different data")
	}

	// A truncated tag must not verify — protects against length-
	// extension confusion.
	truncated := tag[:MACSize-1]
	if VerifyHMACSHA256(key, data, truncated) {
		t.Error("verify must reject a truncated tag")
	}
}

// TestVerify_ConstantTime is a smoke test that Verify uses hmac.Equal
// rather than bytes.Equal under the hood. We can't measure timing
// reliably from a unit test, but we can at least cover the contract
// that two distinct tags both fail.
func TestVerify_ConstantTime(t *testing.T) {
	key := bytes.Repeat([]byte{0x77}, 32)
	tag := HMACSHA256(key, []byte("data"))

	// Flip every bit position — none should verify.
	for i := 0; i < len(tag); i++ {
		bad := append([]byte{}, tag...)
		bad[i] ^= 0xff
		if VerifyHMACSHA256(key, []byte("data"), bad) {
			t.Errorf("verify accepted a tag with byte %d corrupted", i)
		}
	}
}
