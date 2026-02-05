package impersonation

import (
	"context"
)

type contextKey struct{}

var subjectKey = contextKey{}

func GetSubject(ctx context.Context) (string, bool) {
	sub, ok := ctx.Value(subjectKey).(string)
	return sub, ok
}

func WithSubject(ctx context.Context, sub string) context.Context {
	return context.WithValue(ctx, subjectKey, sub)
}
