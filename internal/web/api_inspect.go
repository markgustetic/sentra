package web

import (
	"net/http"

	"github.com/markgustetic/sentra/internal/recoverykit"
	"github.com/markgustetic/sentra/internal/repo"
)

// handleCheck runs a full integrity check and returns the report. It is
// read-only (no op guard) and runs synchronously under the request context, so
// a client disconnect cancels it. CheckReport already carries JSON tags.
func (s *Server) handleCheck(w http.ResponseWriter, r *http.Request) {
	rep, err := s.currentRepo().Check(r.Context(), repo.CheckOptions{})
	if err != nil {
		writeErr(w, http.StatusBadGateway, "integrity check failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// handleDiff compares two snapshots (a and b) and returns the path deltas.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	a := r.URL.Query().Get("a")
	b := r.URL.Query().Get("b")
	if a == "" || b == "" {
		writeErr(w, http.StatusBadRequest, "select two snapshots to compare")
		return
	}
	d, err := s.currentRepo().Diff(r.Context(), a, b)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "diff failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string][]string{
		"added":   orEmpty(d.Added),
		"removed": orEmpty(d.Removed),
		"changed": orEmpty(d.Changed),
	})
}

// handleRecoveryKit renders the disaster-recovery kit as markdown. The kit
// carries how-to-recover metadata (bucket, region, KDF parameters) but NEVER a
// passphrase, wrapped key, or credentials — the same no-secrets invariant the
// CLI/TUI kit honors.
func (s *Server) handleRecoveryKit(w http.ResponseWriter, r *http.Request) {
	k, err := recoverykit.Build(r.Context(), s.currentRepo(), s.currentConfig(), s.deps.ConfigPath)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not build recovery kit: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"markdown": recoverykit.RenderMarkdown(k)})
}

// orEmpty coalesces a nil slice to an empty one so JSON is [] not null.
func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
