package domain

import (
	"context"
	"errors"
	"time"
)

type Role string

const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

type User struct {
	ID           int64
	Username     string
	Email        string
	PasswordHash string
	Role         Role
	Disabled     bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrUserNotFound is returned by Repo lookups when no row matches.
var ErrUserNotFound = errors.New("user not found")

// ErrUserConflict is returned when a unique constraint (username/email) is violated.
var ErrUserConflict = errors.New("user already exists")

type Repo interface {
	Create(ctx context.Context, username, email, passwordHash string, role Role) (*User, error)
	GetByID(ctx context.Context, id int64) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	GetByEmail(ctx context.Context, email string) (*User, error)
	Count(ctx context.Context) (int64, error)
	List(ctx context.Context, offset, limit int32) ([]*User, error)
	UpdateProfile(ctx context.Context, id int64, email string) (*User, error)
	UpdatePassword(ctx context.Context, id int64, passwordHash string) error
	UpdateRole(ctx context.Context, id int64, role Role) (*User, error)
	UpdateDisabled(ctx context.Context, id int64, disabled bool) (*User, error)
}
