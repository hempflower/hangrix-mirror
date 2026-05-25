// Package infra holds the Redis-backed session store implementing
// domain.SessionStore. Sessions are short-lived KV entries with a TTL that
// matches the cookie expiry, plus a per-user secondary index so we can revoke
// every session for one user without scanning all keys.
package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hangrix/hangrix/apps/hangrix/internal/modules/auth/domain"
)

const (
	sessionKeyPrefix      = "session:"
	userSessionsKeyPrefix = "user_sessions:"
)

// RedisSessionStore persists sessions in Redis. Each session is stored under
// `session:<token>` as JSON with TTL = ExpiresAt - now. A companion set at
// `user_sessions:<user_id>` lets us enumerate and bulk-delete all of a user's
// sessions when the account is disabled or logged out everywhere.
type RedisSessionStore struct {
	client redis.UniversalClient
}

type RedisSessionStoreDeps struct {
	Client redis.UniversalClient
}

func NewRedisSessionStore(deps *RedisSessionStoreDeps) *RedisSessionStore {
	return &RedisSessionStore{client: deps.Client}
}

type sessionPayload struct {
	UserID    int64     `json:"user_id"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func (s *RedisSessionStore) Create(ctx context.Context, token string, userID int64, expiresAt time.Time) (*domain.Session, error) {
	now := time.Now()
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return nil, errors.New("session expiresAt must be in the future")
	}

	payload := sessionPayload{UserID: userID, ExpiresAt: expiresAt, CreatedAt: now}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal session: %w", err)
	}

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, sessionKey(token), body, ttl)
	pipe.SAdd(ctx, userSessionsKey(userID), token)
	// Index TTL is set to the max session TTL so the set self-cleans even if
	// every token has been individually expired/deleted.
	pipe.ExpireGT(ctx, userSessionsKey(userID), ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("redis create session: %w", err)
	}

	return &domain.Session{
		Token:     token,
		UserID:    userID,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}, nil
}

func (s *RedisSessionStore) Get(ctx context.Context, token string) (*domain.Session, error) {
	body, err := s.client.Get(ctx, sessionKey(token)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return nil, domain.ErrSessionNotFound
		}
		return nil, fmt.Errorf("redis get session: %w", err)
	}
	var p sessionPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}
	return &domain.Session{
		Token:     token,
		UserID:    p.UserID,
		ExpiresAt: p.ExpiresAt,
		CreatedAt: p.CreatedAt,
	}, nil
}

func (s *RedisSessionStore) Delete(ctx context.Context, token string) error {
	// Best-effort: read user_id so we can prune the index too.
	if sess, err := s.Get(ctx, token); err == nil {
		pipe := s.client.TxPipeline()
		pipe.Del(ctx, sessionKey(token))
		pipe.SRem(ctx, userSessionsKey(sess.UserID), token)
		if _, err := pipe.Exec(ctx); err != nil {
			return fmt.Errorf("redis delete session: %w", err)
		}
		return nil
	}
	return s.client.Del(ctx, sessionKey(token)).Err()
}

func (s *RedisSessionStore) DeleteByUser(ctx context.Context, userID int64) error {
	tokens, err := s.client.SMembers(ctx, userSessionsKey(userID)).Result()
	if err != nil {
		return fmt.Errorf("redis list user sessions: %w", err)
	}
	pipe := s.client.TxPipeline()
	for _, t := range tokens {
		pipe.Del(ctx, sessionKey(t))
	}
	pipe.Del(ctx, userSessionsKey(userID))
	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("redis delete user sessions: %w", err)
	}
	return nil
}

// DeleteExpired is a no-op: Redis evicts expired keys on its own. We keep the
// method to satisfy domain.SessionStore so the Postgres-backed alternative
// could fit the same interface later if we ever need to swap it back.
func (s *RedisSessionStore) DeleteExpired(ctx context.Context) error {
	return nil
}

func sessionKey(token string) string      { return sessionKeyPrefix + token }
func userSessionsKey(userID int64) string { return fmt.Sprintf("%s%d", userSessionsKeyPrefix, userID) }
