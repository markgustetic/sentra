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
}
