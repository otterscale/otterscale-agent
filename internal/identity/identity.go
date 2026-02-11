package identity

import "context"

type contextKey struct{}

var userKey = contextKey{}

func GetUser(ctx context.Context) (string, bool) {
	sub, ok := ctx.Value(userKey).(string)
	return sub, ok
}

func WithUser(ctx context.Context, user string) context.Context {
	return context.WithValue(ctx, userKey, user)
}
