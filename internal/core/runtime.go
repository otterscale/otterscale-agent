package core

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/remotecommand"
)

// ---------------------------------------------------------------------------
// Interfaces
// ---------------------------------------------------------------------------

// RuntimeRepo abstracts Kubernetes runtime operations (logs, exec,
// scale, restart, port-forward). All methods accept a cluster name so
// that the underlying implementation can route requests through the
// correct tunnel.
type RuntimeRepo interface {
	// PodLogs opens a streaming reader for container log output.
	PodLogs(ctx context.Context, cluster, namespace, name string, opts PodLogOptions) (io.ReadCloser, error)
	// Exec starts an exec session and blocks until it completes.
	Exec(ctx context.Context, cluster, namespace, name string, opts ExecOptions) error
	// GetScale reads the current replica count via the /scale subresource.
	GetScale(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) (int32, error)
	// UpdateScale sets the desired replica count via the /scale subresource
	// and returns the updated value.
	UpdateScale(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string, replicas int32) (int32, error)
	// Restart triggers a rolling restart by patching the pod template annotation.
	Restart(ctx context.Context, cluster string, gvr schema.GroupVersionResource, namespace, name string) error
	// PortForward opens a port-forward session and copies data
	// bidirectionally until the context is cancelled or the
	// connection closes.
	PortForward(ctx context.Context, cluster, namespace, name string, opts PortForwardOptions) error
}

// ---------------------------------------------------------------------------
// Options types
// ---------------------------------------------------------------------------

// PodLogOptions mirrors the fields of corev1.PodLogOptions that are
// exposed through the RuntimeService proto.
type PodLogOptions struct {
	Container    string
	Follow       bool
	TailLines    *int64
	SinceSeconds *int64
	SinceTime    *time.Time
	Previous     bool
	Timestamps   bool
	LimitBytes   *int64
}

// ExecOptions holds parameters for an interactive exec session.
type ExecOptions struct {
	Container string
	Command   []string
	TTY       bool
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	SizeQueue remotecommand.TerminalSizeQueue
}

// PortForwardOptions holds parameters for a port-forward session.
type PortForwardOptions struct {
	Port   int32
	Stdin  io.Reader
	Stdout io.Writer
}

// ---------------------------------------------------------------------------
// Terminal size queue
// ---------------------------------------------------------------------------

// TerminalSizeQueue implements remotecommand.TerminalSizeQueue using a
// buffered channel. Resize events are enqueued via Set and dequeued by
// the remotecommand executor via Next.
type TerminalSizeQueue struct {
	mu sync.Mutex
	ch chan remotecommand.TerminalSize
}

// NewTerminalSizeQueue returns a TerminalSizeQueue with a small buffer
// so resize events can be sent without blocking.
func NewTerminalSizeQueue() *TerminalSizeQueue {
	return &TerminalSizeQueue{ch: make(chan remotecommand.TerminalSize, 4)}
}

// Next returns the next terminal size event. It blocks until an event
// is available or the channel is closed (returns nil).
func (q *TerminalSizeQueue) Next() *remotecommand.TerminalSize {
	size, ok := <-q.ch
	if !ok {
		return nil
	}
	return &size
}

// Set enqueues a resize event. If the queue is full, the oldest event
// is dropped to make room. A mutex prevents concurrent callers from
// racing on the drain-then-push sequence.
func (q *TerminalSizeQueue) Set(width, height uint16) {
	q.mu.Lock()
	defer q.mu.Unlock()

	select {
	case q.ch <- remotecommand.TerminalSize{Width: width, Height: height}:
	default:
		// Drop the oldest and push the new size.
		<-q.ch
		q.ch <- remotecommand.TerminalSize{Width: width, Height: height}
	}
}

// Close closes the underlying channel, causing Next to return nil.
func (q *TerminalSizeQueue) Close() {
	close(q.ch)
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
			_ = sess.Stdin.Close()
			delete(s.execSess, id)
			reaped++
		default:
		}
	}

	for id, sess := range s.pfSess {
		select {
		case <-sess.Done:
			sess.Cancel()
			_ = sess.Writer.Close()
			delete(s.pfSess, id)
			reaped++
		default:
		}
	}

	return reaped
}

// ---------------------------------------------------------------------------
// Use case
// ---------------------------------------------------------------------------

// RuntimeUseCase provides application-level runtime operations with
// session management for exec and port-forward.
type RuntimeUseCase struct {
	discovery DiscoveryClient
	runtime   RuntimeRepo
	sessions  *SessionStore
}

// NewRuntimeUseCase returns a RuntimeUseCase wired to the given
// discovery and runtime backends.
func NewRuntimeUseCase(discovery DiscoveryClient, runtime RuntimeRepo) *RuntimeUseCase {
	return &RuntimeUseCase{
		discovery: discovery,
		runtime:   runtime,
		sessions:  NewSessionStore(),
	}
}

// StartPodLogs validates the request and opens a streaming log reader.
func (uc *RuntimeUseCase) StartPodLogs(ctx context.Context, cluster, namespace, name string, opts PodLogOptions) (io.ReadCloser, error) {
	if name == "" {
		return nil, apierrors.NewBadRequest("pod name is required")
	}
	return uc.runtime.PodLogs(ctx, cluster, namespace, name, opts)
}

