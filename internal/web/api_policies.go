package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
)

// A named policy is a backup job: source paths + tags + a schedule + optional
// post-backup maintenance (check/prune). CRUD is pure config — it rewrites
// sentra.yaml via config.Update and never takes the repo lock. Only "run"
// touches the repo, and only then through the single-op guard.

// policyScheduleDTO / policyDTO give PolicyConfig JSON field names (the config
// struct carries koanf tags only, so a raw marshal would emit Go field names).
type policyScheduleDTO struct {
	Cadence string `json:"cadence"`
	At      string `json:"at"`
	Weekday string `json:"weekday"`
}

type policyDTO struct {
	Name         string            `json:"name"`
	Paths        []string          `json:"paths"`
	Tags         []string          `json:"tags"`
	Schedule     policyScheduleDTO `json:"schedule"`
	ScheduleSpec string            `json:"scheduleSpec"`
	Check        bool              `json:"check"`
	Prune        string            `json:"prune"`
	Valid        bool              `json:"valid"`
	Error        string            `json:"error,omitempty"`
}

func toPolicyDTO(name string, p config.PolicyConfig) policyDTO {
	sched := policycfg.NormalizeSchedule(p.Schedule)
	prune := strings.ToLower(strings.TrimSpace(p.AfterBackup.Prune))
	if prune == "" {
		prune = policycfg.PruneOff
	}
	d := policyDTO{
		Name:         name,
		Paths:        orEmpty(p.Paths),
		Tags:         orEmpty(p.Tags),
		Schedule:     policyScheduleDTO{Cadence: sched.Cadence, At: sched.At, Weekday: sched.Weekday},
		ScheduleSpec: policycfg.FormatScheduleSpec(p.Schedule),
		Check:        p.AfterBackup.Check,
		Prune:        prune,
	}
	if err := policycfg.Validate(name, p); err != nil {
		d.Error = err.Error()
	} else {
		d.Valid = true
	}
	return d
}

// handlePolicies lists every named policy, read fresh from disk (like the TUI's
// reload) so an edit made elsewhere is reflected immediately. An individually
// invalid policy is reported with valid:false rather than failing the list.
func (s *Server) handlePolicies(w http.ResponseWriter, _ *http.Request) {
	cfg, ok := s.loadConfigForPolicies(w)
	if !ok {
		return
	}
	names := make([]string, 0, len(cfg.Policies))
	for n := range cfg.Policies {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]policyDTO, 0, len(names))
	for _, n := range names {
		out = append(out, toPolicyDTO(n, cfg.Policies[n]))
	}
	writeJSON(w, http.StatusOK, out)
}

// errPolicyExists is a sentinel returned from the config.Update closure so a
// duplicate name maps to 409 rather than a generic write failure.
var errPolicyExists = errors.New("policy already exists")
var errPolicyMissing = errors.New("policy not found")

