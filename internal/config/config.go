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

func (c *Config) BindFlags(fs *pflag.FlagSet, options []Option) error {
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
	return c.v.GetString(keyServerAddress) // OTTERSCALE_SERVER_ADDRESS
}

func (c *Config) ServerAllowedOrigins() []string {
	return c.v.GetStringSlice(keyServerAllowedOrigins) // OTTERSCALE_SERVER_ALLOWED_ORIGINS
}

func (c *Config) ServerTunnelAddress() string {
	return c.v.GetString(keyServerTunnelAddress) // OTTERSCALE_SERVER_TUNNEL_ADDRESS
}

func (c *Config) ServerTunnelKeySeed() string {
	return c.v.GetString(keyServerTunnelKeySeed) // OTTERSCALE_SERVER_TUNNEL_KEY_SEED
}

func (c *Config) ServerKeycloakRealmURL() string {
	return c.v.GetString(keyServerKeycloakRealmURL) // OTTERSCALE_SERVER_KEYCLOAK_REALM_URL
}

func (c *Config) ServerKeycloakClientID() string {
	return c.v.GetString(keyServerKeycloakClientID) // OTTERSCALE_SERVER_KEYCLOAK_CLIENT_ID
}

func (c *Config) AgentCluster() string {
	return c.v.GetString(keyAgentCluster) // OTTERSCALE_AGENT_CLUSTER
}

func (c *Config) AgentTunnelServerURL() string {
	return c.v.GetString(keyAgentTunnelServerURL) // OTTERSCALE_AGENT_TUNNEL_SERVER_URL
}

func (c *Config) AgentTunnelPort() int {
	return c.v.GetInt(keyAgentTunnelPort) // OTTERSCALE_AGENT_TUNNEL_PORT
}

func (c *Config) AgentTunnelAuth() string {
	return c.v.GetString(keyAgentTunnelAuth) // OTTERSCALE_AGENT_TUNNEL_AUTH
}

func (c *Config) AgentTunnelFingerprint() string {
	return c.v.GetString(keyAgentTunnelFingerprint) // OTTERSCALE_AGENT_TUNNEL_FINGERPRINT
}

func (c *Config) AgentTunnelTimeout() time.Duration {
	return c.v.GetDuration(keyAgentTunnelTimeout) // OTTERSCALE_AGENT_TUNNEL_TIMEOUT
}
