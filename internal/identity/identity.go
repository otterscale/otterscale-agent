package identity

import "context"

// UserInfo holds the authenticated user's identity and group memberships.
type UserInfo struct {
	Subject string
	Groups  []string
}

type contextKey struct{}

var userInfoKey = contextKey{}

// GetUserInfo retrieves the full UserInfo from the context.
func GetUserInfo(ctx context.Context) (UserInfo, bool) {
	info, ok := ctx.Value(userInfoKey).(UserInfo)
	return info, ok
}

// WithUserInfo stores a UserInfo in the context.
func WithUserInfo(ctx context.Context, info UserInfo) context.Context {
	return context.WithValue(ctx, userInfoKey, info)
}

// WithUser stores a user subject in the context (backward compatible helper).
func WithUser(ctx context.Context, user string) context.Context {
	return WithUserInfo(ctx, UserInfo{Subject: user, Groups: []string{"system:authenticated"}})
}
