package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	chclient "github.com/jpillora/chisel/client"
)

// TODO: multiple agents per cluster
func NewAgent() *cobra.Command {
	var (
		cluster, server, auth, fingerprint, remote string
		timeout                                    time.Duration
	)

	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Start agent that connects to server and executes requests in-cluster",
		Example: "otterscale agent --cluster=dev --server=https://server.otterscale.io --auth=user:pass --fingerprint=... --remote=R:127.0.0.1:8299:kubernetes.default.svc:443 --timeout=30s",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cluster == "" {
				return fmt.Errorf("cluster is required")
			}

			if server == "" {
				return fmt.Errorf("server is required")
			}

			if auth == "" {
				return fmt.Errorf("auth is required")
			}

			if fingerprint == "" {
				return fmt.Errorf("fingerprint is required")
			}

			if remote == "" {
				return fmt.Errorf("remote is required")
			}

			if timeout == 0 {
				return fmt.Errorf("timeout is required")
			}

			c, err := newReverseTunnel(cluster, server, auth, fingerprint, remote, timeout)
			if err != nil {
				return err
			}
			defer c.Close()

			if err := c.Start(cmd.Context()); err != nil {
				return err
			}
			return c.Wait()
		},
	}

	cmd.Flags().StringVar(
		&cluster,
		"cluster",
		"",
		"Cluster name to connect to (e.g. dev)",
	)

	cmd.Flags().StringVar(
		&server,
		"server",
		"",
		"URL of the server to connect to (e.g. https://server.otterscale.io)",
	)

	cmd.Flags().StringVar(
		&auth,
		"auth",
		"",
		"Basic auth credentials for server authentication (e.g. user:pass)",
	)

	cmd.Flags().StringVar(
		&fingerprint,
		"fingerprint",
		"",
		"Fingerprint of the server to connect to (printed by the server on startup and required to verify the server's identity)",
	)

	cmd.Flags().StringVar(
		&remote,
		"remote",
		"R:127.0.0.1:8299:kubernetes.default.svc:443",
		"Remote address to connect to (e.g. R:127.0.0.1:8299:kubernetes.default.svc:443)",
	)

	cmd.Flags().DurationVar(
		&timeout,
		"timeout",
		30*time.Second,
		"Timeout for the connection to the server (e.g. 30s, 1m, 1h)",
	)

	return cmd
}

// newReverseTunnel creates a new reverse tunnel client.
func newReverseTunnel(cluster, server, auth, fingerprint, remote string, keepAlive time.Duration) (*chclient.Client, error) {
	headers := http.Header{}
	headers.Set("X-Cluster", cluster)
	headers.Set("X-Agent-ID", uuid.NewString())

	cfg := &chclient.Config{
		Server:        server,
		Auth:          auth,
		Fingerprint:   fingerprint,
		Headers:       headers,
		Remotes:       []string{remote},
		KeepAlive:     keepAlive,
		MaxRetryCount: -1,
	}

	return chclient.NewClient(cfg)
}
