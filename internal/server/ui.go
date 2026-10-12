package server

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dynago/dynago/internal/catalog"
)

//go:embed ui.html
var uiHTML string

// handleUI serves the main web dashboard if DYNAGO_ENABLE_UI is true.
func (h *handler) handleUI(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EnableUI {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `<!DOCTYPE html>
<html>
<head><title>Dynago UI Disabled</title><meta charset="utf-8"><style>body{font-family:system-ui,-apple-system,sans-serif;background:#0b0f19;color:#94a3b8;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;} .box{background:#111827;border:1px solid #1f2937;padding:32px;border-radius:12px;max-width:500px;text-align:center;} h1{color:#f87171;font-size:20px;margin-bottom:12px;} code{background:#1f2937;color:#38bdf8;padding:2px 6px;border-radius:4px;}</style></head>
<body>
<div class="box">
  <h1>Dynago UI is Disabled</h1>
  <p>To enable this interactive web dashboard, add the following to your <code>.env</code> file and restart the server:</p>
  <pre style="background:#0b0f19;padding:12px;border-radius:8px;color:#4ade80;text-align:left;">DYNAGO_ENABLE_UI=true</pre>
</div>
</body>
</html>`)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(uiHTML))
}

// handleUIMeta returns metadata about endpoints, tables, example JSON, and active config flags.
func (h *handler) handleUIMeta(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EnableUI {
		jsonError(w, http.StatusNotFound, "UI is disabled")
		return
	}

	type colMeta struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		Nullable   bool   `json:"nullable"`
		Default    string `json:"default,omitempty"`
		PrimaryKey bool   `json:"primary_key"`
	}

	type relMeta struct {
		Column       string `json:"column"`
		ForeignTable string `json:"foreign_table"`
		ForeignCol   string `json:"foreign_column"`
	}

	type tableMeta struct {
		Name        string         `json:"name"`
		Schema      string         `json:"schema"`
		Columns     []colMeta      `json:"columns"`
		PrimaryKeys []string       `json:"primary_keys"`
		Relations   []relMeta      `json:"relations"`
		Examples    map[string]any `json:"examples"`
		IsPublic    bool           `json:"is_public"`
	}

	tables := h.filterAccessibleTables(r.Context())

	var tableMetas []tableMeta
	for _, name := range tables {
		tbl, _ := h.graph.GetTable(name)
		var cols []colMeta
		for _, cName := range tbl.ColumnOrder {
			c := tbl.Columns[cName]
			cols = append(cols, colMeta{
				Name:       c.Name,
				Type:       c.DataType,
				Nullable:   c.IsNullable,
				Default:    c.DefaultValue,
				PrimaryKey: c.IsPrimaryKey,
			})
		}

		var rels []relMeta
		for _, r := range tbl.Relations {
			rels = append(rels, relMeta{
				Column:       r.ColumnName,
				ForeignTable: r.ForeignTable,
				ForeignCol:   r.ForeignColumn,
			})
		}

		tableMetas = append(tableMetas, tableMeta{
			Name:        name,
			Schema:      tbl.Schema,
			Columns:     cols,
			PrimaryKeys: tbl.PrimaryKeys,
			Relations:   rels,
			Examples:    generateExampleJSON(tbl),
			IsPublic:    h.isTablePublic(r.Context(), name),
		})
	}

	isAdmin := true
	if h.cfg.JWTSecret != "" {
		user, ok := UserFromContext(r.Context())
		isAdmin = ok && user.Role == "admin"
	}

	var wasmFuncs []string
	if h.plugins != nil {
		for _, fn := range h.plugins.FunctionNames() {
			if isAdmin || h.isFunctionPublic(r.Context(), fn) {
				wasmFuncs = append(wasmFuncs, fn)
			}
		}
	}
	var dbFuncs []string
	if h.graph != nil {
		for _, fn := range h.graph.FunctionNames() {
			if isAdmin || h.isFunctionPublic(r.Context(), fn) {
				dbFuncs = append(dbFuncs, fn)
			}
		}
	}

	resp := map[string]any{
		"flags": map[string]any{
			"enable_ui":        h.cfg.EnableUI,
			"enable_sql":       h.cfg.EnableSQL,
			"enable_data_view": h.cfg.EnableDataView,
			"data_limit":       h.cfg.DataLimit,
			"auth_enabled":     h.cfg.JWTSecret != "",
		},
		"server": map[string]any{
			"port":         h.cfg.Port,
			"cache_ttl":    h.cfg.CacheTTL.String(),
			"redis":        h.cfg.RedisURL != "",
			"tables_count": len(tables),
			"auth_enabled": h.cfg.JWTSecret != "",
		},
		"tables": tableMetas,
		"functions": map[string]any{
			"wasm":     wasmFuncs,
			"database": dbFuncs,
		},
	}

	if user, ok := UserFromContext(r.Context()); ok {
		resp["user"] = map[string]any{
			"username": user.Username,
			"role":     user.Role,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleUISQL runs raw SQL queries from the UI console if DYNAGO_ENABLE_SQL is true.
func (h *handler) handleUISQL(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EnableUI {
		jsonError(w, http.StatusNotFound, "UI is disabled")
		return
	}
	if !h.cfg.EnableSQL {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "SQL execution is disabled. Set DYNAGO_ENABLE_SQL=true in .env to enable.",
		})
		return
	}
	if h.cfg.JWTSecret != "" {
		user, ok := UserFromContext(r.Context())
		if !ok || user.Role != "admin" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]any{
				"error": "Admin privileges required to execute SQL queries in UI mode.",
			})
			return
		}
	}

	bodyBytes, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	bodyBytes = bytes.Trim(bytes.TrimSpace(bodyBytes), "'")

	var req struct {
		Query string `json:"query"`
	}
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		jsonError(w, http.StatusBadRequest, "invalid request json: "+err.Error())
		return
	}

	trimmed := strings.TrimSpace(req.Query)
	if trimmed == "" {
		jsonError(w, http.StatusBadRequest, "query cannot be empty")
		return
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	rows, err := h.pool.Query(ctx, trimmed)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]any{
			"error":       err.Error(),
			"duration_ms": float64(time.Since(start).Microseconds()) / 1000.0,
		})
		return
	}
	defer rows.Close()

	fds := rows.FieldDescriptions()
	var cols []string
	for _, fd := range fds {
		cols = append(cols, fd.Name)
	}

	var resultRows []map[string]any
	if len(cols) > 0 {
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				continue
			}
			rowMap := make(map[string]any, len(cols))
			for i, c := range cols {
				rowMap[c] = formatSQLValue(vals[i])
			}
			resultRows = append(resultRows, rowMap)
		}
	}

	tag := rows.CommandTag()
	rowCount := int64(len(resultRows))
	if len(cols) == 0 {
		rowCount = tag.RowsAffected()
	}

	// Invalidate cache and broadcast stream event if mutation occurred
	upper := strings.ToUpper(trimmed)
	if strings.Contains(upper, "INSERT") || strings.Contains(upper, "UPDATE") || strings.Contains(upper, "DELETE") || strings.Contains(upper, "DROP") || strings.Contains(upper, "ALTER") {
		if h.invalidator != nil {
			h.invalidator.EvictAll(r.Context())
		} else {
			h.cache.Flush(r.Context())
		}

		action := "MUTATION"
		if strings.Contains(upper, "INSERT") {
			action = "INSERT"
		} else if strings.Contains(upper, "UPDATE") {
			action = "UPDATE"
		} else if strings.Contains(upper, "DELETE") {
			action = "DELETE"
		}

		rawJSON, _ := json.Marshal(map[string]any{
			"sql":           trimmed,
			"rows_affected": rowCount,
		})

		h.stream.Publish(MutationEvent{
			Table:     "sql_console",
			Action:    action,
			Record:    rawJSON,
			Timestamp: time.Now().UTC(),
		})
	}

	durationMs := float64(time.Since(start).Microseconds()) / 1000.0

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"columns":     cols,
		"rows":        resultRows,
		"row_count":   rowCount,
		"command_tag": tag.String(),
		"duration_ms": durationMs,
	})
}

