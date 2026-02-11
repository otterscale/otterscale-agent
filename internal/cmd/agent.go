package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	chclient "github.com/jpillora/chisel/client"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
)

// TODO: refactor to domain-driven architecture
func NewAgent(conf *config.Config) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Start agent that connects to server and executes requests in-cluster",
		Example: "otterscale agent --cluster=default --tunnel-server-url=https://server.otterscale.io --tunnel-auth=user:pass --tunnel-fingerprint=... --tunnel-port=16598",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgent(cmd.Context(), conf)
		},
	}

	if err := conf.BindFlags(cmd.Flags(), config.AgentOptions); err != nil {
		return nil, err
	}

	return cmd, nil
}

// runAgent contains the main execution logic for the agent.
func runAgent(ctx context.Context, conf *config.Config) error {
	var (
		debugEnabled = conf.AgentDebugEnabled()
		kubeAPIURL   = conf.AgentDebugKubeAPIURL()
		serverURL    = conf.AgentTunnelServerURL()
		tunnelPort   = conf.AgentTunnelPort()
	)

	// 1. Prepare the reverse proxy for the Kubernetes API Server.
	var k8sProxy *httputil.ReverseProxy
	var err error
	if debugEnabled {
		k8sProxy, err = newKubeAPIProxyFromURL(kubeAPIURL)
	} else {
		k8sProxy, err = newKubeAPIProxy()
	}
	if err != nil {
		return fmt.Errorf("failed to create k8s proxy: %w", err)
	}

	// 2. Listen on a random local port (target for the tunnel).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen on local port: %w", err)
	}
	localProxyPort := listener.Addr().(*net.TCPAddr).Port

	// 3. Prepare the Chisel Reverse Tunnel Client.
	tunnelClient, err := newTunnelClient(conf, localProxyPort)
	if err != nil {
		return fmt.Errorf("failed to create tunnel client: %w", err)
	}

	// 4. Start services (concurrently).
	errChan := make(chan error, 1)

	// Goroutine: Run the local Auth-Proxy Server.
	go func() {
		slog.Info("Local K8s Auth-Proxy listening", "port", localProxyPort)
		if err := http.Serve(listener, k8sProxy); err != nil {
			// http.Serve returns ErrServerClosed on normal shutdown; simplified handling here.
			errChan <- fmt.Errorf("local proxy server failed: %w", err)
		}
	}()

	// Goroutine: Run the Chisel Agent connection.
	go func() {
		slog.Info("Agent connecting to server", "server", serverURL, "tunnelPort", tunnelPort)
		if err := tunnelClient.Start(ctx); err != nil {
			errChan <- fmt.Errorf("agent failed to start: %w", err)
			return
		}

		if err := tunnelClient.Wait(); err != nil {
			errChan <- fmt.Errorf("agent connection lost: %w", err)
		}
	}()

	// 5. Wait for error or Context cancellation.
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		slog.Info("Shutting down agent...")
		// Logic to explicitly close tunnelClient could be added here, but context cancellation usually triggers it.
		return nil
	}
}

// newTunnelClient creates a Chisel Client instance.
func newTunnelClient(conf *config.Config, localPort int) (*chclient.Client, error) {
	var (
		cluster     = conf.AgentCluster()
		serverURL   = conf.AgentTunnelServerURL()
		auth        = conf.AgentTunnelAuth()
		fingerprint = conf.AgentTunnelFingerprint()
		tunnelPort  = conf.AgentTunnelPort()
		timeout     = conf.AgentTunnelTimeout()
	)

	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %w", err)
	}

	headers := http.Header{}
	headers.Set("X-Cluster", cluster)
	headers.Set("X-Agent-ID", hostname) // Usually the Pod Name

	chiselConfig := &chclient.Config{
		Server:      serverURL,
		Auth:        auth,
		Fingerprint: fingerprint,
		Headers:     headers,
		Remotes: []string{
			// Forward traffic from the remote TunnelPort to the local port.
			fmt.Sprintf("R:127.0.0.1:%d:127.0.0.1:%d", tunnelPort, localPort),
		},
		KeepAlive:     timeout,
		MaxRetryCount: -1, // Infinite retries
	}

	return chclient.NewClient(chiselConfig)
}

// newKubeAPIProxy creates a reverse proxy pointing to the in-cluster API Server.
// It relies on rest.TransportFor to handle automatic token rotation from the
// projected ServiceAccount token file, instead of injecting a static BearerToken.
func newKubeAPIProxy() (*httputil.ReverseProxy, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load in-cluster config: %w", err)
	}

	targetURL, err := url.Parse(config.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse k8s host URL: %w", err)
	}

	// rest.TransportFor handles BearerTokenFile rotation + TLS automatically.
	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create rest transport: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = targetURL.Scheme
		req.URL.Host = targetURL.Host
		req.Host = targetURL.Host
		// Do NOT set Authorization header: the transport's RoundTripper
		// automatically injects the latest token from BearerTokenFile.
	}
	proxy.Transport = transport

	return proxy, nil
}

// newKubeAPIProxyFromURL creates a simple reverse proxy to an explicit URL.
// This is used in integration tests where rest.InClusterConfig is unavailable
// and the target is a fake K8s API server.
func newKubeAPIProxyFromURL(rawURL string) (*httputil.ReverseProxy, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid kube-api-url: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = target.Scheme
		req.URL.Host = target.Host
		req.Host = target.Host
	}

	return proxy, nil
}
