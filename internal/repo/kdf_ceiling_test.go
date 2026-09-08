package repo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/crypto"
)

// TestOpen_RejectsKDFParamsOverCeilingBeforeDeriving: Open validates the
// KDF params from an untrusted meta/config BEFORE running Argon2id,
// because the MAC that would prove tampering derives from the KEK — so
// a hostile Memory value would OOM-kill the process (or a hostile Time
// hang it) before ErrConfigTampered could ever be returned. The test
// runs each over-ceiling parameter through Open and expects a fast
// Validate error naming the field; a process that reached DeriveKEK
// with 1 GiB+1 KiB would not fail this way, it would run for a long
// time or die.
func TestOpen_RejectsKDFParamsOverCeilingBeforeDeriving(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(p *crypto.KDFParams)
		field  string
	}{
		{"memory over ceiling", func(p *crypto.KDFParams) { p.Memory = crypto.MaxMemoryKiB + 1 }, "Memory"},
		{"time over ceiling", func(p *crypto.KDFParams) { p.Time = crypto.MaxTime + 1 }, "Time"},
		{"threads over ceiling", func(p *crypto.KDFParams) { p.Threads = crypto.MaxThreads + 1 }, "Threads"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := blobstore.NewMemory()
			r, err := Init(ctx, store, []byte("hunter2"))
			if err != nil {
				t.Fatalf("init: %v", err)
			}
			r.Close()

			rc, err := store.Get(ctx, configKey)
			if err != nil {
				t.Fatalf("get config: %v", err)
			}
			raw, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			var cfg RepoConfig
			if err := json.Unmarshal(raw, &cfg); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			tc.mutate(&cfg.KDF)
			tampered, err := json.Marshal(cfg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if err := store.Put(ctx, configKey, bytes.NewReader(tampered)); err != nil {
				t.Fatalf("put tampered: %v", err)
			}

			_, err = Open(ctx, store, []byte("hunter2"))
			if err == nil {
				t.Fatal("Open accepted a config with KDF params over the ceiling")
			}
			if errors.Is(err, ErrWrongPassphrase) || errors.Is(err, ErrConfigTampered) {
				t.Fatalf("Open ran the KDF on tampered params before validating them: %v", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("Open error %q does not name the offending field %q", err, tc.field)
			}
		})
	}
}
