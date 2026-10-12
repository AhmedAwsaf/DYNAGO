package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dynago/dynago/internal/catalog"
)

// FunctionInfo describes an available custom function (Wasm or Database).
type FunctionInfo struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // "wasm" or "database"
	Endpoint    string `json:"endpoint"`
	IsPublic    bool   `json:"is_public"`
	Arguments   string `json:"arguments,omitempty"`
	ReturnType  string `json:"return_type,omitempty"`
	Description string `json:"description,omitempty"`
}

// handleListFunctions lists all available custom Wasm and Database RPC functions.
func (h *handler) handleListFunctions(w http.ResponseWriter, r *http.Request) {
	isAdmin := true
	if h.cfg.JWTSecret != "" {
		user, ok := UserFromContext(r.Context())
		isAdmin = ok && user.Role == "admin"
	}

	var wasmFuncs []FunctionInfo
	if h.plugins != nil {
		for _, name := range h.plugins.FunctionNames() {
			isPub := h.isFunctionPublic(r.Context(), name)
			if !isAdmin && !isPub {
				continue
			}
			wasmFuncs = append(wasmFuncs, FunctionInfo{
				Name:        name,
				Type:        "wasm",
				Endpoint:    "/api/v1/functions/" + name,
				IsPublic:    isPub,
				Description: "WebAssembly in-process microservice function",
			})
		}
	}
	if wasmFuncs == nil {
		wasmFuncs = []FunctionInfo{}
	}

	var dbFuncs []FunctionInfo
	if h.graph != nil {
		for _, name := range h.graph.FunctionNames() {
			isPub := h.isFunctionPublic(r.Context(), name)
			if !isAdmin && !isPub {
				continue
			}
			if fn, ok := h.graph.GetFunction(name); ok {
				dbFuncs = append(dbFuncs, FunctionInfo{
					Name:        fn.Name,
					Type:        "database",
					Endpoint:    "/api/v1/rpc/" + fn.Name,
					IsPublic:    isPub,
					Arguments:   fn.Arguments,
					ReturnType:  fn.ReturnType,
					Description: fn.Description,
				})
			}
		}
	}
	if dbFuncs == nil {
		dbFuncs = []FunctionInfo{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"total":              len(wasmFuncs) + len(dbFuncs),
		"wasm_functions":     wasmFuncs,
		"database_functions": dbFuncs,
	})
}

// handleInvokeFunction executes a custom Wasm virtual route or PostgreSQL stored procedure.
func (h *handler) handleInvokeFunction(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		jsonError(w, http.StatusBadRequest, "function name required")
		return
	}

	// Public vs Private access control
	isPublic := h.isFunctionPublic(r.Context(), name)
	if h.cfg.JWTSecret != "" && !isPublic {
		_, ok := UserFromContext(r.Context())
		if !ok {
			jsonError(w, http.StatusUnauthorized, fmt.Sprintf("authentication required for private function: %s", name))
			return
		}
	}

	// Read input payload
	var bodyBytes []byte
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			jsonError(w, http.StatusBadRequest, "read body: "+err.Error())
			return
		}
		bodyBytes = bytes.Trim(bytes.TrimSpace(raw), "'")
	}

	// If no body provided or GET request, map query parameters to JSON map
	var inputMap map[string]any
	if len(bodyBytes) > 0 {
		_ = json.Unmarshal(bodyBytes, &inputMap)
	} else {
		inputMap = make(map[string]any)
		for k, v := range r.URL.Query() {
			if k != "token" && len(v) > 0 {
				inputMap[k] = v[0]
			}
		}
		bodyBytes, _ = json.Marshal(inputMap)
	}

	// 1. Try Wasm Route / Custom Function
	if h.plugins != nil && h.plugins.HasFunction(name) {
		start := time.Now()
		outBytes, err := h.plugins.ExecuteFunction(r.Context(), name, bodyBytes)
		if err != nil {
			jsonError(w, http.StatusInternalServerError, fmt.Sprintf("wasm function %s error: %v", name, err))
			return
		}

		var parsedResult any
		if err := json.Unmarshal(outBytes, &parsedResult); err != nil {
			parsedResult = string(outBytes)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"source":      "wasm",
			"function":    name,
			"is_public":   isPublic,
			"duration_ms": float64(time.Since(start).Microseconds()) / 1000.0,
			"result":      parsedResult,
		})
		return
	}

	// 2. Try PostgreSQL Stored Function / RPC
	if fn, ok := h.graph.GetFunction(name); ok {
		h.executeDBFunction(w, r, fn, inputMap)
		return
	}


	// 3. Function not found
	var availableWasm []string
	if h.plugins != nil {
		availableWasm = h.plugins.FunctionNames()
	}
	var availableDB []string
	if h.graph != nil {
		availableDB = h.graph.FunctionNames()
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusNotFound)
	json.NewEncoder(w).Encode(map[string]any{
		"error":              "function not found: " + name,
		"available_wasm":     availableWasm,
		"available_database": availableDB,
	})
}

