package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"

	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	chclient "github.com/jpillora/chisel/client"

	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/impersonation"
	"github.com/otterscale/otterscale-agent/internal/mux"
)

func NewAgent(conf *config.Config, spoke *mux.Spoke) *cobra.Command {
	var address, configPath, cluster string

	cmd := &cobra.Command{
		Use:     "agent",
		Short:   "Start agent that connects to server and executes requests in-cluster",
		Example: "otterscale agent --cluster=dev --address=:8299 --config=otterscale.yaml",
		RunE: func(_ *cobra.Command, _ []string) error {
			if os.Getenv(containerEnvVar) != "" {
				address = defaultContainerAddress
				configPath = defaultContainerConfigPath
				slog.Info("Container environment detected, using default configuration", "address", address, "config", configPath)
			}

			slog.Info("Loading configuration file", "path", configPath)
			if err := conf.Load(configPath); err != nil {
				return err
			}

			if cluster == "" {
				return fmt.Errorf("--cluster is required")
			}

			agentPort, resolvedAddress, err := resolveAgentListenPort(address, conf, cluster)
			if err != nil {
				return err
			}

			openTelemetryInterceptor, err := otelconnect.NewInterceptor()
			if err != nil {
				return err
			}

			// Agent trusts the subject injected by the server proxy.
			trustedSubjectInterceptor := impersonation.NewTrustedSubjectHeaderInterceptor()

			opts := []connect.HandlerOption{
				connect.WithInterceptors(openTelemetryInterceptor, trustedSubjectInterceptor),
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			g, ctx := errgroup.WithContext(ctx)

			g.Go(func() error {
				return runChiselClient(ctx, conf, cluster, agentPort)
			})

			g.Go(func() error {
				return startHTTPServer(ctx, resolvedAddress, spoke, opts...)
			})

			return g.Wait()
		},
	}

	cmd.Flags().StringVarP(
		&address,
		"address",
		"a",
		":8299",
		"Address for agent API to listen on",
	)

	cmd.Flags().StringVarP(
		&configPath,
		"config",
		"c",
		"otterscale.yaml",
		"Config path for agent to load",
	)

	cmd.Flags().StringVar(
		&cluster,
		"cluster",
		"",
		"Cluster name to select from config (clusters.<name>.*)",
	)

	return cmd
}

func resolveAgentListenPort(address string, conf *config.Config, cluster string) (port int, resolvedAddress string, _ error) {
	host, portStr, err := net.SplitHostPort(address)
	if err != nil {
		// net.SplitHostPort requires bracketed IPv6; support the common ":8299" form.
		if strings.HasPrefix(address, ":") {
			host = ""
			portStr = strings.TrimPrefix(address, ":")
		} else {
			return 0, "", err
		}
	}

	p, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, "", fmt.Errorf("invalid --address port: %w", err)
	}

	if p == 0 {
		p = conf.ClusterAgentAPIPortOr(cluster, 0)
		if p == 0 {
			return 0, "", fmt.Errorf("agent listen port is 0; set --address=:<port> or clusters.%s.agent.api_port", cluster)
		}
	}

	resolved := net.JoinHostPort(host, strconv.Itoa(p))
	if host == "" {
		// JoinHostPort("", "8299") returns ":8299" already, but keep this explicit.
		resolved = ":" + strconv.Itoa(p)
	}

	return p, resolved, nil
}

func runChiselClient(ctx context.Context, conf *config.Config, cluster string, agentPort int) error {
	tunnelPort := conf.ClusterAgentTunnelPort(cluster)
	if tunnelPort == 0 {
		return fmt.Errorf("missing clusters.%s.agent.tunnel_port", cluster)
	}

	user := conf.ClusterAgentAuthUser(cluster)
	pass := conf.ClusterAgentAuthPass(cluster)
	if user == "" || pass == "" {
		return fmt.Errorf("missing clusters.%s.agent.auth.user/pass", cluster)
	}

	remote := fmt.Sprintf("R:127.0.0.1:%d:127.0.0.1:%d", tunnelPort, agentPort)

	cfg := &chclient.Config{
		Server:        conf.TunnelServerAddr(),
		Fingerprint:   conf.ClusterAgentFingerprint(cluster),
		Auth:          fmt.Sprintf("%s:%s", user, pass),
		Remotes:       []string{remote},
		KeepAlive:     30 * time.Second,
		MaxRetryCount: -1, // retry forever
	}

	c, err := chclient.NewClient(cfg)
	if err != nil {
		return err
	}
	if err := c.Start(ctx); err != nil {
		return err
	}
	return c.Wait()
}
