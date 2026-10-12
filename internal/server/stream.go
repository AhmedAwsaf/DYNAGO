package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// MutationEvent represents a real-time table modification event.
type MutationEvent struct {
	Table     string          `json:"table"`
	Action    string          `json:"action"` // "INSERT", "UPDATE", "DELETE"
	Record    json.RawMessage `json:"record,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

// StreamBroker manages active SSE subscribers and broadcasts mutation events.
type StreamBroker struct {
	mu      sync.RWMutex
	clients map[chan MutationEvent]string // channel -> table name filter ("" = all tables)
}

// NewStreamBroker creates an initialized StreamBroker.
func NewStreamBroker() *StreamBroker {
	return &StreamBroker{
		clients: make(map[chan MutationEvent]string),
	}
}

// Subscribe registers a new subscriber channel with an optional table filter.
func (b *StreamBroker) Subscribe(tableFilter string) chan MutationEvent {
	b.mu.Lock()
	defer b.mu.Unlock()

	ch := make(chan MutationEvent, 64)
	b.clients[ch] = tableFilter
	return ch
}

// Unsubscribe removes and closes a subscriber channel.
func (b *StreamBroker) Unsubscribe(ch chan MutationEvent) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if _, ok := b.clients[ch]; ok {
		delete(b.clients, ch)
		close(ch)
	}
}

// Publish distributes an event to all interested subscribers without blocking.
func (b *StreamBroker) Publish(evt MutationEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for ch, filter := range b.clients {
		if filter == "" || filter == evt.Table {
			select {
			case ch <- evt:
			default:
				// Channel buffer full — drop to prevent slow clients from blocking engine
			}
		}
	}
}

// ClientCount returns the number of currently active streaming subscribers.
func (b *StreamBroker) ClientCount() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.clients)
}

// handleStreamTable streams real-time SSE mutation events for a single table.
func (h *handler) handleStreamTable(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	if _, ok := h.graph.GetTable(table); !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	// Permission check (requires read permission on table)
	if err := h.checkTablePermission(r.Context(), table, "read"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	h.serveSSE(w, r, table)
}

// handleStreamAll streams real-time SSE mutation events for all tables (authenticated).
func (h *handler) handleStreamAll(w http.ResponseWriter, r *http.Request) {
	if h.cfg.JWTSecret != "" {
		user, ok := UserFromContext(r.Context())
		if !ok {
			jsonError(w, http.StatusUnauthorized, "authentication required for global event stream")
			return
		}
		if user.Role != "admin" {
			jsonError(w, http.StatusForbidden, "admin privileges required to subscribe to global event stream")
			return
		}
	}

	h.serveSSE(w, r, "")
}

// serveSSE handles the HTTP Server-Sent Events lifecycle for a subscriber.
func (h *handler) serveSSE(w http.ResponseWriter, r *http.Request, tableFilter string) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported by client connection", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	ch := h.stream.Subscribe(tableFilter)
	defer h.stream.Unsubscribe(ch)

	// Send initial connected handshake event
	initMsg := map[string]any{
		"connected": true,
		"table":     tableFilter,
		"message":   "subscribed to real-time mutation stream",
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}
	initBytes, _ := json.Marshal(initMsg)
	fmt.Fprintf(w, "event: connected\ndata: %s\n\n", initBytes)
	flusher.Flush()

	// Keep-alive heartbeat ticker every 15 seconds
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": keepalive %d\n\n", time.Now().Unix())
			flusher.Flush()
		case evt, ok := <-ch:
			if !ok {
				return
			}
			payload, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Action, payload)
			flusher.Flush()
		}
	}
}
