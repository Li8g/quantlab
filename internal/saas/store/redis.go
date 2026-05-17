package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"quantlab/internal/saas/config"
)

// RedisClient is a thin wrapper exposing the Get/Set/Del verbs needed by
// the caching layer. The underlying *redis.Client is exported for code
// paths that need pipeline / scripting / pubsub support.
type RedisClient struct {
	C *redis.Client
}

// NewRedis dials the Redis server described in cfg.Redis.
func NewRedis(ctx context.Context, cfg *config.Config) (*RedisClient, error) {
	if cfg == nil {
		return nil, errors.New("store.NewRedis: cfg is nil")
	}
	c := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	if err := c.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("store.NewRedis: ping: %w", err)
	}
	return &RedisClient{C: c}, nil
}

// Get reads a string value. Returns ("", nil) on miss to keep callers
// from importing go-redis just for redis.Nil.
func (r *RedisClient) Get(ctx context.Context, key string) (string, error) {
	v, err := r.C.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("redis get %s: %w", key, err)
	}
	return v, nil
}

// Set writes a string value with a TTL. ttl=0 means no expiry.
func (r *RedisClient) Set(ctx context.Context, key, value string, ttl time.Duration) error {
	if err := r.C.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("redis set %s: %w", key, err)
	}
	return nil
}

// Del removes one or more keys. Returns the count of keys actually deleted.
func (r *RedisClient) Del(ctx context.Context, keys ...string) (int64, error) {
	n, err := r.C.Del(ctx, keys...).Result()
	if err != nil {
		return 0, fmt.Errorf("redis del: %w", err)
	}
	return n, nil
}

// Close releases the underlying connection pool.
func (r *RedisClient) Close() error {
	return r.C.Close()
}
