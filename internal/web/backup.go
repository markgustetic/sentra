package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"

	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/walker"
)

// handleBackupStart validates the folder and launches CreateSnapshot as a
// streaming op. The client opens /api/backup/{opId}/events (SSE) to watch it.
func (s *Server) handleBackupStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root string `json:"root"`
		Tag  string `json:"tag"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	root := strings.TrimSpace(body.Root)
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "directory not found: "+root)
		return
	}
	tag := strings.TrimSpace(body.Tag)

	var wopts walker.Options
	if s.currentConfig() != nil {
		wopts = walker.Options{IgnoreFile: s.currentConfig().Backup.IgnoreFile, ExcludeCaches: s.currentConfig().Backup.ExcludeCaches}
	}

	opID, err := s.startOp("backup", func(ctx context.Context, rep progress.Reporter, rp *repo.Repo) (any, error) {
		info, err := rp.CreateSnapshot(ctx, root, repo.SnapshotOptions{Tag: tag, Progress: rep, Walker: wopts})
		if err != nil {
			return nil, err
		}
		dto := toDTO(info)
		return map[string]any{"snapshot": &dto}, nil
	})
	writeOpStart(w, opID, err)
}
