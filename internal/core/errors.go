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
