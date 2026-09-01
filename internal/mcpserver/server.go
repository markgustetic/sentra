// Package mcpserver exposes an opened Sentra repository as a Model
// Context Protocol server, so outside agents (Claude, editors, anything
// speaking MCP) can query snapshots and drive confirm-gated operations
// within the same guardrails the CLI and TUI enforce.
//
// Two rules shape every tool here (see the spec in
// docs/superpowers/specs/2026-08-28-mcp-server-design.md):
//
//   - Metadata only, never contents. Tools return names, sizes, times,
//     ids, and stats. Nothing reads file bodies out of snapshots, so a
//     malicious or curious client cannot exfiltrate backed-up data
//     through this surface.
//
//   - Mutations are two-phase. MCP has no interactive prompt, so the
//     confirm gate becomes protocol: plan_* validates and returns a
//     single-use token bound to that exact plan; confirm_* executes it.
//     Tokens expire (tokenTTL) and die on first use.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/markgustetic/sentra/internal/repo"
)

// tokenTTL bounds how long a plan may sit unconfirmed. Long enough for a
// human to read the plan in their client; short enough that a stale plan
// cannot be replayed much later against a changed filesystem.
const tokenTTL = 10 * time.Minute

// resultCap bounds list-shaped tool outputs so a huge repository cannot
// blow up a client's context window. Capped results say so.
const resultCap = 200

type planKind string

const (
	planBackup  planKind = "backup"
	planRestore planKind = "restore"
)

type pendingPlan struct {
	kind    planKind
	created time.Time
	// backup
	path string
	tag  string
	// restore
	snapshotID string
	dest       string
	paths      []string
}

// Server wraps an opened repository with the MCP tool set. now is a seam
// for the token-expiry tests.
type Server struct {
	repo    *repo.Repo
	version string
	now     func() time.Time

	mu     sync.Mutex
	tokens map[string]pendingPlan
}

// New builds a Server over an already-opened repository. The caller owns
// the repo's lifecycle; Run does not close it.
func New(r *repo.Repo, version string) *Server {
	return &Server{
		repo:    r,
		version: version,
		now:     time.Now,
		tokens:  map[string]pendingPlan{},
	}
}

// Run serves MCP over the given transport until ctx ends or the client
// disconnects. Production passes &mcp.StdioTransport{}; tests use the
// in-memory pair.
func (s *Server) Run(ctx context.Context, t mcp.Transport) error {
	return s.mcpServer().Run(ctx, t)
}

func (s *Server) mcpServer() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "sentra", Version: s.version}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_snapshots",
		Description: "List snapshots (newest first): id, created, tag, source root, file count, bytes. Metadata only.",
	}, s.listSnapshots)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "snapshot_files",
		Description: "List the files recorded in one snapshot (path, size, mtime). Optional path prefix filter. Metadata only — never file contents.",
	}, s.snapshotFiles)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "find",
		Description: "Search file paths across every snapshot for a substring (case-insensitive). Reports each snapshot that contains a match.",
	}, s.find)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "diff_snapshots",
		Description: "Compare two snapshots: added, removed, and changed paths (capped), with counts.",
	}, s.diffSnapshots)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "repo_stats",
		Description: "Repository storage totals: snapshot count, logical vs stored bytes (dedup + compression), unique chunks.",
	}, s.repoStats)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "plan_backup",
		Description: "Validate a backup of a local directory and return a plan plus a single-use confirmation token. Nothing runs until confirm_backup.",
	}, s.planBackup)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "confirm_backup",
		Description: "Execute a backup previously planned with plan_backup. The token is single-use and expires.",
	}, s.confirmBackup)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "plan_restore",
		Description: "Validate a restore of a snapshot to a local destination and return a plan plus a single-use confirmation token. Nothing runs until confirm_restore.",
	}, s.planRestore)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "confirm_restore",
		Description: "Execute a restore previously planned with plan_restore. The token is single-use and expires.",
	}, s.confirmRestore)
	return srv
}

