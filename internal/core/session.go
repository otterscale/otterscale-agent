package core

import (
	"context"
	"io"
	"log/slog"
	"sync"
)

// ---------------------------------------------------------------------------
// Terminal size types
// ---------------------------------------------------------------------------

// TerminalSize holds terminal dimensions. This is a domain-level type
// that decouples the core layer from k8s.io/client-go/tools/remotecommand.
// The adapter layer is responsible for converting this to the
// remotecommand.TerminalSize type required by SPDY executors.
type TerminalSize struct {
	Width  uint16
	Height uint16
}

// TerminalSizer provides the next terminal size event. Implementations
// block until an event is available or return nil when no more events
// will be produced (e.g. the queue is closed).
type TerminalSizer interface {
	Next() *TerminalSize
}

// TerminalSizeQueue is a buffered, concurrency-safe queue that
// implements TerminalSizer. Resize events are enqueued via Set and
// dequeued via Next.
type TerminalSizeQueue struct {
	mu     sync.Mutex
	ch     chan TerminalSize
	closed bool
}

// NewTerminalSizeQueue returns a TerminalSizeQueue with a small buffer
// so resize events can be sent without blocking.
func NewTerminalSizeQueue() *TerminalSizeQueue {
	return &TerminalSizeQueue{ch: make(chan TerminalSize, 4)}
}

// Next returns the next terminal size event. It blocks until an event
// is available or the channel is closed (returns nil).
func (q *TerminalSizeQueue) Next() *TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// Set enqueues a resize event. If the queue is full, the oldest event
// is dropped to make room. A mutex prevents concurrent callers from
// racing on the drain-then-push sequence. Calls after Close are
// silently ignored to prevent a send-on-closed-channel panic.
func (q *TerminalSizeQueue) Set(width, height uint16) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.closed {
		return
	}

	select {
	case q.ch <- TerminalSize{Width: width, Height: height}:
	default:
		// Drop the oldest and push the new size.
		<-q.ch
		q.ch <- TerminalSize{Width: width, Height: height}
	}
}

// Close closes the underlying channel, causing Next to return nil.
// It is safe to call Close multiple times.
func (q *TerminalSizeQueue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()

	if !q.closed {
		q.closed = true
		close(q.ch)
	}
}

// ---------------------------------------------------------------------------
// Session types
// ---------------------------------------------------------------------------

// ExecSession represents an active exec session.
type ExecSession struct {
	// ID is the unique session identifier.
	ID string
	// Stdin is the writer side of the stdin pipe. WriteTTY writes here.
	Stdin io.WriteCloser
	// SizeQueue receives terminal resize events from ResizeTTY.
	SizeQueue *TerminalSizeQueue
	// Cancel stops the exec session.
	Cancel context.CancelFunc
	// Done receives the error (or nil) when the exec goroutine finishes.
	Done <-chan error
}

// PortForwardSession represents an active port-forward session.
type PortForwardSession struct {
	// ID is the unique session identifier.
	ID string
	// Writer is the writer side of the data pipe. WritePortForward writes here.
	Writer io.WriteCloser
	// Cancel stops the port-forward session.
	Cancel context.CancelFunc
	// Done receives the error (or nil) when the port-forward goroutine finishes.
	Done <-chan error
}

// ---------------------------------------------------------------------------
// Session store
// ---------------------------------------------------------------------------

// SessionStore manages active exec and port-forward sessions.
type SessionStore struct {
	mu       sync.RWMutex
	execSess map[string]*ExecSession
	pfSess   map[string]*PortForwardSession
}

// NewSessionStore returns an initialised SessionStore.
func NewSessionStore() *SessionStore {
	return &SessionStore{
		execSess: make(map[string]*ExecSession),
		pfSess:   make(map[string]*PortForwardSession),
	}
}

// PutExec stores an exec session.
func (s *SessionStore) PutExec(sess *ExecSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.execSess[sess.ID] = sess
}

// GetExec retrieves an exec session by ID.
func (s *SessionStore) GetExec(id string) (*ExecSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.execSess[id]
	return sess, ok
}

// DeleteExec removes an exec session.
func (s *SessionStore) DeleteExec(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.execSess, id)
}

// PutPortForward stores a port-forward session.
func (s *SessionStore) PutPortForward(sess *PortForwardSession) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pfSess[sess.ID] = sess
}

// GetPortForward retrieves a port-forward session by ID.
func (s *SessionStore) GetPortForward(id string) (*PortForwardSession, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.pfSess[id]
	return sess, ok
}

// DeletePortForward removes a port-forward session.
func (s *SessionStore) DeletePortForward(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pfSess, id)
}

// ReapStaleSessions scans all sessions and removes those whose Done
// channel has already been closed (goroutine finished). This prevents
// session leaks when clients disconnect without calling Cleanup.
func (s *SessionStore) ReapStaleSessions() int {
	reaped := 0

	s.mu.Lock()
	defer s.mu.Unlock()

	for id, sess := range s.execSess {
		select {
		case <-sess.Done:
			sess.Cancel()
			if err := sess.Stdin.Close(); err != nil {
				slog.Warn("failed to close exec stdin", "session", id, "error", err)
			}
			delete(s.execSess, id)
			reaped++
		default:
		}
	}

	for id, sess := range s.pfSess {
		select {
		case <-sess.Done:
			sess.Cancel()
			if err := sess.Writer.Close(); err != nil {
				slog.Warn("failed to close port-forward writer", "session", id, "error", err)
			}
			delete(s.pfSess, id)
			reaped++
		default:
		}
	}

	return reaped
}
