package main

import (
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/cli"
	"github.com/markgustetic/sentra/internal/config"
)

func TestKeyringUserForConfigIncludesBucketAndPrefix(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"

	got := config.KeyringUserForConfig(&cfg)
	if got != "shared-bucket/repo-a/" {
		t.Fatalf("keyring user: got %q, want bucket/prefix identity", got)
	}
}

func TestLegacyKeyringUsersForConfigFallsBackToBucketOnly(t *testing.T) {
	var cfg config.Config
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"

	got := config.LegacyKeyringUsersForConfig(&cfg)
	if len(got) != 1 || got[0] != "shared-bucket" {
		t.Fatalf("legacy users: got %v, want [shared-bucket]", got)
	}

	cfg.Repo.S3.Prefix = ""
	if got := config.LegacyKeyringUsersForConfig(&cfg); len(got) != 0 {
		t.Fatalf("legacy users without prefix: got %v, want none", got)
	}
}

func TestBuildResolveOptsAddsLegacyKeyringFallback(t *testing.T) {
	var cfg config.Config
	cfg.Passphrase.UseKeyring = true
	cfg.Repo.S3.Bucket = "shared-bucket"
	cfg.Repo.S3.Prefix = "repo-a/"

	opts := buildResolveOptsFromConfig(&cli.RootFlags{}, &cfg, nil)
	if opts.KeyringUser != "shared-bucket/repo-a/" {
		t.Fatalf("KeyringUser: got %q", opts.KeyringUser)
	}
	if strings.Join(opts.KeyringFallbackUsers, ",") != "shared-bucket" {
		t.Fatalf("KeyringFallbackUsers: got %v", opts.KeyringFallbackUsers)
	}
}
