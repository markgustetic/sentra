package web

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

func scheduleServer(t *testing.T, r *repo.Repo, cfgPath, goos, home string) *Server {
	t.Helper()
	return New(Deps{
		Repo:       r,
		Config:     &config.Config{},
		RepoName:   "test-repo",
		ConfigPath: cfgPath,
		Assets:     Assets,
		Unlock:     func([]byte) (*repo.Repo, error) { return r, nil },
		Schedule: ScheduleEnv{
			OS:         goos,
			HomeDir:    func() (string, error) { return home, nil },
			Executable: func() (string, error) { return "/opt/sentra/bin/sentra", nil },
		},
	})
}

// seedSchedulePolicies writes a daily policy and a manual policy to disk.
func seedSchedulePolicies(t *testing.T, cfgPath string) {
	t.Helper()
	if err := config.Update(cfgPath, func(cfg *config.Config) error {
		cfg.Policies["nightly"] = config.PolicyConfig{
			Paths:    []string{"/tmp/data"},
			Schedule: config.PolicySchedule{Cadence: "daily", At: "02:30"},
		}
		cfg.Policies["adhoc"] = config.PolicyConfig{
			Paths:    []string{"/tmp/other"},
			Schedule: config.PolicySchedule{Cadence: "manual"},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSchedule_StatusListsPoliciesWithManualFlag(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	srv := scheduleServer(t, testRepo(t), cfgPath, "linux", t.TempDir())
	rec := req(t, srv, "GET", "/api/schedule", "", true)
	if rec.Code != 200 {
		t.Fatalf("schedule status = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		OS       string `json:"os"`
		Policies []struct {
			Policy    string `json:"policy"`
			Spec      string `json:"spec"`
			Installed bool   `json:"installed"`
			Manual    bool   `json:"manual"`
		} `json:"policies"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OS != "systemd" {
		t.Errorf("os = %q, want systemd", out.OS)
	}
	byName := map[string]bool{}
	for _, p := range out.Policies {
		byName[p.Policy] = p.Manual
		if p.Installed {
			t.Errorf("%s should not be installed in a fresh HOME", p.Policy)
		}
	}
	if byName["nightly"] != false || byName["adhoc"] != true {
		t.Errorf("manual flags wrong: %+v", byName)
	}
}

func TestSchedule_PreviewDarwinRendersPlist(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	srv := scheduleServer(t, testRepo(t), cfgPath, "darwin", t.TempDir())
	rec := req(t, srv, "GET", "/api/schedule/nightly/preview", "", true)
	if rec.Code != 200 {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		OS    string                        `json:"os"`
		Files []struct{ Path, Body string } `json:"files"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OS != "launchd" || len(out.Files) != 1 {
		t.Fatalf("launchd preview = %+v", out)
	}
	body := out.Files[0].Body
	if !strings.Contains(body, "com.sentra.nightly") || !strings.Contains(body, "StartCalendarInterval") {
		t.Errorf("plist body missing expected content:\n%s", body)
	}
}

func TestSchedule_PreviewLinuxRendersSystemdUnits(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	srv := scheduleServer(t, testRepo(t), cfgPath, "linux", t.TempDir())
	rec := req(t, srv, "GET", "/api/schedule/nightly/preview", "", true)
	if rec.Code != 200 {
		t.Fatalf("preview = %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Files []struct{ Path, Body string } `json:"files"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Files) != 2 {
		t.Fatalf("systemd should render 2 units, got %d", len(out.Files))
	}
	joined := out.Files[0].Body + out.Files[1].Body
	if !strings.Contains(joined, "OnCalendar") || !strings.Contains(joined, "policy") {
		t.Errorf("systemd units missing expected content:\n%s", joined)
	}
}

func TestSchedule_ManualPolicyRejectedFromPreviewAndInstall(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	srv := scheduleServer(t, testRepo(t), cfgPath, "linux", t.TempDir())
	if rec := req(t, srv, "GET", "/api/schedule/adhoc/preview", "", true); rec.Code != 400 {
		t.Errorf("manual preview = %d, want 400", rec.Code)
	}
	if rec := req(t, srv, "POST", "/api/schedule/adhoc/install", `{"confirm":true}`, true); rec.Code != 400 {
		t.Errorf("manual install = %d, want 400", rec.Code)
	}
}

func TestSchedule_InstallNeedsConfirm(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	srv := scheduleServer(t, testRepo(t), cfgPath, "linux", t.TempDir())
	if rec := req(t, srv, "POST", "/api/schedule/nightly/install", `{}`, true); rec.Code != 400 {
		t.Errorf("install without confirm = %d, want 400", rec.Code)
	}
}

func TestSchedule_InstallStatusUninstallRoundTrip(t *testing.T) {
	cfgPath := tempConfigPath(t)
	seedSchedulePolicies(t, cfgPath)
	home := t.TempDir()
	srv := scheduleServer(t, testRepo(t), cfgPath, "linux", home)

	if rec := req(t, srv, "POST", "/api/schedule/nightly/install", `{"confirm":true}`, true); rec.Code != 200 {
		t.Fatalf("install = %d: %s", rec.Code, rec.Body)
	}
	// Status now reports installed.
	rec := req(t, srv, "GET", "/api/schedule", "", true)
	var out struct {
		Policies []struct {
			Policy    string `json:"policy"`
			Installed bool   `json:"installed"`
		} `json:"policies"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	installed := false
	for _, p := range out.Policies {
		if p.Policy == "nightly" {
			installed = p.Installed
		}
	}
	if !installed {
		t.Fatalf("nightly not reported installed after install: %+v", out)
	}
	// Files exist on disk under the temp HOME.
	if entries, _ := os.ReadDir(home); len(entries) == 0 {
		t.Error("install wrote nothing under HOME")
	}
	// Uninstall clears it.
	if rec := req(t, srv, "POST", "/api/schedule/nightly/uninstall", `{"confirm":true}`, true); rec.Code != 200 {
		t.Fatalf("uninstall = %d: %s", rec.Code, rec.Body)
	}
	rec = req(t, srv, "GET", "/api/schedule", "", true)
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, p := range out.Policies {
		if p.Policy == "nightly" && p.Installed {
			t.Error("nightly still installed after uninstall")
		}
	}
}