// --- read tools -----------------------------------------------------

type listSnapshotsIn struct {
	Tag   string `json:"tag,omitempty" jsonschema:"only snapshots carrying this tag"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum rows (default 50)"`
}

type snapshotRow struct {
	ID      string    `json:"id"`
	Created time.Time `json:"created"`
	Tag     string    `json:"tag,omitempty"`
	Root    string    `json:"root"`
	Files   int       `json:"files"`
	Bytes   int64     `json:"bytes"`
}

type listSnapshotsOut struct {
	Snapshots []snapshotRow `json:"snapshots"`
	Truncated bool          `json:"truncated,omitempty"`
}

func (s *Server) listSnapshots(ctx context.Context, _ *mcp.CallToolRequest, in listSnapshotsIn) (*mcp.CallToolResult, listSnapshotsOut, error) {
	snaps, err := s.repo.ListSnapshots(ctx)
	if err != nil {
		return nil, listSnapshotsOut{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > resultCap {
		limit = 50
	}
	out := listSnapshotsOut{}
	for _, sn := range snaps {
		if in.Tag != "" && sn.Tag != in.Tag {
			continue
		}
		if len(out.Snapshots) == limit {
			out.Truncated = true
			break
		}
		out.Snapshots = append(out.Snapshots, snapshotRow{
			ID: sn.ID, Created: sn.CreatedAt, Tag: sn.Tag, Root: sn.Root,
			Files: sn.Stats.Files, Bytes: sn.Stats.Bytes,
		})
	}
	return nil, out, nil
}

type snapshotFilesIn struct {
	SnapshotID string `json:"snapshot_id" jsonschema:"the snapshot to list"`
	Prefix     string `json:"prefix,omitempty" jsonschema:"only paths under this prefix"`
	Limit      int    `json:"limit,omitempty" jsonschema:"maximum rows (default 200)"`
}

type fileRow struct {
	Path  string    `json:"path"`
	Size  int64     `json:"size"`
	MTime time.Time `json:"mtime"`
}

type snapshotFilesOut struct {
	Files     []fileRow `json:"files"`
	Truncated bool      `json:"truncated,omitempty"`
}

func (s *Server) snapshotFiles(ctx context.Context, _ *mcp.CallToolRequest, in snapshotFilesIn) (*mcp.CallToolResult, snapshotFilesOut, error) {
	man, err := s.repo.LoadSnapshot(ctx, in.SnapshotID)
	if err != nil {
		return nil, snapshotFilesOut{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > resultCap {
		limit = resultCap
	}
	out := snapshotFilesOut{}
	for _, e := range man.Tree {
		if in.Prefix != "" && !strings.HasPrefix(e.Path, in.Prefix) {
			continue
		}
		if len(out.Files) == limit {
			out.Truncated = true
			break
		}
		out.Files = append(out.Files, fileRow{Path: e.Path, Size: e.Size, MTime: e.MTime})
	}
	return nil, out, nil
}

type findIn struct {
	Pattern string `json:"pattern" jsonschema:"case-insensitive substring of the path"`
	Limit   int    `json:"limit,omitempty" jsonschema:"maximum rows (default 100)"`
}

type findHit struct {
	Path        string    `json:"path"`
	SnapshotID  string    `json:"snapshot_id"`
	SnapshotTag string    `json:"snapshot_tag,omitempty"`
	Created     time.Time `json:"created"`
	Size        int64     `json:"size"`
}

type findOut struct {
	Matches   []findHit `json:"matches"`
	Truncated bool      `json:"truncated,omitempty"`
}

func (s *Server) find(ctx context.Context, _ *mcp.CallToolRequest, in findIn) (*mcp.CallToolResult, findOut, error) {
	if strings.TrimSpace(in.Pattern) == "" {
		return nil, findOut{}, fmt.Errorf("pattern must not be empty")
	}
	snaps, err := s.repo.ListSnapshots(ctx)
	if err != nil {
		return nil, findOut{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > resultCap {
		limit = 100
	}
	needle := strings.ToLower(in.Pattern)
	out := findOut{}
	for _, sn := range snaps {
		man, err := s.repo.LoadSnapshot(ctx, sn.ID)
		if err != nil {
			return nil, findOut{}, err
		}
		for _, e := range man.Tree {
			if !strings.Contains(strings.ToLower(e.Path), needle) {
				continue
			}
			if len(out.Matches) == limit {
				out.Truncated = true
				return nil, out, nil
			}
			out.Matches = append(out.Matches, findHit{
				Path: e.Path, SnapshotID: sn.ID, SnapshotTag: sn.Tag,
				Created: sn.CreatedAt, Size: e.Size,
			})
		}
	}
	return nil, out, nil
}

type diffIn struct {
	A     string `json:"a" jsonschema:"older snapshot id"`
	B     string `json:"b" jsonschema:"newer snapshot id"`
	Limit int    `json:"limit,omitempty" jsonschema:"maximum paths per bucket (default 100)"`
}

type diffOut struct {
	Added        []string `json:"added"`
	Removed      []string `json:"removed"`
	Changed      []string `json:"changed"`
	AddedCount   int      `json:"added_count"`
	RemovedCount int      `json:"removed_count"`
	ChangedCount int      `json:"changed_count"`
	Truncated    bool     `json:"truncated,omitempty"`
}

func (s *Server) diffSnapshots(ctx context.Context, _ *mcp.CallToolRequest, in diffIn) (*mcp.CallToolResult, diffOut, error) {
	res, err := s.repo.Diff(ctx, in.A, in.B)
	if err != nil {
		return nil, diffOut{}, err
	}
	limit := in.Limit
	if limit <= 0 || limit > resultCap {
		limit = 100
	}
	capped := func(paths []string) ([]string, bool) {
		sort.Strings(paths)
		if len(paths) > limit {
			return paths[:limit], true
		}
		return paths, false
	}
	out := diffOut{AddedCount: len(res.Added), RemovedCount: len(res.Removed), ChangedCount: len(res.Changed)}
	var t1, t2, t3 bool
	out.Added, t1 = capped(res.Added)
	out.Removed, t2 = capped(res.Removed)
	out.Changed, t3 = capped(res.Changed)
	out.Truncated = t1 || t2 || t3
	return nil, out, nil
}

type statsOut struct {
	Snapshots    int   `json:"snapshots"`
	LogicalBytes int64 `json:"logical_bytes"`
	StoredBytes  int64 `json:"stored_bytes"`
	UniqueChunks int   `json:"unique_chunks"`
}

func (s *Server) repoStats(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, statsOut, error) {
	st, err := s.repo.Stats(ctx)
	if err != nil {
		return nil, statsOut{}, err
	}
	return nil, statsOut{
		Snapshots: st.Snapshots, LogicalBytes: st.LogicalBytes,
		StoredBytes: st.StoredBytes, UniqueChunks: st.UniqueChunks,
	}, nil
}

// --- two-phase mutations --------------------------------------------

type planOut struct {
	Token   string `json:"token"`
	Summary string `json:"summary"`
	Expires string `json:"expires"`
}

func (s *Server) storePlan(p pendingPlan) (planOut, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return planOut{}, fmt.Errorf("mint token: %w", err)
	}
	token := hex.EncodeToString(buf)
	p.created = s.now()
	s.mu.Lock()
	s.tokens[token] = p
	s.mu.Unlock()
	return planOut{Token: token, Expires: p.created.Add(tokenTTL).Format(time.RFC3339)}, nil
}

// takePlan redeems a token: single-use, kind-bound, TTL-checked.
func (s *Server) takePlan(token string, kind planKind) (pendingPlan, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.tokens[token]
	if !ok {
		return pendingPlan{}, fmt.Errorf("unknown or already-used token — call the matching plan_ tool first")
	}
	delete(s.tokens, token) // single-use even on the failure paths below
	if p.kind != kind {
		return pendingPlan{}, fmt.Errorf("token was minted for %s, not %s — plans are bound to one operation", p.kind, kind)
	}
	if s.now().Sub(p.created) > tokenTTL {
		return pendingPlan{}, fmt.Errorf("token expired — plan again")
	}
	return p, nil
}

type planBackupIn struct {
	Path string `json:"path" jsonschema:"local directory to back up"`
	Tag  string `json:"tag,omitempty" jsonschema:"optional snapshot tag"`
}

func (s *Server) planBackup(_ context.Context, _ *mcp.CallToolRequest, in planBackupIn) (*mcp.CallToolResult, planOut, error) {
	info, err := os.Stat(in.Path)
	if err != nil || !info.IsDir() {
		return nil, planOut{}, fmt.Errorf("not a readable directory: %s", in.Path)
	}
	out, err := s.storePlan(pendingPlan{kind: planBackup, path: in.Path, tag: in.Tag})
	if err != nil {
		return nil, planOut{}, err
	}
	out.Summary = fmt.Sprintf("back up %s (tag %q) into this repository", in.Path, in.Tag)
	return nil, out, nil
}

type confirmIn struct {
	Token string `json:"token" jsonschema:"the token returned by the matching plan_ tool"`
}

type backupDoneOut struct {
	SnapshotID string `json:"snapshot_id"`
	Files      int    `json:"files"`
	Bytes      int64  `json:"bytes"`
	NewBytes   int64  `json:"new_bytes"`
}

func (s *Server) confirmBackup(ctx context.Context, _ *mcp.CallToolRequest, in confirmIn) (*mcp.CallToolResult, backupDoneOut, error) {
	p, err := s.takePlan(in.Token, planBackup)
	if err != nil {
		return nil, backupDoneOut{}, err
	}
	info, err := s.repo.CreateSnapshot(ctx, p.path, repo.SnapshotOptions{Tag: p.tag})
	if err != nil {
		return nil, backupDoneOut{}, err
	}
	return nil, backupDoneOut{
		SnapshotID: info.ID, Files: info.Stats.Files,
		Bytes: info.Stats.Bytes, NewBytes: info.Stats.NewBytes,
	}, nil
}

type planRestoreIn struct {
	SnapshotID string   `json:"snapshot_id" jsonschema:"snapshot to restore"`
	Dest       string   `json:"dest" jsonschema:"destination directory (must not exist, or be empty)"`
	Paths      []string `json:"paths,omitempty" jsonschema:"optional paths/subtrees to restore; empty restores everything"`
}

func (s *Server) planRestore(ctx context.Context, _ *mcp.CallToolRequest, in planRestoreIn) (*mcp.CallToolResult, planOut, error) {
	plan, err := s.repo.PlanRestore(ctx, in.SnapshotID, in.Dest, in.Paths...)
	if err != nil {
		return nil, planOut{}, err
	}
	out, err := s.storePlan(pendingPlan{
		kind: planRestore, snapshotID: in.SnapshotID, dest: in.Dest, paths: in.Paths,
	})
	if err != nil {
		return nil, planOut{}, err
	}
	out.Summary = fmt.Sprintf("restore %d files (%d bytes) from %s to %s",
		plan.Files, plan.Bytes, in.SnapshotID, in.Dest)
	return nil, out, nil
}

type restoreDoneOut struct {
	Dest string `json:"dest"`
}

func (s *Server) confirmRestore(ctx context.Context, _ *mcp.CallToolRequest, in confirmIn) (*mcp.CallToolResult, restoreDoneOut, error) {
	p, err := s.takePlan(in.Token, planRestore)
	if err != nil {
		return nil, restoreDoneOut{}, err
	}
	if err := s.repo.Restore(ctx, p.snapshotID, p.dest, repo.RestoreOptions{Paths: p.paths}); err != nil {
		return nil, restoreDoneOut{}, err
	}
	return nil, restoreDoneOut{Dest: p.dest}, nil
}
