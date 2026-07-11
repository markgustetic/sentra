package web

import (
	"net/http"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/setup"
)

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
