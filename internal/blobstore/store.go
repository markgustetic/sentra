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
}

// Info describes an object in the store.
type Info struct {
	Key  string
	Size int64
}
