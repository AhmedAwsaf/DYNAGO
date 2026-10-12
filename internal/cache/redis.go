package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCache wraps a Redis client as a Cache.
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisCache connects to Redis and returns a Cache.
func NewRedisCache(url string, ttl time.Duration) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, err
	}
	return &RedisCache{client: client, ttl: ttl}, nil
}

func (r *RedisCache) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := r.client.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return val, true
}

func (r *RedisCache) Set(ctx context.Context, key string, val []byte) {
	r.client.Set(ctx, key, val, r.ttl)
}

func (r *RedisCache) Del(ctx context.Context, key string) {
	r.client.Del(ctx, key)
}

func (r *RedisCache) Flush(ctx context.Context) {
	r.client.FlushDB(ctx)
}

func (r *RedisCache) EvictPrefix(ctx context.Context, prefix string) {
	iter := r.client.Scan(ctx, 0, prefix+"*", 100).Iterator()
	for iter.Next(ctx) {
		r.client.Del(ctx, iter.Val())
	}
}
