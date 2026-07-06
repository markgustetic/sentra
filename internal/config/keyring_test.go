package config

import (
	"strings"
	"testing"
)

func TestKeyringUserForConfig(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		prefix string
		want   string
	}{
		{name: "empty", bucket: "", prefix: "", want: "default"},
		{name: "bucket only", bucket: "shared-bucket", prefix: "", want: "shared-bucket"},
		{name: "bucket and prefix", bucket: "shared-bucket", prefix: "repo-a/", want: "shared-bucket/repo-a/"},
		{name: "whitespace bucket", bucket: "  ", prefix: "", want: "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			cfg.Repo.S3.Bucket = tt.bucket
			cfg.Repo.S3.Prefix = tt.prefix
			if got := KeyringUserForConfig(&cfg); got != tt.want {
				t.Fatalf("KeyringUserForConfig = %q, want %q", got, tt.want)
			}
		})
	}
	if got := KeyringUserForConfig(nil); got != KeyringDefaultUser {
		t.Fatalf("KeyringUserForConfig(nil) = %q, want %q", got, KeyringDefaultUser)
	}
}

func TestLegacyKeyringUsersForConfig(t *testing.T) {
	tests := []struct {
		name   string
		bucket string
		prefix string
		want   []string
	}{
		{name: "nil-ish empty", bucket: "", prefix: "", want: nil},
		{name: "bucket only has no legacy fallback", bucket: "shared-bucket", prefix: "", want: nil},
		{name: "bucket and prefix falls back to bucket", bucket: "shared-bucket", prefix: "repo-a/", want: []string{"shared-bucket"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg Config
			cfg.Repo.S3.Bucket = tt.bucket
			cfg.Repo.S3.Prefix = tt.prefix
			got := LegacyKeyringUsersForConfig(&cfg)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("LegacyKeyringUsersForConfig = %v, want %v", got, tt.want)
			}
		})
	}
	if got := LegacyKeyringUsersForConfig(nil); got != nil {
		t.Fatalf("LegacyKeyringUsersForConfig(nil) = %v, want nil", got)
	}
}

func TestKeyringOptionsForConfig(t *testing.T) {
	var cfg Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"
	opts := KeyringOptionsForConfig(&cfg)
	if opts.KeyringService != KeyringService {
		t.Fatalf("KeyringService = %q, want %q", opts.KeyringService, KeyringService)
	}
	if opts.KeyringUser != "shared-bucket/repo-a/" {
		t.Fatalf("KeyringUser = %q, want %q", opts.KeyringUser, "shared-bucket/repo-a/")
	}
	if KeyringService != "sentra" || KeyringDefaultUser != "default" {
		t.Fatalf("constants drifted: service=%q user=%q", KeyringService, KeyringDefaultUser)
	}
}
