package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/markgustetic/sentra/internal/agent"
	"github.com/markgustetic/sentra/internal/agent/action"
	"github.com/markgustetic/sentra/internal/agent/heuristics"
	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/walker"
)

// The agent surface is advisory: a scan runs local heuristics first and (unless
// local-only) triages them with the LLM, returning read-only recommendations.
// The summaries-only invariant is inherited for free — the web only calls
// Agent.Scan (content-free Recommendation/Finding) and action.Dispatch; no
// endpoint returns file bytes or secrets. Apply is the one mutating path, gated
// by a typed confirm, a server-held recommendation set, and a wipe guard.

// handleAgentStatus reports whether an LLM key is present. Local-only scan works
// regardless, so the UI uses this only to explain the LLM-configured state.
func (s *Server) handleAgentStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"llmConfigured": llmKeyPresent()})
}

func llmKeyPresent() bool {
	return os.Getenv("ANTHROPIC_API_KEY") != "" || os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
}

// handleAgentScan runs a read-only advisory scan, streaming the model's
// reasoning tokens over SSE. The done event carries the recommendation list,
// which the server also stashes so apply can resolve approvals by ID.
func (s *Server) handleAgentScan(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Root       string   `json:"root"`
		LocalOnly  bool     `json:"localOnly"`
		Categories []string `json:"categories"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	root := strings.TrimSpace(body.Root)
	if root == "" {
		root = "."
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "directory not found: "+root)
		return
	}
	agentCfg := s.agentConfig(body.LocalOnly, body.Categories)

	opID, err := s.startReadStream(func(ctx context.Context, emit func(string), rp *repo.Repo) (any, error) {
		actions := s.deps.Actions
		if actions == nil {
			actions = action.NewDefaultRegistry()
		}
		a := &agent.Agent{
			Repo:       rp,
			Heuristics: heuristics.NewRegistry(s.deps.Heuristics...),
			Provider:   s.deps.Provider,
			Config:     agentCfg,
			Actions:    actions,
		}
		// Bridge Scan's token channel onto the op's emit callback. Wait for the
		// forwarder to finish draining before returning, so the op's done event
		// can't race ahead of the last reasoning tokens and truncate the stream.
		stream := make(chan string, 64)
		var fwd sync.WaitGroup
		fwd.Go(func() {
			for tok := range stream {
				emit(tok)
			}
		})
		recs, scanErr := a.Scan(ctx, root, stream)
		close(stream)
		fwd.Wait()
		if scanErr != nil {
			return nil, scanErr
		}
		byID := make(map[string]agent.Recommendation, len(recs))
		for _, rec := range recs {
			byID[rec.ID] = rec
		}
		s.mu.Lock()
		s.lastRecs = byID
		s.mu.Unlock()
		if recs == nil {
			recs = []agent.Recommendation{}
		}
		return map[string]any{"recommendations": recs}, nil
	})
	writeOpStart(w, opID, err)
}

// agentConfig assembles the scan config from the resolved sentra config,
// mirroring the CLI (retention feeds the retention-drift heuristic; walker
// options come from the backup block).
func (s *Server) agentConfig(localOnly bool, categories []string) agent.Config {
	cfg := s.currentConfig()
	c := agent.Config{
		LocalOnly:  localOnly,
		Categories: categories,
	}
	if cfg != nil {
		c.Model = cfg.Agent.Model
		c.MaxFindingsToLLM = cfg.Agent.MaxFindingsToLLM
		c.InputConfig = heuristics.InputConfig{Retention: retentionFromConfig(cfg)}
		c.Walker = walker.Options{IgnoreFile: cfg.Backup.IgnoreFile, ExcludeCaches: cfg.Backup.ExcludeCaches}
	}
	return c.Defaults()
}

// handleAgentApply executes approved recommendations. It requires a typed
// "apply" confirm, resolves each id against the last scan's recs (so the browser
// can never inject an arbitrary prune target or ignore line), and enforces a
// server-side wipe guard: pruning every snapshot needs a typed "wipe" too.
func (s *Server) handleAgentApply(w http.ResponseWriter, r *http.Request) {
	var body struct {
		IDs         []string `json:"ids"`
		Confirm     string   `json:"confirm"`
		WipeConfirm string   `json:"wipeConfirm"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	if body.Confirm != "apply" {
		writeErr(w, http.StatusBadRequest, `type "apply" to confirm`)
		return
	}
	recs, ok := s.resolveRecs(w, body.IDs)
	if !ok {
		return
	}
	wipeAuthorized := body.WipeConfirm == "wipe"
	if !s.checkWipeGuard(w, r, recs, wipeAuthorized) {
		return
	}

	actions := s.deps.Actions
	if actions == nil {
		actions = action.NewDefaultRegistry()
	}
	cwd := s.agentCwd()

	opID, err := s.startOp("agent-apply", func(ctx context.Context, _ progress.Reporter, rp *repo.Repo) (any, error) {
		type applied struct {
			ID     string `json:"id"`
			Action string `json:"action"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}
		results := make([]applied, 0, len(recs))
		failed := 0
		for _, rec := range recs {
			verb := action.Action(rec.Action)
			// Belt-and-suspenders wipe guard: re-check before each prune so a
			// concurrent change can't slip past the pre-flight count.
			if verb == action.PruneSnapshot && !wipeAuthorized {
				snaps, err := rp.ListSnapshots(ctx)
				if err != nil {
					return nil, err
				}
				if len(snaps)-1 <= 0 {
					return nil, errors.New("refusing to delete the last snapshot without a wipe confirmation")
				}
			}
			var buf bytes.Buffer
			env := action.Env{Repo: rp, Stdout: &buf, Cwd: cwd}
			derr := actions.Dispatch(ctx, env, verb, rec.ID, rec.Target, rec.Severity, rec.Rationale)
			res := applied{ID: rec.ID, Action: rec.Action, OK: derr == nil, Detail: strings.TrimSpace(buf.String())}
			if derr != nil {
				res.OK = false
				res.Detail = derr.Error()
				failed++
			}
			results = append(results, res)
		}
		return map[string]any{"applied": results, "failed": failed}, nil
	})
	writeOpStart(w, opID, err)
}

// resolveRecs maps approved ids onto the last scan's recommendations, dropping
// "none" verbs (nothing to apply). An unknown id is a 400 — the browser must
// reference recs the server actually produced.
func (s *Server) resolveRecs(w http.ResponseWriter, ids []string) ([]agent.Recommendation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agent.Recommendation, 0, len(ids))
	for _, id := range ids {
		rec, found := s.lastRecs[id]
		if !found {
			writeErr(w, http.StatusBadRequest, "unknown recommendation id: "+id)
			return nil, false
		}
		if action.Action(rec.Action) == action.None {
			continue
		}
		out = append(out, rec)
	}
	if len(out) == 0 {
		writeErr(w, http.StatusBadRequest, "select at least one actionable recommendation")
		return nil, false
	}
	return out, true
}

// checkWipeGuard refuses (400) an apply whose prune recommendations would leave
// zero snapshots unless a typed "wipe" authorized it.
func (s *Server) checkWipeGuard(w http.ResponseWriter, r *http.Request, recs []agent.Recommendation, wipeAuthorized bool) bool {
	pruneTargets := map[string]bool{}
	for _, rec := range recs {
		if action.Action(rec.Action) == action.PruneSnapshot && strings.TrimSpace(rec.Target) != "" {
			pruneTargets[rec.Target] = true
		}
	}
	if len(pruneTargets) == 0 || wipeAuthorized {
		return true
	}
	rp := s.currentRepo()
	if rp == nil {
		writeErr(w, http.StatusUnauthorized, "repository is locked")
		return false
	}
	snaps, err := rp.ListSnapshots(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, "list snapshots: "+err.Error())
		return false
	}
	if len(pruneTargets) >= len(snaps) {
		writeErr(w, http.StatusBadRequest, `this would delete every snapshot — type "wipe" to authorize`)
		return false
	}
	return true
}

// agentCwd is where add_to_ignore writes .sentraignore — the directory holding
// sentra.yaml, a stable and predictable home for it on a long-running server.
func (s *Server) agentCwd() string {
	if s.deps.ConfigPath != "" {
		if abs, err := filepath.Abs(s.deps.ConfigPath); err == nil {
			return filepath.Dir(abs)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		return wd
	}
	return "."
}
