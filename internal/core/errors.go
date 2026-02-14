package core

import "fmt"

// ErrClusterNotFound indicates that the requested cluster is not
// registered with the tunnel provider.
type ErrClusterNotFound struct {
	Cluster string
}

func (e *ErrClusterNotFound) Error() string {
	return fmt.Sprintf("cluster %s not registered", e.Cluster)
}

// ErrNotReady indicates that a required subsystem (e.g. the tunnel
// server) has not been initialized yet.
type ErrNotReady struct {
	Subsystem string
}

func (e *ErrNotReady) Error() string {
	return fmt.Sprintf("%s not initialized", e.Subsystem)
}

// ErrInvalidInput indicates a domain-level input validation failure.
// It replaces the use of k8s apierrors.NewBadRequest in the domain
// layer, keeping the core package free of infrastructure error types.
type ErrInvalidInput struct {
	Field   string
	Message string
}

func (e *ErrInvalidInput) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("invalid %s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ErrSessionNotFound indicates that a requested session (exec or
// port-forward) does not exist in the session store.
type ErrSessionNotFound struct {
	Resource string
	ID       string
}

func (e *ErrSessionNotFound) Error() string {
	return fmt.Sprintf("%s %q not found", e.Resource, e.ID)
}
