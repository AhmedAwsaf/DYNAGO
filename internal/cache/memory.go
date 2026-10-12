package cache

import (
	"context"
	"sync"
	"time"
)

// Cache is the interface for both Redis and in-memory caches.
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte)
	Del(ctx context.Context, key string)
	Flush(ctx context.Context)
	EvictPrefix(ctx context.Context, prefix string)
}

// --- In-memory cache (no dependency needed) ---

type memEntry struct {
	data    []byte
	expires time.Time
}

// MemoryCache is a simple TTL map cache.
type MemoryCache struct {
	mu    sync.RWMutex
	items map[string]memEntry
	ttl   time.Duration
}

// NewMemoryCache creates a TTL-based in-memory cache.
func NewMemoryCache(ttl time.Duration) *MemoryCache {
	mc := &MemoryCache{
		items: make(map[string]memEntry),
		ttl:   ttl,
	}
	// ponytail: lazy eviction on Get, no background goroutine unless scale demands it
	return mc
}

func (m *MemoryCache) Get(_ context.Context, key string) ([]byte, bool) {
	m.mu.RLock()
	entry, ok := m.items[key]
	m.mu.RUnlock()
	if !ok || time.Now().After(entry.expires) {
		return nil, false
	}
	return entry.data, true
}

func (m *MemoryCache) Set(_ context.Context, key string, val []byte) {
	m.mu.Lock()
	m.items[key] = memEntry{data: val, expires: time.Now().Add(m.ttl)}
	m.mu.Unlock()
}

func (m *MemoryCache) Del(_ context.Context, key string) {
	m.mu.Lock()
	delete(m.items, key)
	m.mu.Unlock()
}

func (m *MemoryCache) Flush(_ context.Context) {
	m.mu.Lock()
	m.items = make(map[string]memEntry)
	m.mu.Unlock()
}

func (m *MemoryCache) EvictPrefix(_ context.Context, prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k := range m.items {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			delete(m.items, k)
		}
	}
}
