package config

import (
	"strings"
	"time"
)

// Option describes a single configuration entry: its viper key, the
// corresponding CLI flag name, the compiled default, and a
// human-readable description shown in --help output.
type Option struct {
	Key         string
	Flag        string
	Default     any
	Description string
}

// ServerOptions defines the configuration entries available in server
// mode. Each entry is registered as a viper default and a CLI flag.
var ServerOptions = []Option{
	{Key: keyServerAddress, Flag: toFlag(keyServerAddress), Default: ":8299", Description: "Server listen address"},
	{Key: keyServerAllowedOrigins, Flag: toFlag(keyServerAllowedOrigins), Default: []string{}, Description: "Server allowed origins"},
	{Key: keyServerTunnelAddress, Flag: toFlag(keyServerTunnelAddress), Default: "127.0.0.1:8300", Description: "Server tunnel address"},
	{Key: keyServerTunnelKeySeed, Flag: toFlag(keyServerTunnelKeySeed), Default: "change-me", Description: "Server tunnel key seed"},
	{Key: keyServerKeycloakRealmURL, Flag: toFlag(keyServerKeycloakRealmURL), Default: "https://keycloak.example.com/realms/otterscale", Description: "Server keycloak realm url"},
	{Key: keyServerKeycloakClientID, Flag: toFlag(keyServerKeycloakClientID), Default: "otterscale", Description: "Server keycloak client id"},
}

// AgentOptions defines the configuration entries available in agent
// mode.
var AgentOptions = []Option{
	{Key: keyAgentCluster, Flag: toFlag(keyAgentCluster), Default: "default", Description: "Agent cluster"},
	{Key: keyAgentServerURL, Flag: toFlag(keyAgentServerURL), Default: "http://127.0.0.1:8299", Description: "Agent control-plane server url"},
	{Key: keyAgentTunnelServerURL, Flag: toFlag(keyAgentTunnelServerURL), Default: "http://127.0.0.1:8300", Description: "Agent tunnel server url"},
	{Key: keyAgentTunnelTimeout, Flag: toFlag(keyAgentTunnelTimeout), Default: 30 * time.Second, Description: "Agent tunnel timeout"},
}

// toFlag converts a viper key like "server.tunnel.key_seed" into a
// CLI flag like "tunnel-key-seed" by lower-casing, replacing dots and
// underscores with hyphens, and stripping the "server-" or "agent-"
// prefix.
func toFlag(key string) string {
	flag := strings.ToLower(key)
	flag = strings.ReplaceAll(flag, ".", "-")
	flag = strings.ReplaceAll(flag, "_", "-")
	flag = strings.TrimPrefix(flag, "server-")
	flag = strings.TrimPrefix(flag, "agent-")
	return flag
}
