// Package kubernetes provides Kubernetes API access through the
// reverse-tunnel established by the agent. It implements
// core.DiscoveryClient and core.ResourceRepo.
//
// All requests are impersonated: the authenticated user's identity
// (subject + groups) is forwarded to the target cluster's API server
// via Kubernetes impersonation headers, so RBAC is enforced at the
// cluster level rather than at this proxy.
package kubernetes

import (
	"context"
	"net/http"
	"sync"

	"connectrpc.com/authn"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// clusterClients holds cached Kubernetes clients for a single cluster.
// All fields are created from the same tunnel address; when the
// address changes the entire entry is replaced.
type clusterClients struct {
	address   string
	rt        http.RoundTripper
	discovery *discovery.DiscoveryClient
	dynamic   *dynamic.DynamicClient
	clientset *kubernetes.Clientset
}

// Kubernetes is the shared foundation for discoveryClient and
// resourceRepo. It resolves cluster names to tunnel addresses and
// builds impersonated rest.Configs. Clients are cached per-cluster
// and invalidated when the tunnel address changes.
type Kubernetes struct {
	mu      sync.Mutex
	tunnel  core.TunnelProvider
	clients map[string]*clusterClients // keyed by cluster name
}

// New creates a Kubernetes helper bound to the given TunnelProvider.
func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel:  tunnel,
		clients: make(map[string]*clusterClients),
	}
}

// impersonationConfig builds a rest.Config that targets the given
// cluster through its tunnel address and impersonates the calling
// user extracted from the request context.
func (k *Kubernetes) impersonationConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	userInfo, ok := authn.GetInfo(ctx).(core.UserInfo)
	if !ok {
		return nil, apierrors.NewUnauthorized("user info not found in context")
	}

	address, err := k.tunnel.ResolveAddress(cluster)
	if err != nil {
		// Cluster is no longer registered; evict stale cached
		// clients and their TCP connections.
		k.evictClients(cluster)
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}

	rt, err := k.roundTripper(cluster, address)
	if err != nil {
		return nil, err
	}

	cfg := &rest.Config{
		Host: address,
		Impersonate: rest.ImpersonationConfig{
			UserName: userInfo.Subject,
			Groups:   userInfo.Groups,
		},
		Transport: rt,
	}

	return cfg, nil
}

// spdyConfig builds a rest.Config suitable for SPDY connections
// (exec, port-forward). Unlike impersonationConfig, it does NOT
// set a pre-built Transport because SPDY executors and dialers need
// to negotiate their own connection upgrade.
func (k *Kubernetes) spdyConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	userInfo, ok := authn.GetInfo(ctx).(core.UserInfo)
	if !ok {
		return nil, apierrors.NewUnauthorized("user info not found in context")
	}

	address, err := k.tunnel.ResolveAddress(cluster)
	if err != nil {
		// Cluster is no longer registered; evict stale cached
		// clients and their TCP connections.
		k.evictClients(cluster)
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}

	return &rest.Config{
		Host: address,
		Impersonate: rest.ImpersonationConfig{
			UserName: userInfo.Subject,
			Groups:   userInfo.Groups,
		},
	}, nil
}

// getClients returns cached clients for the given cluster, creating
// or replacing them if the tunnel address has changed.
func (k *Kubernetes) getClients(cluster, address string) (*clusterClients, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if entry, ok := k.clients[cluster]; ok && entry.address == address {
		return entry, nil
	}

	// Address changed or first access — create fresh clients.
	// Close idle connections on the old transport to avoid leaking
	// TCP connections to a stale tunnel address.
	if old, ok := k.clients[cluster]; ok {
		closeTransport(old.rt)
	}

	cfg := &rest.Config{Host: address}

	rt, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	cfgWithRT := &rest.Config{Host: address, Transport: rt}

	disc, err := discovery.NewDiscoveryClientForConfig(cfgWithRT)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	dyn, err := dynamic.NewForConfig(cfgWithRT)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	cs, err := kubernetes.NewForConfig(cfgWithRT)
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}

	entry := &clusterClients{
		address:   address,
		rt:        rt,
		discovery: disc,
		dynamic:   dyn,
		clientset: cs,
	}
	k.clients[cluster] = entry
	return entry, nil
}

// roundTripper returns a cached HTTP transport for the given cluster.
// If the cached transport's address does not match the current tunnel
// address (e.g. after cluster re-registration), the stale entry is
// evicted and a fresh transport is created.
//
// Transports are shared across users because impersonation is handled
// via HTTP headers, not at the transport level. This avoids creating
// new TCP connections on every request.
func (k *Kubernetes) roundTripper(cluster, address string) (http.RoundTripper, error) {
	clients, err := k.getClients(cluster, address)
	if err != nil {
		return nil, err
	}
	return clients.rt, nil
}

// evictClients removes the cached clients for the given cluster and
// closes idle TCP connections on the old transport. This is called
// when a cluster is no longer registered (e.g. after deregistration)
// to prevent connection and memory leaks.
func (k *Kubernetes) evictClients(cluster string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if old, ok := k.clients[cluster]; ok {
		closeTransport(old.rt)
		delete(k.clients, cluster)
	}
}

// closeTransport closes idle connections on the transport if it
// supports the CloseIdleConnections method (e.g. *http.Transport).
func closeTransport(rt http.RoundTripper) {
	type idleCloser interface {
		CloseIdleConnections()
	}
	if ic, ok := rt.(idleCloser); ok {
		ic.CloseIdleConnections()
	}
}
