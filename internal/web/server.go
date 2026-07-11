// Package web serves the sentra browser UI: a thin HTTP adapter over the same
// internal/repo core the CLI and TUI use, plus an embedded synthwave frontend.
// It adds NO crypto or storage logic of its own.
//
// Security posture (this is a backup tool holding an unlocked repo):
//   - the listener is bound to 127.0.0.1 only (see internal/cli/web.go); there
//     is deliberately no way to bind elsewhere;
//   - every request passes a Host/Origin allow-list, defeating DNS-rebinding;
//   - a random per-run session token is set as a SameSite=Strict, HttpOnly
//     cookie on the shell page and required on every /api call that touches the
//     repo, so a cross-origin page cannot drive the API;
//   - the passphrase is POSTed once over loopback, used to open the repo, then
//     zeroized — never logged, written, or returned.
package web

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"strings"
	"sync"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/repo"
)

// sessionCookie names the cookie carrying the per-run session token.
const sessionCookie = "sentra_session"

// Deps wires the server's collaborators so it stays testable: an already-opened
// repo (or nil to start locked) and an Unlock closure the command builds from
// the config's store factory + passphrase resolver.
type Deps struct {
	// Repo, when non-nil, means the command already resolved the passphrase
	// (keyring / env / --passphrase-file) and the browser lands unlocked.
	Repo *repo.Repo
	// Config is the resolved config (for repo name / display only — never
	// serialized back with secrets).
	Config *config.Config
	// RepoName is the human label shown in the chrome (bucket or config name).
	RepoName string
	// ConfigPath is the sentra.yaml path, needed to render the recovery kit's
	// "where your config lives" guidance. Display only — never its contents.
	ConfigPath string
	// Unlock opens the repo from a passphrase typed in the browser. It owns the
	// passphrase bytes' lifetime beyond the call is the caller's concern; this
	// server zeroizes the copy it received. Nil is allowed only when Repo is set.
	Unlock func(passphrase []byte) (*repo.Repo, error)
	// Assets is the embedded frontend (index.html, app.css, app.js, images).
	Assets fs.FS
}

// Server holds the session and the routes. All mutable state is guarded by mu
// because net/http dispatches handlers concurrently.
type Server struct {
	deps  Deps
	token string

	mu        sync.Mutex
	repo      *repo.Repo // nil ⇒ locked
	opRunning string     // "" when idle; one mutating op at a time
	ops       map[string]*backupOp

	mux *http.ServeMux
}

// New builds the server and its routes. It panics only on a failure to read
// crypto/rand, which is fatal and unrecoverable.
func New(deps Deps) *Server {
	tok := make([]byte, 32)
	if _, err := rand.Read(tok); err != nil {
		panic("web: cannot read crypto/rand: " + err.Error())
	}
	s := &Server{
		deps:  deps,
		token: hex.EncodeToString(tok),
		repo:  deps.Repo,
		ops:   map[string]*backupOp{},
	}
	s.routes()
	return s
}

// Handler exposes the router for httptest and for the listening command.
func (s *Server) Handler() http.Handler { return s.originGuard(s.mux) }

func (s *Server) routes() {
	m := http.NewServeMux()

	// Frontend shell + assets (no session required — the shell IS what sets it).
	m.HandleFunc("GET /", s.handleShell)
	m.Handle("GET /assets/", http.StripPrefix("/assets/", http.FileServerFS(s.deps.Assets)))

	// Auth (no session-repo requirement).
	m.HandleFunc("GET /api/session", s.handleSession)
	m.HandleFunc("POST /api/unlock", s.handleUnlock)
	m.HandleFunc("POST /api/lock", s.requireSession(s.handleLock))

	// Repo-backed data (session + unlocked required).
	m.HandleFunc("GET /api/dashboard", s.requireSession(s.handleDashboard))
	m.HandleFunc("GET /api/snapshots", s.requireSession(s.handleSnapshots))
	m.HandleFunc("GET /api/snapshots/{id}", s.requireSession(s.handleSnapshotDetail))
	m.HandleFunc("GET /api/fs", s.requireSession(s.handleFS))
	m.HandleFunc("POST /api/backup", s.requireSession(s.handleBackupStart))
	m.HandleFunc("GET /api/backup/{id}/events", s.requireSession(s.handleBackupEvents))

	// Inspect surfaces (Phase 2) — read-only.
	m.HandleFunc("GET /api/check", s.requireSession(s.handleCheck))
	m.HandleFunc("GET /api/diff", s.requireSession(s.handleDiff))
	m.HandleFunc("GET /api/recovery-kit", s.requireSession(s.handleRecoveryKit))

	s.mux = m
}

