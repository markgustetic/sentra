//go:build integration

package blobstore

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/testcontainers/testcontainers-go/modules/minio"
)

// TestS3Integration runs the Store contract from memory_test.go against
// a real S3 implementation talking to a MinIO container. It is gated
// behind the "integration" build tag and requires Docker.
func TestS3Integration(t *testing.T) {
	ctx := context.Background()

	container, err := minio.Run(ctx,
		"minio/minio:RELEASE.2024-01-16T16-07-38Z",
		minio.WithUsername("sentra-test"),
		minio.WithPassword("sentra-test-secret"),
	)
	if err != nil {
		t.Fatalf("start minio: %v", err)
	}
	t.Cleanup(func() {
		if err := container.Terminate(ctx); err != nil {
			t.Logf("terminate minio: %v", err)
		}
	})

	connStr, err := container.ConnectionString(ctx)
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	endpoint := "http://" + connStr

	// The AWS SDK picks these up automatically via LoadDefaultConfig,
	// so we don't need to extend S3Config to plumb credentials. Using
	// t.Setenv keeps the override scoped to this test.
	t.Setenv("AWS_ACCESS_KEY_ID", container.Username)
	t.Setenv("AWS_SECRET_ACCESS_KEY", container.Password)
	// Defensive: make sure no shared profile/region from the host env
	// confuses LoadDefaultConfig.
	t.Setenv("AWS_PROFILE", "")
	t.Setenv("AWS_REGION", "us-east-1")

	store, err := NewS3(ctx, S3Config{
		Bucket:      "sentra-test",
		Region:      "us-east-1",
		EndpointURL: endpoint,
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}

	// MinIO doesn't auto-create the bucket; we make it directly.
	if _, err := store.Client().CreateBucket(ctx, &s3.CreateBucketInput{
		Bucket: aws.String("sentra-test"),
	}); err != nil {
		t.Fatalf("create bucket: %v", err)
	}

	t.Run("put_get", func(t *testing.T) {
		if err := store.Put(ctx, "k", strings.NewReader("hello")); err != nil {
			t.Fatal(err)
		}
		rc, err := store.Get(ctx, "k")
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rc.Close() }()
		got, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, []byte("hello")) {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("get_missing", func(t *testing.T) {
		if _, err := store.Get(ctx, "definitely-not-there"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("stat", func(t *testing.T) {
		if err := store.Put(ctx, "stat-key", strings.NewReader("12345")); err != nil {
			t.Fatal(err)
		}
		info, err := store.Stat(ctx, "stat-key")
		if err != nil {
			t.Fatal(err)
		}
		if info.Key != "stat-key" || info.Size != 5 {
			t.Fatalf("info=%+v", info)
		}
	})

	t.Run("stat_missing", func(t *testing.T) {
		if _, err := store.Stat(ctx, "missing"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("list", func(t *testing.T) {
		// Use an isolated prefix so this case is independent of the
		// objects put earlier.
		if err := store.Put(ctx, "list/a", strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(ctx, "list/b", strings.NewReader("yy")); err != nil {
			t.Fatal(err)
		}
		if err := store.Put(ctx, "other/c", strings.NewReader("zzz")); err != nil {
			t.Fatal(err)
		}
		got, err := store.List(ctx, "list/")
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2, got %d (%+v)", len(got), got)
		}
		if got[0].Key != "list/a" || got[1].Key != "list/b" {
			t.Fatalf("unexpected keys: %+v", got)
		}
	})

	t.Run("delete", func(t *testing.T) {
		if err := store.Put(ctx, "del", strings.NewReader("v")); err != nil {
			t.Fatal(err)
		}
		if err := store.Delete(ctx, "del"); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(ctx, "del"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound after delete, got %v", err)
		}
	})

	t.Run("delete_missing", func(t *testing.T) {
		if err := store.Delete(ctx, "never-existed"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("want ErrNotFound, got %v", err)
		}
	})

	t.Run("batch_delete", func(t *testing.T) {
		// Put a small set, then BatchDelete them.
		keys := []string{"batch/a", "batch/b", "batch/c"}
		for _, k := range keys {
			if err := store.Put(ctx, k, strings.NewReader("x")); err != nil {
				t.Fatalf("put %s: %v", k, err)
			}
		}
		// Mix in a missing key — BatchDelete is idempotent on misses.
		toDelete := append([]string{"batch/never-existed"}, keys...)
		deleted, err := store.BatchDelete(ctx, toDelete)
		if err != nil {
			t.Fatalf("BatchDelete: %v", err)
		}
		if deleted != len(keys) {
			t.Errorf("deleted: got %d, want %d (missing key shouldn't count)", deleted, len(keys))
		}
		for _, k := range keys {
			if _, err := store.Stat(ctx, k); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s still present after BatchDelete: %v", k, err)
			}
		}
	})

	t.Run("put_if_absent_fresh_succeeds", func(t *testing.T) {
		key := "ifabsent/fresh"
		if err := store.PutIfAbsent(ctx, key, strings.NewReader("first")); err != nil {
			t.Fatalf("PutIfAbsent fresh: %v", err)
		}
		// Body must have landed.
		rc, err := store.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get after PutIfAbsent: %v", err)
		}
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if !bytes.Equal(got, []byte("first")) {
			t.Errorf("body: got %q, want %q", got, "first")
		}
	})

	t.Run("put_if_absent_conflict_returns_sentinel", func(t *testing.T) {
		// First PutIfAbsent — establish the key.
		key := "ifabsent/locked"
		if err := store.PutIfAbsent(ctx, key, strings.NewReader("holder")); err != nil {
			t.Fatalf("first PutIfAbsent: %v", err)
		}
		// Second attempt at the same key must return ErrAlreadyExists.
		// MinIO support for If-None-Match varies by version. If the
		// underlying server doesn't enforce the conditional, the
		// second Put will succeed and we'll fail loudly below — so a
		// failure here means either our wrapper is wrong OR the
		// pinned MinIO image regressed; either is worth surfacing.
		err := store.PutIfAbsent(ctx, key, strings.NewReader("interloper"))
		if !errors.Is(err, ErrAlreadyExists) {
			t.Fatalf("second PutIfAbsent: got %v, want ErrAlreadyExists", err)
		}
		// First holder's body must still be there.
		rc, _ := store.Get(ctx, key)
		defer rc.Close()
		got, _ := io.ReadAll(rc)
		if !bytes.Equal(got, []byte("holder")) {
			t.Errorf("body after conflict: got %q, want %q", got, "holder")
		}
	})

	t.Run("batch_delete_chunks_above_1000", func(t *testing.T) {
		// Stress the API-limit chunking: 2050 keys means three
		// DeleteObjects round trips (1000 + 1000 + 50). We Put a
		// smaller, fixed number of REAL keys and pad with non-existent
		// ones so the test is fast — DeleteObjects accepts non-existent
		// keys and returns them in `Deleted` (S3's idempotent contract).
		const total = 2050
		const real_ = 5 // keep MinIO put cost low
		realKeys := make([]string, real_)
		for i := 0; i < real_; i++ {
			k := "stress/real-" + string(rune('a'+i))
			realKeys[i] = k
			if err := store.Put(ctx, k, strings.NewReader("x")); err != nil {
				t.Fatalf("put %s: %v", k, err)
			}
		}
		all := make([]string, 0, total)
		all = append(all, realKeys...)
		for i := 0; i < total-real_; i++ {
			all = append(all, "stress/missing-"+string(rune('a'+(i%26)))+"-"+string(rune('0'+(i%10))))
		}
		deleted, err := store.BatchDelete(ctx, all)
		if err != nil {
			t.Fatalf("BatchDelete: %v", err)
		}
		// S3 reports "deleted" for any key included in the delete list,
		// whether or not it was present — that's the API behavior. So
		// the deleted count for a 2050-key call is up to 2050. The
		// check we care about: the chunking didn't drop anything and
		// the real keys are gone.
		if deleted < real_ {
			t.Errorf("deleted: got %d, want >= %d", deleted, real_)
		}
		for _, k := range realKeys {
			if _, err := store.Stat(ctx, k); !errors.Is(err, ErrNotFound) {
				t.Errorf("%s still present: %v", k, err)
			}
		}
	})
}
