// Package core defines the domain interfaces and use-case logic for
// the otterscale agent. Infrastructure adapters (chisel, kubernetes,
// otterscale) implement the interfaces declared here.
package core

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// maxClusterNameLength is the maximum allowed length for a cluster
// name. This matches the Kubernetes label value length limit.
const maxClusterNameLength = 63

// reClusterName matches a valid Kubernetes label value: lowercase
// alphanumeric characters or hyphens, must start and end with an
// alphanumeric character. This prevents YAML injection via cluster
// names that contain quotes, newlines, or other special characters.
var reClusterName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// TunnelProvider is the server-side abstraction for managing reverse
// tunnels. It allocates unique endpoints per cluster, signs agent
// CSRs, and provisions tunnel users for each connecting agent.
type TunnelProvider interface {
	// CACertPEM returns the PEM-encoded CA certificate so that
	// agents can verify the tunnel server and the server can
	// configure mTLS.
	CACertPEM() []byte
	// ListClusters returns the names of all registered clusters.
	ListClusters() map[string]Cluster
	// RegisterCluster validates and signs the agent's CSR, creates
	// a tunnel user, and returns the allocated endpoint together
	// with the PEM-encoded signed certificate.
	RegisterCluster(cluster, agentID, agentVersion string, csrPEM []byte) (endpoint string, certPEM []byte, err error)
	// ResolveAddress returns the HTTP base URL for the given cluster.
	ResolveAddress(cluster string) (string, error)
}

// TunnelConsumer is the agent-side abstraction for registering with
// the fleet server and obtaining tunnel credentials via CSR/mTLS.
type TunnelConsumer interface {
	// Register calls the fleet API with a CSR and returns the
	// signed certificate, CA certificate, tunnel endpoint, and the
	// private key that corresponds to the CSR. Returning the key
	// alongside the certificate eliminates the TOCTOU race that
	// would occur if callers had to fetch the key separately.
	Register(ctx context.Context, serverURL, cluster string) (Registration, error)
}

// Registration holds the credentials and connection details returned
// by the fleet server after a successful CSR-based registration.
type Registration struct {
	// Endpoint is the tunnel endpoint the agent should connect to.
	Endpoint string
	// Certificate is the PEM-encoded X.509 certificate signed by
	// the server's CA, used for mTLS client authentication.
	Certificate []byte
	// CACertificate is the PEM-encoded CA certificate used to
	// verify the tunnel server's identity.
	CACertificate []byte
	// PrivateKeyPEM is the PEM-encoded ECDSA private key that
	// corresponds to the CSR sent during this registration.
	// Returned alongside the certificate to ensure the key/cert
	// pair is always consistent (no TOCTOU race).
	PrivateKeyPEM []byte
	// AgentID is the identifier of the agent that registered. It is
	// set by the TunnelConsumer so that callers can derive auth
	// credentials without re-querying the hostname.
	AgentID string
	// ServerVersion is the version of the server binary. Agents
	// compare this against their own version to decide whether a
	// self-update is needed.
	ServerVersion string
}

// Cluster holds the per-cluster tunnel state: the allocated
// loopback host and the chisel user name.
type Cluster struct {
	Host         string // unique 127.x.x.x loopback address
	User         string // chisel user name
	AgentVersion string // agent binary version
}

// manifestTokenTTL is the validity period of HMAC-signed manifest
// tokens. After this duration the token expires and a new one must
// be issued via the GetAgentManifest RPC.
const manifestTokenTTL = 1 * time.Hour

// AgentManifestConfig holds the external URLs and HMAC key needed to
// generate agent installation manifests and sign manifest tokens.
type AgentManifestConfig struct {
	// ServerURL is the externally reachable URL of the control-plane
	// server (e.g. "https://otterscale.example.com").
	ServerURL string
	// TunnelURL is the externally reachable URL of the tunnel server
	// (e.g. "https://tunnel.example.com:8300").
	TunnelURL string
	// HMACKey is a 32-byte key derived from the CA seed via HKDF.
	// It is used to sign and verify stateless manifest tokens.
	HMACKey []byte
}

// ManifestParams holds the parameters needed to render an agent
// installation manifest. It is defined in the core layer as a
// pure value object; the rendering logic lives in the providers layer.
type ManifestParams struct {
	Cluster   string
	UserName  string
	Image     string
	ServerURL string
	TunnelURL string
}

// ManifestRenderer renders agent installation manifests from the given
// parameters. Implementations live in the providers layer and own the
// template and formatting details.
type ManifestRenderer interface {
	RenderAgentManifest(params ManifestParams) (string, error)
}

// FleetUseCase orchestrates cluster registration on the server side.
// It delegates CSR signing and tunnel setup to the TunnelProvider.
type FleetUseCase struct {
	tunnel      TunnelProvider
	version     Version
	manifestCfg AgentManifestConfig
	renderer    ManifestRenderer
}

// NewFleetUseCase returns a FleetUseCase backed by the given
// TunnelProvider. version is the server binary version, included in
// registration responses so agents can detect mismatches.
// manifestCfg provides the external URLs embedded in generated agent
// installation manifests. It returns an error if any required
// manifest configuration field is missing.
func NewFleetUseCase(tunnel TunnelProvider, version Version, manifestCfg AgentManifestConfig, renderer ManifestRenderer) (*FleetUseCase, error) {
	if manifestCfg.ServerURL == "" {
		return nil, fmt.Errorf("manifest config: server URL is required")
	}
	if manifestCfg.TunnelURL == "" {
		return nil, fmt.Errorf("manifest config: tunnel URL is required")
	}
	if len(manifestCfg.HMACKey) == 0 {
		return nil, fmt.Errorf("manifest config: HMAC key is required")
	}
	return &FleetUseCase{
		tunnel:      tunnel,
		version:     version,
		manifestCfg: manifestCfg,
		renderer:    renderer,
	}, nil
}

