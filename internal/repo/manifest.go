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
//
// v2 added Kind/LinkTarget entries (symlinks and directories). v1
// manifests load cleanly under v2 rules — an absent kind is a regular
// file — but a v2 manifest under a v1 reader would restore symlinks
// as empty files, so the version was bumped and LoadSnapshot refuses
// manifests newer than it understands.
const ManifestVersion = 2

// FileEntry kinds. The zero value (empty string) is a regular file so
// v1 manifests — written before Kind existed — parse unchanged.
const (
	EntryKindFile    = ""
	EntryKindDir     = "dir"
	EntryKindSymlink = "symlink"
)

// FileEntry is one filesystem object in a snapshot manifest. The path
// is stored relative to Manifest.Root, in slash form regardless of
// host OS, so the manifest is portable across platforms.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	// Mode is the object's permission bits as observed at backup time.
	// Restore applies only the permission bits (Mode.Perm()) to avoid
	// re-introducing setuid/setgid/sticky surprises.
	Mode  os.FileMode `json:"mode"`
	MTime time.Time   `json:"mtime"`
	// Chunks is the ordered list of chunk hashes that, concatenated,
	// reproduce the file content. Each entry is the hex-encoded SHA-256
	// of the plaintext chunk. Empty for dirs and symlinks.
	Chunks []string `json:"chunks"`
	// Kind distinguishes regular files (empty, the v1 form), dirs,
	// and symlinks. See the EntryKind* constants.
	Kind string `json:"kind,omitempty"`
	// LinkTarget is the symlink's target verbatim (relative or
	// absolute, never resolved). Set only when Kind is
	// EntryKindSymlink. It rides inside the encrypted manifest, so
	// targets leak nothing to the bucket.
	LinkTarget string `json:"link_target,omitempty"`
}

// IsFile reports whether the entry is a regular file (chunk-backed).
func (fe FileEntry) IsFile() bool { return fe.Kind == EntryKindFile }

// IsDir reports whether the entry records a directory.
func (fe FileEntry) IsDir() bool { return fe.Kind == EntryKindDir }

// IsSymlink reports whether the entry records a symlink.
func (fe FileEntry) IsSymlink() bool { return fe.Kind == EntryKindSymlink }

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
