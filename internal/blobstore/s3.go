package blobstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Config configures an S3-backed Store. Profile and EndpointURL are
// optional. EndpointURL targets MinIO/LocalStack and forces path-style
// addressing when set.
type S3Config struct {
	Bucket      string
	Prefix      string
	Region      string
	Profile     string
	EndpointURL string
}

// S3 is a Store implementation against S3 (or any S3-compatible
// service such as MinIO).
type S3 struct {
	client *s3.Client
	cfg    S3Config
}

// Compile-time assertion that *S3 implements Store.
var _ Store = (*S3)(nil)

// NewS3 builds an S3-backed Store from cfg. It loads default AWS
// configuration (env, shared config, IAM role, etc.) and overlays the
// region/profile/endpoint from cfg. No network calls are issued here;
// failures will surface on first request.
func NewS3(ctx context.Context, cfg S3Config) (*S3, error) {
	loadOpts := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.Profile != "" {
		loadOpts = append(loadOpts, awsconfig.WithSharedConfigProfile(cfg.Profile))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return nil, fmt.Errorf("blobstore/s3: load aws config: %w", err)
	}

	clientOpts := []func(*s3.Options){}
	if cfg.EndpointURL != "" {
		ep := cfg.EndpointURL
		clientOpts = append(clientOpts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(ep)
			// MinIO/LocalStack require path-style addressing.
			o.UsePathStyle = true
		})
	}

	client := s3.NewFromConfig(awsCfg, clientOpts...)
	return &S3{client: client, cfg: cfg}, nil
}

// Client exposes the underlying *s3.Client. Used by tests that need to
// create buckets etc. against the same configuration.
func (s *S3) Client() *s3.Client { return s.client }

// Bucket returns the configured bucket name.
func (s *S3) Bucket() string { return s.cfg.Bucket }

// fullKey applies cfg.Prefix to key. Result has no leading slash.
// path.Join collapses adjacent slashes and strips trailing slashes;
// for full keys (Put/Get/Stat/Delete) that's exactly what S3 wants.
func (s *S3) fullKey(key string) string {
	if s.cfg.Prefix == "" {
		return key
	}
	return path.Join(s.cfg.Prefix, key)
}

// listPrefix joins cfg.Prefix and the user-supplied prefix while
// preserving a trailing slash. Without this, path.Join("p", "data/")
// returns "p/data" and List would match both "p/data/x" and "p/dataX",
// diverging from the in-memory store's byte-prefix semantics.
func (s *S3) listPrefix(prefix string) string {
	if s.cfg.Prefix == "" {
		return prefix
	}
	full := path.Join(s.cfg.Prefix, prefix)
	if strings.HasSuffix(prefix, "/") && !strings.HasSuffix(full, "/") {
		full += "/"
	}
	return full
}

// Put uploads r under key. SSE-S3 (AES256) is requested as
// defense-in-depth on top of the client-side encryption sentra
// already does at the blob layer.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:               aws.String(s.cfg.Bucket),
		Key:                  aws.String(s.fullKey(key)),
		Body:                 r,
		ServerSideEncryption: types.ServerSideEncryptionAes256,
	})
	if err != nil {
		return fmt.Errorf("blobstore/s3: put %q: %w", key, err)
	}
	return nil
}

// Get downloads the object at key and returns its body. Caller is
// responsible for closing the returned ReadCloser.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("blobstore/s3: get %q: %w", key, err)
	}
	return out.Body, nil
}

// Stat returns metadata for key via HeadObject.
func (s *S3) Stat(ctx context.Context, key string) (Info, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		if isNotFound(err) {
			return Info{}, ErrNotFound
		}
		return Info{}, fmt.Errorf("blobstore/s3: head %q: %w", key, err)
	}
	var size int64
	if out.ContentLength != nil {
		size = *out.ContentLength
	}
	return Info{Key: key, Size: size}, nil
}

// Delete removes key. Returns ErrNotFound if the object does not
// exist (parity with the in-memory implementation). S3's DeleteObject
// is idempotent and does not surface NoSuchKey on its own, so we
// pre-check via HeadObject.
func (s *S3) Delete(ctx context.Context, key string) error {
	if _, err := s.Stat(ctx, key); err != nil {
		return err
	}
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
	})
	if err != nil {
		return fmt.Errorf("blobstore/s3: delete %q: %w", key, err)
	}
	return nil
}

