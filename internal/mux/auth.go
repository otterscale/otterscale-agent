package mux

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/authn"
	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/otterscale/otterscale-agent/internal/config"
)

// UserInfo holds the authenticated user's identity and group memberships.
type UserInfo struct {
	Subject string
	Groups  []string
}

// NewAuthMiddleware creates a new authentication middleware.
func NewAuthMiddleware(conf *config.Config) (*authn.Middleware, error) {
	var (
		issuer   = conf.ServerKeycloakRealmURL()
		clientID = conf.ServerKeycloakClientID()
	)

	const timeout = 30 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, fmt.Errorf("failed to init oidc provider: %w", err)
	}

	verifier := provider.Verifier(&oidc.Config{
		ClientID: clientID,
	})

	authenticate := func(ctx context.Context, r *http.Request) (any, error) {
		authHeader := r.Header.Get("Authorization")
		token, found := strings.CutPrefix(authHeader, "Bearer ")
		if !found || token == "" {
			return nil, authn.Errorf("missing or invalid bearer token")
		}

		token = strings.TrimSpace(token)

		idToken, err := verifier.Verify(ctx, token)
		if err != nil {
			return nil, authn.Errorf("invalid token: %s", err)
		}

		return UserInfo{
			Subject: idToken.Subject,
			Groups:  []string{"system:authenticated"}, // hardcoded
		}, nil
	}

	return authn.NewMiddleware(authenticate), nil
}
