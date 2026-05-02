package blobstore

import (
	"context"
	"testing"
)

func TestNewS3_WithEndpoint(t *testing.T) {
	_, err := NewS3(context.Background(), S3Config{
		Bucket:      "test",
		Region:      "us-east-1",
		EndpointURL: "http://127.0.0.1:9000",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
}

func TestNewS3_NoEndpoint(t *testing.T) {
	// Without an endpoint URL the SDK still loads default config and we
	// should construct cleanly. (Does not contact AWS.)
	_, err := NewS3(context.Background(), S3Config{
		Bucket: "test",
		Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("NewS3: %v", err)
	}
}
