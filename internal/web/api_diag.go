package web

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
)

// checkItem is one doctor finding rendered by the frontend.
type checkItem struct {
	Label  string `json:"label"`
	Status string `json:"status"` // "ok" | "warn" | "fail"
	Detail string `json:"detail,omitempty"`
}

// handleDoctor runs environment diagnostics, mirroring the CLI/TUI doctor: the
// bucket name, and — only for the AWS backend (no endpoint_url) — a live
// identity check and an S3 bucket inspection (accessible, public-access-block,
// default encryption). S3-compatible backends skip the AWS-specific probes.
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	cfg := s.currentConfig()
	if cfg == nil {
		writeErr(w, http.StatusBadGateway, "no configuration loaded")
		return
	}
	var checks []checkItem
	backend := "s3-compatible"

	bucket := cfg.Repo.S3.Bucket
	if bucket == "" {
		checks = append(checks, checkItem{Label: "S3 bucket configured", Status: "fail", Detail: "no bucket set in sentra.yaml"})
		writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "checks": checks})
		return
	}
	if err := diag.ValidateBucketName(bucket); err != nil {
		checks = append(checks, checkItem{Label: "Bucket name valid", Status: "fail", Detail: err.Error()})
	} else {
		checks = append(checks, checkItem{Label: "Bucket name valid", Status: "ok", Detail: bucket})
	}

	if cfg.Repo.S3.EndpointURL != "" {
		checks = append(checks, checkItem{Label: "S3-compatible endpoint configured", Status: "ok", Detail: cfg.Repo.S3.EndpointURL})
		writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "checks": checks})
		return
	}

	// AWS backend — live probes.
	backend = "aws"
	if cfg.Repo.S3.Region == "" {
		checks = append(checks, checkItem{Label: "AWS region set", Status: "warn", Detail: "not set; relying on SDK defaults"})
	}
	if err := diag.CheckSDKIdentity(r.Context(), cfg); err != nil {
		checks = append(checks, checkItem{Label: "AWS identity verified", Status: "fail", Detail: err.Error()})
		writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "checks": checks})
		return
	}
	checks = append(checks, checkItem{Label: "AWS identity verified", Status: "ok"})

	report, err := diag.Inspect(r.Context(), cfg)
	if err != nil {
		checks = append(checks, checkItem{Label: "S3 bucket inspected", Status: "fail", Detail: err.Error()})
		writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "checks": checks})
		return
	}
	if report.BucketAccessible {
		checks = append(checks, checkItem{Label: "Bucket accessible", Status: "ok"})
	}
	switch {
	case report.PublicAccessReadable && report.PublicAccessBlocked:
		checks = append(checks, checkItem{Label: "Public access blocked", Status: "ok"})
	case report.PublicAccessReadable:
		checks = append(checks, checkItem{Label: "Public access block", Status: "warn", Detail: "not fully enabled"})
	}
	switch {
	case report.DefaultEncryptionReadable && report.DefaultEncryptionEnabled:
		checks = append(checks, checkItem{Label: "Default encryption enabled", Status: "ok"})
	case report.DefaultEncryptionReadable:
		checks = append(checks, checkItem{Label: "Default encryption", Status: "warn", Detail: "not enabled"})
	}
	writeJSON(w, http.StatusOK, map[string]any{"backend": backend, "checks": checks})
}

// handleSync replicates the repository to a destination described by another
// sentra.yaml (dstConfigPath), mirroring `sentra sync --dst-config`. It spreads
// the wrapped repo key to the destination, so it requires a typed "sync"
// confirm. Progress streams over the generic op SSE.
func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	if s.deps.NewStore == nil {
		writeErr(w, http.StatusInternalServerError, "sync is not available (no store factory)")
		return
	}
	var body struct {
		DstConfigPath string `json:"dstConfigPath"`
		InitDest      bool   `json:"initDest"`
		DryRun        bool   `json:"dryRun"`
		Confirm       string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Confirm != "sync" {
		writeErr(w, http.StatusBadRequest, `type "sync" to confirm`)
		return
	}
	path := strings.TrimSpace(body.DstConfigPath)
	if path == "" {
		writeErr(w, http.StatusBadRequest, "destination sentra.yaml path is required")
		return
	}
	dstCfg, err := config.Load(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "load destination config: "+err.Error())
		return
	}
	if sameS3Location(s.currentConfig(), dstCfg) {
		writeErr(w, http.StatusBadRequest, "source and destination resolve to the same S3 location — refusing to sync a repo onto itself")
		return
	}
	dest, err := s.deps.NewStore(r.Context(), dstCfg)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "open destination blobstore: "+err.Error())
		return
	}
	initDest, dryRun := body.InitDest, body.DryRun

	opID, err := s.startOp("sync", func(ctx context.Context, rep progress.Reporter, rp *repo.Repo) (any, error) {
		stats, err := rp.SyncTo(ctx, dest, repo.SyncOptions{InitDest: initDest, DryRun: dryRun, Progress: rep})
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"bootstrapped": stats.Bootstrapped, "copiedBlobs": stats.CopiedBlobs,
			"copiedBytes": stats.CopiedBytes, "skippedBlobs": stats.SkippedBlobs, "dryRun": stats.DryRun,
		}, nil
	})
	writeOpStart(w, opID, err)
}

// sameS3Location reports whether two configs point at the same bucket+prefix on
// the same endpoint — the guard against syncing a repo onto itself. Prefixes are
// compared after stripping surrounding slashes because the S3 store keys via
// path.Join (blobstore/s3.go), which collapses "backup" and "backup/" to the
// same namespace — so a trailing-slash difference must not slip past the guard.
func sameS3Location(a, b *config.Config) bool {
	if a == nil || b == nil {
		return false
	}
	return a.Repo.S3.Bucket == b.Repo.S3.Bucket &&
		strings.Trim(a.Repo.S3.Prefix, "/") == strings.Trim(b.Repo.S3.Prefix, "/") &&
		a.Repo.S3.EndpointURL == b.Repo.S3.EndpointURL
}
