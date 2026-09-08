package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/scheduler"
)

// startOpFrom returns the startOpMsg cmd yields, looking through a batch,
// and fails the test when there is none. The rule it enforces: a mutation
// reaches the App as a startOpMsg — never as a bare cmd that runs the
// work unserialized and uncancellable, and never synchronously inside
// Update, where a slow launchctl freezes the whole TUI with no esc.
func startOpFrom(t *testing.T, cmd tea.Cmd) startOpMsg {
	t.Helper()
	for _, msg := range execCmds(t, cmd) {
		if start, ok := msg.(startOpMsg); ok {
			return start
		}
	}
	t.Fatal("no startOpMsg emitted — the mutation is not going through the one-op guard")
	return startOpMsg{}
}

// runGuardedOp does what the App does with a startOpMsg: runs it, then
// delivers its result to the view. It returns the updated model and the
// cmd the view answered the result with (a chained start, a modal push,
// a blink).
func runGuardedOp(t *testing.T, m tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	t.Helper()
	start := startOpFrom(t, cmd)
	res := start.run(context.Background())
	if _, ok := res.(opResultMsg); !ok {
		t.Fatalf("op %q returned %T, which does not implement opResultMsg — the guard would never clear", start.name, res)
	}
	return m.Update(res)
}

// TestJobs_TimerAndConfigMutationsGoThroughTheGuard drives every Schedules
// mutation that touches sentra.yaml or the OS scheduler and asserts the
// rule for the class: the confirm emits a startOpMsg with a distinct op
// name, nothing has changed on disk when Update returns, the view shows
// the busy stage, and running the op then delivering its result applies
// the change and returns the view to its list. A second mutation while
// one runs is bounced by opRejectedMsg back to the list with a notice.
func TestJobs_TimerAndConfigMutationsGoThroughTheGuard(t *testing.T) {
	type tc struct {
		name      string
		opName    string
		arm       func(t *testing.T, v JobsView, path string) (JobsView, string) // returns the confirm id
		untouched func(t *testing.T, v JobsView, path string)                    // disk state before the op runs
		applied   func(t *testing.T, v JobsView, path string)                    // disk state after
	}
	alphaPaths := func(v JobsView) scheduler.Paths {
		p, _ := scheduler.PathsFor("darwin", v.homeOverride, "alpha")
		return p
	}
	cases := []tc{
		{
			name: "install", opName: "job-install",
			arm: func(t *testing.T, v JobsView, _ string) (JobsView, string) {
				v.tbl.SetCursor(0)
				return v, jobInstallConfirmID
			},
			untouched: func(t *testing.T, v JobsView, _ string) {
				if installed, _ := scheduler.Installed(alphaPaths(v)); installed {
					t.Fatal("timer files written before the op ran")
				}
			},
			applied: func(t *testing.T, v JobsView, _ string) {
				if installed, _ := scheduler.Installed(alphaPaths(v)); !installed {
					t.Fatal("timer files not written by the op")
				}
			},
		},
		{
			name: "uninstall", opName: "job-uninstall",
			arm: func(t *testing.T, v JobsView, path string) (JobsView, string) {
				installAlphaFiles(t, v, path)
				v.tbl.SetCursor(0)
				return v, jobUninstallConfirmID
			},
			untouched: func(t *testing.T, v JobsView, _ string) {
				if installed, _ := scheduler.Installed(alphaPaths(v)); !installed {
					t.Fatal("timer files removed before the op ran")
				}
			},
			applied: func(t *testing.T, v JobsView, _ string) {
				if installed, _ := scheduler.Installed(alphaPaths(v)); installed {
					t.Fatal("timer files not removed by the op")
				}
			},
		},
		{
			name: "delete", opName: "job-delete",
			arm: func(t *testing.T, v JobsView, path string) (JobsView, string) {
				installAlphaFiles(t, v, path)
				v.tbl.SetCursor(0)
				return v, jobDeleteConfirmID
			},
			untouched: func(t *testing.T, _ JobsView, path string) {
				cfg, _ := config.Load(path)
				if _, ok := cfg.Policies["alpha"]; !ok {
					t.Fatal("policy removed before the op ran")
				}
			},
			applied: func(t *testing.T, v JobsView, path string) {
				cfg, _ := config.Load(path)
				if _, ok := cfg.Policies["alpha"]; ok {
					t.Fatal("policy not removed by the op")
				}
				if installed, _ := scheduler.Installed(alphaPaths(v)); installed {
					t.Fatal("timer files not removed by the op")
				}
			},
		},
		{
			name: "save (edit)", opName: "job-save",
			arm: func(t *testing.T, v JobsView, path string) (JobsView, string) {
				installAlphaFiles(t, v, path)
				v.tbl.SetCursor(0)
				v, _ = pressJobsKey(v, 'e')
				v.form.schedule.SetValue("daily@09:00")
				m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
				return m.(JobsView), jobEditConfirmID
			},
			untouched: func(t *testing.T, _ JobsView, path string) {
				cfg, _ := config.Load(path)
				if cfg.Policies["alpha"].Schedule.At != "03:00" {
					t.Fatal("policy rewritten before the op ran")
				}
			},
			applied: func(t *testing.T, v JobsView, path string) {
				cfg, _ := config.Load(path)
				if cfg.Policies["alpha"].Schedule.At != "09:00" {
					t.Fatal("policy not rewritten by the op")
				}
				body, _ := os.ReadFile(alphaPaths(v).Files[0])
				if !strings.Contains(string(body), "<integer>9</integer>") {
					t.Fatalf("timer not re-rendered by the op:\n%s", body)
				}
			},
		},
		{
			name: "save (add)", opName: "job-save",
			arm: func(t *testing.T, v JobsView, _ string) (JobsView, string) {
				v, _ = pressJobsKey(v, 'a')
				v.form.name.SetValue("gamma")
				v.form.path.SetValue("/data/gamma")
				m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
				return m.(JobsView), jobAddConfirmID
			},
			untouched: func(t *testing.T, _ JobsView, path string) {
				cfg, _ := config.Load(path)
				if _, ok := cfg.Policies["gamma"]; ok {
					t.Fatal("policy written before the op ran")
				}
			},
			applied: func(t *testing.T, _ JobsView, path string) {
				cfg, _ := config.Load(path)
				if _, ok := cfg.Policies["gamma"]; !ok {
					t.Fatal("policy not written by the op")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps, path := jobsDeps(t)
			v := newJobsForTest(t, deps)
			v.osOverride = "darwin"
			v.exeOverride = "/usr/local/bin/sentra"
			v, id := tc.arm(t, v, path)
			v.reload()

			m, cmd := v.Update(confirmedMsg{id: id})
			busy := m.(JobsView)
			tc.untouched(t, busy, path)
			if busy.stage != jobsBusy {
				t.Fatalf("stage = %v, want jobsBusy while the op runs", busy.stage)
			}
			if f := busy.form; f.name.Focused() || f.path.Focused() || f.tags.Focused() || f.schedule.Focused() {
				t.Fatal("a form field is still focused on the busy stage — nothing renders it there")
			}
			start := startOpFrom(t, cmd)
			if start.name != tc.opName {
				t.Fatalf("op name = %q, want %q", start.name, tc.opName)
			}

			// Rejected: back to the list, nothing applied, a notice says why.
			rej, _ := busy.Update(opRejectedMsg{name: tc.opName})
			if r := rej.(JobsView); r.stage == jobsBusy || !strings.Contains(r.notice, "another operation") {
				t.Fatalf("after opRejectedMsg: stage=%v notice=%q", r.stage, r.notice)
			}
			tc.untouched(t, busy, path)

			// Ran: applied, and the view is back on its list.
			done, _ := runGuardedOp(t, busy, cmd)
			d := done.(JobsView)
			tc.applied(t, d, path)
			if d.stage != jobsList {
				t.Fatalf("stage after result = %v, want jobsList", d.stage)
			}
		})
	}
}

// TestJobs_BusyStageIgnoresKeysAndShowsTheOp: while an op runs the view
// renders what it is doing and takes no list/form keys, so a second
// mutation cannot even be armed from here.
func TestJobs_BusyStageIgnoresKeysAndShowsTheOp(t *testing.T) {
	deps, _ := jobsDeps(t)
	v := newJobsForTest(t, deps)
	v.osOverride = "darwin"
	v.exeOverride = "/usr/local/bin/sentra"
	v.tbl.SetCursor(0)
	m, _ := v.Update(confirmedMsg{id: jobInstallConfirmID})
	busy := m.(JobsView)
	if !strings.Contains(busy.View(), "alpha") {
		t.Fatalf("busy view must name the job being worked on:\n%s", busy.View())
	}
	for _, r := range []rune{'a', 'e', 'd', 'i', 'u', 'r'} {
		m, cmd := busy.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		if cmd != nil || m.(JobsView).stage != jobsBusy {
			t.Fatalf("key %q must be ignored while busy", r)
		}
	}
}

// TestBackupWizard_ScheduleInstallGoesThroughTheGuard: confirming a
// scheduled backup used to install the policy and timer synchronously
// inside Update (config write + launchctl) before starting the run. Now
// the install is its own guarded op: nothing lands on disk when Update
// returns, a rejection returns to Confirm, a failure returns to Confirm
// with the error, and success chains into the backup's own startOpMsg
// with the in-memory config mirrored and the done-screen record set.
func TestBackupWizard_ScheduleInstallGoesThroughTheGuard(t *testing.T) {
	newDir := func(t *testing.T) string {
		dir := filepath.Join(t.TempDir(), "docs")
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	t.Run("nothing on disk until the op runs, then the backup starts", func(t *testing.T) {
		v, cfgPath, home := repeatFixture(t)
		v = atDailyConfirm(t, v, newDir(t))
		m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		busy := m.(BackupView)
		if busy.stage != backupInstalling {
			t.Fatalf("stage = %v, want backupInstalling", busy.stage)
		}
		if busy.confirm.tag.Focused() {
			t.Error("leaving Confirm must blur the tag")
		}
		if onDisk, _ := config.Load(cfgPath); len(onDisk.Policies) != 0 {
			t.Fatal("policy written before the op ran")
		}
		if start := startOpFrom(t, cmd); start.name != "backup-schedule" {
			t.Fatalf("op name = %q, want backup-schedule", start.name)
		}

		done, _ := runGuardedOp(t, busy, cmd)
		d := done.(BackupView)
		if onDisk, _ := config.Load(cfgPath); onDisk.Policies["docs"].Schedule.At != "02:00" {
			t.Fatalf("policy not written by the op: %+v", onDisk.Policies)
		}
		if _, err := os.Stat(filepath.Join(home, ".config", "systemd", "user", "sentra-docs.timer")); err != nil {
			t.Errorf("timer not installed by the op: %v", err)
		}
		if _, ok := d.deps.Config.Policies["docs"]; !ok {
			t.Error("in-memory config not mirrored after the op")
		}
		if d.installedName != "docs" || !d.installedNextOK {
			t.Errorf("done-screen record: name=%q nextOK=%v", d.installedName, d.installedNextOK)
		}
		if d.stage != backupRunning {
			t.Fatalf("stage after install = %v, want backupRunning", d.stage)
		}
	})
	t.Run("rejected returns to Confirm", func(t *testing.T) {
		v, _, _ := repeatFixture(t)
		v = atDailyConfirm(t, v, newDir(t))
		m, _ := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m, cmd := m.Update(opRejectedMsg{name: "backup-schedule"})
		got := m.(BackupView)
		if got.stage != backupConfirm || !strings.Contains(got.notice, "another operation") {
			t.Fatalf("stage=%v notice=%q", got.stage, got.notice)
		}
		if !got.confirm.tag.Focused() {
			t.Error("returning to Confirm must re-focus the tag")
		}
		assertBlinkCmd(t, cmd)
	})
	t.Run("install failure returns to Confirm with the error", func(t *testing.T) {
		v, _, _ := repeatFixture(t)
		v.schedGOOS = "plan9"
		v = atDailyConfirm(t, v, newDir(t))
		m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		done, _ := runGuardedOp(t, m, cmd)
		got := done.(BackupView)
		if got.stage != backupConfirm {
			t.Fatalf("stage = %v, want backupConfirm", got.stage)
		}
		if !strings.Contains(got.View(), "could not install the schedule") {
			t.Errorf("view must surface the install error:\n%s", got.View())
		}
	})
}

// TestSettings_MutationsGoThroughTheGuard: the splash toggle and the
// forget-keyring confirm both rewrite sentra.yaml (and the latter talks
// to the OS keyring); they were synchronous config.Update calls inside
// Update. Each now emits a startOpMsg, applies only when it runs, and is
// bounced by opRejectedMsg with the busy flag cleared.
func TestSettings_MutationsGoThroughTheGuard(t *testing.T) {
	t.Run("splash", func(t *testing.T) {
		v, path, cfg := settingsWithConfig(t)
		v = cursorTo(v, entryToggleSplash)
		m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if got, _ := config.Load(path); got.UI.HideSplash || cfg.UI.HideSplash {
			t.Fatal("toggle applied before the op ran")
		}
		if start := startOpFrom(t, cmd); start.name != "settings-splash" {
			t.Fatalf("op name = %q", start.name)
		}
		busy := m.(SettingsView)
		if !busy.busy {
			t.Fatal("view must be busy while the op runs")
		}
		if m2, _ := busy.Update(tea.KeyMsg{Type: tea.KeyEnter}); !m2.(SettingsView).busy {
			t.Fatal("enter while busy must be ignored")
		}
		rej, _ := busy.Update(opRejectedMsg{name: "settings-splash"})
		if r := rej.(SettingsView); r.busy || !strings.Contains(r.err, "another operation") {
			t.Fatalf("after rejection: busy=%v err=%q", r.busy, r.err)
		}
		done, _ := runGuardedOp(t, busy, cmd)
		d := done.(SettingsView)
		if got, _ := config.Load(path); !got.UI.HideSplash || !cfg.UI.HideSplash {
			t.Fatal("toggle not applied by the op")
		}
		if d.busy || !strings.Contains(d.View(), "[off]") {
			t.Fatalf("after the op: busy=%v view:\n%s", d.busy, d.View())
		}
	})
	t.Run("forget keyring", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sentra.yaml")
		cfg := config.Defaults()
		cfg.Repo.S3.Bucket = "b"
		cfg.Passphrase.UseKeyring = true
		if err := config.Write(path, &cfg); err != nil {
			t.Fatal(err)
		}
		deleted := false
		v := NewSettingsView(Deps{Config: &cfg, ConfigPath: path,
			DeleteKeyringPassphrase: func(*config.Config) (bool, error) { deleted = true; return true, nil }})
		m, cmd := v.Update(confirmedMsg{id: settingsForgetConfirmID})
		if deleted {
			t.Fatal("keyring entry deleted before the op ran")
		}
		if start := startOpFrom(t, cmd); start.name != "settings-forget" {
			t.Fatalf("op name = %q", start.name)
		}
		done, _ := runGuardedOp(t, m, cmd)
		if !deleted {
			t.Fatal("op must call the keyring delete seam")
		}
		if got, _ := config.Load(path); got.Passphrase.UseKeyring || cfg.Passphrase.UseKeyring {
			t.Fatal("op must persist and mirror use_keyring: false")
		}
		if done.(SettingsView).busy {
			t.Fatal("busy must clear on the result")
		}
	})
}

// TestApp_TimerInstallRejectedWhileAnotherOpRuns drives the Schedules
// install through the shell with the guard already held: the App must
// refuse the second start, push its error modal, and bounce the view off
// its busy stage via opRejectedMsg — with nothing written to disk.
func TestApp_TimerInstallRejectedWhileAnotherOpRuns(t *testing.T) {
	deps, _ := jobsDeps(t)
	deps.RepoName = "x"
	deps.SchedulerRunner = (&fakeSchedRunner{}).run
	app := NewApp(deps)
	sized, _ := app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app = sized.(App)
	jobs := newJobsForTest(t, deps)
	jobs.osOverride = "darwin"
	jobs.exeOverride = "/usr/local/bin/sentra"
	jobs.tbl.SetCursor(0)
	installView(t, &app, "jobs", jobs)

	m, _ := app.Update(startOpMsg{name: "backup", run: func(context.Context) tea.Msg { return backupDoneMsg{} }})
	app = m.(App)
	m, _ = app.Update(activateMsg{id: "jobs"})
	app = m.(App)
	m, cmd := app.Update(confirmedMsg{id: jobInstallConfirmID})
	app = m.(App)
	for _, msg := range execCmds(t, cmd) {
		if _, tick := msg.(spinner.TickMsg); tick {
			continue
		}
		var next tea.Cmd
		m, next = app.Update(msg)
		app = m.(App)
		for _, msg2 := range execCmds(t, next) {
			m, _ = app.Update(msg2)
			app = m.(App)
		}
	}
	if len(app.modals) == 0 {
		t.Error("the rejected install must push the App's error modal")
	}
	var jv JobsView
	for _, v := range app.views {
		if v.id == "jobs" {
			jv = v.model.(JobsView)
		}
	}
	if jv.stage == jobsBusy || !strings.Contains(jv.notice, "another operation") {
		t.Fatalf("jobs view after rejection: stage=%v notice=%q", jv.stage, jv.notice)
	}
	paths, _ := scheduler.PathsFor("darwin", jobs.homeOverride, "alpha")
	if installed, _ := scheduler.Installed(paths); installed {
		t.Fatal("a rejected install must write nothing")
	}
	if app.opRunning != "backup" {
		t.Errorf("guard must still be held by the first op, opRunning = %q", app.opRunning)
	}
}
