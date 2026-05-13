package domain

import (
	"context"
	"errors"
	"time"
)

type Session struct {
	Token     string
	UserID    int64
	ExpiresAt time.Time
	CreatedAt time.Time
}

var ErrSessionNotFound = errors.New("session not found")

type SessionStore interface {
	Create(ctx context.Context, token string, userID int64, expiresAt time.Time) (*Session, error)
	Get(ctx context.Context, token string) (*Session, error)
	Delete(ctx context.Context, token string) error
	DeleteByUser(ctx context.Context, userID int64) error
	DeleteExpired(ctx context.Context) error
}
