package agent

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	utilproxy "k8s.io/apimachinery/pkg/util/proxy"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Handler sets up the HTTP routes served by the agent. Its sole route
// is a reverse proxy to the local Kubernetes API server.
type Handler struct{}

// NewHandler returns a new agent Handler.
func NewHandler() *Handler {
	return &Handler{}
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

	proxy := utilproxy.NewUpgradeAwareHandler(targetURL, transport, false, false, &errorResponder{})
	mux.Handle("/", proxy)
	return nil
}

// errorResponder implements k8s.io/apimachinery/pkg/util/proxy.ErrorResponder.
// It logs errors and returns a 502 Bad Gateway response to the client.
type errorResponder struct{}

func (r *errorResponder) Error(w http.ResponseWriter, _ *http.Request, err error) {
	slog.Error("proxy error", "error", err)
	http.Error(w, "bad gateway", http.StatusBadGateway)
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

	return clientcmd.BuildConfigFromFlags("", clientcmd.RecommendedHomeFile)
}
