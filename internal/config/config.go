package config

import (
	"strings"

	"github.com/spf13/viper"
)

const (
	keyKeycloakRealmURL    = "keycloak.realm_url"
	keyKeycloakClientID    = "keycloak.client_id"
	keyTunnelServerKeySeed = "tunnel.server.key_seed"
)

type Config struct {
	v *viper.Viper
}

func New() *Config {
	return &Config{
		v: viper.New(),
	}
}

func (c *Config) Load() {
	c.v.SetEnvPrefix("OTTERSCALE")
	c.v.AutomaticEnv()
	c.v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
}

func (c *Config) KeycloakRealmURL() string {
	return c.v.GetString(keyKeycloakRealmURL) // OTTERSCALE_KEYCLOAK_REALM_URL
}

func (c *Config) KeycloakClientID() string {
	return c.v.GetString(keyKeycloakClientID) // OTTERSCALE_KEYCLOAK_CLIENT_ID
}

func (c *Config) TunnelServerKeySeed() string {
	return c.v.GetString(keyTunnelServerKeySeed) // OTTERSCALE_TUNNEL_SERVER_KEY_SEED
}
