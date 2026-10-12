package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/dynago/dynago/internal/cache"
	"github.com/dynago/dynago/internal/catalog"
	"github.com/dynago/dynago/internal/config"
	"github.com/dynago/dynago/internal/query"
	"github.com/dynago/dynago/internal/wasm"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewRouter creates the HTTP mux with all dynamic endpoints.
func NewRouter(
	graph *catalog.SchemaGraph,
	compiler *query.Compiler,
	pool *pgxpool.Pool,
	cacheLayer cache.Cache,
	invalidator *cache.Invalidator,
	streamBroker *StreamBroker,
	plugins *wasm.Manager,
	cfg *config.Config,
) http.Handler {
	mux := http.NewServeMux()

	if streamBroker == nil {
		streamBroker = NewStreamBroker()
	}
	if invalidator == nil {
		invalidator = cache.NewInvalidator(cacheLayer)
	}

	h := &handler{
		graph:       graph,
		compiler:    compiler,
		pool:        pool,
		cache:       cacheLayer,
		invalidator: invalidator,
		plugins:     plugins,
		cfg:         cfg,
		stream:      streamBroker,
	}

	// Health check
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Schema refresh endpoint
	mux.HandleFunc("POST /api/v1/_refresh", h.handleRefresh)

	// Endpoint checklist — lists all auto-generated endpoints
	mux.HandleFunc("GET /api/v1/_endpoints", h.handleEndpoints)

	// Schema info
	mux.HandleFunc("GET /api/v1/_schema", h.handleSchema)
	mux.HandleFunc("GET /api/v1/_schema/{table}", h.handleSchemaTable)

	// Dynamic CRUD for any table: /api/v1/{table}
	mux.HandleFunc("GET /api/v1/{table}", h.handleSelect)
	mux.HandleFunc("POST /api/v1/{table}", h.handleInsert)
	mux.HandleFunc("PATCH /api/v1/{table}", h.handleUpdate)
	mux.HandleFunc("DELETE /api/v1/{table}", h.handleDelete)

	// Web UI & Interactive Dashboard
	mux.HandleFunc("GET /{$}", h.handleUI)
	mux.HandleFunc("GET /ui", h.handleUI)
	mux.HandleFunc("GET /api/v1/_ui/meta", h.handleUIMeta)
	mux.HandleFunc("POST /api/v1/_ui/sql", h.handleUISQL)
	mux.HandleFunc("GET /api/v1/_ui/data/{table}", h.handleUIData)

	// Authentication
	mux.HandleFunc("POST /api/v1/_auth/login", h.handleAuthLogin)
	mux.HandleFunc("POST /api/v1/_auth/register", h.handleAuthRegister)
	mux.HandleFunc("GET /api/v1/_auth/me", h.handleAuthMe)

	// RBAC Users & Table Permissions
	mux.HandleFunc("GET /api/v1/_auth/users", h.handleListUsers)
	mux.HandleFunc("POST /api/v1/_auth/users", h.handleCreateUser)
	mux.HandleFunc("PATCH /api/v1/_auth/users/{username}", h.handleUpdateUser)
	mux.HandleFunc("PUT /api/v1/_auth/users/{username}", h.handleUpdateUser)
	mux.HandleFunc("DELETE /api/v1/_auth/users/{username}", h.handleDeleteUser)
	mux.HandleFunc("GET /api/v1/_auth/permissions", h.handleGetPermissions)
	mux.HandleFunc("PUT /api/v1/_auth/permissions", h.handleUpdatePermission)
	mux.HandleFunc("PUT /api/v1/_auth/tables/{name}", h.handleUpdateTableVisibility)

	// Real-Time SSE Streams
	mux.HandleFunc("GET /api/v1/stream", h.handleStreamAll)
	mux.HandleFunc("GET /api/v1/stream/{table}", h.handleStreamTable)

	// Custom Endpoints & RPC Functions
	mux.HandleFunc("GET /api/v1/functions", h.handleListFunctions)
	mux.HandleFunc("GET /api/v1/functions/{name}", h.handleInvokeFunction)
	mux.HandleFunc("POST /api/v1/functions/{name}", h.handleInvokeFunction)
	mux.HandleFunc("GET /api/v1/rpc/{name}", h.handleInvokeFunction)
	mux.HandleFunc("POST /api/v1/rpc/{name}", h.handleInvokeFunction)
	mux.HandleFunc("PUT /api/v1/_auth/functions/{name}", h.handleUpdateFunctionPermission)

	// Wrap with middleware
	var root http.Handler = mux
	root = corsMiddleware(root)
	root = loggingMiddleware(root)

	if cfg.JWTSecret != "" {
		root = jwtMiddleware(root, cfg.JWTSecret)
	}

	return root
}