// s3DeleteObjectsBatch is the S3 API hard limit on keys per
// DeleteObjects call. We chunk on this boundary internally so
// callers can pass arbitrarily large slices.
const s3DeleteObjectsBatch = 1000

// BatchDelete removes keys via S3's DeleteObjects API in chunks of
// up to 1000 (the API limit). Idempotent: keys that don't exist are
// silently OK. Returns the total count of objects S3 reported as
// successfully deleted across all chunks.
//
// On a partial failure, BatchDelete returns the count of keys that
// did succeed plus an error describing the per-key failures from
// the first failing chunk. This matches what callers like GC want:
// they can record the partial progress and surface the error.
func (s *S3) BatchDelete(ctx context.Context, keys []string) (int, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	deleted := 0
	for start := 0; start < len(keys); start += s3DeleteObjectsBatch {
		end := start + s3DeleteObjectsBatch
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		objs := make([]types.ObjectIdentifier, len(chunk))
		for i, k := range chunk {
			objs[i] = types.ObjectIdentifier{Key: aws.String(s.fullKey(k))}
		}
		// Quiet=false so the response includes per-key errors; we
		// want to surface them rather than swallow.
		out, err := s.client.DeleteObjects(ctx, &s3.DeleteObjectsInput{
			Bucket: aws.String(s.cfg.Bucket),
			Delete: &types.Delete{Objects: objs, Quiet: aws.Bool(false)},
		})
		if err != nil {
			return deleted, fmt.Errorf("blobstore/s3: batch delete: %w", err)
		}
		deleted += len(out.Deleted)
		if len(out.Errors) > 0 {
			// Build a compact summary; per-key error detail isn't
			// useful at the call-site (GC), so we surface the count
			// and the first key/code for triage.
			first := out.Errors[0]
			return deleted, fmt.Errorf("blobstore/s3: batch delete: %d errors (first: key=%q code=%q msg=%q)",
				len(out.Errors),
				aws.ToString(first.Key),
				aws.ToString(first.Code),
				aws.ToString(first.Message),
			)
		}
	}
	return deleted, nil
}

// List returns every object under cfg.Prefix + prefix, with cfg.Prefix
// stripped from the returned keys so callers see the same key space
// they passed to Put. Trailing slash on prefix is preserved so that
// byte-prefix semantics match the in-memory implementation: List(ctx,
// "data/") matches "data/foo" but not "dataX/foo".
func (s *S3) List(ctx context.Context, prefix string) ([]Info, error) {
	full := s.listPrefix(prefix)
	paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.cfg.Bucket),
		Prefix: aws.String(full),
	})
	out := make([]Info, 0)
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("blobstore/s3: list %q: %w", prefix, err)
		}
		for _, obj := range page.Contents {
			if obj.Key == nil {
				continue
			}
			rel := stripPrefix(*obj.Key, s.cfg.Prefix)
			var size int64
			if obj.Size != nil {
				size = *obj.Size
			}
			out = append(out, Info{Key: rel, Size: size})
		}
	}
	return out, nil
}

// stripPrefix removes a leading "<prefix>/" (or "<prefix>") from key.
// If the prefix is empty, key is returned unchanged.
func stripPrefix(key, prefix string) string {
	if prefix == "" {
		return key
	}
	// path.Join("a/b/", "") == "a/b", so the same canonicalization is
	// applied here to match what fullKey produces on the way in.
	canon := path.Join(prefix)
	switch {
	case key == canon:
		return ""
	case len(key) > len(canon) && key[len(canon)] == '/' && key[:len(canon)] == canon:
		return key[len(canon)+1:]
	default:
		// Prefix did not actually match; return key unchanged so the
		// caller can spot the surprise instead of silently mangling.
		return key
	}
}

// isNotFound reports whether err is an S3 "object missing" condition
// (NoSuchKey from Get/Delete, NotFound from Head). Both are mapped to
// ErrNotFound by the Store implementation.
func isNotFound(err error) bool {
	var nsk *types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var nf *types.NotFound
	return errors.As(err, &nf)
}
