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
	"strings"
	"time"

	chclient "github.com/jpillora/chisel/client"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/spf13/cobra"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	pb "github.com/otterscale/otterscale-agent/api/fleet/v1"
	"github.com/otterscale/otterscale-agent/api/fleet/v1/pbconnect"
)

const (
	sessionRetryBaseDelay         = 1 * time.Second
	sessionRetryMaxDelay          = 30 * time.Second
	tunnelReconnectBeforeRegister = 3
	tunnelReconnectMaxInterval    = 10 * time.Second
)

type tunnelSession interface {
	Start(ctx context.Context) error
	Wait() error
	Close() error
}

type registerFunc func(context.Context, *config.Config) (registrationResult, error)

type tunnelClientFactory func(*config.Config, int, registrationResult) (tunnelSession, error)

// TODO: refactor to domain-driven architecture
func NewAgentCommand(conf *config.Config) (*cobra.Command, error) {
	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Start agent that connects to server and executes requests in-cluster",
		Example: "otterscale agent --cluster=default --server-url=https://api.otterscale.io --tunnel-server-url=https://tunnel.otterscale.io",
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
	serverURL := conf.AgentTunnelServerURL()

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

	// 3. Start services (concurrently).
	errChan := make(chan error, 1)

	// Goroutine: Run the local Auth-Proxy Server.
	go func() {
		slog.Info("Local K8s Auth-Proxy listening", "port", localProxyPort)
		if err := http.Serve(listener, k8sProxy); err != nil {
			// http.Serve returns ErrServerClosed on normal shutdown; simplified handling here.
			errChan <- fmt.Errorf("local proxy server failed: %w", err)
		}
	}()

	// Goroutine: Run the Chisel Agent connection manager.
	go func() {
		if err := runTunnelSessionManager(ctx, conf, localProxyPort, serverURL); err != nil {
			errChan <- err
		}
	}()

	// 4. Wait for error or Context cancellation.
	select {
	case err := <-errChan:
		return err
	case <-ctx.Done():
		slog.Info("Shutting down agent...")
		// Logic to explicitly close tunnelClient could be added here, but context cancellation usually triggers it.
		return nil
	}
}

func runTunnelSessionManager(ctx context.Context, conf *config.Config, localProxyPort int, serverURL string) error {
	return runTunnelSessionManagerWithDeps(
		ctx,
		conf,
		localProxyPort,
		serverURL,
		registerWithServer,
		newTunnelClient,
	)
}

func runTunnelSessionManagerWithDeps(
	ctx context.Context,
	conf *config.Config,
	localProxyPort int,
	serverURL string,
	register registerFunc,
	newClient tunnelClientFactory,
) error {
	retryDelay := sessionRetryBaseDelay

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		reg, err := register(ctx, conf)
		if err != nil {
			slog.Warn("Failed to register with server, retrying", "error", err, "retry_in", retryDelay)
			if !sleepOrDone(ctx, retryDelay) {
				return nil
			}
			retryDelay = nextRetryDelay(retryDelay)
			continue
		}
		retryDelay = sessionRetryBaseDelay

		slog.Info("Registered with server", "fingerprint", reg.Fingerprint, "endpoint", reg.Endpoint)

		tunnelClient, err := newClient(conf, localProxyPort, reg)
		if err != nil {
			slog.Warn("Failed to create tunnel client, retrying registration", "error", err, "retry_in", retryDelay)
			if !sleepOrDone(ctx, retryDelay) {
				return nil
			}
			retryDelay = nextRetryDelay(retryDelay)
			continue
		}

		slog.Info("Agent connecting to server", "server", serverURL, "endpoint", reg.Endpoint)
		if err := tunnelClient.Start(ctx); err != nil {
			_ = tunnelClient.Close()
			if isAuthFailure(err) {
				slog.Warn("Agent authentication failed, re-registering immediately", "error", err)
				continue
			}
			slog.Warn("Agent failed to start tunnel, retrying", "error", err, "retry_in", retryDelay)
			if !sleepOrDone(ctx, retryDelay) {
				return nil
			}
			retryDelay = nextRetryDelay(retryDelay)
			continue
		}

		err = tunnelClient.Wait()
		_ = tunnelClient.Close()

		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			// The client stops after bounded reconnect attempts; get fresh credentials and re-register.
			slog.Warn("Tunnel session ended, re-registering")
			retryDelay = sessionRetryBaseDelay
			continue
		}
		if isAuthFailure(err) {
			slog.Warn("Agent authentication failed, re-registering immediately", "error", err)
			retryDelay = sessionRetryBaseDelay
			continue
		}

		slog.Warn("Agent connection lost, retrying registration", "error", err, "retry_in", retryDelay)
		if !sleepOrDone(ctx, retryDelay) {
			return nil
		}
		retryDelay = nextRetryDelay(retryDelay)
	}
}

