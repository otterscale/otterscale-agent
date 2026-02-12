package core

// UserInfo holds the authenticated user's identity and group memberships.
type UserInfo struct {
	Subject string
	Groups  []string
}
