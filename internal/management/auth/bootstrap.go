package auth

import "context"

type BootstrapAdmin struct {
	User         User
	PasswordHash string
}

type BootstrapStore interface {
	// CreateFirstSuperAdmin must atomically reject creation when any admin user
	// row exists, including disabled or soft-deleted rows.
	CreateFirstSuperAdmin(ctx context.Context, admin BootstrapAdmin) (User, bool, error)
}
