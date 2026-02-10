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
	"time"

	chclient "github.com/jpillora/chisel/client"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
)

// AgentConfig holds the configuration parameters required to run the agent.
type AgentConfig struct {
	Cluster     string
	Server      string
	Auth        string
	Fingerprint string
	TunnelPort  int
	Timeout     time.Duration
}

// Validate checks if the required parameters are present.
func (c *AgentConfig) Validate() error {
	if c.Cluster == "" || c.Server == "" || c.Auth == "" || c.Fingerprint == "" {
		return fmt.Errorf("missing required flags: cluster, server, auth, and fingerprint")
	}
	if c.TunnelPort == 0 {
		return fmt.Errorf("tunnel-port is required")
	}
	return nil
}

func NewAgent() *cobra.Command {
	cfg := &AgentConfig{}

	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Start agent that connects to server and executes requests in-cluster",
		Example: "otterscale agent --cluster=dev --server=https://server.otterscale.io --auth=user:pass --fingerprint=... --tunnel-port=16598",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := cfg.Validate(); err != nil {
				return err
			}
			return runAgent(cmd.Context(), cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&cfg.Cluster, "cluster", "", "Cluster name (e.g. dev)")
	f.StringVar(&cfg.Server, "server", "", "Server URL (e.g. https://server.otterscale.io)")
	f.StringVar(&cfg.Auth, "auth", "", "Basic auth (user:pass)")
	f.StringVar(&cfg.Fingerprint, "fingerprint", "", "Server SSH fingerprint")
	f.IntVar(&cfg.TunnelPort, "tunnel-port", 0, "The dedicated remote port on server for this cluster (e.g. 16598)")
	f.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "Connection timeout")

	return cmd
}

// runAgent contains the main execution logic for the agent.
func runAgent(ctx context.Context, cfg *AgentConfig) error {
	// 1. Prepare the reverse proxy for the Kubernetes API Server.
	k8sProxy, err := newKubeAPIProxy()
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
	tunnelClient, err := newTunnelClient(cfg, localProxyPort)
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
		slog.Info("Agent connecting to server", "server", cfg.Server, "tunnelPort", cfg.TunnelPort)
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
func newTunnelClient(cfg *AgentConfig, localPort int) (*chclient.Client, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("failed to get hostname: %w", err)
	}

	headers := http.Header{}
	headers.Set("X-Cluster", cfg.Cluster)
	headers.Set("X-Agent-ID", hostname) // Usually the Pod Name

	chiselConfig := &chclient.Config{
		Server:      cfg.Server,
		Auth:        cfg.Auth,
		Fingerprint: cfg.Fingerprint,
		Headers:     headers,
		Remotes: []string{
			// Forward traffic from the remote TunnelPort to the local port.
			fmt.Sprintf("R:127.0.0.1:%d:127.0.0.1:%d", cfg.TunnelPort, localPort),
		},
		KeepAlive:     cfg.Timeout,
		MaxRetryCount: -1, // Infinite retries
	}

	return chclient.NewClient(chiselConfig)
}

// newKubeAPIProxy creates a reverse proxy pointing to the in-cluster API Server.
// It automatically injects the ServiceAccount Token.
func newKubeAPIProxy() (*httputil.ReverseProxy, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load in-cluster config: %w", err)
	}

	targetURL, err := url.Parse(config.Host)
	if err != nil {
		return nil, fmt.Errorf("failed to parse k8s host URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		// Inject Bearer Token for authentication.
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", config.BearerToken))
		// Modify Host Header to match the target API Server.
		req.Host = targetURL.Host
	}

	// Use K8s Transport settings (handles TLS CA certificates, etc.).
	transport, err := rest.TransportFor(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create rest transport: %w", err)
	}
	proxy.Transport = transport

	return proxy, nil
}
