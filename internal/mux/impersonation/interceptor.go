package impersonation

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/otterscale/otterscale-agent/internal/config"
	"github.com/otterscale/otterscale-agent/internal/identity"
)

type Interceptor struct {
	*oidc.IDTokenVerifier
}

func NewInterceptor(conf *config.Config) (*Interceptor, error) {
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

	return &Interceptor{
		IDTokenVerifier: verifier,
	}, nil
}

func (i *Interceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		newCtx, err := i.enrichContextWithSubject(ctx, req.Header())
		if err != nil {
			return nil, err
		}
		return next(newCtx, req)
	}
}

func (i *Interceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (i *Interceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return func(ctx context.Context, conn connect.StreamingHandlerConn) error {
		newCtx, err := i.enrichContextWithSubject(ctx, conn.RequestHeader())
		if err != nil {
			return err
		}
		return next(newCtx, conn)
	}
}

func (i *Interceptor) enrichContextWithSubject(ctx context.Context, h http.Header) (context.Context, error) {
	token, err := extractToken(h)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, err)
	}

	idToken, err := i.Verify(ctx, token)
	if err != nil {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("invalid token"))
	}

	return identity.WithUserInfo(ctx, identity.UserInfo{
		Subject: idToken.Subject,
		Groups:  []string{"system:authenticated"}, // hardcoded
	}), nil
}

func extractToken(h http.Header) (string, error) {
	auth := h.Get("Authorization")
	if auth == "" {
		return "", fmt.Errorf("missing authorization header")
	}

	parts := strings.Fields(auth)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return "", fmt.Errorf("invalid authorization header format")
	}

	return parts[1], nil
}
