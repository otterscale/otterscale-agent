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
