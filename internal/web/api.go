package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/markgustetic/sentra/internal/repo"
)

// unlockErrMessage maps repo.Open failures to operator-readable text without
// leaking crypto detail. A wrong passphrase and a tampered config both present
// as "wrong passphrase" — the honest, non-oracle answer.
func unlockErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrWrongPassphrase), errors.Is(err, repo.ErrConfigTampered):
		return "wrong passphrase"
	default:
		return "could not open the repository: " + err.Error()
	}
}

// snapshotDTO is the JSON shape the frontend renders. It flattens Stats so the
// client needn't know the repo's nesting.
type snapshotDTO struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
	Tag       string `json:"tag"`
	Files     int    `json:"files"`
	Bytes     int64  `json:"bytes"`
	NewBytes  int64  `json:"newBytes"`
}

func toDTO(s repo.SnapshotInfo) snapshotDTO {
	return snapshotDTO{
		ID:        s.ID,
		CreatedAt: s.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		Tag:       s.Tag,
		Files:     s.Stats.Files,
		Bytes:     s.Stats.Bytes,
		NewBytes:  s.Stats.NewBytes,
	}
}

// handleDashboard returns repo summary stats: snapshot count, total plaintext
// bytes, and the most-recent snapshot. Failures are surfaced, not swallowed —
// unlike the TUI's best-effort dashboard, the web client can show an error.
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.currentRepo().ListSnapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list snapshots: "+err.Error())
		return
	}
	var total int64
	for _, sn := range snaps {
		total += sn.Stats.Bytes
	}
	out := map[string]any{
		"snapshotCount": len(snaps),
		"totalBytes":    total,
		"recCount":      0, // agent recommendations arrive in a later phase
	}
	if len(snaps) > 0 {
		last := toDTO(snaps[0]) // ListSnapshots returns newest-first
		out["lastSnapshot"] = last
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSnapshots returns the full list, newest first.
func (s *Server) handleSnapshots(w http.ResponseWriter, r *http.Request) {
	snaps, err := s.currentRepo().ListSnapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list snapshots: "+err.Error())
		return
	}
	out := make([]snapshotDTO, 0, len(snaps))
	for _, sn := range snaps {
		out = append(out, toDTO(sn))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSnapshotDetail loads one manifest and returns its file tree.
func (s *Server) handleSnapshotDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	man, err := s.currentRepo().LoadSnapshot(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusNotFound, "snapshot not found: "+err.Error())
		return
	}
	type fileDTO struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		Mode string `json:"mode"`
	}
	files := make([]fileDTO, 0, len(man.Tree))
	for _, f := range man.Tree {
		files = append(files, fileDTO{Path: f.Path, Size: f.Size, Mode: f.Mode.String()})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":        man.ID,
		"createdAt": man.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		"tag":       man.Tag,
		"root":      man.Root,
		"files":     files,
		"stats": map[string]any{
			"files": man.Stats.Files,
			"bytes": man.Stats.Bytes,
		},
	})
}

// handleFS lists subdirectories for the backup folder picker. Directories only;
// symlinks to directories are followed via Stat. It never reads file contents.
func (s *Server) handleFS(w http.ResponseWriter, r *http.Request) {
	dir := r.URL.Query().Get("path")
	if strings.TrimSpace(dir) == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = home
		} else {
			dir = string(filepath.Separator)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid path")
		return
	}
	dir = abs

	out := map[string]any{"cwd": dir, "dirs": []string{}}
	if parent := filepath.Dir(dir); parent != dir {
		out["parent"] = parent
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		out["error"] = "cannot read " + dir
		writeJSON(w, http.StatusOK, out) // still navigable via parent
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
			continue
		}
		if e.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && info.IsDir() {
				names = append(names, e.Name())
			}
		}
	}
	sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	out["dirs"] = names
	writeJSON(w, http.StatusOK, out)
}
