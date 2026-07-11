package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

// setupForm is the browser-submitted wizard state. It carries only non-secret
// S3 coordinates and provisioning choices — never AWS credentials (the engine
// uses ambient credentials) and never the passphrase (that rides in the apply
// body separately and is zeroized after use).
type setupForm struct {
	Backend           string `json:"backend"` // "aws" | "s3-compatible"
	Bucket            string `json:"bucket"`
	Prefix            string `json:"prefix"`
	Region            string `json:"region"`
	Profile           string `json:"profile"`
	EndpointURL       string `json:"endpointUrl"`
	CreateBucket      bool   `json:"createBucket"`
	BlockPublicAccess bool   `json:"blockPublicAccess"`
	DefaultEncryption bool   `json:"defaultEncryption"`
	InitRepo          bool   `json:"initRepo"`
}

// buildPlan assembles a setup.Plan from browser form data, mirroring the
// CLI/TUI drivers. The web AWS path is existing-credentials only (interactive
// login can't run over HTTP); S3-compatible skips all AWS provisioning but keeps
// the init choice. It does not validate — callers call setup.ValidatePlan.
func (s *Server) buildPlan(f setupForm) setup.Plan {
	seed := s.setupPlanBase()
	plan := setup.DefaultPlan(seed, setup.DefaultEnvProbe())
	plan.Config.Repo.S3.Bucket = f.Bucket
	plan.Config.Repo.S3.Prefix = f.Prefix
	plan.Config.Repo.S3.Region = f.Region
	plan.Config.Repo.S3.Profile = f.Profile
	plan.Config.Repo.S3.EndpointURL = f.EndpointURL

	backend := setup.Backend(f.Backend)
	if backend != setup.BackendS3Compatible {
		backend = setup.BackendAWS
	}
	setup.ApplyBackendChoice(&plan, backend, seed.Repo.S3.Profile)

	plan.InitRepo = f.InitRepo
	if backend == setup.BackendS3Compatible {
		plan.PrepareAWS = false
		plan.AWSAuthMethod = setup.AWSAuthSkip
		plan.CreateBucket = false
		plan.BlockPublicAccess = false
		plan.DefaultEncryption = false
	} else {
		plan.PrepareAWS = true
		plan.AWSAuthMethod = setup.AWSAuthExisting // ambient creds; no browser login
		plan.CreateBucket = f.CreateBucket
		plan.BlockPublicAccess = f.BlockPublicAccess
		plan.DefaultEncryption = f.DefaultEncryption
	}

	setup.NormalizeConfig(&plan.Config)
	setup.ApplyPassphraseConfig(&plan)
	return plan
}

// The setup surface is the first-run wizard: it drives the shared internal/setup
// engine (DefaultPlan → transforms → ValidatePlan → WriteDraft → PrepareAWS →
// WriteConfig → InitRepo) from browser-collected form data. It adds NO
// provisioning or config logic of its own. AWS credentials are never collected
// or written — the engine relies on ambient credentials resolved by the SDK.

// setupPlanBase returns the seed config to build the wizard's default plan from:
// the injected SetupSeedConfig (e.g. `sentra local` MinIO coordinates) or the
// package defaults.
func (s *Server) setupPlanBase() config.Config {
	if s.deps.SetupSeedConfig != nil {
		return *s.deps.SetupSeedConfig
	}
	return config.Defaults()
}

// handleSetupStatus reports the inferred defaults the wizard pre-fills with:
// the smart-defaulted backend, seeded S3 coordinates, the inferred region/
// profile, whether ambient AWS credentials appear present, and whether a seed
// endpoint locks the backend to S3-compatible (the `sentra local` case).
func (s *Server) handleSetupStatus(w http.ResponseWriter, _ *http.Request) {
	probe := setup.DefaultEnvProbe()
	plan := setup.DefaultPlan(s.setupPlanBase(), probe)
	s3 := plan.Config.Repo.S3
	writeJSON(w, http.StatusOK, map[string]any{
		"setupNeeded":           true, // gated by requireSetupSession
		"backend":               string(plan.Backend),
		"endpointLocked":        s3.EndpointURL != "" && plan.Backend == setup.BackendS3Compatible,
		"awsCredentialsPresent": probe.HasEnvCredentials(),
		"seed": map[string]string{
			"bucket":      s3.Bucket,
			"prefix":      s3.Prefix,
			"region":      s3.Region,
			"profile":     s3.Profile,
			"endpointUrl": s3.EndpointURL,
		},
	})
}

// handleSetupValidate reports whether a draft plan would pass, with no side
// effects. A validation failure is a 200 with ok:false (it's a valid request
// asking "is this configuration valid?"), so the frontend can show it inline.
func (s *Server) handleSetupValidate(w http.ResponseWriter, r *http.Request) {
	var f setupForm
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&f); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	plan := s.buildPlan(f)
	if err := setup.ValidatePlan(plan); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSetupIAMPolicy returns the least-privilege IAM policy JSON for a bucket
// (+ optional prefix), for the wizard's "show me the policy" affordance.
func (s *Server) handleSetupIAMPolicy(w http.ResponseWriter, r *http.Request) {
	bucket := strings.TrimSpace(r.URL.Query().Get("bucket"))
	prefix := strings.TrimSpace(r.URL.Query().Get("prefix"))
	if bucket == "" {
		writeErr(w, http.StatusBadRequest, "bucket is required")
		return
	}
	var buf bytes.Buffer
	if err := setup.WriteIAMPolicy(&buf, bucket, prefix); err != nil {
		writeErr(w, http.StatusBadGateway, "render policy: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"policy": buf.String()})
}
