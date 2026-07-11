package web

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/markgustetic/sentra/internal/config"
	policycfg "github.com/markgustetic/sentra/internal/policy"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// The schedule surface renders and installs OS scheduler units (launchd on
// darwin, systemd user units on linux) from a named policy's schedule. It is
// filesystem-only — no repo lock — matching the CLI/TUI, which write the units
// and never touch the bucket. A manual-cadence policy has nothing to schedule
// and is rejected before rendering.

func (s *Server) scheduleGOOS() string {
	if s.deps.Schedule.OS != "" {
		return s.deps.Schedule.OS
	}
	return runtime.GOOS
}

func (s *Server) scheduleHome() (string, error) {
	if s.deps.Schedule.HomeDir != nil {
		return s.deps.Schedule.HomeDir()
	}
	return os.UserHomeDir()
}

// scheduleExeHint returns the executable path to bake into the unit, or "" to
// let scheduler.Executable fall back to os.Executable.
func (s *Server) scheduleExeHint() (string, error) {
	if s.deps.Schedule.Executable != nil {
		return s.deps.Schedule.Executable()
	}
	return "", nil
}

// osScheduler is the human label for the target platform's scheduler.
func osScheduler(goos string) string {
	switch goos {
	case "darwin":
		return "launchd"
	case "linux":
		return "systemd"
	default:
		return goos
	}
}

// handleSchedule reports install state per policy, mirroring the TUI table:
// policy, schedule shorthand, cadence, installed, manual.
func (s *Server) handleSchedule(w http.ResponseWriter, _ *http.Request) {
	cfg, ok := s.loadConfigForPolicies(w)
	if !ok {
		return
	}
	goos := s.scheduleGOOS()
	home, err := s.scheduleHome()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve home: "+err.Error())
		return
	}
	names := make([]string, 0, len(cfg.Policies))
	for n := range cfg.Policies {
		names = append(names, n)
	}
	sort.Strings(names)

	type row struct {
		Policy    string `json:"policy"`
		Spec      string `json:"spec"`
		Cadence   string `json:"cadence"`
		Installed bool   `json:"installed"`
		Manual    bool   `json:"manual"`
	}
	out := make([]row, 0, len(names))
	for _, name := range names {
		p := cfg.Policies[name]
		sched := policycfg.NormalizeSchedule(p.Schedule)
		manual := sched.Cadence == policycfg.CadenceManual
		installed := false
		if !manual {
			if paths, err := scheduler.PathsFor(goos, home, name); err == nil {
				installed, _ = scheduler.Installed(paths)
			}
		}
		out = append(out, row{
			Policy:    name,
			Spec:      policycfg.FormatScheduleSpec(p.Schedule),
			Cadence:   sched.Cadence,
			Installed: installed,
			Manual:    manual,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"os": osScheduler(goos), "policies": out})
}

// handleSchedulePreview renders (without installing) the exact unit file(s) that
// installing would write — a capability the TUI lacks. Read-only.
func (s *Server) handleSchedulePreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	p, ok := s.lookupPolicy(w, name)
	if !ok {
		return
	}
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		writeErr(w, http.StatusBadRequest, "policy has a manual schedule — nothing to install")
		return
	}
	files, paths, err := s.renderSchedule(name, p.Schedule)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "render schedule: "+err.Error())
		return
	}
	type file struct {
		Path string `json:"path"`
		Body string `json:"body"`
	}
	list := make([]file, 0, len(paths.Files))
	for _, fp := range paths.Files {
		list = append(list, file{Path: fp, Body: files[fp]})
	}
	writeJSON(w, http.StatusOK, map[string]any{"os": osScheduler(s.scheduleGOOS()), "files": list})
}

// handleScheduleInstall writes the rendered unit(s) to disk (0600). Additive and
// reversible via uninstall, so an explicit confirm:true suffices (no typed word).
func (s *Server) handleScheduleInstall(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.decodeConfirm(w, r) {
		return
	}
	p, ok := s.lookupPolicy(w, name)
	if !ok {
		return
	}
	if policycfg.NormalizeSchedule(p.Schedule).Cadence == policycfg.CadenceManual {
		writeErr(w, http.StatusBadRequest, "policy has a manual schedule — set a cadence first")
		return
	}
	files, paths, err := s.renderSchedule(name, p.Schedule)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "render schedule: "+err.Error())
		return
	}
	if err := scheduler.Install(files); err != nil {
		writeErr(w, http.StatusBadGateway, "install schedule: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"installed": true, "files": paths.Files})
}

// handleScheduleUninstall removes the policy's unit file(s), tolerating absence.
func (s *Server) handleScheduleUninstall(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.decodeConfirm(w, r) {
		return
	}
	home, err := s.scheduleHome()
	if err != nil {
		writeErr(w, http.StatusBadGateway, "resolve home: "+err.Error())
		return
	}
	paths, err := scheduler.PathsFor(s.scheduleGOOS(), home, name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := scheduler.Uninstall(paths); err != nil {
		writeErr(w, http.StatusBadGateway, "uninstall schedule: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"uninstalled": true})
}

// renderSchedule runs PathsFor → Executable → Render for a policy, baking in the
// absolute config path (the unit calls `sentra policy run <name> --config …`).
func (s *Server) renderSchedule(name string, sched config.PolicySchedule) (map[string]string, scheduler.Paths, error) {
	home, err := s.scheduleHome()
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	paths, err := scheduler.PathsFor(s.scheduleGOOS(), home, name)
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	hint, err := s.scheduleExeHint()
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	exe, err := scheduler.Executable(hint)
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	absCfg, err := filepath.Abs(s.deps.ConfigPath)
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	files, err := scheduler.Render(paths, exe, absCfg, name, sched)
	if err != nil {
		return nil, scheduler.Paths{}, err
	}
	return files, paths, nil
}

// lookupPolicy loads the config fresh and returns the named policy, writing a
// 404 if it's absent.
func (s *Server) lookupPolicy(w http.ResponseWriter, name string) (config.PolicyConfig, bool) {
	cfg, ok := s.loadConfigForPolicies(w)
	if !ok {
		return config.PolicyConfig{}, false
	}
	p, found := cfg.Policies[name]
	if !found {
		writeErr(w, http.StatusNotFound, "policy not found: "+name)
		return config.PolicyConfig{}, false
	}
	return p, true
}

// decodeConfirm requires a JSON body of {"confirm": true}. Returns false (after
// writing a 400) when it's missing, so install/uninstall can't fire by accident.
func (s *Server) decodeConfirm(w http.ResponseWriter, r *http.Request) bool {
	var body struct {
		Confirm bool `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || !body.Confirm {
		writeErr(w, http.StatusBadRequest, "confirm required")
		return false
	}
	return true
}
