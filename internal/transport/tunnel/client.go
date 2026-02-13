package tunnel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	chclient "github.com/jpillora/chisel/client"
)

// Sentinel errors for well-known failure modes.
var (
	ErrLocalPortRequired = errors.New("tunnel: local port is required")
	ErrRegisterRequired  = errors.New("tunnel: register function is required")
	ErrEmptyFingerprint  = errors.New("tunnel: server returned empty fingerprint")
)

// RegisterFunc registers an agent to the fleet and returns endpoint credentials.
type RegisterFunc func(ctx context.Context, serverURL, cluster string) (endpoint, fingerprint, auth string, err error)

// ClientOption configures a Client.
type ClientOption func(*Client)

// Client manages a reverse tunnel connection with automatic
// registration, reconnection, and exponential backoff.
type Client struct {
	inner            *chclient.Client // owned lifecycle, not exported
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
	log              *slog.Logger
}

// WithCluster configures the cluster name used for registration.
func WithCluster(cluster string) ClientOption {
	return func(c *Client) { c.cluster = cluster }
}

// WithServerURL configures the fleet server URL for registration.
func WithServerURL(serverURL string) ClientOption {
	return func(c *Client) { c.serverURL = serverURL }
}

// WithTunnelServerURL configures the chisel tunnel server URL.
func WithTunnelServerURL(tunnelServerURL string) ClientOption {
	return func(c *Client) { c.tunnelServerURL = tunnelServerURL }
}

// WithLocalPort configures the local port to expose through the tunnel.
func WithLocalPort(localPort int) ClientOption {
	return func(c *Client) { c.localPort = localPort }
}

// WithKeepAlive configures the keep-alive interval for the tunnel.
func WithKeepAlive(keepAlive time.Duration) ClientOption {
	return func(c *Client) { c.keepAlive = keepAlive }
}

// WithMaxRetryCount configures chisel's internal maximum retry count.
func WithMaxRetryCount(maxRetryCount int) ClientOption {
	return func(c *Client) { c.maxRetryCount = maxRetryCount }
}

// WithMaxRetryInterval configures chisel's internal maximum retry interval.
func WithMaxRetryInterval(maxRetryInterval time.Duration) ClientOption {
	return func(c *Client) { c.maxRetryInterval = maxRetryInterval }
}

// WithBaseRetryDelay configures the initial delay for the outer reconnect backoff.
func WithBaseRetryDelay(baseRetryDelay time.Duration) ClientOption {
	return func(c *Client) { c.baseRetryDelay = baseRetryDelay }
}

// WithMaxRetryDelay configures the maximum delay for the outer reconnect backoff.
func WithMaxRetryDelay(maxRetryDelay time.Duration) ClientOption {
	return func(c *Client) { c.maxRetryDelay = maxRetryDelay }
}

// WithRegister configures the function used to register with the fleet server.
func WithRegister(register RegisterFunc) ClientOption {
	return func(c *Client) { c.register = register }
}

// WithLogger configures a structured logger. Defaults to slog.Default
// with "component" and "cluster" attributes.
func WithLogger(log *slog.Logger) ClientOption {
	return func(c *Client) { c.log = log }
}

// NewClient creates a tunnel client. It validates required fields
// but does not perform any I/O.
func NewClient(opts ...ClientOption) (*Client, error) {
	c := &Client{
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
		opt(c)
	}

	if c.localPort == 0 {
		return nil, ErrLocalPortRequired
	}
	if c.register == nil {
		return nil, ErrRegisterRequired
	}
	if c.log == nil {
		c.log = slog.Default().With("component", "tunnel-client", "cluster", c.cluster)
	}

	return c, nil
}

// Start runs the tunnel client loop. It blocks until ctx is cancelled,
// automatically re-registering and reconnecting on failures with
// exponential backoff.
func (c *Client) Start(ctx context.Context) error {
	bo := newBackoff(c.baseRetryDelay, c.maxRetryDelay)

	for {
		if ctx.Err() != nil {
			return nil
		}

		inner, err := c.dial(ctx)
		if err != nil {
			c.log.Warn("registration failed, retrying", "error", err, "retry_in", bo.current)
			if !sleepCtx(ctx, bo.Next()) {
				return nil
			}
			continue
		}
		bo.Reset()
		c.inner = inner

		err = c.runSession(ctx, inner)
		if ctx.Err() != nil {
			return nil
		}
		if err == nil || isAuthErr(err) {
			if err != nil {
				c.log.Warn("authentication failed, re-registering", "error", err)
			} else {
				c.log.Warn("session ended, re-registering")
			}
			bo.Reset()
			continue
		}

		c.log.Warn("connection lost, retrying", "error", err, "retry_in", bo.current)
		if !sleepCtx(ctx, bo.Next()) {
			return nil
		}
	}
}

// Stop gracefully shuts down the tunnel client.
func (c *Client) Stop(_ context.Context) error {
	if c.inner == nil {
		return nil
	}
	c.log.Info("shutting down")
	return c.inner.Close()
}

// dial registers with the fleet server and creates a new chisel client.
func (c *Client) dial(ctx context.Context) (*chclient.Client, error) {
	endpoint, fingerprint, auth, err := c.register(ctx, c.serverURL, c.cluster)
	if err != nil {
		return nil, fmt.Errorf("register: %w", err)
	}

	c.log.Info("registered", "endpoint", endpoint, "fingerprint", fingerprint)

	if fingerprint == "" {
		return nil, ErrEmptyFingerprint
	}

	return chclient.NewClient(&chclient.Config{
		Server:           c.tunnelServerURL,
		Fingerprint:      fingerprint,
		Auth:             auth,
		Remotes:          []string{fmt.Sprintf("R:%s:127.0.0.1:%d", endpoint, c.localPort)},
		KeepAlive:        c.keepAlive,
		MaxRetryCount:    c.maxRetryCount,
		MaxRetryInterval: c.maxRetryInterval,
	})
}

// runSession starts the inner chisel client and waits for it to finish.
// It always closes the inner client before returning.
func (c *Client) runSession(ctx context.Context, inner *chclient.Client) error {
	c.log.Info("connecting", "server", c.tunnelServerURL)

	if err := inner.Start(ctx); err != nil {
		_ = inner.Close()
		return fmt.Errorf("start: %w", err)
	}

	err := inner.Wait()
	_ = inner.Close()
	return err
}

// isAuthErr detects authentication-related errors from chisel by
// inspecting the error message. This is necessary because chisel does
// not expose typed errors for auth failures.
func isAuthErr(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "auth failed") ||
		strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "invalid auth")
}

// sleepCtx blocks for d or until ctx is done.
// Returns true if the sleep completed (context still alive).
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// backoff implements simple exponential backoff capped at a maximum.
type backoff struct {
	base    time.Duration
	max     time.Duration
	current time.Duration
}

func newBackoff(base, max time.Duration) *backoff {
	return &backoff{base: base, max: max, current: base}
}

// Next returns the current delay, then doubles it for the next call.
func (b *backoff) Next() time.Duration {
	d := b.current
	if next := b.current * 2; next > b.max {
		b.current = b.max
	} else {
		b.current = next
	}
	return d
}

// Reset sets the delay back to the base value.
func (b *backoff) Reset() {
	b.current = b.base
}
