package kubernetes

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/identity"
)

type Kubernetes struct {
	tunnel core.TunnelProvider
}

func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel: tunnel,
	}
}

func (k *Kubernetes) impersonationConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	user, ok := identity.GetUser(ctx)
	if !ok {
		return nil, fmt.Errorf("username not found in context")
	}

	impersonate := rest.ImpersonationConfig{
		UserName: user,
		Groups:   []string{"system:authenticated"},
	}

	address, err := k.tunnel.GetTunnelAddress(cluster)
	if err != nil {
		return nil, fmt.Errorf("tunnel unavailable: %w", err)
	}

	return &rest.Config{Host: address, Impersonate: impersonate}, nil
}
