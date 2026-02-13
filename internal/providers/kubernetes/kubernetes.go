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
	"k8s.io/client-go/rest"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// Kubernetes is the shared foundation for discoveryClient and
// resourceRepo. It resolves cluster names to tunnel addresses and
// builds impersonated rest.Configs.
type Kubernetes struct {
	mu         sync.Mutex
	tunnel     core.TunnelProvider
	transports map[string]http.RoundTripper // keyed by cluster name
}

// New creates a Kubernetes helper bound to the given TunnelProvider.
func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel:     tunnel,
		transports: make(map[string]http.RoundTripper),
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
		return nil, apierrors.NewServiceUnavailable(err.Error())
	}

	cfg := &rest.Config{
		Host: address,
		Impersonate: rest.ImpersonationConfig{
			UserName: userInfo.Subject,
			Groups:   userInfo.Groups,
		},
	}

	rt, err := k.roundTripper(cluster, address)
	if err != nil {
		return nil, err
	}
	cfg.Transport = rt

	return cfg, nil
}

// roundTripper returns a cached HTTP transport for the given cluster.
// Transports are shared across users because impersonation is handled
// via HTTP headers, not at the transport level. This avoids creating
// new TCP connections on every request.
func (k *Kubernetes) roundTripper(cluster, address string) (http.RoundTripper, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	if rt, ok := k.transports[cluster]; ok {
		return rt, nil
	}

	rt, err := rest.TransportFor(&rest.Config{Host: address})
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	k.transports[cluster] = rt

	return rt, nil
}
