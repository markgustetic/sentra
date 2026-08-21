package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/blobstore"
	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/repo"
)

// connectDeps builds Deps for the gate: an AWS-proper config (no endpoint,
// profile "sentra"), a canned open error, and an OpenRepo stub.
func connectDeps(openRepo func(context.Context) (*repo.Repo, *config.Config, error)) Deps {
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-mg-002"
	cfg.Repo.S3.Profile = "sentra"
	return Deps{
		RepoName:     "sentra-mg-002",
		Config:       &cfg,
		ConnectError: errors.New("login session has expired, please reauthenticate"),
		OpenRepo:     openRepo,
	}
}

// The gate's first frame must tell the operator what failed and exactly
// what pressing l will run — the command is the whole point of the view.
func TestConnect_RendersErrorRetryAndLoginCommand(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	view := v.View()
	for _, want := range []string{
		"login session has expired",
		"retry",
		"aws sso login --profile sentra",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("connect view missing %q:\n%s", want, view)
		}
	}
}

// S3-compatible backends (MinIO, R2) have nothing SSO can fix: the login
// affordance must not render and l must be inert.
func TestConnect_HidesLoginForEndpointBackends(t *testing.T) {
	deps := connectDeps(nil)
	deps.Config.Repo.S3.EndpointURL = "http://127.0.0.1:9000"
	v := NewConnectView(deps)
	if strings.Contains(v.View(), "aws sso login") {
		t.Fatal("login hint rendered for an endpoint backend")
	}
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}})
	if cmd != nil {
		t.Fatal("l produced a command on an endpoint backend")
	}
	if m.(ConnectView).stage != connectIdle {
		t.Fatal("l changed stage on an endpoint backend")
	}
}

// r → OpenRepo succeeds → the gate forwards repoReadyMsg with the live
// repo, exactly like unlock: the App swap is the success path.
func TestConnect_RetrySuccessEmitsRepoReady(t *testing.T) {
	store := blobstore.NewMemory()
	r, err := repo.Init(context.Background(), store, []byte("hunter2"))
	if err != nil {
		t.Fatalf("repo.Init: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("repo.Close: %v", err)
	}
	deps := connectDeps(func(ctx context.Context) (*repo.Repo, *config.Config, error) {
		live, err := repo.Open(ctx, store, []byte("hunter2"))
		if err != nil {
			return nil, nil, err
		}
		cfg := config.Defaults()
		return live, &cfg, nil
	})
	v := NewConnectView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("r did not start an open attempt")
	}
	if m.(ConnectView).stage != connectOpening {
		t.Fatal("r did not enter connectOpening")
	}
	res := cmd() // run the open closure
	m2, fwd := m.(ConnectView).Update(res)
	if fwd == nil {
		t.Fatal("successful open produced no follow-up command")
	}
	ready, ok := fwd().(repoReadyMsg)
	if !ok {
		t.Fatalf("expected repoReadyMsg, got %T", fwd())
	}
	if ready.repo == nil {
		t.Fatal("repoReadyMsg carried a nil repo")
	}
	_ = m2
	_ = ready.repo.Close()
}

// r → OpenRepo fails → the NEW error replaces the old one and the gate
// returns to idle for another attempt.
func TestConnect_RetryFailureShowsFreshError(t *testing.T) {
	deps := connectDeps(func(context.Context) (*repo.Repo, *config.Config, error) {
		return nil, nil, errors.New("dial tcp: network is unreachable")
	})
	v := NewConnectView(deps)
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	res := cmd()
	m2, _ := m.(ConnectView).Update(res)
	got := m2.(ConnectView)
	if got.stage != connectIdle {
		t.Fatal("failed open did not return to idle")
	}
	if !strings.Contains(got.View(), "network is unreachable") {
		t.Fatalf("view does not show the fresh open error:\n%s", got.View())
	}
}

// A successful login child auto-retries the open — the operator should
// not have to press r after authenticating.
func TestConnect_AuthSuccessAutoRetries(t *testing.T) {
	called := false
	deps := connectDeps(func(context.Context) (*repo.Repo, *config.Config, error) {
		called = true
		return nil, nil, errors.New("still failing")
	})
	v := NewConnectView(deps)
	v.stage = connectAuthing // as if ExecProcess just returned
	m, cmd := v.Update(connectAuthDoneMsg{})
	if m.(ConnectView).stage != connectOpening {
		t.Fatal("auth success did not start an open attempt")
	}
	if cmd == nil {
		t.Fatal("auth success produced no open command")
	}
	_ = cmd()
	if !called {
		t.Fatal("auto-retry did not invoke OpenRepo")
	}
}

// A failed login child surfaces ITS error and idles — no auto-retry that
// would just repeat the credential failure.
func TestConnect_AuthFailureShowsAuthError(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	v.stage = connectAuthing
	m, cmd := v.Update(connectAuthDoneMsg{err: errors.New("aws: command not found")})
	got := m.(ConnectView)
	if cmd != nil {
		t.Fatal("failed auth should not auto-retry")
	}
	if got.stage != connectIdle {
		t.Fatal("failed auth did not return to idle")
	}
	if !strings.Contains(got.View(), "aws: command not found") {
		t.Fatalf("view does not show the auth error:\n%s", got.View())
	}
}

// Keys are ignored while an attempt is in flight — one action at a time.
func TestConnect_KeysIgnoredWhileOpening(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	v.stage = connectOpening
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd != nil || m.(ConnectView).stage != connectOpening {
		t.Fatal("r was not ignored while opening")
	}
}

// q quits from idle: startup gates bypass the shell's global quit binding, so
// the connect view owns q. The returned cmd's message must be tea.QuitMsg.
func TestConnect_QQuitsFromIdle(t *testing.T) {
	v := NewConnectView(connectDeps(nil))
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatalf("q's cmd returned %T, expected tea.QuitMsg", cmd())
	}
	_ = m
}
