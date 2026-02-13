package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

type Handler struct{}

func NewHandler() *Handler {
	return &Handler{}
}

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

	mux.Handle("/", proxy)
	return nil
}

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
