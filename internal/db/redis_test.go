package db

import (
	"context"
	"testing"
	"time"

	"github.com/sourcebridge/sourcebridge/internal/config"
)

func TestInMemoryCacheSetGet(t *testing.T) {
	cache := NewInMemoryCache()
	ctx := context.Background()

	err := cache.Set(ctx, "key1", "value1", 0)
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "value1" {
		t.Errorf("expected value1, got %s", val)
	}
}

func TestInMemoryCacheMiss(t *testing.T) {
	cache := NewInMemoryCache()
	ctx := context.Background()

	val, err := cache.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for miss, got %s", val)
	}
}

func TestInMemoryCacheTTL(t *testing.T) {
	cache := NewInMemoryCache()
	ctx := context.Background()

	err := cache.Set(ctx, "key1", "value1", 1*time.Millisecond)
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "" {
		t.Errorf("expected empty string for expired entry, got %s", val)
	}
}

func TestInMemoryCacheDelete(t *testing.T) {
	cache := NewInMemoryCache()
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", 0)
	cache.Delete(ctx, "key1")

	val, _ := cache.Get(ctx, "key1")
	if val != "" {
		t.Errorf("expected empty string after delete, got %s", val)
	}
}

// --- NewCache factory tests ---

func TestNewCacheMemoryMode(t *testing.T) {
	cfg := config.StorageConfig{RedisMode: "memory"}
	cache := NewCache(cfg)

	if cache == nil {
		t.Fatal("NewCache should return non-nil cache")
	}

	// Should be in-memory cache, verify it works
	ctx := context.Background()
	err := cache.Set(ctx, "test", "value", 0)
	if err != nil {
		t.Fatalf("Set() error: %v", err)
	}
	val, err := cache.Get(ctx, "test")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if val != "value" {
		t.Errorf("expected 'value', got %q", val)
	}
}

func TestNewCacheRedisFallback(t *testing.T) {
	// With an invalid Redis URL, should fall back to in-memory
	cfg := config.StorageConfig{
		RedisMode: "external",
		RedisURL:  "redis://localhost:59999/0",
	}
	cache := NewCache(cfg)

	if cache == nil {
		t.Fatal("NewCache should return non-nil cache even on Redis failure")
	}

	// Should still work (in-memory fallback)
	ctx := context.Background()
	err := cache.Set(ctx, "fallback", "works", 0)
	if err != nil {
		t.Fatalf("Set() error on fallback cache: %v", err)
	}
	val, _ := cache.Get(ctx, "fallback")
	if val != "works" {
		t.Errorf("expected 'works', got %q", val)
	}
}

func TestNewRedisCache_InvalidURL(t *testing.T) {
	_, err := NewRedisCache("not-a-url")
	if err == nil {
		t.Error("expected error for invalid Redis URL")
	}
}

func TestNewRedisCache_Unreachable(t *testing.T) {
	_, err := NewRedisCache("redis://localhost:59999/0")
	if err == nil {
		t.Error("expected error for unreachable Redis")
	}
}

func TestCacheInterface(t *testing.T) {
	// Verify InMemoryCache satisfies Cache interface
	var _ Cache = NewInMemoryCache()
}
