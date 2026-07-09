package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

const (
	// minioHealthURL is MinIO's liveness probe; it answers 200 when the server
	// is up. The docker-compose.yaml in the repo root publishes MinIO on :9000.
	minioHealthURL = "http://localhost:9000/minio/health/live"

	// minioHealthTimeout bounds a single health probe so an "already up?" check
	// fails fast instead of hanging when nothing is listening.
	minioHealthTimeout = 2 * time.Second

	// minioStartTimeout bounds how long we wait for a freshly started MinIO to
	// become reachable after `docker compose up -d`.
	minioStartTimeout = 30 * time.Second
)

// minioStartHint is the actionable fallback shown when Sentra can neither reach
// nor start a local MinIO.
const minioStartHint = "run `docker compose up -d` from the repo (needs Docker + docker-compose.yaml), then retry"

// ensureLocalMinIO makes a local MinIO reachable before `sentra local` launches
// the UI. It returns nil if MinIO already answers, otherwise it starts the
// docker-compose stack (streaming output to stderr) and polls the health
// endpoint. If it can neither reach nor start MinIO it returns a clear,
// actionable error. It is the production EnsureMinIO wired into cli.LocalDeps.
func ensureLocalMinIO(ctx context.Context) error {
	if minioHealthy(ctx, minioHealthURL) {
		return nil // already up — reuse it
	}

	// Start (or ensure) the compose stack in the current directory. `up -d`
	// is idempotent: it exits 0 when the stack is already running.
	up := exec.CommandContext(ctx, "docker", "compose", "up", "-d")
	up.Stdout = os.Stderr
	up.Stderr = os.Stderr
	if err := up.Run(); err != nil {
		// docker missing or the stack failed to start: no point polling for
		// 30s — surface the failure with the manual fallback immediately.
		return fmt.Errorf("could not start local MinIO (`docker compose up -d`: %w) — %s", err, minioStartHint)
	}

	deadline := time.Now().Add(minioStartTimeout)
	for {
		if minioHealthy(ctx, minioHealthURL) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("could not reach local MinIO at %s after %s — %s", minioHealthURL, minioStartTimeout, minioStartHint)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// minioHealthy reports whether a GET to url returns 200 within a short timeout.
// Any transport error or non-200 status counts as unhealthy.
func minioHealthy(ctx context.Context, url string) bool {
	reqCtx, cancel := context.WithTimeout(ctx, minioHealthTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
