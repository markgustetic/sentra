package web

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/crypto"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
)

// minPasswordLen mirrors the CLI/TUI floor on a new passphrase.
const minPasswordLen = 8

// handleRestoreStart restores a snapshot to a destination directory as a
// streaming op. Restore writes files, so it requires a typed "restore" confirm.
func (s *Server) handleRestoreStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		SnapshotID string `json:"snapshotId"`
		Dest       string `json:"dest"`
		Confirm    string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Confirm != "restore" {
		writeErr(w, http.StatusBadRequest, `type "restore" to confirm`)
		return
	}
	id := strings.TrimSpace(body.SnapshotID)
	dest := strings.TrimSpace(body.Dest)
	if id == "" || dest == "" {
		writeErr(w, http.StatusBadRequest, "snapshot and destination are required")
		return
	}
	opID, err := s.startOp("restore", func(ctx context.Context, rep progress.Reporter, rp *repo.Repo) (any, error) {
		if err := rp.Restore(ctx, id, dest, repo.RestoreOptions{Progress: rep}); err != nil {
			return nil, err
		}
		return map[string]any{"restored": true, "dest": dest}, nil
	})
	writeOpStart(w, opID, err)
}

// retentionFromConfig maps the config's retention block onto repo.RetentionPolicy.
func retentionFromConfig(cfg *config.Config) repo.RetentionPolicy {
	if cfg == nil {
		return repo.RetentionPolicy{}
	}
	return repo.RetentionPolicy{
		KeepLast:    cfg.Retention.KeepLast,
		KeepDaily:   cfg.Retention.KeepDaily,
		KeepWeekly:  cfg.Retention.KeepWeekly,
		KeepMonthly: cfg.Retention.KeepMonthly,
	}
}

// handlePrunePreview returns which snapshots retention would keep vs drop, with
// reasons. Read-only — safe to call before the destructive apply.
func (s *Server) handlePrunePreview(w http.ResponseWriter, r *http.Request) {
	policy := retentionFromConfig(s.currentConfig())
	if policy == (repo.RetentionPolicy{}) {
		writeErr(w, http.StatusBadRequest, "no retention policy configured — set retention.keep_* in sentra.yaml first")
		return
	}
	snaps, err := s.currentRepo().ListSnapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "could not list snapshots: "+err.Error())
		return
	}
	type row struct {
		ID        string   `json:"id"`
		Tag       string   `json:"tag"`
		CreatedAt string   `json:"createdAt"`
		Keep      bool     `json:"keep"`
		Reasons   []string `json:"reasons"`
	}
	decisions := repo.PlanRetentionExplain(snaps, policy)
	out := make([]row, 0, len(decisions))
	drop := 0
	for _, d := range decisions {
		if !d.Keep {
			drop++
		}
		out = append(out, row{
			ID:        d.Snapshot.ID,
			Tag:       d.Snapshot.Tag,
			CreatedAt: d.Snapshot.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			Keep:      d.Keep,
			Reasons:   d.Reasons,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy, "dropCount": drop, "decisions": out})
}