// executeDBFunction invokes a PostgreSQL stored procedure and returns the JSON result.
func (h *handler) executeDBFunction(w http.ResponseWriter, r *http.Request, fn *catalog.FunctionDefinition, inputMap map[string]any) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	argsMeta := parseArguments(fn.Arguments)

	var sqlQuery string
	var args []any

	if len(argsMeta) == 0 {
		// Zero-argument function: SELECT to_json(public.func())
		sqlQuery = fmt.Sprintf("SELECT to_json(public.%s())", quoteIdent(fn.Name))
	} else {
		// Parameterized function
		var placeholders []string
		for i, arg := range argsMeta {
			placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
			val := resolveArgValue(arg, inputMap, len(argsMeta))
			args = append(args, val)
		}
		sqlQuery = fmt.Sprintf("SELECT to_json(public.%s(%s))", quoteIdent(fn.Name), strings.Join(placeholders, ", "))
	}

	var rawResult []byte
	err := h.pool.QueryRow(ctx, sqlQuery, args...).Scan(&rawResult)

	// If scalar to_json failed, try table-valued function: SELECT json_agg(t) FROM public.func(...) t
	if err != nil {
		var tableSQL string
		if len(args) == 0 {
			tableSQL = fmt.Sprintf("SELECT coalesce(json_agg(t), '[]'::json) FROM (SELECT * FROM public.%s()) t", quoteIdent(fn.Name))
		} else {
			var placeholders []string
			for i := range args {
				placeholders = append(placeholders, fmt.Sprintf("$%d", i+1))
			}
			tableSQL = fmt.Sprintf("SELECT coalesce(json_agg(t), '[]'::json) FROM (SELECT * FROM public.%s(%s)) t", quoteIdent(fn.Name), strings.Join(placeholders, ", "))
		}
		err = h.pool.QueryRow(ctx, tableSQL, args...).Scan(&rawResult)
	}

	if err != nil {
		jsonError(w, http.StatusBadRequest, fmt.Sprintf("database function %s error: %v", fn.Name, err))
		return
	}

	var parsed any
	if err := json.Unmarshal(rawResult, &parsed); err != nil {
		parsed = string(rawResult)
	}

	if r.Method != http.MethodGet {
		h.cache.Flush(r.Context())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"source":      "database",
		"function":    fn.Name,
		"is_public":   h.isFunctionPublic(r.Context(), fn.Name),
		"duration_ms": float64(time.Since(start).Microseconds()) / 1000.0,
		"result":      parsed,
	})
}

type funcArg struct {
	Name string
	Type string
}

