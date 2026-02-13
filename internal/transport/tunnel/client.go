package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// RegisterFunc registers an agent to the fleet and returns endpoint credentials.
type RegisterFunc func(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error)

// Option defines a functional option for configuring the tunnel.
type ClientOption func(*Client)

type Client struct {
	*chclient.Client
	cluster          string
	serverURL        string
	tunnelServerURL  string
	localPort        int
	keepAlive        time.Duration
	maxRetryCount    int
	maxRetryInterval time.Duration
	baseRetryDelay   time.Duration
	maxRetryDelay    time.Duration
	register         RegisterFunc
}

// WithCluster configures the cluster.
func WithCluster(cluster string) ClientOption {
	return func(o *Client) {
		o.cluster = cluster
	}
}

// WithServerURL configures the server URL.
func WithServerURL(serverURL string) ClientOption {
	return func(o *Client) {
		o.serverURL = serverURL
	}
}

// WithTunnelServerURL configures the tunnel server URL.
func WithTunnelServerURL(tunnelServerURL string) ClientOption {
	return func(o *Client) {
		o.tunnelServerURL = tunnelServerURL
	}
}

// WithLocalPort configures the local port.
func WithLocalPort(localPort int) ClientOption {
	return func(o *Client) {
		o.localPort = localPort
	}
}

// WithKeepAlive configures the keep alive duration.
func WithKeepAlive(keepAlive time.Duration) ClientOption {
	return func(o *Client) {
		o.keepAlive = keepAlive
	}
}

// WithMaxRetryCount configures the maximum retry count.
func WithMaxRetryCount(maxRetryCount int) ClientOption {
	return func(o *Client) {
		o.maxRetryCount = maxRetryCount
	}
}

// WithMaxRetryInterval configures the maximum retry interval.
func WithMaxRetryInterval(maxRetryInterval time.Duration) ClientOption {
	return func(o *Client) {
		o.maxRetryInterval = maxRetryInterval
	}
}

// WithBaseRetryDelay configures the base retry delay.
func WithBaseRetryDelay(baseRetryDelay time.Duration) ClientOption {
	return func(o *Client) {
		o.baseRetryDelay = baseRetryDelay
	}
}

// WithMaxRetryDelay configures the maximum retry delay.
func WithMaxRetryDelay(maxRetryDelay time.Duration) ClientOption {
	return func(o *Client) {
		o.maxRetryDelay = maxRetryDelay
	}
}

// WithRegister configures the register function.
func WithRegister(register RegisterFunc) ClientOption {
	return func(o *Client) {
		o.register = register
	}
}

// NewClient creates a new tunnel client manager with the given options.
func NewClient(ctx context.Context, opts ...ClientOption) (*Client, error) {
	clt := &Client{
		cluster:          "default",
		serverURL:        "http://127.0.0.1:8299",
		tunnelServerURL:  "http://127.0.0.1:8300",
		keepAlive:        30 * time.Second,
		maxRetryCount:    3,
		maxRetryInterval: 10 * time.Second,
		baseRetryDelay:   1 * time.Second,
		maxRetryDelay:    30 * time.Second,
	}
	for _, opt := range opts {
		opt(clt)
	}

	if clt.localPort == 0 {
		return nil, fmt.Errorf("local port is required")
	}

	if clt.register == nil {
		return nil, fmt.Errorf("register function is required")
	}

	return clt, nil
}

func (s *Client) Start(ctx context.Context) error {
	retryDelay := s.baseRetryDelay

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		clt, err := s.newClient(ctx)
		if err != nil {
			slog.Warn("Failed to register with server, retrying", "error", err, "retry_in", retryDelay)
			if !s.sleepOrDone(ctx, retryDelay) {
				return nil
			}
			retryDelay = s.nextRetryDelay(retryDelay)
			continue
		}
		s.Client = clt

		retryDelay = s.baseRetryDelay

		slog.Info("Tunnel client connecting to", "server", s.serverURL)
		if err := clt.Start(ctx); err != nil {
			_ = clt.Close()
			if s.isAuthFailure(err) {
				slog.Warn("Agent authentication failed, re-registering immediately", "error", err)
				continue
			}
			slog.Warn("Agent failed to start tunnel, retrying", "error", err, "retry_in", retryDelay)
			if !s.sleepOrDone(ctx, retryDelay) {
				return nil
			}
			retryDelay = s.nextRetryDelay(retryDelay)
			continue
		}

		err = clt.Wait()
		_ = clt.Close()

		if ctx.Err() != nil {
			return nil
		}
		if err == nil {
			slog.Warn("Tunnel session ended, re-registering")
			retryDelay = s.baseRetryDelay
			continue
		}
		if s.isAuthFailure(err) {
			slog.Warn("Agent authentication failed, re-registering immediately", "error", err)
			retryDelay = s.baseRetryDelay
			continue
		}

		slog.Warn("Agent connection lost, retrying registration", "error", err, "retry_in", retryDelay)
		if !s.sleepOrDone(ctx, retryDelay) {
			return nil
		}
		retryDelay = s.nextRetryDelay(retryDelay)
	}
}

func (s *Client) Stop(ctx context.Context) error {
	if s.Client == nil {
		return nil
	}
	slog.Info("Gracefully shutting down tunnel client...")
	return s.Close()
}

func (s *Client) newClient(ctx context.Context) (*chclient.Client, error) {
	endpoint, fingerprint, auth, err := s.register(ctx, s.serverURL, s.cluster)
	if err != nil {
		return nil, err
	}

	slog.Info("Registered with server", "endpoint", endpoint, "fingerprint", fingerprint)

	// Without a fingerprint, server identity cannot be verified.
	if fingerprint == "" {
		return nil, fmt.Errorf("fingerprint is required")
	}

	cfg := &chclient.Config{
		Server:           s.tunnelServerURL,
		Fingerprint:      fingerprint,
		Auth:             auth,
		Remotes:          []string{fmt.Sprintf("R:%s:127.0.0.1:%d", endpoint, s.localPort)},
		KeepAlive:        s.keepAlive,
		MaxRetryCount:    s.maxRetryCount,
		MaxRetryInterval: s.maxRetryInterval,
	}

	return chclient.NewClient(cfg)
}

func (s *Client) isAuthFailure(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "auth failed") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid auth")
}

func (s *Client) sleepOrDone(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Client) nextRetryDelay(current time.Duration) time.Duration {
	if current <= 0 {
		return s.baseRetryDelay
	}
	next := current * 2
	if next > s.maxRetryDelay {
		return s.maxRetryDelay
	}
	return next
}
