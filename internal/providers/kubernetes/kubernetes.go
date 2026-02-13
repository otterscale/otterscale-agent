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

type Kubernetes struct {
	mu         sync.Mutex
	tunnel     core.TunnelProvider
	transports map[string]http.RoundTripper // keyed by cluster name
}

func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel:     tunnel,
		transports: make(map[string]http.RoundTripper),
	}
}

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

// Share the underlying transport per cluster to avoid creating new TCP
// connections on every request. Impersonation is handled via HTTP headers,
// so the transport can safely be shared across users.
func (k *Kubernetes) roundTripper(cluster, address string) (http.RoundTripper, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	transport, ok := k.transports[cluster]
	if ok {
		return transport, nil
	}

	t, err := rest.TransportFor(&rest.Config{Host: address})
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	k.transports[cluster] = t

	return t, nil
}
