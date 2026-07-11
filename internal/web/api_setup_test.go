package web

import (
	"encoding/json"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
)

// setupServer builds a server in first-run setup mode (no repo, no config yet).
func setupServer(t *testing.T, seed *config.Config) *Server {
	t.Helper()
	return New(Deps{
		Config:          &config.Config{},
		RepoName:        "sentra",
		ConfigPath:      tempConfigPath(t),
		Assets:          Assets,
		SetupNeeded:     true,
		SetupSeedConfig: seed,
	})
}

func TestSetup_SessionReportsSetupNeeded(t *testing.T) {
	srv := setupServer(t, nil)
	rec := req(t, srv, "GET", "/api/session", "", false)
	var out struct {
		Locked      bool `json:"locked"`
		SetupNeeded bool `json:"setupNeeded"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.SetupNeeded || !out.Locked {
		t.Fatalf("session in setup mode = %+v, want setupNeeded+locked", out)
	}
}

func TestSetup_StatusReturnsDefaults(t *testing.T) {
	srv := setupServer(t, nil)
	rec := req(t, srv, "GET", "/api/setup", "", true)
	if rec.Code != 200 {
		t.Fatalf("setup status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		SetupNeeded bool   `json:"setupNeeded"`
		Backend     string `json:"backend"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	// With no seed endpoint the smart default is the AWS backend.
	if !out.SetupNeeded || out.Backend != "aws" {
		t.Errorf("setup status = %+v", out)
	}
}

func TestSetup_StatusSeedEndpointLocksS3Compatible(t *testing.T) {
	// DefaultPlan infers S3-compatible only when a seed endpoint AND ambient
	// credentials are both present (the `sentra local` MinIO case).
	t.Setenv("AWS_ACCESS_KEY_ID", "minioadmin")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "minioadmin")
	seed := &config.Config{}
	seed.Repo.S3.EndpointURL = "http://localhost:9000"
	seed.Repo.S3.Bucket = "sentra-test"
	srv := setupServer(t, seed)

	rec := req(t, srv, "GET", "/api/setup", "", true)
	var out struct {
		Backend        string `json:"backend"`
		EndpointLocked bool   `json:"endpointLocked"`
		Seed           struct {
			EndpointURL string `json:"endpointUrl"`
			Bucket      string `json:"bucket"`
		} `json:"seed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Backend != "s3-compatible" || !out.EndpointLocked {
		t.Errorf("seeded endpoint should lock S3-compatible: %+v", out)
	}
	if out.Seed.EndpointURL != "http://localhost:9000" || out.Seed.Bucket != "sentra-test" {
		t.Errorf("seed not surfaced: %+v", out.Seed)
	}
}

func TestSetup_StatusRequiresSessionCookie(t *testing.T) {
	srv := setupServer(t, nil)
	if rec := req(t, srv, "GET", "/api/setup", "", false); rec.Code != 401 {
		t.Errorf("no-cookie setup = %d, want 401", rec.Code)
	}
}

func TestSetup_RefusedOnceConfigured(t *testing.T) {
	// A normal (already-configured) server has setupNeeded=false → 409.
	srv := testServer(t, testRepo(t))
	if rec := req(t, srv, "GET", "/api/setup", "", true); rec.Code != 409 {
		t.Errorf("setup on configured server = %d, want 409", rec.Code)
	}
}
