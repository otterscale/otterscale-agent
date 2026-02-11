package config

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

type ConfigOption struct {
	Key         string
	Flag        string
	Default     any
	Description string
}

const (
	KeyServerAddress          = "server.address"
	KeyServerAllowedOrigins   = "server.allowed_origins"
	KeyServerTunnelAddress    = "server.tunnel.address"
	KeyServerTunnelKeySeed    = "server.tunnel.key_seed"
	KeyServerKeycloakRealmURL = "server.keycloak.realm_url"
	KeyServerKeycloakClientID = "server.keycloak.client_id"
	KeyServerDebugEnabled     = "server.debug.enabled"
)

const (
	KeyAgentCluster           = "agent.cluster"
	KeyAgentTunnelServerURL   = "agent.tunnel.server_url"
	KeyAgentTunnelFingerprint = "agent.tunnel.fingerprint"
	KeyAgentTunnelAuth        = "agent.tunnel.auth"
	KeyAgentTunnelPort        = "agent.tunnel.port"
	KeyAgentTunnelTimeout     = "agent.tunnel.timeout"
	KeyAgentDebugEnabled      = "agent.debug.enabled"
	KeyAgentDebugKubeAPIURL   = "agent.debug.kube_api_url"
)

var ServerOptions = []ConfigOption{
	{Key: KeyServerAddress, Flag: flag(KeyServerAddress), Default: ":8299", Description: "Server listen address"},
	{Key: KeyServerAllowedOrigins, Flag: flag(KeyServerAllowedOrigins), Default: []string{}, Description: "Server allowed origins"},
	{Key: KeyServerTunnelAddress, Flag: flag(KeyServerTunnelAddress), Default: "127.0.0.1:8300", Description: "Server tunnel address"},
	{Key: KeyServerTunnelKeySeed, Flag: flag(KeyServerTunnelKeySeed), Default: "change-me", Description: "Server tunnel key seed"},
	{Key: KeyServerKeycloakRealmURL, Flag: flag(KeyServerKeycloakRealmURL), Default: "https://keycloak.example.com/realms/otterscale", Description: "Server keycloak realm url"},
	{Key: KeyServerKeycloakClientID, Flag: flag(KeyServerKeycloakClientID), Default: "otterscale", Description: "Server keycloak client id"},
	{Key: KeyServerDebugEnabled, Flag: flag(KeyServerDebugEnabled), Default: false, Description: "Server debug enabled"},
}

var AgentOptions = []ConfigOption{
	{Key: KeyAgentCluster, Flag: flag(KeyAgentCluster), Default: "default", Description: "Agent cluster"},
	{Key: KeyAgentTunnelServerURL, Flag: flag(KeyAgentTunnelServerURL), Default: "http://127.0.0.1:8300", Description: "Agent tunnel server url"},
	{Key: KeyAgentTunnelFingerprint, Flag: flag(KeyAgentTunnelFingerprint), Default: "", Description: "Agent tunnel fingerprint"},
	{Key: KeyAgentTunnelAuth, Flag: flag(KeyAgentTunnelAuth), Default: "user:pass", Description: "Agent tunnel auth"},
	{Key: KeyAgentTunnelPort, Flag: flag(KeyAgentTunnelPort), Default: 16598, Description: "Agent tunnel port"},
	{Key: KeyAgentTunnelTimeout, Flag: flag(KeyAgentTunnelTimeout), Default: 30 * time.Second, Description: "Agent tunnel timeout"},
	{Key: KeyAgentDebugEnabled, Flag: flag(KeyAgentDebugEnabled), Default: false, Description: "Agent debug enabled"},
	{Key: KeyAgentDebugKubeAPIURL, Flag: flag(KeyAgentDebugKubeAPIURL), Default: "", Description: "Agent debug kube api url"},
}

type Config struct {
	v *viper.Viper
}