type handler struct {
	graph       *catalog.SchemaGraph
	compiler    *query.Compiler
	pool        *pgxpool.Pool
	cache       cache.Cache
	invalidator *cache.Invalidator
	plugins     *wasm.Manager
	cfg         *config.Config
	stream      *StreamBroker
}

// --- SELECT ---
func (h *handler) handleSelect(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	if _, ok := h.graph.GetTable(table); !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "read"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	// Build cache key from full URL
	cacheKey := "dynago:select:" + r.URL.RequestURI()
	if cached, ok := h.cache.Get(r.Context(), cacheKey); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Dynago-Cache", "HIT")
		w.Write(cached)
		return
	}

	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		params[k] = v[0]
	}

	q, err := h.compiler.CompileSelect(table, params)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	rows, err := h.pool.Query(r.Context(), q.SQL, q.Args...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "query error: "+err.Error())
		return
	}
	defer rows.Close()

	var results []json.RawMessage
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			jsonError(w, http.StatusInternalServerError, "scan error: "+err.Error())
			return
		}
		results = append(results, payload)
	}
	if rows.Err() != nil {
		jsonError(w, http.StatusInternalServerError, "rows error: "+rows.Err().Error())
		return
	}

	// Wasm post-hook
	var body []byte
	if results == nil {
		body = []byte("[]")
	} else if len(results) == 1 && !strings.Contains(r.URL.RawQuery, "limit") {
		// Single result and no explicit limit — check if filtering by PK
		body = results[0]
	} else {
		body, _ = json.Marshal(results)
	}

	if h.plugins != nil && h.plugins.HasHook(table, "posthook") {
		body, err = h.plugins.Execute(r.Context(), table, "posthook", body)
		if err != nil {
			log.Printf("[wasm] posthook error on %s: %v", table, err)
		}
	}

	// Cache the response
	h.cache.Set(r.Context(), cacheKey, body)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Dynago-Cache", "MISS")
	w.Write(body)
}

// --- INSERT ---
func (h *handler) handleInsert(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	if _, ok := h.graph.GetTable(table); !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "insert"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1MB limit
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	// Wasm pre-hook
	if h.plugins != nil && h.plugins.HasHook(table, "prehook") {
		bodyBytes, err = h.plugins.Execute(r.Context(), table, "prehook", bodyBytes)
		if err != nil {
			jsonError(w, http.StatusUnprocessableEntity, "prehook error: "+err.Error())
			return
		}
	}

	// PG Hook
	h.firePGHook(r.Context(), "INSERT", table, bodyBytes)

	var data map[string]any
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	var cols []string
	var vals []any
	for k, v := range data {
		cols = append(cols, k)
		vals = append(vals, v)
	}

	q, err := h.compiler.CompileInsert(table, cols, vals)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	var payload []byte
	if err := h.pool.QueryRow(r.Context(), q.SQL, q.Args...).Scan(&payload); err != nil {
		jsonError(w, http.StatusInternalServerError, "insert error: "+err.Error())
		return
	}

	// Invalidate cache for this table
	if h.invalidator != nil {
		h.invalidator.EvictTable(r.Context(), table)
	} else {
		h.cache.Flush(r.Context())
	}

	// Broadcast real-time SSE mutation event
	h.stream.Publish(MutationEvent{
		Table:     table,
		Action:    "INSERT",
		Record:    payload,
		Timestamp: time.Now().UTC(),
	})

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	w.Write(payload)
}