// handleUIData returns paginated table data with dynamic filters if DYNAGO_ENABLE_DATA_VIEW is true.
func (h *handler) handleUIData(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.EnableUI {
		jsonError(w, http.StatusNotFound, "UI is disabled")
		return
	}
	if !h.cfg.EnableDataView {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]any{
			"error": "Data view is disabled. Set DYNAGO_ENABLE_DATA_VIEW=true in .env to enable.",
		})
		return
	}

	table := r.PathValue("table")
	tbl, ok := h.graph.GetTable(table)
	if !ok {
		jsonError(w, http.StatusNotFound, "table not found: "+table)
		return
	}

	if err := h.checkTablePermission(r.Context(), table, "read"); err != nil {
		jsonError(w, http.StatusForbidden, err.Error())
		return
	}

	// Parse query params into map
	params := make(map[string]string)
	for k, v := range r.URL.Query() {
		if len(v) > 0 {
			params[k] = v[0]
		}
	}

	// Handle limit: cannot exceed cfg.DataLimit
	maxLimit := h.cfg.DataLimit
	if maxLimit <= 0 {
		maxLimit = 50
	}
	limit := maxLimit
	if lStr, exists := params["limit"]; exists {
		if parsed, err := strconv.Atoi(lStr); err == nil && parsed > 0 {
			if parsed > maxLimit {
				limit = maxLimit
			} else {
				limit = parsed
			}
		}
	}
	params["limit"] = strconv.Itoa(limit)

	// Handle offset / page
	page := 1
	offset := 0
	if pStr, exists := params["page"]; exists {
		if parsed, err := strconv.Atoi(pStr); err == nil && parsed > 1 {
			page = parsed
			offset = (page - 1) * limit
		}
	} else if oStr, exists := params["offset"]; exists {
		if parsed, err := strconv.Atoi(oStr); err == nil && parsed >= 0 {
			offset = parsed
			page = (offset / limit) + 1
		}
	}
	params["offset"] = strconv.Itoa(offset)

	// Execute count query for pagination
	countQ, err := h.compiler.CompileCount(table, params)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "count compile error: "+err.Error())
		return
	}

	var totalCount int64
	if err := h.pool.QueryRow(r.Context(), countQ.SQL, countQ.Args...).Scan(&totalCount); err != nil {
		jsonError(w, http.StatusInternalServerError, "count query error: "+err.Error())
		return
	}

	// Execute select query
	selectQ, err := h.compiler.CompileSelect(table, params)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "select compile error: "+err.Error())
		return
	}

	rows, err := h.pool.Query(r.Context(), selectQ.SQL, selectQ.Args...)
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

	if results == nil {
		results = []json.RawMessage{}
	}

	totalPages := int(math.Ceil(float64(totalCount) / float64(limit)))
	if totalPages == 0 {
		totalPages = 1
	}

	type colSimple struct {
		Name       string `json:"name"`
		Type       string `json:"type"`
		PrimaryKey bool   `json:"primary_key"`
	}
	var cols []colSimple
	for _, colName := range tbl.ColumnOrder {
		c := tbl.Columns[colName]
		cols = append(cols, colSimple{
			Name:       c.Name,
			Type:       c.DataType,
			PrimaryKey: c.IsPrimaryKey,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"table":          table,
		"columns":        cols,
		"rows":           results,
		"total":          totalCount,
		"page":           page,
		"limit":          limit,
		"offset":         offset,
		"total_pages":    totalPages,
		"data_limit_max": maxLimit,
	})
}

