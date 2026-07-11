package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/walker"
)

// backupOp tracks one running backup so its progress can be streamed over SSE
// and its result delivered even to a client that connects mid-run.
type backupOp struct {
	id       string
	progress chan progressMsg // buffered + lossy: only the latest matters
	done     chan struct{}    // closed when the op finishes
	mu       sync.Mutex
	result   backupResult
}

type progressMsg struct{ Done, Total int64 }

type backupResult struct {
	snapshot *snapshotDTO
	err      error
}

// opReporter adapts progress.Reporter onto a backupOp's channel. Sends are
// non-blocking: a slow (or absent) SSE reader never stalls the backup, it just
// misses intermediate frames — the terminal result is delivered via op.done.
type opReporter struct {
	op    *backupOp
	total int64
	done  int64
	mu    sync.Mutex
}

func (r *opReporter) Total(n int64) {
	r.mu.Lock()
	r.total = n
	r.mu.Unlock()
	r.emit()
}

func (r *opReporter) Add(delta int64) {
	r.mu.Lock()
	r.done += delta
	r.mu.Unlock()
	r.emit()
}

func (r *opReporter) emit() {
	r.mu.Lock()
	msg := progressMsg{Done: r.done, Total: r.total}
	r.mu.Unlock()
	select {
	case r.op.progress <- msg:
	default:
	}
}

// handleBackupStart validates the folder, takes the single-op guard, and
// launches CreateSnapshot in a goroutine. It returns the opId the client uses to
// open the SSE stream.
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

	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		writeErr(w, http.StatusUnauthorized, "repository is locked")
		return
	}
	if s.opRunning != "" {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict, "another operation is in progress")
		return
	}
	op := &backupOp{
		id:       newOpID(),
		progress: make(chan progressMsg, 8),
		done:     make(chan struct{}),
	}
	s.opRunning = "backup"
	s.ops[op.id] = op
	rp := s.repo
	cfg := s.deps.Config
	s.mu.Unlock()

	var wopts walker.Options
	if cfg != nil {
		wopts = walker.Options{IgnoreFile: cfg.Backup.IgnoreFile, ExcludeCaches: cfg.Backup.ExcludeCaches}
	}
	tag := strings.TrimSpace(body.Tag)

	go s.runBackup(op, rp, root, tag, wopts)

	writeJSON(w, http.StatusOK, map[string]string{"opId": op.id})
}

// runBackup executes the snapshot and records the result, then releases the guard.
func (s *Server) runBackup(op *backupOp, rp *repo.Repo, root, tag string, wopts walker.Options) {
	rep := &opReporter{op: op}
	info, err := rp.CreateSnapshot(context.Background(), root, repo.SnapshotOptions{
		Tag:      tag,
		Progress: rep,
		Walker:   wopts,
	})
	op.mu.Lock()
	if err != nil {
		op.result = backupResult{err: err}
	} else {
		dto := toDTO(info)
		op.result = backupResult{snapshot: &dto}
	}
	op.mu.Unlock()
	close(op.done)

	s.mu.Lock()
	s.opRunning = ""
	s.mu.Unlock()
}

// handleBackupEvents streams a running backup's progress and terminal result as
// Server-Sent Events. It ends the stream on completion or client disconnect.
func (s *Server) handleBackupEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	op := s.ops[id]
	s.mu.Unlock()
	if op == nil {
		writeErr(w, http.StatusNotFound, "no such operation")
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	for {
		select {
		case <-r.Context().Done():
			return
		case p := <-op.progress:
			sse(w, "progress", map[string]int64{"done": p.Done, "total": p.Total})
			flusher.Flush()
		case <-op.done:
			op.mu.Lock()
			res := op.result
			op.mu.Unlock()
			if res.err != nil {
				sse(w, "error", map[string]string{"message": res.err.Error()})
			} else {
				sse(w, "done", map[string]any{"snapshot": res.snapshot})
			}
			flusher.Flush()
			return
		}
	}
}

// sse writes one Server-Sent Event frame: an event name and a JSON data line.
func sse(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

// newOpID returns a short random hex id for a running operation.
func newOpID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