// ListClusters returns the names of all currently registered clusters.
func (uc *FleetUseCase) ListClusters() map[string]Cluster {
	return uc.tunnel.ListClusters()
}

// RegisterCluster validates the inputs, forwards the agent's CSR to
// the tunnel provider for signing, and returns the signed certificate,
// CA certificate, tunnel endpoint, and the server's version.
func (uc *FleetUseCase) RegisterCluster(cluster, agentID, agentVersion string, csrPEM []byte) (Registration, error) {
	if cluster == "" {
		return Registration{}, &ErrInvalidInput{Field: "cluster", Message: "must not be empty"}
	}
	if len(cluster) > maxClusterNameLength {
		return Registration{}, &ErrInvalidInput{
			Field:   "cluster",
			Message: fmt.Sprintf("must not exceed %d characters", maxClusterNameLength),
		}
	}
	if !reClusterName.MatchString(cluster) {
		return Registration{}, &ErrInvalidInput{
			Field:   "cluster",
			Message: fmt.Sprintf("must match [a-z0-9]([a-z0-9-]*[a-z0-9])?, got %q", cluster),
		}
	}
	if agentID == "" {
		return Registration{}, &ErrInvalidInput{Field: "agent_id", Message: "must not be empty"}
	}
	if len(csrPEM) == 0 {
		return Registration{}, &ErrInvalidInput{Field: "csr", Message: "must not be empty"}
	}

	endpoint, certPEM, err := uc.tunnel.RegisterCluster(cluster, agentID, agentVersion, csrPEM)
	if err != nil {
		return Registration{}, err
	}
	return Registration{
		Endpoint:      endpoint,
		Certificate:   certPEM,
		CACertificate: uc.tunnel.CACertPEM(),
		ServerVersion: string(uc.version),
	}, nil
}

// IssueManifestURL generates an HMAC-signed token that encodes the
// cluster name and user identity, and returns a full URL that serves
// the agent manifest as raw YAML. The token is valid for
// manifestTokenTTL.
func (uc *FleetUseCase) IssueManifestURL(cluster, userName string) (string, error) {
	token, err := uc.issueManifestToken(cluster, userName)
	if err != nil {
		return "", fmt.Errorf("issue manifest token: %w", err)
	}
	return strings.TrimRight(uc.manifestCfg.ServerURL, "/") + "/fleet/manifest/" + token, nil
}

// VerifyManifestToken validates the HMAC signature and expiry of a
// manifest token and returns the embedded cluster name and user
// identity.
func (uc *FleetUseCase) VerifyManifestToken(token string) (cluster, userName string, err error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("malformed token")
	}

	payloadBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return "", "", fmt.Errorf("decode payload: %w", err)
	}

	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", fmt.Errorf("decode signature: %w", err)
	}

	// Verify HMAC before trusting any payload content.
	mac := hmac.New(sha256.New, uc.manifestCfg.HMACKey)
	mac.Write(payloadBytes)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return "", "", fmt.Errorf("invalid token signature")
	}

	var claims manifestTokenClaims
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return "", "", fmt.Errorf("parse token claims: %w", err)
	}

	if time.Now().Unix() > claims.Exp {
		return "", "", fmt.Errorf("token expired")
	}

	return claims.Cluster, claims.Sub, nil
}

// issueManifestToken creates a signed token containing the user
// identity, cluster name, and expiry timestamp.
func (uc *FleetUseCase) issueManifestToken(cluster, userName string) (string, error) {
	claims := manifestTokenClaims{
		Sub:     userName,
		Cluster: cluster,
		Exp:     time.Now().Add(manifestTokenTTL).Unix(),
	}

	payload, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("marshal token claims: %w", err)
	}

	mac := hmac.New(sha256.New, uc.manifestCfg.HMACKey)
	mac.Write(payload)
	sig := mac.Sum(nil)

	return base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(sig), nil
}

// manifestTokenClaims is the JSON payload embedded in manifest tokens.
type manifestTokenClaims struct {
	Sub     string `json:"sub"`
	Cluster string `json:"cluster"`
	Exp     int64  `json:"exp"`
}

// GenerateAgentManifest produces a multi-document YAML manifest for
// installing the otterscale agent on a target Kubernetes cluster.
// The manifest includes a Namespace, ServiceAccount,
// ClusterRoleBinding (binding userName to cluster-admin), and a
// Deployment that runs the agent with the correct server/tunnel URLs.
func (uc *FleetUseCase) GenerateAgentManifest(cluster, userName string) (string, error) {
	if cluster == "" {
		return "", &ErrInvalidInput{Field: "cluster", Message: "must not be empty"}
	}
	if !reClusterName.MatchString(cluster) {
		return "", &ErrInvalidInput{
			Field:   "cluster",
			Message: fmt.Sprintf("must match [a-z0-9]([a-z0-9-]*[a-z0-9])?, got %q", cluster),
		}
	}
	if userName == "" {
		return "", &ErrInvalidInput{Field: "user_name", Message: "must not be empty"}
	}

	return uc.renderer.RenderAgentManifest(ManifestParams{
		Cluster:   cluster,
		UserName:  userName,
		Image:     fmt.Sprintf("ghcr.io/otterscale/otterscale:%s", uc.version),
		ServerURL: uc.manifestCfg.ServerURL,
		TunnelURL: uc.manifestCfg.TunnelURL,
	})
}
