// Package transport coordinates the lifecycle of multiple server
// components (HTTP, tunnel, health-check, etc.) using an errgroup.
package transport

import (
	"context"
	"errors"
	"time"

	"golang.org/x/sync/errgroup"
)

// shutdownTimeout is the maximum time allowed for graceful shutdown
// of each listener after the context is cancelled.
const shutdownTimeout = 15 * time.Second

// Listener defines a component that can be started and stopped as
// part of the server lifecycle. Start should block until the
// component finishes or ctx is cancelled. Stop performs graceful
// shutdown within the provided context deadline.
type Listener interface {
	Start(context.Context) error
	Stop(context.Context) error
}

// Serve runs all listeners concurrently and coordinates graceful
// shutdown. When ctx is cancelled or any listener returns an error,
// all listeners are started first, then a single goroutine waits for
// the derived context to be done and calls Stop on every listener.
// This avoids calling Stop before Start has had a chance to run.
func Serve(ctx context.Context, lis ...Listener) error {
	eg, egCtx := errgroup.WithContext(ctx)

	for _, li := range lis {
		eg.Go(func() error {
			return li.Start(egCtx)
		})
	}

	// A single goroutine waits for the derived context to be
	// cancelled (either parent ctx or a listener failure), then
	// stops all listeners sequentially.
	eg.Go(func() error {
		<-egCtx.Done()

		stopCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()

		var errs []error
		for _, li := range lis {
			if err := li.Stop(stopCtx); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	})

	return eg.Wait()
}