// generateExampleJSON produces mock request and response JSON for API documentation.
func generateExampleJSON(tbl *catalog.TableDefinition) map[string]any {
	getResp := make(map[string]any)
	postReq := make(map[string]any)
	patchReq := make(map[string]any)

	patchCount := 0
	for _, colName := range tbl.ColumnOrder {
		col := tbl.Columns[colName]
		val := generateMockValue(tbl.Name, col)
		getResp[colName] = val

		// For POST: omit primary keys and auto timestamps with default values
		colLower := strings.ToLower(col.Name)
		isAutoTime := (strings.Contains(colLower, "created_at") || strings.Contains(colLower, "updated_at")) && col.DefaultValue != ""

		if !col.IsPrimaryKey && !isAutoTime {
			postReq[colName] = val
		}

		// For PATCH: include 1 or 2 editable fields
		if !col.IsPrimaryKey && !isAutoTime && patchCount < 2 {
			if strings.Contains(colLower, "price") {
				patchReq[colName] = 149.99
			} else if strings.Contains(colLower, "status") {
				patchReq[colName] = "completed"
			} else if strings.Contains(colLower, "stock") {
				patchReq[colName] = 80
			} else if strings.Contains(colLower, "title") || strings.Contains(colLower, "name") {
				patchReq[colName] = "Updated " + strings.ReplaceAll(col.Name, "_", " ")
			} else {
				patchReq[colName] = val
			}
			patchCount++
		}
	}

	if len(patchReq) == 0 {
		for _, colName := range tbl.ColumnOrder {
			col := tbl.Columns[colName]
			if !col.IsPrimaryKey {
				patchReq[colName] = generateMockValue(tbl.Name, col)
				break
			}
		}
	}

	return map[string]any{
		"get_response":  getResp,
		"post_request":  postReq,
		"patch_request": patchReq,
	}
}

