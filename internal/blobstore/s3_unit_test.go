package blobstore

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// TestS3ListPrefix_EmptyUserPrefixIsDirectoryBounded: List(ctx, "") must
// resolve to the configured prefix as a DIRECTORY boundary (trailing
// slash), so the S3 Prefix filter can't also match sibling namespaces
// like "sentra-old/..." when two repos share a bucket.
func TestS3ListPrefix_EmptyUserPrefixIsDirectoryBounded(t *testing.T) {
	s := &S3{cfg: S3Config{Prefix: "sentra"}}
	cases := map[string]string{
		"":           "sentra/",
		"data/":      "sentra/data/",
		"snapshots/": "sentra/snapshots/",
	}
	for in, want := range cases {
		if got := s.listPrefix(in); got != want {
			t.Errorf("listPrefix(%q) = %q, want %q", in, got, want)
		}
	}
	// A prefix that already carries the slash must not be doubled.
	s2 := &S3{cfg: S3Config{Prefix: "sentra/"}}
	if got := s2.listPrefix(""); got != "sentra/" {
		t.Errorf("listPrefix(\"\") with trailing-slash cfg = %q, want %q", got, "sentra/")
	}
}

// TestLengthCheckedReadCloser_ShortBodyErrors: a body shorter than the
// object's Content-Length (a truncated download that still hits a clean
// EOF) must surface as a store-layer error rather than a silent short
// read that only the AEAD tag would later catch.
func TestLengthCheckedReadCloser_ShortBodyErrors(t *testing.T) {
	rc := newLengthCheckedReadCloser(io.NopCloser(strings.NewReader("abc")), 5, "data/x")
	_, err := io.ReadAll(rc)
	if err == nil {
		t.Fatal("expected an error reading a truncated body, got nil")
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("expected ErrUnexpectedEOF, got %v", err)
	}
}

// TestLengthCheckedReadCloser_FullBodyOK: a body matching Content-Length
// reads through cleanly with no spurious error.
func TestLengthCheckedReadCloser_FullBodyOK(t *testing.T) {
	rc := newLengthCheckedReadCloser(io.NopCloser(strings.NewReader("abcde")), 5, "data/x")
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != "abcde" {
		t.Errorf("got %q, want abcde", string(got))
	}
}

func TestNewS3_WithEndpoint(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{
		Bucket:      "test",
		Region:      "us-east-1",
		EndpointURL: "http://127.0.0.1:9000",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
}

func TestNewS3_NoEndpoint(t *testing.T) {
	// Without an endpoint URL the SDK still loads default config and we
	// should construct cleanly. (Does not contact AWS.)
	_, err := NewS3(context.Background(), S3Config{
		Bucket: "test",
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
}
