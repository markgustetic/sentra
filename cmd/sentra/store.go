package main

import (
	"context"
	"fmt"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
)

// newS3Store is the production blobstore factory. It constructs a real S3
// client from the merged config and wraps it in a coarse retry layer for
// sustained throttling and transient S3 failures.
func newS3Store(ctx context.Context, cfg *config.Config) (blobstore.Store, error) {
	if cfg.Repo.S3.Bucket == "" {
		return nil, fmt.Errorf("repo.s3.bucket not set in sentra.yaml — edit the file and re-run")
	}
	s3, err := blobstore.NewS3(ctx, blobstore.S3Config{
		Bucket:       cfg.Repo.S3.Bucket,
		Prefix:       cfg.Repo.S3.Prefix,
		Region:       cfg.Repo.S3.Region,
		Profile:      cfg.Repo.S3.Profile,
		EndpointURL:  cfg.Repo.S3.EndpointURL,
		StorageClass: cfg.Repo.S3.StorageClass,
	})
	if err != nil {
		return nil, err
	}
	// Upload cap wraps BELOW retry so each retry attempt pays for its
	// bytes; a zero rate is a pass-through.
	limited := blobstore.NewRateLimitedStore(s3, cfg.Backup.MaxUploadRate)
	return blobstore.NewRetryStore(limited, blobstore.DefaultRetryPolicy()), nil
}
