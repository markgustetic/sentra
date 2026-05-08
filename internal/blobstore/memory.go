package blobstore

import (
	"bytes"
	"cmp"
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
)

// Memory is an in-memory Store implementation backed by a
// map[string][]byte. Safe for concurrent use. Useful for tests and as
// a reference implementation of the Store contract.
type Memory struct {
	mu   sync.RWMutex
	data map[string][]byte
}

// Compile-time assertion that *Memory implements Store.
var _ Store = (*Memory)(nil)

// NewMemory returns an empty in-memory Store.
func NewMemory() *Memory {
	return &Memory{data: make(map[string][]byte)}
}

// Put stores the entire reader contents under key. Because this is an
// in-memory backend, the reader is fully drained into a byte slice.
func (m *Memory) Put(ctx context.Context, key string, r io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("blobstore/memory: read: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// Defensive copy so callers can reuse their buffer freely.
	stored := make([]byte, len(buf))
	copy(stored, buf)
	m.data[key] = stored
	return nil
}

// Get returns a ReadCloser over the stored bytes for key, or
// ErrNotFound if the key is absent.
func (m *Memory) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}
	// Hand back a copy: the caller owns the bytes and must not see
	// later mutations.
	out := make([]byte, len(b))
	copy(out, b)
	return io.NopCloser(bytes.NewReader(out)), nil
}

// Stat returns Info{Key, Size} for the matching key, or ErrNotFound.
func (m *Memory) Stat(ctx context.Context, key string) (Info, error) {
	if err := ctx.Err(); err != nil {
		return Info{}, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	b, ok := m.data[key]
	if !ok {
		return Info{}, ErrNotFound
	}
	return Info{Key: key, Size: int64(len(b))}, nil
}

// Delete removes key. It returns ErrNotFound if the key does not
// exist (matches S3 behavior for parity across implementations).
func (m *Memory) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.data[key]; !ok {
		return ErrNotFound
	}
	delete(m.data, key)
	return nil
}

// List returns all keys having the given literal prefix, sorted
// ascending by key for stable output.
func (m *Memory) List(ctx context.Context, prefix string) ([]Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0)
	for k, v := range m.data {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Info{Key: k, Size: int64(len(v))})
		}
	}
	slices.SortFunc(out, func(a, b Info) int { return cmp.Compare(a.Key, b.Key) })
	return out, nil
}
