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
	tunnel     core.TunnelProvider
	transports sync.Map // map[string]http.RoundTripper, keyed by cluster name
}

func New(tunnel core.TunnelProvider) *Kubernetes {
	return &Kubernetes{
		tunnel: tunnel,
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

	// Share the underlying transport per cluster to avoid creating new TCP
	// connections on every request. Impersonation is handled via HTTP headers,
	// so the transport can safely be shared across users.
	if t, ok := k.transports.Load(cluster); ok {
		cfg.Transport = t.(http.RoundTripper)
	} else {
		t, err := rest.TransportFor(&rest.Config{Host: address})
		if err != nil {
			return nil, apierrors.NewInternalError(err)
		}
		actual, _ := k.transports.LoadOrStore(cluster, t)
		cfg.Transport = actual.(http.RoundTripper)
	}

	return cfg, nil
}