// handlePolicyCreate adds or replaces a named policy. It mirrors `sentra policy
// add`: parse the schedule shorthand, validate, then config.Update with the
// exists-check inside the mutation so the whole thing is one on-disk transaction.
func (s *Server) handlePolicyCreate(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeErr(w, http.StatusBadRequest, "no config path configured")
		return
	}
	var body struct {
		Name         string   `json:"name"`
		Paths        []string `json:"paths"`
		Tags         []string `json:"tags"`
		ScheduleSpec string   `json:"scheduleSpec"`
		Check        bool     `json:"check"`
		Prune        string   `json:"prune"`
		Replace      bool     `json:"replace"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	name := strings.TrimSpace(body.Name)
	spec := strings.TrimSpace(body.ScheduleSpec)
	if spec == "" {
		spec = policycfg.CadenceManual
	}
	sched, err := policycfg.ParseScheduleSpec(spec)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "schedule: "+err.Error())
		return
	}
	prune := strings.TrimSpace(body.Prune)
	if prune == "" {
		prune = policycfg.PruneOff
	}
	p := config.PolicyConfig{
		Paths:       cleanStrings(body.Paths),
		Tags:        cleanStrings(body.Tags),
		Schedule:    sched,
		AfterBackup: config.PolicyAfterBackup{Check: body.Check, Prune: prune},
	}
	if err := policycfg.Validate(name, p); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	err = config.Update(s.deps.ConfigPath, func(cfg *config.Config) error {
		if cfg.Policies == nil {
			cfg.Policies = map[string]config.PolicyConfig{}
		}
		if _, exists := cfg.Policies[name]; exists && !body.Replace {
			return errPolicyExists
		}
		cfg.Policies[name] = p
		return nil
	})
	switch {
	case errors.Is(err, errPolicyExists):
		writeErr(w, http.StatusConflict, "policy "+name+" already exists — set replace to overwrite")
	case err != nil:
		writeErr(w, http.StatusBadGateway, "save config: "+err.Error())
	default:
		writeJSON(w, http.StatusOK, toPolicyDTO(name, p))
	}
}

// handlePolicyDelete removes a named policy. Config-only.
func (s *Server) handlePolicyDelete(w http.ResponseWriter, r *http.Request) {
	if s.deps.ConfigPath == "" {
		writeErr(w, http.StatusBadRequest, "no config path configured")
		return
	}
	name := r.PathValue("name")
	err := config.Update(s.deps.ConfigPath, func(cfg *config.Config) error {
		if _, ok := cfg.Policies[name]; !ok {
			return errPolicyMissing
		}
		delete(cfg.Policies, name)
		return nil
	})
	switch {
	case errors.Is(err, errPolicyMissing):
		writeErr(w, http.StatusNotFound, "policy not found: "+name)
	case err != nil:
		writeErr(w, http.StatusBadGateway, "save config: "+err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
	}
}

// handlePolicyRun executes a policy: snapshot each path (tag policy:<name> plus
// the policy's tags), optionally check, optionally prune. It validates first so
// a corrupt prune mode can't slip into the delete path, and requires a typed
// "run" confirm only when the policy prunes on apply.
func (s *Server) handlePolicyRun(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var body struct {
		Confirm string `json:"confirm"`
	}
	// Body is optional (only prune:apply policies need a confirm word).
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body)

	cfg, ok := s.loadConfigForPolicies(w)
	if !ok {
		return
	}
	p, found := cfg.Policies[name]
	if !found {
		writeErr(w, http.StatusNotFound, "policy not found: "+name)
		return
	}
	if err := policycfg.Validate(name, p); err != nil {
		writeErr(w, http.StatusBadRequest, "policy invalid: "+err.Error())
		return
	}
	pruneMode := strings.ToLower(strings.TrimSpace(p.AfterBackup.Prune))
	if pruneMode == policycfg.PruneApply && body.Confirm != "run" {
		writeErr(w, http.StatusBadRequest, `this policy prunes on apply — type "run" to confirm`)
		return
	}

	tag := policyRunTag(name, p.Tags)
	paths := append([]string(nil), p.Paths...)
	doCheck := p.AfterBackup.Check
	retention := retentionFromConfig(cfg)

	opID, err := s.startOp("policy-run", func(ctx context.Context, rep progress.Reporter, rp *repo.Repo) (any, error) {
		snapshots := 0
		for _, path := range paths {
			if _, err := rp.CreateSnapshot(ctx, path, repo.SnapshotOptions{Tag: tag, Progress: rep}); err != nil {
				return nil, err
			}
			snapshots++
		}
		checked := false
		if doCheck {
			if _, err := rp.Check(ctx, repo.CheckOptions{}); err != nil {
				return nil, err
			}
			checked = true
		}
		pruned := 0
		if pruneMode == policycfg.PruneApply {
			n, err := pruneToRetention(ctx, rp, retention)
			if err != nil {
				return nil, err
			}
			pruned = n
		}
		return map[string]any{"snapshots": snapshots, "checked": checked, "pruned": pruned}, nil
	})
	writeOpStart(w, opID, err)
}

// loadConfigForPolicies reads the config fresh from disk, writing the error
// response and returning ok=false on failure.
func (s *Server) loadConfigForPolicies(w http.ResponseWriter) (*config.Config, bool) {
	if s.deps.ConfigPath == "" {
		writeErr(w, http.StatusBadRequest, "no config path configured")
		return nil, false
	}
	cfg, err := config.Load(s.deps.ConfigPath)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "load config: "+err.Error())
		return nil, false
	}
	return cfg, true
}

// policyRunTag builds the snapshot tag: "policy:<name>" joined with the policy's
// own tags, matching the CLI's policySnapshotTag.
func policyRunTag(name string, tags []string) string {
	return strings.Join(append([]string{"policy:" + name}, tags...), " ")
}

// pruneToRetention applies the global retention policy after a run: delete
// dropped manifests, then GC. It refuses to drop every snapshot (the same
// safety rail as handlePrune). Returns the number of snapshots dropped.
func pruneToRetention(ctx context.Context, rp *repo.Repo, policy repo.RetentionPolicy) (int, error) {
	if policy == (repo.RetentionPolicy{}) {
		return 0, nil
	}
	snaps, err := rp.ListSnapshots(ctx)
	if err != nil {
		return 0, err
	}
	keep, drop := repo.PlanRetention(snaps, policy)
	if len(drop) == 0 {
		return 0, nil
	}
	if len(keep) == 0 {
		return 0, errors.New("retention would drop every snapshot — adjust retention.keep_* so at least one survives")
	}
	for _, id := range drop {
		if err := rp.DeleteSnapshot(ctx, id); err != nil {
			return 0, err
		}
	}
	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	if _, err := rp.GC(ctx, keepIDs); err != nil {
		return 0, err
	}
	return len(drop), nil
}

// cleanStrings trims each element and drops the blanks.
func cleanStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}
