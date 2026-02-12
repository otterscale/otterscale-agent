package config

const (
	keyServerAddress          = "server.address"
	keyServerAllowedOrigins   = "server.allowed_origins"
	keyServerTunnelAddress    = "server.tunnel.address"
	keyServerTunnelKeySeed    = "server.tunnel.key_seed"
	keyServerKeycloakRealmURL = "server.keycloak.realm_url"
	keyServerKeycloakClientID = "server.keycloak.client_id"
)

const (
	keyAgentCluster           = "agent.cluster"
	keyAgentTunnelServerURL   = "agent.tunnel.server_url"
	keyAgentTunnelFingerprint = "agent.tunnel.fingerprint"
	keyAgentTunnelAuth        = "agent.tunnel.auth"
	keyAgentTunnelPort        = "agent.tunnel.port"
	keyAgentTunnelTimeout     = "agent.tunnel.timeout"
)