func generateMockValue(tblName string, col *catalog.ColumnDefinition) any {
	colLower := strings.ToLower(col.Name)
	typeLower := strings.ToLower(col.DataType)

	if col.IsPrimaryKey || colLower == "id" {
		return 1
	}
	if strings.HasSuffix(colLower, "_id") {
		return 1
	}

	switch {
	case strings.Contains(colLower, "email"):
		return "alex.morgan@example.com"
	case strings.Contains(colLower, "sku"):
		return "PROD-A109"
	case strings.Contains(colLower, "slug"):
		return "premium-mechanical-keyboard"
	case strings.Contains(colLower, "title") || strings.Contains(colLower, "name"):
		return "Ergonomic Mechanical Keyboard"
	case strings.Contains(colLower, "status"):
		return "active"
	case strings.Contains(colLower, "tier"):
		return "gold"
	case strings.Contains(colLower, "phone"):
		return "+1-555-019-2834"
	case strings.Contains(colLower, "price") || strings.Contains(colLower, "amount") || strings.Contains(colLower, "cost") || strings.Contains(colLower, "total"):
		return 129.99
	case strings.Contains(colLower, "stock") || strings.Contains(colLower, "quantity") || strings.Contains(colLower, "count") || strings.Contains(colLower, "points"):
		return 45
	case strings.Contains(colLower, "rating"):
		return 5
	case strings.Contains(colLower, "desc"):
		return "High-performance low-profile mechanical keyboard with hot-swappable switches."
	case strings.Contains(colLower, "meta") || strings.Contains(colLower, "pref"):
		return map[string]any{"color": "space_gray", "bluetooth": true}
	}

	switch {
	case strings.Contains(typeLower, "int") || strings.Contains(typeLower, "serial"):
		return 42
	case strings.Contains(typeLower, "numeric") || strings.Contains(typeLower, "decimal") || strings.Contains(typeLower, "float") || strings.Contains(typeLower, "double") || strings.Contains(typeLower, "real"):
		return 49.99
	case strings.Contains(typeLower, "bool"):
		return true
	case strings.Contains(typeLower, "json"):
		return map[string]any{"key": "value"}
	case strings.Contains(typeLower, "time") || strings.Contains(typeLower, "date"):
		return "2026-10-11T03:30:00Z"
	default:
		return "Sample " + strings.ReplaceAll(col.Name, "_", " ")
	}
}

func formatSQLValue(val any) any {
	if val == nil {
		return nil
	}
	switch v := val.(type) {
	case []byte:
		var j any
		if err := json.Unmarshal(v, &j); err == nil {
			return j
		}
		return string(v)
	case time.Time:
		return v.Format(time.RFC3339)
	default:
		return v
	}
}
