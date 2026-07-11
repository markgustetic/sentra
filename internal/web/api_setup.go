package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
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
	// Pass the SUBMITTED profile as the "configured" one: the web driver rebuilds
	// the plan from scratch on every request, so a value in the profile field is
	// always operator-typed, never DefaultPlan's inference. Passing the (empty)
	// seed profile would let ApplyBackendChoice clear a profile the user wrote for
	// an S3-compatible target (R2/Wasabi creds live in a named profile) — the
	// invariant CLAUDE.md guards.
	setup.ApplyBackendChoice(&plan, backend, f.Profile)

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

// setupPlanBase returns the base config the wizard's default plan is built from.
// It always starts from config.Defaults() so retention (and other) defaults
// survive, then overlays the seed's non-secret S3 coordinates (e.g. `sentra
// local`'s MinIO endpoint/bucket). Overlaying only Repo.S3 keeps the seed to its
// documented job — coordinates, not policy — so a bare seed can't wipe defaults.
func (s *Server) setupPlanBase() config.Config {
	base := config.Defaults()
	if seed := s.deps.SetupSeedConfig; seed != nil {
		base.Repo.S3 = seed.Repo.S3
	}
	return base
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

// setupEffects returns the setup engine's side-effect seam, defaulting to the
// production effects when none was injected.
func (s *Server) setupEffects() setup.Effects {
	if s.deps.SetupEffects != nil {
		return s.deps.SetupEffects
	}
	return setup.DefaultEffects()
}

// handleSetupApply provisions the repository from the wizard's final submission
// and streams progress. It builds and validates the plan, then runs the shared
// engine — WriteDraft → PrepareAWS → WriteConfig → InitRepo → RemoveDraft — as a
// setup op, emitting a step marker per stage. On success it re-opens the repo
// and transitions the server to unlocked in place; the passphrase is used once
// and zeroized. AWS uses ambient credentials only; no secret is ever written.
func (s *Server) handleSetupApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		setupForm
		Passphrase     string `json:"passphrase"`
		SavePassphrase bool   `json:"savePassphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	plan := s.buildPlan(body.setupForm)
	plan.SavePassphrase = body.SavePassphrase
	setup.ApplyPassphraseConfig(&plan)

	pass := []byte(body.Passphrase)
	body.Passphrase = ""

	if err := setup.ValidatePlan(plan); err != nil {
		crypto.Zeroize(pass)
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// The web wizard has no config-only mode and no way to resume initialization
	// later, so a config-written-but-repo-less server would be a dead end (setup
	// 409s, unlock can't init). Refuse it rather than wedge.
	if !plan.InitRepo {
		crypto.Zeroize(pass)
		writeErr(w, http.StatusBadRequest, "the web setup wizard always initializes the repository")
		return
	}
	if plan.InitRepo && len(pass) < minPasswordLen {
		crypto.Zeroize(pass)
		writeErr(w, http.StatusBadRequest, "passphrase must be at least 8 characters")
		return
	}

	cfgPath := s.deps.ConfigPath
	opID, err := s.startSetupOp(func(ctx context.Context, emit func(string)) (any, error) {
		defer crypto.Zeroize(pass) // the op owns the passphrase's lifetime now
		return s.runSetup(ctx, emit, cfgPath, &plan, pass)
	})
	if err != nil {
		crypto.Zeroize(pass)
	}
	writeOpStart(w, opID, err)
}

// runSetup drives the engine steps and, on success, unlocks the server. Step
// markers ("bucket-created", "public-blocked", "encrypted", "repo-initialized")
// stream over SSE; the done payload carries the repo id and a no-secrets summary.
func (s *Server) runSetup(ctx context.Context, emit func(string), cfgPath string, plan *setup.Plan, pass []byte) (any, error) {
	eng := setup.NewEngine(s.setupEffects())
	if err := eng.WriteDraft(cfgPath, &plan.Config); err != nil {
		return nil, fmt.Errorf("write draft: %w", err)
	}
	var auth setup.AWSAuthReport
	var prep setup.AWSPrepareReport
	if plan.PrepareAWS {
		a, p, err := eng.PrepareAWS(ctx, plan)
		if err != nil {
			return nil, enrichAWSError(err, plan.Config)
		}
		auth, prep = a, p
		if prep.BucketCreated {
			emit("bucket-created")
		}
		if prep.PublicAccessBlocked {
			emit("public-blocked")
		}
		if prep.DefaultEncryptionEnabled {
			emit("encrypted")
		}
	}
	if err := eng.WriteConfig(cfgPath, plan); err != nil {
		return nil, fmt.Errorf("write config: %w", err)
	}
	var initRes setup.InitResult
	if plan.InitRepo {
		res, err := eng.InitRepo(ctx, &plan.Config, pass, plan.SavePassphrase)
		if err != nil {
			return nil, fmt.Errorf("initialize repository: %w", err)
		}
		initRes = res
		emit("repo-initialized")
	}
	eng.RemoveDraft(cfgPath)

	if err := s.completeSetup(ctx, plan, pass); err != nil {
		// A pre-existing repo that this passphrase doesn't open is the common
		// case (e.g. re-running setup against a bucket that already holds one).
		// Say so plainly instead of a bare "wrong passphrase".
		if initRes.AlreadyInitialized &&
			(errors.Is(err, repo.ErrWrongPassphrase) || errors.Is(err, repo.ErrConfigTampered)) {
			return nil, errors.New("this bucket already contains a Sentra repository — enter its existing passphrase to use it, or choose a different bucket or prefix")
		}
		return nil, fmt.Errorf("open repository after setup: %w", err)
	}
	return map[string]any{
		"repoId":  initRes.RepoID,
		"summary": setup.SummaryLines(cfgPath, *plan, &auth, &prep, &initRes),
	}, nil
}

// completeSetup swaps the server from setup mode to unlocked in place: it
// re-opens the just-initialized repo (the engine's InitRepo closes the handle it
// opened), then publishes the new repo/config/label and a fresh unlock closure
// so a later lock→unlock uses the config setup just wrote.
func (s *Server) completeSetup(ctx context.Context, plan *setup.Plan, pass []byte) error {
	newCfg := &plan.Config
	name := newCfg.Repo.S3.Bucket
	if name == "" {
		name = "sentra"
	}

	var opened *repo.Repo
	if plan.InitRepo {
		store, err := s.setupEffects().NewStore(ctx, newCfg)
		if err != nil {
			return err
		}
		r, err := repo.Open(ctx, store, pass)
		if err != nil {
			return err
		}
		opened = r
	}

	unlock := func(p []byte) (*repo.Repo, error) {
		store, err := s.setupEffects().NewStore(context.Background(), newCfg)
		if err != nil {
			return nil, err
		}
		return repo.Open(context.Background(), store, p)
	}

	s.mu.Lock()
	s.repo = opened
	s.cfg = newCfg
	s.name = name
	s.setupNeeded = false
	s.unlockFn = unlock
	s.mu.Unlock()
	return nil
}

// enrichAWSError folds setup.ErrorAdvice lines into the error message so the
// browser shows the operator-facing hints (e.g. "run `aws sso login`") inline.
func enrichAWSError(err error, cfg config.Config) error {
	advice := setup.ErrorAdvice(err, cfg)
	if len(advice) == 0 {
		return err
	}
	return fmt.Errorf("%s\n%s", err.Error(), strings.Join(advice, "\n"))
}
