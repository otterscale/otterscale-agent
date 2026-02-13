package transport

import (
	"context"
	"time"

	"golang.org/x/sync/errgroup"
)

type Listener interface {
	Start(context.Context) error
	Stop(context.Context) error
}

func Serve(ctx context.Context, lis ...Listener) error {
	eg, egCtx := errgroup.WithContext(ctx)

	for _, li := range lis {
		eg.Go(func() error {
			return li.Start(egCtx)
		})

		eg.Go(func() error {
			<-egCtx.Done()

			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()

			return li.Stop(stopCtx)
		})
	}

	return eg.Wait()
}