// parseArguments extracts argument names and types from a Postgres signature string.
// Example: "price numeric, discount_percent numeric DEFAULT 10" -> [{price, numeric}, {discount_percent, numeric}]
func parseArguments(argList string) []funcArg {
	if strings.TrimSpace(argList) == "" {
		return nil
	}
	parts := strings.Split(argList, ",")
	var args []funcArg
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		if idx := strings.Index(strings.ToUpper(trimmed), " DEFAULT"); idx != -1 {
			trimmed = strings.TrimSpace(trimmed[:idx])
		}
		tokens := strings.Fields(trimmed)
		if len(tokens) == 0 {
			continue
		}
		name := tokens[0]
		argType := ""
		if strings.EqualFold(name, "IN") || strings.EqualFold(name, "OUT") || strings.EqualFold(name, "INOUT") {
			if len(tokens) > 1 {
				name = tokens[1]
				if len(tokens) > 2 {
					argType = strings.ToLower(strings.Join(tokens[2:], " "))
				}
			}
		} else if len(tokens) > 1 {
			argType = strings.ToLower(strings.Join(tokens[1:], " "))
		}
		args = append(args, funcArg{Name: name, Type: argType})
	}
	return args
}

// resolveArgValue matches caller input payload to function arguments with alias and type coercion.
func resolveArgValue(arg funcArg, inputMap map[string]any, totalArgs int) any {
	// 1. Direct match
	if v, ok := inputMap[arg.Name]; ok && v != nil {
		return coerceValue(v, arg.Type)
	}
	// 2. Case-insensitive match
	for k, v := range inputMap {
		if strings.EqualFold(k, arg.Name) && v != nil {
			return coerceValue(v, arg.Type)
		}
	}
	// 3. Normalized / common alias matching
	argLower := strings.ToLower(arg.Name)
	for k, v := range inputMap {
		if v == nil {
			continue
		}
		kLower := strings.ToLower(k)
		if (strings.Contains(argLower, "order") && strings.Contains(argLower, "id")) &&
			(kLower == "order_id" || kLower == "orderid" || kLower == "id" || kLower == "target_order_id" || kLower == "target_id") {
			return coerceValue(v, arg.Type)
		}
		if strings.Contains(argLower, "status") &&
			(kLower == "status" || kLower == "new_status" || kLower == "order_status") {
			return coerceValue(v, arg.Type)
		}
	}
	// 4. Single-argument function fallback
	if totalArgs == 1 && len(inputMap) == 1 {
		for _, v := range inputMap {
			if v != nil {
				return coerceValue(v, arg.Type)
			}
		}
	}
	// 5. If missing, pass nil so Postgres handles NULL
	return nil
}

// coerceValue casts raw JSON or query string input into types expected by pgx.
func coerceValue(val any, argType string) any {
	if val == nil {
		return nil
	}
	typeLower := strings.ToLower(argType)

	switch v := val.(type) {
	case string:
		vTrim := strings.TrimSpace(v)
		if strings.Contains(typeLower, "int") || strings.Contains(typeLower, "serial") {
			if i, err := strconv.ParseInt(vTrim, 10, 64); err == nil {
				return i
			}
			return nil
		}
		if strings.Contains(typeLower, "numeric") || strings.Contains(typeLower, "decimal") || strings.Contains(typeLower, "float") || strings.Contains(typeLower, "real") {
			if f, err := strconv.ParseFloat(vTrim, 64); err == nil {
				return f
			}
		}
		if strings.Contains(typeLower, "bool") {
			if b, err := strconv.ParseBool(vTrim); err == nil {
				return b
			}
		}
		return vTrim

	case float64:
		if strings.Contains(typeLower, "int") || strings.Contains(typeLower, "serial") {
			return int64(v)
		}
		if strings.Contains(typeLower, "char") || strings.Contains(typeLower, "text") || strings.Contains(typeLower, "varchar") {
			return fmt.Sprintf("%v", v)
		}
		return v

	case int:
		return int64(v)

	case int64:
		return v

	default:
		return val
	}
}

// quoteIdent safely quotes a PostgreSQL identifier.
func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}