// --- UPDATE ---
func (h *handler) handleUpdate(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	if _, ok := h.graph.GetTable(table); !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "update"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	// Wasm pre-hook
	if h.plugins != nil && h.plugins.HasHook(table, "prehook") {
		bodyBytes, err = h.plugins.Execute(r.Context(), table, "prehook", bodyBytes)
		if err != nil {
			jsonError(w, http.StatusUnprocessableEntity, "prehook error: "+err.Error())
			return
		}
	}

	h.firePGHook(r.Context(), "UPDATE", table, bodyBytes)

	var data map[string]any
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}

	// WHERE clauses from query params
	var whereCols []string
	var whereVals []any
	for k, v := range r.URL.Query() {
		col, _, filterVal := parseSimpleFilter(k, v[0])
		if col != "" {
			whereCols = append(whereCols, col)
			whereVals = append(whereVals, filterVal)
		}
	}

	if len(whereCols) == 0 {
		jsonError(w, http.StatusBadRequest, "UPDATE requires at least one filter (e.g. ?id=eq.1)")
		return
	}

	var setCols []string
	var setVals []any
	for k, v := range data {
		setCols = append(setCols, k)
		setVals = append(setVals, v)
	}

	q, err := h.compiler.CompileUpdate(table, setCols, setVals, whereCols, whereVals)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	rows, err := h.pool.Query(r.Context(), q.SQL, q.Args...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "update error: "+err.Error())
		return
	}
	defer rows.Close()

	var results []json.RawMessage
	for rows.Next() {
		var payload []byte
		rows.Scan(&payload)
		results = append(results, payload)
	}

	if h.invalidator != nil {
		h.invalidator.EvictTable(r.Context(), table)
	} else {
		h.cache.Flush(r.Context())
	}

	// Broadcast real-time SSE mutation event
	var updateRecord json.RawMessage
	if len(results) > 0 {
		updateRecord, _ = json.Marshal(results)
	} else {
		updateRecord = bodyBytes
	}
	h.stream.Publish(MutationEvent{
		Table:     table,
		Action:    "UPDATE",
		Record:    updateRecord,
		Timestamp: time.Now().UTC(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// --- DELETE ---
func (h *handler) handleDelete(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	if _, ok := h.graph.GetTable(table); !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "delete"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	h.firePGHook(r.Context(), "DELETE", table, nil)

	var whereCols []string
	var whereVals []any
	for k, v := range r.URL.Query() {
		col, _, filterVal := parseSimpleFilter(k, v[0])
		if col != "" {
			whereCols = append(whereCols, col)
			whereVals = append(whereVals, filterVal)
		}
	}

	if len(whereCols) == 0 {
		jsonError(w, http.StatusBadRequest, "DELETE requires at least one filter (e.g. ?id=eq.1)")
		return
	}

	q, err := h.compiler.CompileDelete(table, whereCols, whereVals)
	if err != nil {
		jsonError(w, http.StatusBadRequest, err.Error())
		return
	}

	rows, err := h.pool.Query(r.Context(), q.SQL, q.Args...)
	if err != nil {
		jsonError(w, http.StatusInternalServerError, "delete error: "+err.Error())
		return
	}
	defer rows.Close()

	var results []json.RawMessage
	for rows.Next() {
		var payload []byte
		rows.Scan(&payload)
		results = append(results, payload)
	}

	if h.invalidator != nil {
		h.invalidator.EvictTable(r.Context(), table)
	} else {
		h.cache.Flush(r.Context())
	}

	// Broadcast real-time SSE mutation event
	h.stream.Publish(MutationEvent{
		Table:     table,
		Action:    "DELETE",
		Timestamp: time.Now().UTC(),
	})

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(results)
}

// --- REFRESH ---
func (h *handler) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if err := h.graph.Refresh(r.Context(), h.pool); err != nil {
		jsonError(w, http.StatusInternalServerError, "refresh failed: "+err.Error())
		return
	}

	// Rebuild compiler with new graph
	h.compiler = query.NewCompiler(h.graph)
	if h.invalidator != nil {
		h.invalidator.EvictAll(r.Context())
	} else {
		h.cache.Flush(r.Context())
	}

	log.Printf("[dynago] schema refreshed — %d tables", len(h.graph.Tables))
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "refreshed",
		"tables": len(h.graph.Tables),
	})
}

// --- ENDPOINTS ---
func (h *handler) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	type endpoint struct {
		Method string `json:"method"`
		Path   string `json:"path"`
		Table  string `json:"table,omitempty"`
	}

	endpoints := []endpoint{
		{Method: "GET", Path: "/healthz"},
		{Method: "POST", Path: "/api/v1/_refresh"},
		{Method: "GET", Path: "/api/v1/_endpoints"},
		{Method: "GET", Path: "/api/v1/_schema"},
		{Method: "GET", Path: "/api/v1/stream"},
	}

	if h.cfg.EnableUI {
		endpoints = append(endpoints,
			endpoint{Method: "GET", Path: "/"},
			endpoint{Method: "GET", Path: "/ui"},
			endpoint{Method: "GET", Path: "/api/v1/_ui/meta"},
		)
		if h.cfg.EnableSQL {
			endpoints = append(endpoints, endpoint{Method: "POST", Path: "/api/v1/_ui/sql"})
		}
		if h.cfg.EnableDataView {
			endpoints = append(endpoints, endpoint{Method: "GET", Path: "/api/v1/_ui/data/{table}"})
		}
	}

	if h.cfg.JWTSecret != "" {
		endpoints = append(endpoints,
			endpoint{Method: "POST", Path: "/api/v1/_auth/login"},
			endpoint{Method: "POST", Path: "/api/v1/_auth/register"},
			endpoint{Method: "GET", Path: "/api/v1/_auth/me"},
			endpoint{Method: "GET", Path: "/api/v1/_auth/users"},
			endpoint{Method: "POST", Path: "/api/v1/_auth/users"},
			endpoint{Method: "PATCH", Path: "/api/v1/_auth/users/{username}"},
			endpoint{Method: "DELETE", Path: "/api/v1/_auth/users/{username}"},
			endpoint{Method: "GET", Path: "/api/v1/_auth/permissions"},
			endpoint{Method: "PUT", Path: "/api/v1/_auth/permissions"},
			endpoint{Method: "PUT", Path: "/api/v1/_auth/tables/{name}"},
		)
	}

	isAdmin := true
	if h.cfg.JWTSecret != "" {
		user, ok := UserFromContext(r.Context())
		isAdmin = ok && user.Role == "admin"
	}

	if isAdmin {
		endpoints = append(endpoints,
			endpoint{Method: "GET", Path: "/api/v1/functions"},
			endpoint{Method: "POST", Path: "/api/v1/functions/{name}"},
			endpoint{Method: "GET", Path: "/api/v1/functions/{name}"},
			endpoint{Method: "POST", Path: "/api/v1/rpc/{name}"},
			endpoint{Method: "GET", Path: "/api/v1/rpc/{name}"},
			endpoint{Method: "PUT", Path: "/api/v1/_auth/functions/{name}"},
		)
	} else {
		endpoints = append(endpoints,
			endpoint{Method: "GET", Path: "/api/v1/functions"},
		)
	}

	tables := h.filterAccessibleTables(r.Context())
	for _, t := range tables {
		base := "/api/v1/" + t
		endpoints = append(endpoints,
			endpoint{Method: "GET", Path: base, Table: t},
			endpoint{Method: "POST", Path: base, Table: t},
			endpoint{Method: "PATCH", Path: base, Table: t},
			endpoint{Method: "DELETE", Path: base, Table: t},
			endpoint{Method: "GET", Path: "/api/v1/stream/" + t, Table: t},
		)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total":     len(endpoints),
		"endpoints": endpoints,
	})
}