func New() (*Config, error) {
	v := viper.New()

	// default values
	for _, o := range ServerOptions {
		v.SetDefault(o.Key, o.Default)
	}

	for _, o := range AgentOptions {
		v.SetDefault(o.Key, o.Default)
	}

	// load config from file
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/otterscale/")

	if err := v.ReadInConfig(); err != nil {
		var notFoundErr viper.ConfigFileNotFoundError
		if !(errors.As(err, &notFoundErr) || errors.Is(err, os.ErrNotExist)) {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
	}

	// load config from environment variables
	v.SetEnvPrefix("OTTERSCALE")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	return &Config{v: v}, nil
}

func (c *Config) BindFlags(fs *pflag.FlagSet, options []ConfigOption) error {
	for _, o := range options {
		switch v := o.Default.(type) {
		case string:
			fs.String(o.Flag, v, o.Description)
		case int:
			fs.Int(o.Flag, v, o.Description)
		case bool:
			fs.Bool(o.Flag, v, o.Description)
		case []string:
			fs.StringSlice(o.Flag, v, o.Description)
		case time.Duration:
			fs.Duration(o.Flag, v, o.Description)
		default:
			return fmt.Errorf("unsupported flag type for key: %s", o.Key)
		}

		if err := c.v.BindPFlag(o.Key, fs.Lookup(o.Flag)); err != nil {
			return fmt.Errorf("failed to bind flag %s: %w", o.Flag, err)
		}
	}

	return nil
}

func (c *Config) ServerAddress() string {
	return c.v.GetString(KeyServerAddress) // OTTERSCALE_SERVER_ADDRESS
}

func (c *Config) ServerAllowedOrigins() []string {
	return c.v.GetStringSlice(KeyServerAllowedOrigins) // OTTERSCALE_SERVER_ALLOWED_ORIGINS
}

func (c *Config) ServerTunnelAddress() string {
	return c.v.GetString(KeyServerTunnelAddress) // OTTERSCALE_SERVER_TUNNEL_ADDRESS
}

func (c *Config) ServerTunnelKeySeed() string {
	return c.v.GetString(KeyServerTunnelKeySeed) // OTTERSCALE_SERVER_TUNNEL_KEY_SEED
}

func (c *Config) ServerKeycloakRealmURL() string {
	return c.v.GetString(KeyServerKeycloakRealmURL) // OTTERSCALE_SERVER_KEYCLOAK_REALM_URL
}

func (c *Config) ServerKeycloakClientID() string {
	return c.v.GetString(KeyServerKeycloakClientID) // OTTERSCALE_SERVER_KEYCLOAK_CLIENT_ID
}

func (c *Config) ServerDebugEnabled() bool {
	return c.v.GetBool(KeyServerDebugEnabled) // OTTERSCALE_SERVER_DEBUG_ENABLED
}

func (c *Config) AgentCluster() string {
	return c.v.GetString(KeyAgentCluster) // OTTERSCALE_AGENT_CLUSTER
}

func (c *Config) AgentTunnelServerURL() string {
	return c.v.GetString(KeyAgentTunnelServerURL) // OTTERSCALE_AGENT_TUNNEL_SERVER_URL
}

func (c *Config) AgentTunnelPort() int {
	return c.v.GetInt(KeyAgentTunnelPort) // OTTERSCALE_AGENT_TUNNEL_PORT
}

func (c *Config) AgentTunnelAuth() string {
	return c.v.GetString(KeyAgentTunnelAuth) // OTTERSCALE_AGENT_TUNNEL_AUTH
}

func (c *Config) AgentTunnelFingerprint() string {
	return c.v.GetString(KeyAgentTunnelFingerprint) // OTTERSCALE_AGENT_TUNNEL_FINGERPRINT
}

func (c *Config) AgentTunnelTimeout() time.Duration {
	return c.v.GetDuration(KeyAgentTunnelTimeout) // OTTERSCALE_AGENT_TUNNEL_TIMEOUT
}

func (c *Config) AgentDebugEnabled() bool {
	return c.v.GetBool(KeyAgentDebugEnabled) // OTTERSCALE_AGENT_DEBUG_ENABLED
}

func (c *Config) AgentDebugKubeAPIURL() string {
	return c.v.GetString(KeyAgentDebugKubeAPIURL) // OTTERSCALE_AGENT_DEBUG_KUBE_API_URL
}

func flag(key string) string {
	flag := strings.ToLower(key)
	flag = strings.ReplaceAll(flag, ".", "-")
	flag = strings.ReplaceAll(flag, "_", "-")
	flag = strings.TrimPrefix(flag, "server-")
	flag = strings.TrimPrefix(flag, "agent-")
	return flag
}