// StartExec creates an exec session, starts the exec in a background
// goroutine, and returns the session together with stdout and stderr
// readers that the caller can stream from.
func (uc *RuntimeUseCase) StartExec(ctx context.Context, cluster, namespace, name string, container string, command []string, tty bool, rows, cols uint16) (*ExecSession, io.ReadCloser, io.ReadCloser, error) {
	if name == "" {
		return nil, nil, nil, apierrors.NewBadRequest("pod name is required")
	}
	if len(command) == 0 {
		return nil, nil, nil, apierrors.NewBadRequest("command is required")
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	sizeQueue := NewTerminalSizeQueue()

	// Send initial terminal size.
	if rows > 0 && cols > 0 {
		sizeQueue.Set(cols, rows)
	}

	ctx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)

	sess := &ExecSession{
		ID:        uuid.New().String(),
		Stdin:     stdinW,
		SizeQueue: sizeQueue,
		Cancel:    cancel,
		Done:      errCh,
	}

	go func() {
		defer stdoutW.Close()
		defer stderrW.Close()
		defer sizeQueue.Close()

		var stderr io.Writer
		if !tty {
			stderr = stderrW
		}

		errCh <- uc.runtime.Exec(ctx, cluster, namespace, name, ExecOptions{
			Container: container,
			Command:   command,
			TTY:       tty,
			Stdin:     stdinR,
			Stdout:    stdoutW,
			Stderr:    stderr,
			SizeQueue: sizeQueue,
		})
	}()

	uc.sessions.PutExec(sess)
	return sess, stdoutR, stderrR, nil
}

// WriteExec writes stdin data to an active exec session.
func (uc *RuntimeUseCase) WriteExec(sessionID string, data []byte) error {
	sess, ok := uc.sessions.GetExec(sessionID)
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "exec-sessions"}, sessionID)
	}
	_, err := sess.Stdin.Write(data)
	return err
}

// ResizeExec sends a terminal resize event to an active exec session.
func (uc *RuntimeUseCase) ResizeExec(sessionID string, rows, cols uint16) error {
	sess, ok := uc.sessions.GetExec(sessionID)
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "exec-sessions"}, sessionID)
	}
	sess.SizeQueue.Set(cols, rows)
	return nil
}

// CleanupExec stops an exec session and removes it from the store.
func (uc *RuntimeUseCase) CleanupExec(sessionID string) {
	sess, ok := uc.sessions.GetExec(sessionID)
	if !ok {
		return
	}
	sess.Cancel()
	sess.Stdin.Close()
	uc.sessions.DeleteExec(sessionID)
}

// StartPortForward creates a port-forward session, starts the
// forwarding in a background goroutine, and returns the session
// together with a reader for data coming from the pod.
func (uc *RuntimeUseCase) StartPortForward(ctx context.Context, cluster, namespace, name string, port int32) (*PortForwardSession, io.ReadCloser, error) {
	if name == "" {
		return nil, nil, apierrors.NewBadRequest("pod name is required")
	}
	if port <= 0 || port > 65535 {
		return nil, nil, apierrors.NewBadRequest("port must be between 1 and 65535")
	}

	dataInR, dataInW := io.Pipe()
	dataOutR, dataOutW := io.Pipe()

	ctx, cancel := context.WithCancel(ctx)
	errCh := make(chan error, 1)

	sess := &PortForwardSession{
		ID:     uuid.New().String(),
		Writer: dataInW,
		Cancel: cancel,
		Done:   errCh,
	}

	go func() {
		defer dataOutW.Close()
		errCh <- uc.runtime.PortForward(ctx, cluster, namespace, name, PortForwardOptions{
			Port:   port,
			Stdin:  dataInR,
			Stdout: dataOutW,
		})
	}()

	uc.sessions.PutPortForward(sess)
	return sess, dataOutR, nil
}

// WritePortForward writes data to an active port-forward session.
func (uc *RuntimeUseCase) WritePortForward(sessionID string, data []byte) error {
	sess, ok := uc.sessions.GetPortForward(sessionID)
	if !ok {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "portforward-sessions"}, sessionID)
	}
	_, err := sess.Writer.Write(data)
	return err
}

// CleanupPortForward stops a port-forward session and removes it from
// the store.
func (uc *RuntimeUseCase) CleanupPortForward(sessionID string) {
	sess, ok := uc.sessions.GetPortForward(sessionID)
	if !ok {
		return
	}
	sess.Cancel()
	sess.Writer.Close()
	uc.sessions.DeletePortForward(sessionID)
}

// Scale validates the GVR, reads the current scale, updates it to the
// desired replicas, and returns the new replica count.
func (uc *RuntimeUseCase) Scale(ctx context.Context, cluster, group, version, resource, namespace, name string, replicas int32) (int32, error) {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return 0, err
	}
	return uc.runtime.UpdateScale(ctx, cluster, gvr, namespace, name, replicas)
}

// StartSessionReaper launches a background goroutine that
// periodically scans for stale sessions (finished but not cleaned up)
// and removes them. It blocks until ctx is cancelled.
func (uc *RuntimeUseCase) StartSessionReaper(ctx context.Context, interval time.Duration) {
	log := slog.Default().With("component", "session-reaper")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := uc.sessions.ReapStaleSessions(); n > 0 {
				log.Info("reaped stale sessions", "count", n)
			}
		}
	}
}

// Restart validates the GVR and triggers a rolling restart.
func (uc *RuntimeUseCase) Restart(ctx context.Context, cluster, group, version, resource, namespace, name string) error {
	gvr, err := uc.discovery.LookupResource(ctx, cluster, group, version, resource)
	if err != nil {
		return err
	}
	return uc.runtime.Restart(ctx, cluster, gvr, namespace, name)
}
