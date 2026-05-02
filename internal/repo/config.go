package repo

import (
	"time"

	"github.com/markgustetic/sentra/internal/crypto"
)

// configKey is the blobstore key under which the repository config is
// stored. The blob is *not* itself sealed by Seal/Open: its only secret
// is WrappedRepoKey, which is already encrypted with a passphrase-
// derived KEK. Storing the config as plain JSON keeps it inspectable
// for diagnostics.
const configKey = "config"

// RepoConfigVersion is the wire-format version of the on-disk config.
// Bumped if the JSON shape changes incompatibly.
const RepoConfigVersion = 1

// RepoConfig is the JSON document at "config" inside the blobstore.
//
// Only the salt and KDF parameters are exposed unencrypted — they are
// required *before* any decryption can happen, so we store them in
// the clear. The repo key (which encrypts every blob) is itself
// wrapped with a KEK derived from the passphrase + salt; without the
// passphrase an attacker has only KDF parameters and ciphertext.
type RepoConfig struct {
	Version        int              `json:"version"`
	ID             string           `json:"id"`
	KDF            crypto.KDFParams `json:"kdf"`
	Salt           []byte           `json:"salt"`             // 16 bytes
	WrappedRepoKey []byte           `json:"wrapped_repo_key"` // crypto.Seal(KEK, repoKey)
	CreatedAt      time.Time        `json:"created_at"`
}
