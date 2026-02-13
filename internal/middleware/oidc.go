// Package middleware provides HTTP middleware for the otterscale
// server, including OIDC-based authentication.
package middleware

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"connectrpc.com/authn"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/otterscale/otterscale-agent/internal/core"
)

// NewOIDC creates a ConnectRPC authentication middleware that verifies
// incoming Bearer tokens against the given OIDC issuer and client ID.
//
// On success, the authenticated user's subject and a fixed
// "system:authenticated" group are stored in the request context as
// core.UserInfo. Keycloak-native groups are intentionally not
// forwarded to keep Keycloak and Kubernetes group namespaces separate.
func NewOIDC(issuer, clientID string) (*authn.Middleware, error) {
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
			Groups:  []string{"system:authenticated"},
		}, nil
	}

	return authn.NewMiddleware(authenticate), nil
}
