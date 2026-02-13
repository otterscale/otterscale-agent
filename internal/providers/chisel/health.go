package chisel

import (
	"context"
	"net"
	"strconv"
	"time"
)

const (
	// healthCheckInterval is how often the health check probes every
	// registered cluster's tunnel endpoint.
	healthCheckInterval = 15 * time.Second

	// healthDialTimeout is the TCP dial timeout used when probing a
	// cluster's tunnel endpoint.
	healthDialTimeout = 2 * time.Second

	// healthFailThreshold is the number of consecutive probe failures
	// required before a cluster is automatically deregistered.
	healthFailThreshold = 3
)

// HealthCheckListener wraps the Service's health check loop as a
// transport.Listener so that it participates in the same errgroup
// lifecycle as the HTTP and tunnel servers. This ensures panics are
// caught and graceful shutdown is coordinated.
type HealthCheckListener struct {
	service *Service
}

// NewHealthCheckListener returns a listener that runs periodic health
// checks against registered tunnel endpoints.
func NewHealthCheckListener(service *Service) *HealthCheckListener {
	return &HealthCheckListener{service: service}
}

// Start runs the health check loop, blocking until ctx is cancelled.
func (h *HealthCheckListener) Start(ctx context.Context) error {
	h.service.runHealthCheck(ctx)
	return nil
}

// Stop is a no-op; the health check loop exits when its context is
// cancelled.
func (h *HealthCheckListener) Stop(_ context.Context) error {
	return nil
}

// clusterSnapshot returns a copy of the cluster-to-host mapping so
// that health checks can iterate without holding the lock.
func (s *Service) clusterSnapshot() map[string]string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snapshot := make(map[string]string, len(s.clusters))
	for name, entry := range s.clusters {
		snapshot[name] = entry.Host
	}
	return snapshot
}

// runHealthCheck periodically probes every registered cluster's
// tunnel endpoint via TCP dial. Clusters that fail healthFailThreshold
// consecutive probes are automatically deregistered.
//
// The method blocks until ctx is cancelled.
func (s *Service) runHealthCheck(ctx context.Context) {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	dialer := net.Dialer{Timeout: healthDialTimeout}
	failCounts := make(map[string]int)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkClusters(ctx, dialer, failCounts)
		}
	}
}

// checkClusters performs a single round of health checks across all
// registered clusters. failCounts is mutated in place to track
// consecutive failures per cluster.
func (s *Service) checkClusters(ctx context.Context, dialer net.Dialer, failCounts map[string]int) {
	snapshot := s.clusterSnapshot()

	// Clean up failCounts for clusters that are no longer registered.
	for name := range failCounts {
		if _, ok := snapshot[name]; !ok {
			delete(failCounts, name)
		}
	}

	for cluster, host := range snapshot {
		addr := net.JoinHostPort(host, strconv.Itoa(tunnelPort))
		conn, err := dialer.DialContext(ctx, "tcp", addr)
		if err == nil {
			_ = conn.Close()
			if failCounts[cluster] > 0 {
				s.log.Debug("cluster recovered", "cluster", cluster)
			}
			delete(failCounts, cluster)
			continue
		}

		// Don't count context cancellation as a probe failure.
		if ctx.Err() != nil {
			return
		}

		failCounts[cluster]++
		s.log.Debug("probe failed",
			"cluster", cluster,
			"address", addr,
			"consecutive_failures", failCounts[cluster],
			"error", err,
		)

		if failCounts[cluster] >= healthFailThreshold {
			s.log.Info("deregistering disconnected cluster",
				"cluster", cluster,
				"consecutive_failures", failCounts[cluster],
			)
			s.DeregisterCluster(cluster)
			delete(failCounts, cluster)
		}
	}
}