// --- SCHEMA ---
func (h *handler) handleSchema(w http.ResponseWriter, r *http.Request) {
	tables := h.filterAccessibleTables(r.Context())

	type tableInfo struct {
		Name       string   `json:"name"`
		Schema     string   `json:"schema"`
		Columns    int      `json:"columns"`
		PrimaryKey []string `json:"primary_key"`
		Relations  int      `json:"relations"`
	}

	var info []tableInfo
	for _, name := range tables {
		tbl, _ := h.graph.GetTable(name)
		info = append(info, tableInfo{
			Name:       name,
			Schema:     tbl.Schema,
			Columns:    len(tbl.Columns),
			PrimaryKey: tbl.PrimaryKeys,
			Relations:  len(tbl.Relations),
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(info)
}

func (h *handler) handleSchemaTable(w http.ResponseWriter, r *http.Request) {
	table := r.PathValue("table")
	tbl, ok := h.graph.GetTable(table)
	if !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "read"); err != nil {
		if strings.Contains(err.Error(), "authentication required") {
			jsonError(w, http.StatusUnauthorized, err.Error())
			return
		}
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	type colInfo struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Nullable   bool   `json:"nullable"`
		Default    string `json:"default,omitempty"`
		PrimaryKey bool   `json:"primary_key"`
	}

	type relInfo struct {
		Column       string `json:"column"`
		ForeignTable string `json:"foreign_table"`
		ForeignCol   string `json:"foreign_column"`
	}

	var cols []colInfo
	for _, name := range tbl.ColumnOrder {
		c := tbl.Columns[name]
		cols = append(cols, colInfo{
			Name:       c.Name,
			Type:       c.DataType,
			Nullable:   c.IsNullable,
			Default:    c.DefaultValue,
			PrimaryKey: c.IsPrimaryKey,
		})
	}

	var rels []relInfo
	for _, r := range tbl.Relations {
		rels = append(rels, relInfo{
			Column:       r.ColumnName,
			ForeignTable: r.ForeignTable,
			ForeignCol:   r.ForeignColumn,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"name":      table,
		"schema":    tbl.Schema,
		"columns":   cols,
		"relations": rels,
	})
}

// --- PG Hook ---
func (h *handler) firePGHook(ctx context.Context, op, table string, body []byte) {
	if h.cfg.PGHookURL == "" {
		return
	}

	go func() {
		payload, _ := json.Marshal(map[string]any{
			"operation": op,
			"table":     table,
			"body":      json.RawMessage(body),
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, "POST", h.cfg.PGHookURL, strings.NewReader(string(payload)))
		if err != nil {
			log.Printf("[pghook] request build error: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			log.Printf("[pghook] call failed: %v", err)
			return
		}
		resp.Body.Close()
		log.Printf("[pghook] %s %s → %d", op, table, resp.StatusCode)
	}()
}

// --- helpers ---

func jsonError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func parseSimpleFilter(key, val string) (col, op, filterVal string) {
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	return key, parts[0], parts[1]
}
