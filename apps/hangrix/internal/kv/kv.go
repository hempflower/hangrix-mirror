// Package kv provides the shared Redis client used as the KV store for
// sessions and any other short-lived / hot-path key-value data. We expose
// redis.UniversalClient directly so swapping standalone → sentinel → cluster
// is purely a config change (comma-separated Addr for multi-node), never a
// dependency-type change.
package kv

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/hangrix/hangrix/apps/hangrix/internal/config"
	"github.com/hangrix/hangrix/pkg/ioc"
)

type ClientDeps struct {
	Config *config.Config
}

func NewClient(deps *ClientDeps) redis.UniversalClient {
	uc := redis.NewUniversalClient(&redis.UniversalOptions{
		Addrs:    splitAddrs(deps.Config.Redis.Addr),
		Password: deps.Config.Redis.Password,
		DB:       deps.Config.Redis.DB,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := uc.Ping(ctx).Err(); err != nil {
		panic(fmt.Errorf("ping redis: %w", err))
	}
	return uc
}

func splitAddrs(raw string) []string {
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func Module() *ioc.Module {
	m := ioc.NewModule()
	m.Provide(NewClient).ToInterface(new(redis.UniversalClient))
	m.Provide(NewRepoCache).ToSelf()
	return m
}
