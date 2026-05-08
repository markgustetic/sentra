// Package blobstore defines the Store interface used to persist
// encrypted blobs (chunks, manifests, indexes, config) for a sentra
// repository, plus implementations against in-memory storage and S3.
package blobstore

import (
	"context"
	"errors"
	"io"
)

// ErrNotFound is returned by Get, Stat, and Delete when the key does
// not exist in the store. Callers should compare with errors.Is.
var ErrNotFound = errors.New("blob not found")

// Store is the minimal contract sentra needs from any blob backend.
//
// Keys are forward-slash separated, must not start with "/", and must
// not contain ".." segments. The in-memory implementation does not
// currently enforce these constraints — callers are responsible for
// clean keys. The S3 implementation will silently treat ".." as a
// path navigator (because path.Join collapses it), so violating the
// contract there can write outside the configured prefix.
//
// List returns all entries whose key begins with prefix as a literal
// byte-prefix match (HasPrefix semantics). Callers that want to scope
// to a "directory" should pass a trailing "/".
//
// All implementations must be safe for concurrent use by multiple
// goroutines.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Stat(ctx context.Context, key string) (Info, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]Info, error)

	// BatchDelete removes multiple keys in a single backend round
	// trip when supported (the S3 implementation uses DeleteObjects,
	// up to 1000 keys per request). Returns the count of keys that
	// were actually removed plus any error encountered.
	//
	// Unlike Delete, BatchDelete is idempotent: passing keys that
	// don't exist is not an error and they're simply not counted in
	// the returned deleted count. This matches S3's DeleteObjects
	// semantics and is what callers like GC want — concurrent GC
	// runs may race on the same orphan blobs without one of them
	// failing on a NotFound.
	//
	// An empty (or nil) keys slice is a no-op and returns (0, nil).
	BatchDelete(ctx context.Context, keys []string) (deleted int, err error)
}

// Info describes an object in the store.
type Info struct {
	Key  string
	Size int64
}
