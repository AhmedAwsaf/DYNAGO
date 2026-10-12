package cache

import (
	"context"
	"log"
)

// Invalidator coordinates deterministic, targeted cache eviction for modified tables and queries.
type Invalidator struct {
	cache Cache
}

// NewInvalidator creates an Invalidator backed by the active cache layer.
func NewInvalidator(c Cache) *Invalidator {
	return &Invalidator{cache: c}
}

// EvictTable invalidates all cached SELECT query responses for a modified table.
// Key prefix: "dynago:select:/api/v1/{table}"
func (inv *Invalidator) EvictTable(ctx context.Context, table string) {
	if inv == nil || inv.cache == nil || table == "" {
		return
	}
	prefix := "dynago:select:/api/v1/" + table
	inv.cache.EvictPrefix(ctx, prefix)
	log.Printf("[cache:invalidator] evicted cache for table %q (prefix: %s)", table, prefix)
}

// EvictAll wipes the entire cache (used on schema refresh or DDL operations).
func (inv *Invalidator) EvictAll(ctx context.Context) {
	if inv == nil || inv.cache == nil {
		return
	}
	inv.cache.Flush(ctx)
	log.Printf("[cache:invalidator] flushed entire cache")
}
