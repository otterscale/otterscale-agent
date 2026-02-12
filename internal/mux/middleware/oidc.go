package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/authn"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/core"
)

// NewOIDC creates a new OIDC authentication middleware.
func NewOIDC(conf *config.Config) (*authn.Middleware, error) {
	var (
		issuer   = conf.ServerKeycloakRealmURL()
		clientID = conf.ServerKeycloakClientID()
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to init oidc provider: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: clientID,
	})

	authenticate := func(ctx context.Context, r *http.Request) (any, error) {
		token, found := authn.BearerToken(r)
		if !found || token == "" {
			return nil, authn.Errorf("missing or invalid bearer token")
		}

		idToken, err := verifier.Verify(ctx, token)
		if err != nil {
			return nil, authn.Errorf("invalid token: %s", err)
		}

		return core.UserInfo{
			Subject: idToken.Subject,
			Groups:  []string{"system:authenticated"}, // hardcoded, for separation of keycloak and kubernetes groups
		}, nil
	}

	return authn.NewMiddleware(authenticate), nil
}
