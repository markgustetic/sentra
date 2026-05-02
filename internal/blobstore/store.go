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
// All keys are forward-slash separated and must not start with "/".
// Implementations are responsible for prefixing/sharding internally.
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
