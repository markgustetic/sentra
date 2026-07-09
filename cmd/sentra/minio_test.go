package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMinioHealthy_TrueOn200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	if !minioHealthy(context.Background(), srv.URL) {
		t.Fatal("expected healthy for a 200 health response")
	}
}

func TestMinioHealthy_FalseOnNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()
	if minioHealthy(context.Background(), srv.URL) {
		t.Fatal("expected unhealthy for a 503 health response")
	}
}

func TestMinioHealthy_FalseWhenUnreachable(t *testing.T) {
	// Start then immediately close so the address refuses connections.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	if minioHealthy(context.Background(), url) {
		t.Fatal("expected unhealthy for an unreachable endpoint")
	}
}