// handlePrune applies retention: GC every blob not referenced by a kept
// snapshot. Requires a typed "prune" confirm. Runs synchronously under the
// single-op guard (GC also takes the repo's own advisory lock).
func (s *Server) handlePrune(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Confirm string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Confirm != "prune" {
		writeErr(w, http.StatusBadRequest, `type "prune" to confirm`)
		return
	}
	policy := retentionFromConfig(s.currentConfig())
	if policy == (repo.RetentionPolicy{}) {
		writeErr(w, http.StatusBadRequest, "no retention policy configured")
		return
	}
	rp, ok := s.takeOp(w, "prune")
	if !ok {
		return
	}
	defer s.releaseOp()

	snaps, err := rp.ListSnapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list snapshots: "+err.Error())
		return
	}
	keep, drop := repo.PlanRetention(snaps, policy)
	if len(drop) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"droppedSnapshots": 0, "deletedBlobs": 0, "deletedBytes": 0, "liveBlobs": 0})
		return
	}
	if len(keep) == 0 {
		// Safety rail (mirrors the CLI's --all requirement): never let retention
		// silently wipe every snapshot from the web UI.
		writeErr(w, http.StatusBadRequest, "retention would drop every snapshot — adjust retention.keep_* so at least one survives")
		return
	}
	// Delete the dropped manifests first, then GC reclaims their now-orphaned
	// blobs. keepIDs is passed non-nil so GC treats this as a deliberate prune.
	for _, id := range drop {
		if err := rp.DeleteSnapshot(r.Context(), id); err != nil {
			writeErr(w, http.StatusBadGateway, "delete snapshot: "+err.Error())
			return
		}
	}
	keepIDs := make(map[string]bool, len(keep))
	for _, id := range keep {
		keepIDs[id] = true
	}
	stats, err := rp.GC(r.Context(), keepIDs)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "prune (gc) failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"droppedSnapshots": len(drop),
		"liveBlobs":        stats.LiveBlobs, "deletedBlobs": stats.DeletedBlobs, "deletedBytes": stats.DeletedBytes,
	})
}

// handlePassword rotates the repository passphrase. Destructive and
// irreversible (the old passphrase stops working, snapshots stay readable), so
// it needs a typed "rotate" confirm plus a new + matching passphrase. The secret
// is zeroized after use and never logged or returned.
func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		NewPassphrase     string `json:"newPassphrase"`
		ConfirmPassphrase string `json:"confirmPassphrase"`
		Confirm           string `json:"confirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	newPass := []byte(body.NewPassphrase)
	conf := []byte(body.ConfirmPassphrase)
	body.NewPassphrase, body.ConfirmPassphrase = "", ""
	defer crypto.Zeroize(newPass)
	defer crypto.Zeroize(conf)

	if body.Confirm != "rotate" {
		writeErr(w, http.StatusBadRequest, `type "rotate" to confirm`)
		return
	}
	if len(newPass) < minPasswordLen {
		writeErr(w, http.StatusBadRequest, "passphrase must be at least 8 characters")
		return
	}
	if subtle.ConstantTimeCompare(newPass, conf) != 1 {
		writeErr(w, http.StatusBadRequest, "passphrases do not match")
		return
	}
	rp, ok := s.takeOp(w, "password")
	if !ok {
		return
	}
	defer s.releaseOp()

	if err := rp.Passwd(r.Context(), newPass); err != nil {
		writeErr(w, http.StatusBadGateway, passwdErrMessage(err))
		return
	}
	if s.currentConfig() != nil && s.currentConfig().Passphrase.UseKeyring && s.deps.SaveKeyring != nil {
		if err := s.deps.SaveKeyring(s.currentConfig(), newPass); err != nil {
			writeJSON(w, http.StatusOK, map[string]any{
				"rotated": true, "keyringSaved": false,
				"warning": "passphrase rotated, but the keyring update failed: " + err.Error(),
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"rotated": true, "keyringSaved": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rotated": true, "keyringSaved": false})
}

// takeOp acquires the single-op guard for a synchronous mutating op (prune,
// password). It writes the 401/409 response and returns ok=false on failure.
func (s *Server) takeOp(w http.ResponseWriter, name string) (*repo.Repo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.repo == nil {
		writeErr(w, http.StatusUnauthorized, "repository is locked")
		return nil, false
	}
	if s.opRunning != "" {
		writeErr(w, http.StatusConflict, "another operation is in progress")
		return nil, false
	}
	s.opRunning = name
	return s.repo, true
}

func (s *Server) releaseOp() {
	s.mu.Lock()
	s.opRunning = ""
	s.mu.Unlock()
}

func passwdErrMessage(err error) string {
	switch {
	case errors.Is(err, repo.ErrSamePassphrase):
		return "new passphrase matches the current one — nothing to rotate"
	case errors.Is(err, repo.ErrRepoLocked):
		return "another operation is running — try again when it finishes"
	default:
		return "rotation failed: " + err.Error()
	}
}