// originGuard rejects any request whose Host is not loopback, or whose Origin
// (when present) does not match — the anti-DNS-rebinding gate. It wraps the whole
// mux so no route can skip it.
func (s *Server) originGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		if o := r.Header.Get("Origin"); o != "" && !originMatchesHost(o, r.Host) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether host's name is a loopback name. The port is
// ignored; only the name matters for the rebinding defense.
func loopbackHost(host string) bool {
	name := host
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		name = host[:i]
	}
	name = strings.Trim(name, "[]") // strip IPv6 brackets
	switch name {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// originMatchesHost reports whether an Origin header's host:port equals the
// request Host. Origin is "scheme://host[:port]".
func originMatchesHost(origin, host string) bool {
	if i := strings.Index(origin, "://"); i >= 0 {
		origin = origin[i+3:]
	}
	return origin == host
}

// requireSession enforces a valid session cookie and an unlocked repo before
// handing off. It returns the open repo to the handler via the request context
// would be cleaner, but a method closure keeps the handlers plain.
func (s *Server) requireSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(sessionCookie)
		if err != nil || subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, "no session")
			return
		}
		s.mu.Lock()
		unlocked := s.repo != nil
		s.mu.Unlock()
		if !unlocked {
			writeErr(w, http.StatusUnauthorized, "repository is locked")
			return
		}
		next(w, r)
	}
}

// currentRepo returns the open repo or nil under the lock.
func (s *Server) currentRepo() *repo.Repo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repo
}

// setSessionCookie stamps the session token so the frontend's later /api calls
// carry it. SameSite=Strict means a cross-origin page never sends it.
func (s *Server) setSessionCookie(w http.ResponseWriter) {
	// Secure is intentionally omitted: this is a plain-HTTP loopback server, and a
	// Secure cookie would never be set or sent over http://127.0.0.1. HttpOnly +
	// SameSite=Strict (plus the Host/Origin guard) carry the CSRF/session defense.
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // G124: loopback HTTP; see comment
		Name:     sessionCookie,
		Value:    s.token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
}

// handleShell serves index.html and sets the session cookie so the app's fetches
// are authorized. It is the one page that establishes the session.
func (s *Server) handleShell(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s.setSessionCookie(w)
	b, err := fs.ReadFile(s.deps.Assets, "index.html")
	if err != nil {
		http.Error(w, "index.html missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

// handleSession reports lock state and the repo label. No auth: the frontend
// calls it first to decide whether to show the unlock gate.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"locked":   s.currentRepo() == nil,
		"repoName": s.deps.RepoName,
	})
}

// handleUnlock opens the repo from a browser-typed passphrase, then zeroizes it.
func (s *Server) handleUnlock(w http.ResponseWriter, r *http.Request) {
	if s.currentRepo() != nil {
		writeJSON(w, http.StatusOK, map[string]any{"locked": false, "repoName": s.deps.RepoName})
		return
	}
	var body struct {
		Passphrase string `json:"passphrase"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	pass := []byte(body.Passphrase)
	body.Passphrase = ""
	defer crypto.Zeroize(pass)
	if len(pass) == 0 {
		writeErr(w, http.StatusBadRequest, "enter the repository passphrase")
		return
	}
	if s.deps.Unlock == nil {
		writeErr(w, http.StatusInternalServerError, "no unlock path configured")
		return
	}
	r2, err := s.deps.Unlock(pass)
	if err != nil {
		writeErr(w, http.StatusUnauthorized, unlockErrMessage(err))
		return
	}
	s.mu.Lock()
	s.repo = r2
	s.mu.Unlock()
	s.setSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]any{"locked": false, "repoName": s.deps.RepoName})
}

// handleLock closes the repo and drops it from the session.
func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	prev := s.repo
	s.repo = nil
	s.mu.Unlock()
	if prev != nil {
		_ = prev.Close()
	}
	writeJSON(w, http.StatusOK, map[string]any{"locked": true})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
