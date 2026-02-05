package config

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/fsnotify/fsnotify"
	"github.com/spf13/viper"
)

type Keycloak struct {
	RealmURL string `json:"realm_url"`
	ClientID string `json:"client_id"`
}

type Schema struct {
	Keycloak Keycloak `json:"keycloak"`
}

type Config struct {
	v *viper.Viper
}

func New() *Config {
	return &Config{
		v: viper.New(),
	}
}

func (c *Config) Load(path string) error {
	extension := filepath.Ext(path)
	if len(extension) < 2 {
		return fmt.Errorf("extension not found in filename: %q", path)
	}

	filename := filepath.Base(path)
	filenameOnly := filename[0 : len(filename)-len(extension)]

	c.v.AddConfigPath(filepath.Dir(path))
	c.v.SetConfigName(filenameOnly)
	c.v.SetConfigType(extension[1:]) // remove dot

	if err := c.v.ReadInConfig(); err != nil {
		return err
	}

	c.v.WatchConfig()

	c.v.OnConfigChange(func(in fsnotify.Event) {
		slog.Info("configuration file changed", "file", in.Name)
	})

	return nil
}

func (c *Config) KeycloakRealmURL() string {
	return c.v.GetString("keycloak.realm_url")
}

func (c *Config) KeycloakClientID() string {
	return c.v.GetString("keycloak.client_id")
}

// TunnelServerHost is the bind host for the embedded chisel server.
// Example: "0.0.0.0"
func (c *Config) TunnelServerHost() string {
	host := c.v.GetString("tunnel.server.host")
	if host == "" {
		return "0.0.0.0"
	}
	return host
}

// TunnelServerPort is the bind port for the embedded chisel server.
// Example: "8300"
func (c *Config) TunnelServerPort() string {
	port := c.v.GetString("tunnel.server.port")
	if port == "" {
		return "8300"
	}
	return port
}

// TunnelServerAddr returns host:port for the embedded chisel server.
func (c *Config) TunnelServerAddr() string {
	return strings.Join([]string{c.TunnelServerHost(), c.TunnelServerPort()}, ":")
}

// TunnelServerKeyFile is an optional path to the chisel ECDSA private key PEM.
// If empty, TunnelServerKeySeed should be provided.
func (c *Config) TunnelServerKeyFile() string {
	return c.v.GetString("tunnel.server.key_file")
}

// TunnelServerKeySeed is an optional seed used by chisel to generate a keypair.
// If empty, TunnelServerKeyFile should be provided.
func (c *Config) TunnelServerKeySeed() string {
	return c.v.GetString("tunnel.server.key_seed")
}

// ClusterNames returns configured cluster names under the "clusters" root.
func (c *Config) ClusterNames() []string {
	m := c.v.GetStringMap("clusters")
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// ClusterAgentAuthUser returns chisel auth username for the given cluster agent.
func (c *Config) ClusterAgentAuthUser(cluster string) string {
	return c.v.GetString(fmt.Sprintf("clusters.%s.agent.auth.user", cluster))
}

// ClusterAgentAuthPass returns chisel auth password for the given cluster agent.
func (c *Config) ClusterAgentAuthPass(cluster string) string {
	return c.v.GetString(fmt.Sprintf("clusters.%s.agent.auth.pass", cluster))
}

// ClusterAgentFingerprint is the expected chisel server fingerprint (agent-side).
func (c *Config) ClusterAgentFingerprint(cluster string) string {
	return c.v.GetString(fmt.Sprintf("clusters.%s.agent.fingerprint", cluster))
}

// ClusterAgentTunnelPort is the local server port opened by the reverse tunnel.
// Example: 51001 -> server will accept agent traffic on 127.0.0.1:51001.
func (c *Config) ClusterAgentTunnelPort(cluster string) int {
	return c.v.GetInt(fmt.Sprintf("clusters.%s.agent.tunnel_port", cluster))
}

// ClusterAgentAPIPort is the agent local API port which will be exposed through the tunnel.
func (c *Config) ClusterAgentAPIPort(cluster string) int {
	return c.v.GetInt(fmt.Sprintf("clusters.%s.agent.api_port", cluster))
}

// ClusterAgentAPIPortOr parses a string port if api_port is configured as string.
// This is a small compatibility helper for YAML configs that quote numeric ports.
func (c *Config) ClusterAgentAPIPortOr(cluster string, def int) int {
	if p := c.ClusterAgentAPIPort(cluster); p != 0 {
		return p
	}
	raw := c.v.GetString(fmt.Sprintf("clusters.%s.agent.api_port", cluster))
	if raw == "" {
		return def
	}
	if p, err := strconv.Atoi(raw); err == nil {
		return p
	}
	return def
}
