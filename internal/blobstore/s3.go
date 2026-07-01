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
	"github.com/aws/smithy-go"
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
	// An empty user prefix means "everything under our namespace". Return
	// cfg.Prefix as a DIRECTORY boundary (single trailing slash) so the
	// S3 Prefix filter matches only keys under "<prefix>/" and never a
	// sibling namespace like "<prefix>-old/..." that shares the string
	// prefix. path.Join("sentra", "") would drop the boundary to
	// "sentra", so handle this case explicitly.
	if prefix == "" {
		return strings.TrimSuffix(s.cfg.Prefix, "/") + "/"
	}
	full := path.Join(s.cfg.Prefix, prefix)
	if strings.HasSuffix(prefix, "/") && !strings.HasSuffix(full, "/") {
		full += "/"
	}
	return full
}

// Put uploads r under key.
//
// Sentra's client-side AEAD is the primary protection — every blob
// is sealed before it reaches this method. Server-side encryption
// (SSE-S3 / SSE-KMS) is a belt-and-suspenders layer best applied
// via bucket-default encryption, which:
//
//   - applies uniformly to every Put without per-request flags,
//   - lets the operator pick AES256 vs KMS independently of code,
//   - works on every S3-compatible backend (MinIO without KMS,
//     LocalStack, etc.) instead of requiring per-server feature
//     parity with AWS S3.
//
// The README's "Recommendations for operators" section documents
// the bucket-default-encryption setting; we don't request SSE
// here so that contract is the only place SSE policy lives.
func (s *S3) Put(ctx context.Context, key string, r io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.cfg.Bucket),
		Key:    aws.String(s.fullKey(key)),
		Body:   r,
	})
	if err != nil {
		return fmt.Errorf("blobstore/s3: put %q: %w", key, err)
	}
	return nil
}

// lengthCheckedReadCloser wraps an object body and verifies that the
// number of bytes actually read matches the object's Content-Length. A
// backend that returns a truncated body with a clean EOF (a flaky
// connection, or an S3-compatible store returning a short object) would
// otherwise be handed to the caller silently, caught only later — and
// less clearly — by the blob's AEAD tag. This surfaces truncation as a
// precise store-layer error instead.
type lengthCheckedReadCloser struct {
	rc       io.ReadCloser
	expected int64
	read     int64
	key      string
}

func newLengthCheckedReadCloser(rc io.ReadCloser, expected int64, key string) io.ReadCloser {
	return &lengthCheckedReadCloser{rc: rc, expected: expected, key: key}
}

func (r *lengthCheckedReadCloser) Read(p []byte) (int, error) {
	n, err := r.rc.Read(p)
	r.read += int64(n)
	if err == io.EOF && r.read != r.expected {
		return n, fmt.Errorf("blobstore/s3: short read for %q: got %d bytes, want %d: %w",
			r.key, r.read, r.expected, io.ErrUnexpectedEOF)
	}
	return n, err
}

func (r *lengthCheckedReadCloser) Close() error { return r.rc.Close() }

// Get downloads the object at key and returns its body. Caller is
// responsible for closing the returned ReadCloser. When S3 reports a
// Content-Length, the body is wrapped so a truncated download surfaces
// as an error on read rather than a silent short read.
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
	if out.ContentLength != nil {
		return newLengthCheckedReadCloser(out.Body, *out.ContentLength, key), nil
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
//
// The ErrNotFound signal is BEST-EFFORT: the HeadObject pre-check and
// the DeleteObject are not atomic. If a concurrent actor removes the key
// in the window between them, this returns nil (a successful delete)
// rather than ErrNotFound; conversely a key that appears in the window
// is deleted even though the pre-check saw it absent. Callers that need
// idempotent "already gone == success" semantics must therefore treat
// nil and ErrNotFound identically (as DeleteSnapshot / prune / policy
// do). Data-blob reclamation deliberately uses the idempotent
// BatchDelete instead, which needs no such pre-check.
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

// PutIfAbsent uses the S3 conditional-write `If-None-Match: *`
// header so the existence check is atomic with the write
// server-side. ErrAlreadyExists is returned on the 412 / 409
// response that S3 emits when a key is already taken.
//
// MinIO and other S3-compatible servers vary in their support for
// If-None-Match; the test suite covers MinIO via testcontainers
// (s3_integration_test.go).
func (s *S3) PutIfAbsent(ctx context.Context, key string, r io.Reader) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.cfg.Bucket),
		Key:         aws.String(s.fullKey(key)),
		Body:        r,
		IfNoneMatch: aws.String("*"),
		// SSE policy lives on the bucket (see Put's docstring).
	})
	if err != nil {
		// AWS returns PreconditionFailed (412) for an If-None-Match
		// miss. Some S3-compatible stores surface this as the 409
		// "already exists" code instead. Both map to the sentinel.
		var apiErr smithy.APIError
		if errors.As(err, &apiErr) {
			switch apiErr.ErrorCode() {
			case "PreconditionFailed", "ConditionalRequestConflict":
				return ErrAlreadyExists
			}
		}
		return fmt.Errorf("blobstore/s3: putifabsent %q: %w", key, err)
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
