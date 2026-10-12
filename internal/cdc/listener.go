package cdc

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Listener manages the PostgreSQL logical replication consumer.
// It connects to a replication slot via pg_logical_slot_get_changes,
// decodes WAL events, and forwards them to the Dispatcher.
type Listener struct {
	pool         *pgxpool.Pool
	slotName     string
	pluginName   string
	pollInterval time.Duration
	dispatcher   *Dispatcher
	cancel       context.CancelFunc
	wg           sync.WaitGroup
	active       bool
}

// Config holds options for the CDC listener.
type Config struct {
	SlotName     string
	PluginName   string
	PollInterval time.Duration
}

// NewListener creates a new CDC replication Listener.
func NewListener(pool *pgxpool.Pool, cfg Config, dispatcher *Dispatcher) *Listener {
	slot := cfg.SlotName
	if slot == "" {
		slot = "dynago_cdc_slot"
	}
	plugin := cfg.PluginName
	if plugin == "" {
		plugin = "test_decoding"
	}
	interval := cfg.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}

	return &Listener{
		pool:         pool,
		slotName:     slot,
		pluginName:   plugin,
		pollInterval: interval,
		dispatcher:   dispatcher,
	}
}

// Start initializes the logical replication slot and starts the background worker.
func (l *Listener) Start(ctx context.Context) error {
	workerCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	// Ensure replication slot exists
	if err := l.ensureReplicationSlot(workerCtx); err != nil {
		log.Printf("[cdc] logical replication slot init failed: %v", err)
		log.Printf("[cdc] operating with application-level change capture")
		return nil
	}

	l.active = true
	l.wg.Add(1)
	go l.run(workerCtx)

	log.Printf("[cdc] logical replication listener active (slot=%s, plugin=%s, poll=%v)",
		l.slotName, l.pluginName, l.pollInterval)
	return nil
}

// Stop terminates the background replication consumer.
func (l *Listener) Stop() {
	if l.cancel != nil {
		l.cancel()
	}
	l.wg.Wait()
	log.Printf("[cdc] listener stopped")
}

// ensureReplicationSlot checks if the logical replication slot exists, and creates it if not.
func (l *Listener) ensureReplicationSlot(ctx context.Context) error {
	var exists bool
	err := l.pool.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM pg_replication_slots WHERE slot_name = $1)",
		l.slotName,
	).Scan(&exists)
	if err != nil {
		return fmt.Errorf("check replication slot: %w", err)
	}

	if exists {
		log.Printf("[cdc] replication slot %q already exists", l.slotName)
		return nil
	}

	// Create replication slot using test_decoding plugin
	log.Printf("[cdc] creating logical replication slot %q using plugin %q...", l.slotName, l.pluginName)
	_, err = l.pool.Exec(ctx,
		"SELECT pg_create_logical_replication_slot($1, $2)",
		l.slotName, l.pluginName,
	)
	if err != nil {
		return fmt.Errorf("create replication slot %q: %w", l.slotName, err)
	}

	log.Printf("[cdc] replication slot %q created successfully", l.slotName)
	return nil
}

// run is the background worker that polls the replication slot for WAL changes.
func (l *Listener) run(ctx context.Context) {
	defer l.wg.Done()

	ticker := time.NewTicker(l.pollInterval)
	defer ticker.Stop()

	query := "SELECT lsn, xid::text, data FROM pg_logical_slot_get_changes($1, NULL, 100)"

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			changesProcessed, err := l.pollChanges(ctx, query)
			if err != nil {
				// Avoid flooding logs on transient errors
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}

			// If we processed a full batch, poll again immediately without waiting for ticker
			if changesProcessed >= 100 {
				continue
			}
		}
	}
}

// pollChanges reads one batch of changes from the logical replication slot.
func (l *Listener) pollChanges(ctx context.Context, query string) (int, error) {
	rows, err := l.pool.Query(ctx, query, l.slotName)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var lsn, xid, data string
		if err := rows.Scan(&lsn, &xid, &data); err != nil {
			log.Printf("[cdc] scan row error: %v", err)
			continue
		}
		count++

		mutation, err := DecodeWALMessage(data, lsn)
		if err != nil {
			log.Printf("[cdc] decode error on lsn %s: %v", lsn, err)
			continue
		}
		if mutation == nil {
			continue
		}

		// Dispatch decoded mutation
		if l.dispatcher != nil {
			l.dispatcher.Dispatch(ctx, *mutation)
		}
	}

	return count, rows.Err()
}
