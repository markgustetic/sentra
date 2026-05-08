// Package repo is the integration layer that stitches the crypto,
// chunker, walker, and blobstore packages into a working snapshot
// lifecycle: init / open the repository, create snapshots from a
// directory tree, list them, and restore a snapshot back to disk.
//
// The on-disk layout is documented in docs/plans/2026-05-02-sentra-design.md
// under "Storage format". A repository is a blobstore containing:
//
//   - "config"               — repo metadata + wrapped repo key
//   - "snapshots/<id>"       — encrypted, zstd-compressed JSON manifest
//   - "data/<aa>/<sha256hex>" — encrypted, zstd-compressed chunk
//
// Higher layers (CLI, TUI, agent) consume *Repo and never touch the
// blobstore directly.
package repo

import (
	"os"
	"time"
)

// ManifestVersion is the wire-format version of the snapshot manifest
// schema. Incremented when the JSON shape changes in an incompatible
// way; loaders inspect the field after unmarshal and decide what to do.
const ManifestVersion = 1

// FileEntry is one file in a snapshot manifest. The path is stored
// relative to Manifest.Root, in slash form regardless of host OS, so
// the manifest is portable across platforms.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Mode is the file's permission bits as observed at backup time.
	// Restore applies only the permission bits (Mode.Perm()) to avoid
	// re-introducing setuid/setgid/sticky surprises.
	Mode  os.FileMode `json:"mode"`
	MTime time.Time   `json:"mtime"`
	// Chunks is the ordered list of chunk hashes that, concatenated,
	// reproduce the file content. Each entry is the hex-encoded SHA-256
	// of the plaintext chunk.
	Chunks []string `json:"chunks"`
}

// SnapshotStats is the high-level summary persisted alongside the file
// tree. Stored in the manifest so listing snapshots does not require
// summing the tree.
type SnapshotStats struct {
	// Files is the count of regular files captured in the snapshot.
	Files int `json:"files"`
	// Bytes is the total plaintext size of all captured files.
	Bytes int64 `json:"bytes"`
	// NewBytes is the sum of sealed-blob sizes uploaded for this
	// snapshot. Chunks that already existed in the store from a prior
	// snapshot do not contribute. Useful for "how big was this delta?"
	// questions in the UI.
	NewBytes int64 `json:"new_bytes"`
}

// Manifest is the snapshot's file tree and metadata. JSON-encoded then
// zstd-compressed then AEAD-sealed before storage at snapshots/<id>.
//
// Tag uses omitempty so unset tags are absent in the JSON wire form,
// not represented as `"tag": ""`.
type Manifest struct {
	Version   int           `json:"version"`
	ID        string        `json:"id"`
	CreatedAt time.Time     `json:"created_at"`
	Host      string        `json:"host"`
	Tag       string        `json:"tag,omitempty"`
	Root      string        `json:"root"`
	Tree      []FileEntry   `json:"tree"`
	Stats     SnapshotStats `json:"stats"`
}