func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "auth failed") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid auth")
}

func sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func nextRetryDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return sessionRetryBaseDelay
	}
	next := current * 2
	if next > sessionRetryMaxDelay {
		return sessionRetryMaxDelay
	}
	return next
}

// newTunnelClient creates a Chisel Client instance.
// The fingerprint is obtained dynamically from the registration response.
// If the agent has a statically-configured fingerprint, it takes precedence.
func newTunnelClient(conf *config.Config, localPort int, reg registrationResult) (tunnelSession, error) {
	var (
		cluster   = conf.AgentCluster()
		serverURL = conf.AgentTunnelServerURL()
		timeout   = conf.AgentTunnelTimeout()
	)

	// Prefer static config fingerprint if set; otherwise use the one from registration.
	fingerprint := conf.AgentTunnelFingerprint()
	if fingerprint == "" {
		fingerprint = reg.Fingerprint
	}

	if fingerprint == "" {
		return nil, fmt.Errorf("fingerprint is required")
	}

	auth := conf.AgentTunnelAuth()
	if reg.Auth != "" {
		auth = reg.Auth
	}

	headers := http.Header{}
	headers.Set("X-Cluster", cluster)
	headers.Set("X-Agent-ID", reg.AgentID) // Usually the Pod Name

	chiselConfig := &chclient.Config{
		Server:      serverURL,
		Auth:        auth,
		Fingerprint: fingerprint,
		Headers:     headers,
		Remotes: []string{
			// Forward traffic from the server-assigned tunnel endpoint to the local port.
			fmt.Sprintf("R:%s:127.0.0.1:%d", reg.Endpoint, localPort),
		},
		KeepAlive:        timeout,
		MaxRetryCount:    tunnelReconnectBeforeRegister,
		MaxRetryInterval: tunnelReconnectMaxInterval,
	}

	return chclient.NewClient(chiselConfig)
}

type registrationResult struct {
	AgentID     string
	Auth        string
	Fingerprint string
	Endpoint    string
}

// registerWithServer calls FleetService.Register and returns tunnel credentials.
func registerWithServer(ctx context.Context, conf *config.Config) (registrationResult, error) {
	serverURL := conf.AgentServerURL()
	cluster := conf.AgentCluster()
	agentID, err := os.Hostname()
	if err != nil {
		return registrationResult{}, fmt.Errorf("failed to get hostname: %w", err)
	}

	client := pbconnect.NewFleetServiceClient(http.DefaultClient, serverURL)
	req := &pb.RegisterRequest{}
	req.SetCluster(cluster)
	req.SetAgentId(agentID)

	resp, err := client.Register(ctx, req)
	if err != nil {
		return registrationResult{}, err
	}

	endpoint := resp.GetEndpoint()
	if endpoint == "" {
		return registrationResult{}, fmt.Errorf("endpoint is required")
	}

	reg := registrationResult{
		AgentID:     agentID,
		Auth:        fmt.Sprintf("%s:%s", agentID, resp.GetToken()),
		Fingerprint: resp.GetFingerprint(),
		Endpoint:    endpoint,
	}

	return reg, nil
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
