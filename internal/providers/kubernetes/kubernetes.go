package kubernetes

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"k8s.io/client-go/rest"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/identity"
)

type Kubernetes struct {
	tunnel     core.TunnelProvider
	transports sync.Map // map[string]http.RoundTripper, keyed by cluster name
}

func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel: tunnel,
	}
}

func (k *Kubernetes) impersonationConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	userInfo, ok := identity.GetUserInfo(ctx)
	if !ok {
		return nil, fmt.Errorf("user info not found in context")
	}

	address, err := k.tunnel.GetTunnelAddress(cluster)
	if err != nil {
		return nil, fmt.Errorf("tunnel unavailable: %w", err)
	}

	cfg := &rest.Config{
		Host: address,
		Impersonate: rest.ImpersonationConfig{
			UserName: userInfo.Subject,
			Groups:   userInfo.Groups,
		},
	}

	// Share the underlying transport per cluster to avoid creating new TCP
	// connections on every request. Impersonation is handled via HTTP headers,
	// so the transport can safely be shared across users.
	if t, ok := k.transports.Load(cluster); ok {
		cfg.Transport = t.(http.RoundTripper)
	} else {
		t, err := rest.TransportFor(&rest.Config{Host: address})
		if err != nil {
			return nil, fmt.Errorf("failed to create transport for cluster %s: %w", cluster, err)
		}
		actual, _ := k.transports.LoadOrStore(cluster, t)
		cfg.Transport = actual.(http.RoundTripper)
	}

	return cfg, nil
}
