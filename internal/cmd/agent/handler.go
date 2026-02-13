package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync/atomic"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/otterscale/otterscale-agent/internal/core"
)

// Handler sets up the HTTP routes served by the agent. Its sole route
// is a reverse proxy to the local Kubernetes API server, protected by
// proxy-token authentication so that only requests arriving through
// the tunnel are accepted.
type Handler struct {
	proxyToken atomic.Pointer[string]
}

// NewHandler returns a new agent Handler.
func NewHandler() *Handler {
	return &Handler{}
}

// SetProxyToken updates the proxy token used to authenticate incoming
// requests. It is called after every successful tunnel registration.
func (h *Handler) SetProxyToken(token string) {
	h.proxyToken.Store(&token)
}

// Mount registers a catch-all reverse proxy to the Kubernetes API
// server on the given mux. The proxy uses the in-cluster service
// account credentials (or falls back to KUBECONFIG) and rewrites
// the Host header so that the upstream kube-apiserver recognises
// the request.
func (h *Handler) Mount(mux *http.ServeMux) error {
	config, err := h.newKubeConfig()
	if err != nil {
		return fmt.Errorf("failed to load in-cluster config: %w", err)
	}

	targetURL, err := url.Parse(config.Host)
	if err != nil {
		return fmt.Errorf("failed to parse k8s host URL: %w", err)
	}

	transport, err := rest.TransportFor(config)
	if err != nil {
		return fmt.Errorf("failed to create rest transport: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = targetURL.Host
	}
	proxy.Transport = transport

	mux.Handle("/", h.requireProxyToken(proxy))
	return nil
}

// requireProxyToken returns middleware that validates the proxy token
// header on every request. It rejects requests that do not carry a
// valid token and strips the header before forwarding to prevent it
// from leaking to the upstream Kubernetes API server.
func (h *Handler) requireProxyToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		expected := h.proxyToken.Load()
		if expected == nil || *expected == "" {
			http.Error(w, "proxy not ready", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get(core.ProxyTokenHeader) != *expected {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		r.Header.Del(core.ProxyTokenHeader)
		next.ServeHTTP(w, r)
	})
}

// newKubeConfig loads the Kubernetes client configuration. It first
// attempts the in-cluster config (service account token); if that
// fails (e.g. running outside a pod) it falls back to the KUBECONFIG
// environment variable.
func (h *Handler) newKubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}

	slog.Warn("failed to load in-cluster config, falling back to KUBECONFIG environment variable")

	kubeconfigEnv := os.Getenv("KUBECONFIG")
	if kubeconfigEnv == "" {
		return nil, errors.New("KUBECONFIG environment variable is not set")
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfigEnv)
}
