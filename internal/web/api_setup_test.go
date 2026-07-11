package web

import (
	"encoding/json"
	"strings"
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
	if rec := req(t, srv, "POST", "/api/setup/validate", `{"bucket":"x"}`, true); rec.Code != 409 {
		t.Errorf("validate on configured server = %d, want 409", rec.Code)
	}
}

func TestSetup_ValidateGoodAndBad(t *testing.T) {
	srv := setupServer(t, nil)
	decode := func(body string) (bool, string) {
		rec := req(t, srv, "POST", "/api/setup/validate", body, true)
		if rec.Code != 200 {
			t.Fatalf("validate = %d: %s", rec.Code, rec.Body)
		}
		var out struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.OK, out.Error
	}

	// A valid S3-compatible plan.
	if ok, msg := decode(`{"backend":"s3-compatible","bucket":"sentra-test","endpointUrl":"http://localhost:9000","initRepo":true}`); !ok {
		t.Errorf("valid s3-compatible plan rejected: %s", msg)
	}
	// A valid AWS plan (endpoint is cleared by ApplyBackendChoice).
	if ok, msg := decode(`{"backend":"aws","bucket":"my-sentra-bucket","region":"us-east-1"}`); !ok {
		t.Errorf("valid aws plan rejected: %s", msg)
	}
	// Bad bucket name (too short).
	if ok, _ := decode(`{"backend":"aws","bucket":"ab"}`); ok {
		t.Error("a 2-char bucket must fail validation")
	}
	// Missing bucket.
	if ok, _ := decode(`{"backend":"aws","bucket":""}`); ok {
		t.Error("an empty bucket must fail validation")
	}
}

func TestSetup_SeededPlanKeepsRetentionDefaults(t *testing.T) {
	// A minimal MinIO-style seed (S3 coordinates only) must not wipe the
	// retention defaults, or Prune/policy-prune would see no policy.
	seed := &config.Config{}
	seed.Repo.S3.EndpointURL = "http://localhost:9000"
	seed.Repo.S3.Bucket = "sentra-test"
	srv := setupServer(t, seed)
	plan := srv.buildPlan(setupForm{Backend: "s3-compatible", Bucket: "sentra-test", EndpointURL: "http://localhost:9000", InitRepo: true})
	want := config.Defaults().Retention.KeepLast
	if want == 0 || plan.Config.Retention.KeepLast != want {
		t.Errorf("seeded plan retention KeepLast = %d, want %d (defaults dropped)", plan.Config.Retention.KeepLast, want)
	}
}

func TestSetup_IAMPolicy(t *testing.T) {
	srv := setupServer(t, nil)
	rec := req(t, srv, "GET", "/api/setup/iam-policy?bucket=my-sentra-bucket&prefix=team", "", true)
	if rec.Code != 200 {
		t.Fatalf("iam-policy = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Policy string `json:"policy"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !strings.Contains(out.Policy, "my-sentra-bucket") || !strings.Contains(out.Policy, "s3:") {
		t.Errorf("policy missing bucket/actions: %s", out.Policy)
	}
	// Bucket is required.
	if rec := req(t, srv, "GET", "/api/setup/iam-policy", "", true); rec.Code != 400 {
		t.Errorf("iam-policy without bucket = %d, want 400", rec.Code)
	}
}
