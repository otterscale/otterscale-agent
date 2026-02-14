// Package cache provides TTL-based caching infrastructure for
// Kubernetes discovery data. It lives in the providers layer because
// caching is an infrastructure concern — the domain layer
// (internal/core) only defines the SchemaResolver interface.
package cache

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/kube-openapi/pkg/validation/spec"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// DefaultTTL is the default TTL for cached OpenAPI schemas and
// server versions. Exported so that the DI layer can use it when
// constructing a DiscoveryCache.
const DefaultTTL = 10 * time.Minute

// DiscoveryCache provides TTL-based caching with singleflight
// deduplication for OpenAPI schemas and Kubernetes server versions.
// It implements core.SchemaResolver and reduces redundant discovery
// API calls when multiple concurrent requests target the same cluster.
type DiscoveryCache struct {
	discovery core.DiscoveryClient
	ttl       time.Duration

	mu             sync.RWMutex
	schemaCache    map[string]*schemaCacheEntry
	versionCache   map[string]*versionCacheEntry
	schemaFlights  singleflight.Group
	versionFlights singleflight.Group
}

// schemaCacheEntry pairs a cached schema with its expiration time.
type schemaCacheEntry struct {
	schema    *spec.Schema
	expiresAt time.Time
}

// versionCacheEntry pairs a cached server version with its expiration.
type versionCacheEntry struct {
	version   *version.Info
	expiresAt time.Time
}

// singleflightFetchTimeout is the maximum time a cache-miss fetch is
// allowed to run. It uses context.WithoutCancel so that a single
// caller's cancellation does not fail all singleflight waiters.
const singleflightFetchTimeout = 30 * time.Second

// NewDiscoveryCache returns a DiscoveryCache that wraps the given
// DiscoveryClient and caches results for the specified TTL.
func NewDiscoveryCache(discovery core.DiscoveryClient, ttl time.Duration) *DiscoveryCache {
	return &DiscoveryCache{
		discovery:    discovery,
		ttl:          ttl,
		schemaCache:  make(map[string]*schemaCacheEntry),
		versionCache: make(map[string]*versionCacheEntry),
	}
}

// ResolveSchema fetches the OpenAPI schema for the given GVK. Results
// are cached for the configured TTL and concurrent requests for the
// same key are deduplicated via singleflight.
func (c *DiscoveryCache) ResolveSchema(
	ctx context.Context,
	cluster, group, version, kind string,
) (*spec.Schema, error) {
	key := c.schemaCacheKey(cluster, group, version, kind)

	c.mu.RLock()
	entry, ok := c.schemaCache[key]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.schema, nil
	}

	v, err, _ := c.schemaFlights.Do(key, func() (any, error) {
		// Use a non-cancellable context with its own timeout so that
		// a single caller's cancellation does not fail all waiters
		// sharing this singleflight key.
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), singleflightFetchTimeout)
		defer cancel()

		resolved, err := c.discovery.ResolveSchema(fetchCtx, cluster, group, version, kind)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.schemaCache[key] = &schemaCacheEntry{
			schema:    resolved,
			expiresAt: time.Now().Add(c.ttl),
		}
		c.mu.Unlock()

		return resolved, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*spec.Schema), nil
}

// ServerVersion returns the cached Kubernetes version for the given
// cluster. Results are cached for the configured TTL and concurrent
// requests are deduplicated via singleflight.
func (c *DiscoveryCache) ServerVersion(ctx context.Context, cluster string) (*version.Info, error) {
	c.mu.RLock()
	entry, ok := c.versionCache[cluster]
	c.mu.RUnlock()

	if ok && time.Now().Before(entry.expiresAt) {
		return entry.version, nil
	}

	v, err, _ := c.versionFlights.Do(cluster, func() (any, error) {
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), singleflightFetchTimeout)
		defer cancel()

		info, err := c.discovery.ServerVersion(fetchCtx, cluster)
		if err != nil {
			return nil, err
		}

		c.mu.Lock()
		c.versionCache[cluster] = &versionCacheEntry{
			version:   info,
			expiresAt: time.Now().Add(c.ttl),
		}
		c.mu.Unlock()

		return info, nil
	})
	if err != nil {
		return nil, err
	}

	return v.(*version.Info), nil
}

// schemaCacheKey builds a cache key from the cluster/group/version/kind tuple.
func (c *DiscoveryCache) schemaCacheKey(cluster, group, version, kind string) string {
	return strings.Join([]string{cluster, group, version, kind}, "/")
}

// StartEvictionLoop launches a background goroutine that periodically
// removes expired cache entries. This prevents memory leaks when
// clusters go offline or schemas are no longer queried. It blocks
// until ctx is cancelled.
func (c *DiscoveryCache) StartEvictionLoop(ctx context.Context, interval time.Duration) {
	log := slog.Default().With("component", "discovery-cache-evictor")
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			before := len(c.schemaCache) + len(c.versionCache)
			c.evictExpired()
			after := len(c.schemaCache) + len(c.versionCache)
			c.mu.Unlock()

			if evicted := before - after; evicted > 0 {
				log.Info("evicted expired cache entries", "count", evicted)
			}
		}
	}
}

// evictExpired removes expired entries from the schema and version
// caches. Must be called with mu held for writing.
func (c *DiscoveryCache) evictExpired() {
	now := time.Now()
	for key, entry := range c.schemaCache {
		if now.After(entry.expiresAt) {
			delete(c.schemaCache, key)
		}
	}
	for key, entry := range c.versionCache {
		if now.After(entry.expiresAt) {
			delete(c.versionCache, key)
		}
	}
}
