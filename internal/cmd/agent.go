package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	chclient "github.com/jpillora/chisel/client"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// TODO: refactor to domain-driven architecture
func NewAgentCommand(conf *config.Config) (*cobra.Command, error) {
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
		serverURL  = conf.AgentTunnelServerURL()
		tunnelPort = conf.AgentTunnelPort()
	)

	// 1. Prepare the reverse proxy for the Kubernetes API Server.
	k8sProxy, err := newKubeAPIProxy()
	if err != nil {
		return fmt.Errorf("failed to create k8s proxy: %w", err)
	}

	// 2. Register this agent with the server (obtains the fingerprint dynamically).
	// fingerprint, err := registerWithServer(conf)
	// if err != nil {
	// 	return fmt.Errorf("failed to register with server: %w", err)
	// }
	fingerprint := conf.AgentTunnelFingerprint()
	slog.Info("Registered with server", "fingerprint", fingerprint)

	// 3. Listen on a random local port (target for the tunnel).
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("failed to listen on local port: %w", err)
	}
	localProxyPort := listener.Addr().(*net.TCPAddr).Port

	// 4. Prepare the Chisel Reverse Tunnel Client.
	tunnelClient, err := newTunnelClient(conf, localProxyPort, fingerprint)
	if err != nil {
		return fmt.Errorf("failed to create tunnel client: %w", err)
	}

	// 5. Start services (concurrently).
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

	// 6. Wait for error or Context cancellation.
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
// The fingerprint is obtained dynamically from the registration response.
// If the agent has a statically-configured fingerprint, it takes precedence.
func newTunnelClient(conf *config.Config, localPort int, registeredFingerprint string) (*chclient.Client, error) {
	var (
		cluster    = conf.AgentCluster()
		serverURL  = conf.AgentTunnelServerURL()
		auth       = conf.AgentTunnelAuth()
		tunnelPort = conf.AgentTunnelPort()
		timeout    = conf.AgentTunnelTimeout()
	)

	// Prefer static config fingerprint if set; otherwise use the one from registration.
	fingerprint := conf.AgentTunnelFingerprint()
	if fingerprint == "" {
		fingerprint = registeredFingerprint
	}

	if fingerprint == "" {
		return nil, fmt.Errorf("fingerprint is required")
	}

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

// registerWithServer sends a registration request to the tunnel server so
// the server creates the chisel user and port mapping for this cluster.
// Returns the server fingerprint for tunnel verification.
func registerWithServer(conf *config.Config) (string, error) {
	serverURL := conf.AgentTunnelServerURL()
	auth := conf.AgentTunnelAuth() // "user:pass"
	cluster := conf.AgentCluster()
	tunnelPort := conf.AgentTunnelPort()

	body, err := json.Marshal(map[string]any{
		"cluster":     cluster,
		"tunnel_port": tunnelPort,
	})
	if err != nil {
		return "", fmt.Errorf("failed to marshal registration body: %w", err)
	}

	req, err := http.NewRequest(http.MethodPost, serverURL+"/v1/register", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("failed to create registration request: %w", err)
	}

	parts := strings.SplitN(auth, ":", 2)
	if len(parts) != 2 {
		return "", fmt.Errorf("invalid tunnel auth format, expected user:pass")
	}
	req.SetBasicAuth(parts[0], parts[1])
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("registration request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registration failed with status %d", resp.StatusCode)
	}

	var result struct {
		Fingerprint string `json:"fingerprint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode registration response: %w", err)
	}

	return result.Fingerprint, nil
}

// newKubeAPIProxy creates a reverse proxy pointing to the in-cluster API Server.
// It relies on rest.TransportFor to handle automatic token rotation from the
// projected ServiceAccount token file, instead of injecting a static BearerToken.
func newKubeAPIProxy() (*httputil.ReverseProxy, error) {
	config, err := loadKubeConfig()
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

func loadKubeConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}

	// fallback to kubeconfig file
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		home, _ := os.UserHomeDir()
		if home != "" {
			kubeconfig = home + "/.kube/config"
		}
	}

	return clientcmd.BuildConfigFromFlags("", kubeconfig)
}
