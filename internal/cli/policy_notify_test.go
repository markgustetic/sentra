package cli

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/markgustetic/sentra/internal/config"
)

// notifyRecorder stands in for notify.ExecRunner: it records each
// notification's trailing argv (title, subtitle, body), which is the
// message on every supported platform.
type notifyRecorder struct{ bodies []string }

func (n *notifyRecorder) run(_ context.Context, _ string, args ...string) ([]byte, error) {
	n.bodies = append(n.bodies, strings.Join(args[len(args)-2:], " | "))
	return nil, nil
}

// TestPolicyRun_NotifiesOnSuccess: a timer run has no terminal, so the
// desktop notification is how the operator learns it happened. On by
// default — the fixture's config has no notify: section.
func TestPolicyRun_NotifiesOnSuccess(t *testing.T) {
	deps, _, _ := policyHookFixture(t, func(string) config.PolicyHooks { return config.PolicyHooks{} })
	rec := &notifyRecorder{}
	deps.Notify = rec.run
	if err := runPolicyWith(t, deps, context.Background(), "run", "hooked"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("want one notification, got %q", rec.bodies)
	}
	if !strings.HasPrefix(rec.bodies[0], "Backup complete | hooked: 1 files") {
		t.Errorf("notification = %q, want the complete summary naming the policy", rec.bodies[0])
	}
}

func TestPolicyRun_NotifiesOnFailure(t *testing.T) {
	deps, _, _ := policyHookFixture(t, func(string) config.PolicyHooks {
		return config.PolicyHooks{Before: "exit 7"}
	})
	rec := &notifyRecorder{}
	deps.Notify = rec.run
	if err := runPolicyWith(t, deps, context.Background(), "run", "hooked"); err == nil {
		t.Fatal("failing before hook must fail the run")
	}
	if len(rec.bodies) != 1 || !strings.HasPrefix(rec.bodies[0], "Backup failed | hooked: ") {
		t.Errorf("notification = %q, want a failure naming the policy", rec.bodies)
	}
}

// A run that never started owes no notification: neither a not-due
// --if-due launch (the login-time no-op would otherwise notify at every
// login) nor a config-shape error.
func TestPolicyRun_NoNotificationWhenNotDueOrUnknown(t *testing.T) {
	deps, _, _ := policyDueFixture(t, time.Now)
	rec := &notifyRecorder{}
	deps.Notify = rec.run
	if err := runPolicyWith(t, deps, context.Background(), "run", "home"); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("seed run must notify once, got %q", rec.bodies)
	}
	if err := runPolicyWith(t, deps, context.Background(), "run", "home", "--if-due"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if err := runPolicyWith(t, deps, context.Background(), "run", "nonesuch"); err == nil {
		t.Fatal("unknown policy must fail")
	}
	if len(rec.bodies) != 1 {
		t.Fatalf("not-due and unknown-policy runs must not notify, got %q", rec.bodies)
	}
}

func TestPolicyRun_DisableDesktopSilencesIt(t *testing.T) {
	deps, _, _ := policyHookFixture(t, func(string) config.PolicyHooks { return config.PolicyHooks{} })
	rec := &notifyRecorder{}
	deps.Notify = rec.run
	err := config.Update("sentra.yaml", func(cfg *config.Config) error {
		cfg.Notify.DisableDesktop = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := runPolicyWith(t, deps, context.Background(), "run", "hooked"); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(rec.bodies) != 0 {
		t.Fatalf("disable_desktop must silence the run, got %q", rec.bodies)
	}
}

// TestProductionWiresNotifyRunner guards the wiring the way the config-path
// walker guards every command: a nil notify.Runner is OFF by design, so a
// Deps literal that forgets it loses the feature with no test failing.
func TestProductionWiresNotifyRunner(t *testing.T) {
	for _, tc := range []struct {
		file string
		want int
	}{
		{filepath.Join("..", "..", "cmd", "sentra", "commands.go"), 1},
		{"ui.go", 3},
	} {
		body, err := os.ReadFile(tc.file)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(wiredNotify.FindAll(body, -1)); got != tc.want {
			t.Errorf("%s: %d Deps literals wire notify.ExecRunner, want %d", tc.file, got, tc.want)
		}
	}
}

// wiredNotify tolerates gofmt's column alignment inside the literal.
var wiredNotify = regexp.MustCompile(`Notify:\s+notify\.ExecRunner,`)
