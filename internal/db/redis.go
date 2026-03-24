// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright (C) 2026 SourceBridge Contributors

package db

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

// Cache provides a cache interface that works with both Redis and in-memory.
type Cache interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value string, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
}

// --- In-memory cache ---

// InMemoryCache provides an in-memory cache for OSS mode.
type InMemoryCache struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	value     string
	expiresAt time.Time
}

// NewInMemoryCache creates a new in-memory cache.
func NewInMemoryCache() *InMemoryCache {
	return &InMemoryCache{
		entries: make(map[string]cacheEntry),
	}
}

func (c *InMemoryCache) Get(_ context.Context, key string) (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	entry, ok := c.entries[key]
	if !ok {
		return "", nil
	}
	if !entry.expiresAt.IsZero() && time.Now().After(entry.expiresAt) {
		return "", nil
	}
	return entry.value, nil
}

func (c *InMemoryCache) Set(_ context.Context, key string, value string, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var expiresAt time.Time
	if ttl > 0 {
		expiresAt = time.Now().Add(ttl)
	}
	c.entries[key] = cacheEntry{value: value, expiresAt: expiresAt}
	return nil
}

func (c *InMemoryCache) Delete(_ context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.entries, key)
	return nil
}

// --- Redis cache ---

// RedisCache wraps a go-redis client behind the Cache interface.
type RedisCache struct {
	client *redis.Client
}

// NewRedisCache creates and pings a Redis cache.
func NewRedisCache(url string) (*RedisCache, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis URL: %w", err)
	}

	client := redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("Redis ping failed: %w", err)
	}

	return &RedisCache{client: client}, nil
}

func (c *RedisCache) Get(ctx context.Context, key string) (string, error) {
	val, err := c.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return "", nil
	}
	return val, err
}

func (c *RedisCache) Set(ctx context.Context, key string, value string, ttl time.Duration) error {
	return c.client.Set(ctx, key, value, ttl).Err()
}

func (c *RedisCache) Delete(ctx context.Context, key string) error {
	return c.client.Del(ctx, key).Err()
}

// Close closes the underlying Redis connection.
func (c *RedisCache) Close() error {
	return c.client.Close()
}

// --- Factory ---

// NewCache creates the appropriate cache based on configuration.
func NewCache(cfg config.StorageConfig) Cache {
	if cfg.RedisMode == "memory" {
		slog.Info("using in-memory cache")
		return NewInMemoryCache()
	}

	slog.Info("connecting to Redis", "url", cfg.RedisURL)
	rc, err := NewRedisCache(cfg.RedisURL)
	if err != nil {
		slog.Warn("Redis init failed, falling back to in-memory cache", "error", err)
		return NewInMemoryCache()
	}
	slog.Info("Redis cache connected")
	return rc
}
