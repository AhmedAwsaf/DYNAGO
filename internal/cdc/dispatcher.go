package cdc

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/dynago/dynago/internal/cache"
	"github.com/dynago/dynago/internal/server"
)

// Dispatcher broadcasts decoded database mutations to the cache invalidator,
// active SSE clients, and optional external webhooks.
type Dispatcher struct {
	mu          sync.RWMutex
	invalidator *cache.Invalidator
	broker      *server.StreamBroker
	hookURL     string
	httpClient  *http.Client
}

// NewDispatcher creates a configured mutation Dispatcher.
func NewDispatcher(inv *cache.Invalidator, broker *server.StreamBroker, hookURL string) *Dispatcher {
	return &Dispatcher{
		invalidator: inv,
		broker:      broker,
		hookURL:     hookURL,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

// Dispatch processes a single row mutation event across all downstream consumers.
func (d *Dispatcher) Dispatch(ctx context.Context, m RowMutation) {
	if m.Table == "" {
		return
	}

	log.Printf("[cdc] mutation: table=%s action=%s lsn=%s", m.Table, m.Action, m.LSN)

	// 1. Evict related cache entries
	if d.invalidator != nil {
		d.invalidator.EvictTable(ctx, m.Table)
	}

	// 2. Broadcast to real-time SSE clients
	if d.broker != nil {
		recordBytes, _ := json.Marshal(m.Record)
		d.broker.Publish(server.MutationEvent{
			Table:     m.Table,
			Action:    m.Action,
			Record:    recordBytes,
			Timestamp: m.Timestamp,
		})
	}

	// 3. Fire external webhook if configured
	if d.hookURL != "" {
		go d.fireWebhook(m)
	}
}

// fireWebhook sends an asynchronous notification payload to an external webhook.
func (d *Dispatcher) fireWebhook(m RowMutation) {
	payload, err := json.Marshal(m)
	if err != nil {
		return
	}

	req, err := http.NewRequest("POST", d.hookURL, bytes.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Dynago-CDC/1.0")

	resp, err := d.httpClient.Do(req)
	if err != nil {
		log.Printf("[cdc:webhook] delivery to %s failed: %v", d.hookURL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		log.Printf("[cdc:webhook] delivery to %s returned HTTP %d", d.hookURL, resp.StatusCode)
	}
}
