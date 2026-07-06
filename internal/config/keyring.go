package config

import "strings"

// KeyringService is the service name Sentra passes to the OS keyring. It is a
// fixed namespace so every repo's entry lives under one service and only the
// per-repo user string disambiguates them.
const KeyringService = "sentra"

// KeyringDefaultUser is the keyring user for a config with no bucket. A
// single-repo user never collides, and it keeps a clean install from failing
// hard before a bucket is chosen.
const KeyringDefaultUser = "default"

// KeyringUserForConfig derives the per-repo keyring identifier from the S3
// coordinates. Binding both bucket and prefix means two repos that share a
// bucket but differ only by prefix get distinct keyring entries — the fix for
// the earlier bug where they aliased onto the same stored passphrase.
func KeyringUserForConfig(cfg *Config) string {
	if cfg == nil {
		return KeyringDefaultUser
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" {
		return KeyringDefaultUser
	}
	prefix := strings.TrimSpace(cfg.Repo.S3.Prefix)
	if prefix == "" {
		return bucket
	}
	return bucket + "/" + prefix
}

// LegacyKeyringUsersForConfig lists the pre-prefix keyring identifiers to try
// after the current KeyringUserForConfig misses. Before the bucket+prefix
// identity existed, entries were keyed on the bucket alone; this lets an
// existing entry still resolve after an upgrade. It returns nothing when the
// current identity already equals the bucket (nothing to fall back to).
func LegacyKeyringUsersForConfig(cfg *Config) []string {
	if cfg == nil {
		return nil
	}
	bucket := strings.TrimSpace(cfg.Repo.S3.Bucket)
	if bucket == "" || KeyringUserForConfig(cfg) == bucket {
		return nil
	}
	return []string{bucket}
}

// KeyringOptionsForConfig builds the StoreKeyringOptions used to save or delete
// the passphrase for cfg's repo.
func KeyringOptionsForConfig(cfg *Config) StoreKeyringOptions {
	return StoreKeyringOptions{
		KeyringService: KeyringService,
		KeyringUser:    KeyringUserForConfig(cfg),
	}
}
