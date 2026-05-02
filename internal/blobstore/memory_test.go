package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestMemory_PutGet(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	rc, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("hello")) {
		t.Fatalf("got %q", got)
	}
}

func TestMemory_GetMissing(t *testing.T) {
	s := NewMemory()
	_, err := s.Get(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemory_List(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "a/1", strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "a/2", strings.NewReader("yy")); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, "b/1", strings.NewReader("zzz")); err != nil {
		t.Fatal(err)
	}
	got, err := s.List(ctx, "a/")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2, got %d", len(got))
	}
	// Keys must be sorted for stable output.
	if got[0].Key != "a/1" || got[1].Key != "a/2" {
		t.Fatalf("want sorted [a/1 a/2], got %+v", got)
	}
	// Sizes must reflect what was written.
	if got[0].Size != 1 || got[1].Size != 2 {
		t.Fatalf("sizes wrong: %+v", got)
	}
}

func TestMemory_Delete(t *testing.T) {
	s := NewMemory()
	ctx := context.Background()
	if err := s.Put(ctx, "k", strings.NewReader("v")); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "k"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound after delete")
	}
}

func TestMemory_StatMissingKey(t *testing.T) {
	s := NewMemory()
	if _, err := s.Stat(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestMemory_DeleteMissing(t *testing.T) {
	s := NewMemory()
	if err := s.Delete(context.Background(), "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
