package cdc

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// RowMutation represents a decoded database mutation event from the PostgreSQL WAL.
type RowMutation struct {
	Schema    string         `json:"schema"`
	Table     string         `json:"table"`
	Action    string         `json:"action"` // "INSERT", "UPDATE", "DELETE"
	Record    map[string]any `json:"record,omitempty"`
	LSN       string         `json:"lsn,omitempty"`
	Timestamp time.Time      `json:"timestamp"`
}

// DecodeWALMessage parses a raw logical decoding message produced by PostgreSQL's test_decoding plugin.
//
// Example messages:
//   table public.users: INSERT: id[integer]:1 name[text]:'Alice' is_active[boolean]:true
//   table public.orders: UPDATE: id[integer]:42 status[text]:'shipped' total_amount[numeric]:199.99
//   table public.customers: DELETE: id[integer]:5
//   BEGIN 1205
//   COMMIT 1205
func DecodeWALMessage(data, lsn string) (*RowMutation, error) {
	text := strings.TrimSpace(data)
	if text == "" || strings.HasPrefix(text, "BEGIN") || strings.HasPrefix(text, "COMMIT") {
		return nil, nil
	}

	// Format: "table <schema>.<table>: <ACTION>: <col_data>"
	if !strings.HasPrefix(text, "table ") {
		return nil, nil
	}

	rest := text[len("table "):]
	colonIdx := strings.Index(rest, ": ")
	if colonIdx == -1 {
		return nil, fmt.Errorf("malformed WAL message: missing table separator")
	}

	fullTableName := rest[:colonIdx]
	actionAndData := rest[colonIdx+2:]

	// Extract schema and table name
	schema := "public"
	tableName := fullTableName
	if dotIdx := strings.Index(fullTableName, "."); dotIdx != -1 {
		schema = fullTableName[:dotIdx]
		tableName = fullTableName[dotIdx+1:]
	}

	// Parse Action ("INSERT:", "UPDATE:", "DELETE:")
	var action string
	var colsData string
	actionColonIdx := strings.Index(actionAndData, ":")
	if actionColonIdx == -1 {
		action = strings.TrimSpace(actionAndData)
	} else {
		action = strings.TrimSpace(actionAndData[:actionColonIdx])
		colsData = strings.TrimSpace(actionAndData[actionColonIdx+1:])
	}

	record := parseColumnTokens(colsData)

	return &RowMutation{
		Schema:    schema,
		Table:     tableName,
		Action:    action,
		Record:    record,
		LSN:       lsn,
		Timestamp: time.Now().UTC(),
	}, nil
}

// parseColumnTokens parses column key-value pairs formatted as "col[type]:val ...".
// It safely handles quoted strings with spaces, JSON literals, and numeric/boolean types.
func parseColumnTokens(s string) map[string]any {
	result := make(map[string]any)
	if s == "" || s == "(no-tuple-data)" {
		return result
	}

	i := 0
	n := len(s)

	for i < n {
		// Skip whitespace
		for i < n && s[i] == ' ' {
			i++
		}
		if i >= n {
			break
		}

		// Read column name up to '['
		bracketOpen := strings.IndexByte(s[i:], '[')
		if bracketOpen == -1 {
			break
		}
		colName := strings.TrimSpace(s[i : i+bracketOpen])
		i += bracketOpen + 1

		// Read column type up to ']'
		bracketClose := strings.IndexByte(s[i:], ']')
		if bracketClose == -1 {
			break
		}
		colType := s[i : i+bracketClose]
		i += bracketClose + 1

		// Expect ':'
		if i < n && s[i] == ':' {
			i++
		}

		// Read value: either quoted '...' or unquoted token up to space
		var valStr string
		if i < n && s[i] == '\'' {
			// Quoted string: read until unescaped closing quote
			i++ // skip opening quote
			var sb strings.Builder
			for i < n {
				if s[i] == '\'' {
					if i+1 < n && s[i+1] == '\'' {
						// Escaped quote ''
						sb.WriteByte('\'')
						i += 2
						continue
					}
					i++ // consume closing quote
					break
				}
				sb.WriteByte(s[i])
				i++
			}
			valStr = sb.String()
			result[colName] = parseTypedValue(colType, valStr, true)
		} else {
			// Unquoted token (numbers, booleans, null)
			start := i
			for i < n && s[i] != ' ' {
				i++
			}
			valStr = s[start:i]
			result[colName] = parseTypedValue(colType, valStr, false)
		}
	}

	return result
}

// parseTypedValue converts parsed string representations to appropriate native Go types.
func parseTypedValue(colType, raw string, wasQuoted bool) any {
	if !wasQuoted && (raw == "null" || raw == "" || strings.EqualFold(raw, "null")) {
		return nil
	}

	lowerType := strings.ToLower(colType)

	if wasQuoted {
		// Check if it's valid JSON
		if strings.Contains(lowerType, "json") {
			var jsVal any
			if err := json.Unmarshal([]byte(raw), &jsVal); err == nil {
				return jsVal
			}
		}
		return raw
	}

	if strings.Contains(lowerType, "bool") {
		return raw == "true" || raw == "t" || raw == "1"
	}

	if strings.Contains(lowerType, "int") || strings.Contains(lowerType, "serial") {
		if intVal, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return intVal
		}
	}

	if strings.Contains(lowerType, "numeric") || strings.Contains(lowerType, "float") || strings.Contains(lowerType, "double") || strings.Contains(lowerType, "real") {
		if floatVal, err := strconv.ParseFloat(raw, 64); err == nil {
			return floatVal
		}
	}

	return raw
}
