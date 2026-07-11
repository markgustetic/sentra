package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"

	"github.com/markgustetic/sentra/internal/progress"
	"github.com/markgustetic/sentra/internal/repo"
)

// newOpID returns a short random hex id for a running operation.
func newOpID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// sse writes one Server-Sent Event frame: an event name and a JSON data line.
func sse(w http.ResponseWriter, event string, data any) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
}

// A streaming operation (backup, restore, sync) whose progress is delivered over
// SSE and whose terminal result is delivered even to a client that connects
// mid-run. One runs at a time, gated by Server.opRunning.
type op struct {
	id       string
	progress chan progressMsg // buffered + lossy: only the latest matters
	text     chan string      // buffered + lossy: streamed log/reasoning tokens
	done     chan struct{}    // closed when the op finishes
	mu       sync.Mutex
	result   opResult
}

type progressMsg struct{ Done, Total int64 }

type opResult struct {
	data any // the "done" payload (e.g. {"snapshot": …}, {"restored": true})
	err  error
}

var (
	errLocked = errors.New("repository is locked")
	errBusy   = errors.New("another operation is in progress")
)

// startOp takes the single-op guard, registers a streaming op, and runs it in a
// goroutine against the repo it locked against (passed to run, so a concurrent
// lock can't nil it out). It returns the opId for the SSE stream, errBusy if
// another op is running, or errLocked if the repo is locked.
func (s *Server) startOp(name string, run func(context.Context, progress.Reporter, *repo.Repo) (any, error)) (string, error) {
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return "", errLocked
	}
	if s.opRunning != "" {
		s.mu.Unlock()
		return "", errBusy
	}
	o := &op{id: newOpID(), progress: make(chan progressMsg, 8), text: make(chan string, 1), done: make(chan struct{})}
	s.opRunning = name
	s.ops[o.id] = o
	rp := s.repo
	s.mu.Unlock()

	go func() {
		data, err := run(context.Background(), &opReporter{op: o}, rp)
		o.mu.Lock()
		o.result = opResult{data: data, err: err}
		o.mu.Unlock()
		close(o.done)
		s.mu.Lock()
		s.opRunning = ""
		s.mu.Unlock()
	}()
	return o.id, nil
}

// startReadStream registers a read-only streaming op — the agent scan, which
// walks the tree and reads manifests but never mutates. Unlike startOp it does
// NOT take the mutating guard, so a scan neither blocks nor is blocked by a
// backup/prune. The run func gets an emit callback for reasoning/log tokens.
// Returns errLocked if the repo is locked.
func (s *Server) startReadStream(run func(context.Context, func(string), *repo.Repo) (any, error)) (string, error) {
	s.mu.Lock()
	if s.repo == nil {
		s.mu.Unlock()
		return "", errLocked
	}
	o := &op{id: newOpID(), progress: make(chan progressMsg, 1), text: make(chan string, 256), done: make(chan struct{})}
	s.ops[o.id] = o
	rp := s.repo
	s.mu.Unlock()

	go func() {
		emit := func(tok string) {
			select {
			case o.text <- tok:
			default: // reader is slow or absent — drop; the done payload is authoritative
			}
		}
		data, err := run(context.Background(), emit, rp)
		o.mu.Lock()
		o.result = opResult{data: data, err: err}
		o.mu.Unlock()
		close(o.done)
	}()
	return o.id, nil
}

// startSetupOp registers the first-run provisioning op. Unlike startOp it runs
// with no repo (setup is what creates one), so it takes the one-op guard but
// skips the unlocked-repo check. The run func gets an emit callback for step
// markers streamed over SSE. Returns errBusy if another op (or apply) is
// already running.
func (s *Server) startSetupOp(run func(context.Context, func(string)) (any, error)) (string, error) {
	s.mu.Lock()
	if s.opRunning != "" {
		s.mu.Unlock()
		return "", errBusy
	}
	o := &op{id: newOpID(), progress: make(chan progressMsg, 1), text: make(chan string, 64), done: make(chan struct{})}
	s.opRunning = "setup"
	s.ops[o.id] = o
	s.mu.Unlock()

	go func() {
		emit := func(tok string) {
			select {
			case o.text <- tok:
			default:
			}
		}
		data, err := run(context.Background(), emit)
		o.mu.Lock()
		o.result = opResult{data: data, err: err}
		o.mu.Unlock()
		close(o.done)
		s.mu.Lock()
		s.opRunning = ""
		s.mu.Unlock()
	}()
	return o.id, nil
}

// writeOpStart maps startOp's error to an HTTP status, or writes the opId.
func writeOpStart(w http.ResponseWriter, opID string, err error) {
	switch {
	case errors.Is(err, errBusy):
		writeErr(w, http.StatusConflict, errBusy.Error())
	case errors.Is(err, errLocked):
		writeErr(w, http.StatusUnauthorized, errLocked.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err.Error())
	default:
		writeJSON(w, http.StatusOK, map[string]string{"opId": opID})
	}
}

// opReporter adapts progress.Reporter onto an op's channel. Sends are
// non-blocking: a slow or absent SSE reader never stalls the op, it just misses
// intermediate frames — the terminal result is delivered via op.done.
type opReporter struct {
	op    *op
	mu    sync.Mutex
	total int64
	done  int64
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

// drainTokens flushes any tokens still buffered when the op finishes, so a fast
// scan's reasoning transcript isn't truncated by the done event racing ahead.
func drainTokens(w http.ResponseWriter, flusher http.Flusher, o *op) {
	for {
		select {
		case tok := <-o.text:
			sse(w, "token", map[string]string{"text": tok})
		default:
			flusher.Flush()
			return
		}
	}
}

// handleOpEvents streams a running op's progress and terminal result as SSE. It
// backs both /api/backup/{id}/events and the generic /api/op/{id}/events.
func (s *Server) handleOpEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	s.mu.Lock()
	o := s.ops[id]
	s.mu.Unlock()
	if o == nil {
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
		case p := <-o.progress:
			sse(w, "progress", map[string]int64{"done": p.Done, "total": p.Total})
			flusher.Flush()
		case tok := <-o.text:
			sse(w, "token", map[string]string{"text": tok})
			flusher.Flush()
		case <-o.done:
			drainTokens(w, flusher, o)
			o.mu.Lock()
			res := o.result
			o.mu.Unlock()
			if res.err != nil {
				sse(w, "error", map[string]string{"message": res.err.Error()})
			} else {
				sse(w, "done", res.data)
			}
			flusher.Flush()
			return
		}
	}
}
