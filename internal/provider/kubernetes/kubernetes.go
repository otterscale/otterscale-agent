package kubernetes

import (
	"context"
	"fmt"

	"k8s.io/client-go/rest"

	"github.com/otterscale/otterscale-agent/internal/core"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
)

type Kubernetes struct {
	tunnel core.TunnelRepo
}

func New(tunnel core.TunnelRepo) *Kubernetes {
	return &Kubernetes{
		tunnel: tunnel,
	}
}

func (k *Kubernetes) impersonationConfig(ctx context.Context, cluster string) (*rest.Config, error) {
	username, ok := impersonation.GetSubject(ctx)
	if !ok {
		return nil, fmt.Errorf("username not found in context")
	}

	impersonate := rest.ImpersonationConfig{
		UserName: username,
		Groups:   []string{"system:authenticated"},
	}

	host, err := k.tunnel.GetHost(cluster)
	if err != nil {
		return nil, fmt.Errorf("tunnel unavailable: %w", err)
	}

	return &rest.Config{Host: host, Impersonate: impersonate}, nil
}
