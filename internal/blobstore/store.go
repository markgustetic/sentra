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

// ErrAlreadyExists is returned by PutIfAbsent when the destination
// key already holds an object. Callers can errors.Is against this
// sentinel to distinguish "lock already taken" from a transport-
// layer failure.
var ErrAlreadyExists = errors.New("blob already exists")

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
	// up to 1000 keys per request). Returns the count of keys the
	// backend confirms absent after the call plus any error
	// encountered.
	//
	// Unlike Delete, BatchDelete is idempotent: passing keys that
	// don't exist is not an error. The "deleted" count reports keys
	// that are now absent — INCLUDING keys that weren't there to
	// begin with — because that's what S3's DeleteObjects API
	// reports in its `Deleted` slice (it doesn't distinguish
	// "removed an existing key" from "no-op on a missing key").
	// In-memory implementations match the same semantic so callers
	// can rely on a single contract across backends.
	//
	// On a partial backend failure, the returned count is the
	// number of keys the backend confirmed for the chunks it
	// processed before failing; the error wraps the backend's
	// per-key failure summary.
	//
	// An empty (or nil) keys slice is a no-op and returns (0, nil).
	BatchDelete(ctx context.Context, keys []string) (deleted int, err error)

	// PutIfAbsent writes r at key only if no object exists at that
	// key. Returns ErrAlreadyExists when the key is already taken,
	// nil on a successful write, or the underlying transport error
	// for anything else.
	//
	// The S3 implementation uses the `If-None-Match: *` header so
	// the conditional check happens server-side. The in-memory
	// implementation locks its map so concurrent PutIfAbsent calls
	// at the same key serialize correctly.
	//
	// PutIfAbsent is the primitive for advisory locks: callers
	// PutIfAbsent at a known key, run the protected operation,
	// then Delete the key. A crashed lock-holder leaves the lock
	// blob behind; recovery is currently manual (delete the lock
	// key out-of-band).
	//
	// Caveat: at-least-once retry semantics across PutIfAbsent are
	// awkward. If the first attempt's response is lost in transit
	// but the write actually landed, the retry sees its own write
	// and returns ErrAlreadyExists. RetryStore therefore does NOT
	// retry PutIfAbsent — callers see a definitive yes-or-no on
	// the first attempt.
	PutIfAbsent(ctx context.Context, key string, r io.Reader) error
}

// Info describes an object in the store.
type Info struct {
	Key  string
	Size int64
}
