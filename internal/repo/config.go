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

// configMACInfo is the HKDF info string that domain-separates the
// config-authentication sub-key from any other purpose the KEK
// might be expanded for. Bumping the suffix invalidates existing
// MACs — so it ALSO bumps RepoConfigVersion. Don't change without
// a coordinated migration.
const configMACInfo = "sentra/config-mac/v1"

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

	// MAC authenticates everything in this struct EXCEPT itself.
	// Computed as HMAC-SHA256(authKey, canonical-json(cfg with
	// MAC=nil)) where authKey = HKDF-Expand(KEK, configMACInfo).
	//
	// A malicious operator with bucket-write access otherwise could
	// downgrade KDF.Memory or KDF.Time to make brute-forcing the
	// passphrase trivially cheap. The MAC binds the config bytes to
	// the (passphrase-derived) auth key, so any tamper is detected
	// once the operator types the right passphrase.
	//
	// Empty for repos written by pre-MAC builds; Open logs a warning
	// and proceeds, leaving the upgrade to a future write path
	// (sentra passwd or similar). The omitempty tag means the
	// canonical JSON used for verification is identical between the
	// "MAC absent" and "MAC: nil" representations, so legacy and
	// modern Open paths agree on what bytes the MAC covers.
	MAC []byte `json:"mac,omitempty"`
}
