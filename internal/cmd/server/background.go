package server

import (
	"context"
	"time"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/providers/cache"
)

// sessionReapInterval is the interval at which the session reaper
// scans for and removes stale sessions.
const sessionReapInterval = 30 * time.Second

// cacheEvictionInterval is the interval at which the discovery cache
// evictor removes expired schema and version entries.
const cacheEvictionInterval = 5 * time.Minute

// ProvideBackgroundListeners constructs the background transport
// listeners (session reaper, cache evictor) that participate in the
// server's managed lifecycle. Centralising construction here keeps the
// Server struct free of concrete infrastructure types.
func ProvideBackgroundListeners(runtime *core.RuntimeUseCase, discoveryCache *cache.DiscoveryCache) BackgroundListeners {
	return BackgroundListeners{
		&sessionReaperListener{runtime: runtime},
		&cacheEvictorListener{cache: discoveryCache},
	}
}

// sessionReaperListener adapts RuntimeUseCase.StartSessionReaper to
// the transport.Listener interface so it participates in the managed
// lifecycle alongside other servers.
type sessionReaperListener struct {
	runtime *core.RuntimeUseCase
}

func (l *sessionReaperListener) Start(ctx context.Context) error {
	l.runtime.StartSessionReaper(ctx, sessionReapInterval)
	return nil
}

func (l *sessionReaperListener) Stop(_ context.Context) error {
	return nil // reaper stops when its context is cancelled
}

// cacheEvictorListener adapts DiscoveryCache.StartEvictionLoop to
// the transport.Listener interface so it participates in the managed
// lifecycle alongside other servers.
type cacheEvictorListener struct {
	cache *cache.DiscoveryCache
}

func (l *cacheEvictorListener) Start(ctx context.Context) error {
	l.cache.StartEvictionLoop(ctx, cacheEvictionInterval)
	return nil
}

func (l *cacheEvictorListener) Stop(_ context.Context) error {
	return nil // evictor stops when its context is cancelled
}
