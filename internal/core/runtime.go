package core

import (
	"context"
	"io"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	SizeQueue TerminalSizer
}

// StartExecParams collects the parameters for starting an interactive
// exec session. This avoids a long parameter list on
// RuntimeUseCase.StartExec.
type StartExecParams struct {
	Cluster   string
	Namespace string
	Name      string
	Container string
	Command   []string
	TTY       bool
	Rows      uint16
	Cols      uint16
}

// PortForwardOptions holds parameters for a port-forward session.
type PortForwardOptions struct {
	Port   int32
	Stdin  io.Reader
	Stdout io.Writer
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
		return nil, &ErrInvalidInput{Field: "name", Message: "pod name is required"}
	}
	return uc.runtime.PodLogs(ctx, cluster, namespace, name, opts)
}

// StartExec creates an exec session, starts the exec in a background
// goroutine, and returns the session together with stdout and stderr
// readers that the caller can stream from.
func (uc *RuntimeUseCase) StartExec(ctx context.Context, params StartExecParams) (*ExecSession, io.ReadCloser, io.ReadCloser, error) {
	if params.Name == "" {
		return nil, nil, nil, &ErrInvalidInput{Field: "name", Message: "pod name is required"}
	}
	if len(params.Command) == 0 {
		return nil, nil, nil, &ErrInvalidInput{Field: "command", Message: "command is required"}
	}

	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	sizeQueue := NewTerminalSizeQueue()

	// Send initial terminal size.
	if params.Rows > 0 && params.Cols > 0 {
		sizeQueue.Set(params.Cols, params.Rows)
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
		defer stdinR.Close()
		defer stdoutW.Close()
		defer stderrW.Close()
		defer sizeQueue.Close()

		var stderr io.Writer
		if !params.TTY {
			stderr = stderrW
		}

		errCh <- uc.runtime.Exec(ctx, params.Cluster, params.Namespace, params.Name, ExecOptions{
			Container: params.Container,
			Command:   params.Command,
			TTY:       params.TTY,
			Stdin:     stdinR,
			Stdout:    stdoutW,
			Stderr:    stderr,
			SizeQueue: sizeQueue,
		})
	}()

	uc.sessions.PutExec(sess)
	return sess, stdoutR, stderrR, nil
}

// WriteExec writes stdin data to an active exec session. The context
// is accepted so that callers can cancel blocking pipe writes during
// graceful shutdown.
func (uc *RuntimeUseCase) WriteExec(_ context.Context, sessionID string, data []byte) error {
	sess, ok := uc.sessions.GetExec(sessionID)
	if !ok {
		return &ErrSessionNotFound{Resource: "exec-session", ID: sessionID}
	}
	_, err := sess.Stdin.Write(data)
	return err
}

// ResizeExec sends a terminal resize event to an active exec session.
func (uc *RuntimeUseCase) ResizeExec(_ context.Context, sessionID string, rows, cols uint16) error {
	sess, ok := uc.sessions.GetExec(sessionID)
	if !ok {
		return &ErrSessionNotFound{Resource: "exec-session", ID: sessionID}
	}
	sess.SizeQueue.Set(cols, rows)
	return nil
}

// CleanupExec stops an exec session and removes it from the store.
func (uc *RuntimeUseCase) CleanupExec(_ context.Context, sessionID string) {
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
		return nil, nil, &ErrInvalidInput{Field: "name", Message: "pod name is required"}
	}
	if port <= 0 || port > 65535 {
		return nil, nil, &ErrInvalidInput{Field: "port", Message: "must be between 1 and 65535"}
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
		defer dataInR.Close()
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
func (uc *RuntimeUseCase) WritePortForward(_ context.Context, sessionID string, data []byte) error {
	sess, ok := uc.sessions.GetPortForward(sessionID)
	if !ok {
		return &ErrSessionNotFound{Resource: "portforward-session", ID: sessionID}
	}
	_, err := sess.Writer.Write(data)
	return err
}

// CleanupPortForward stops a port-forward session and removes it from
// the store.
func (uc *RuntimeUseCase) CleanupPortForward(_ context.Context, sessionID string) {
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
