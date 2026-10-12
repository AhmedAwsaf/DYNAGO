package query

import (
	"fmt"
	"strings"

	"github.com/dynago/dynago/internal/catalog"
)

// Compiler translates HTTP query parameters into parameterized SQL.
type Compiler struct {
	graph *catalog.SchemaGraph
}

// NewCompiler creates a query compiler bound to a schema graph.
func NewCompiler(graph *catalog.SchemaGraph) *Compiler {
	return &Compiler{graph: graph}
}

// SelectQuery holds a compiled SELECT statement.
type SelectQuery struct {
	SQL    string
	Args   []any
	Table  string
}

// CompileSelect builds a JSON-returning SELECT from parsed request params.
//
// Supported query params:
//   - select: comma-separated columns, e.g. "id,name" (default: all columns)
//   - <column>=eq.<value>: equality filter
//   - <column>=lt.<value>, gt.<value>, lte.<value>, gte.<value>, neq.<value>
//   - order: column.asc or column.desc
//   - limit: integer
//   - offset: integer
//   - Nested relations: select=id,name,orders(id,total) — generates subquery
func (c *Compiler) CompileSelect(table string, params map[string]string) (*SelectQuery, error) {
	tbl, ok := c.graph.GetTable(table)
	if !ok {
		return nil, fmt.Errorf("table %q not found", table)
	}

	selectCols := tbl.ColumnOrder
	if sel, ok := params["select"]; ok && sel != "" {
		selectCols, _ = parseSelectWithRelations(sel)
	}

	// Build json_build_object fields
	var jsonFields []string
	var subqueries []string
	argIdx := 1
	var args []any

	parsedCols, nestedRels := parseSelectWithRelations(params["select"])
	if params["select"] == "" {
		parsedCols = tbl.ColumnOrder
	}

	for _, col := range parsedCols {
		if _, exists := tbl.Columns[col]; exists {
			jsonFields = append(jsonFields, fmt.Sprintf("'%s', %s.%s", col, quoteIdent(table), quoteIdent(col)))
		}
	}

	// Handle nested relations: orders(id,total)
	for relName, relCols := range nestedRels {
		rel := findRelation(tbl, relName, c.graph)
		if rel == nil {
			continue
		}

		var subFields []string
		for _, rc := range relCols {
			subFields = append(subFields, fmt.Sprintf("'%s', %s.%s", rc, quoteIdent(relName), quoteIdent(rc)))
		}

		sub := fmt.Sprintf(`'%s', COALESCE((SELECT json_agg(json_build_object(%s)) FROM %s WHERE %s.%s = %s.%s), '[]'::json)`,
			relName,
			strings.Join(subFields, ", "),
			quoteIdent(relName),
			quoteIdent(relName), quoteIdent(rel.RelTableColumn),
			quoteIdent(table), quoteIdent(rel.LocalTableColumn),
		)
		subqueries = append(subqueries, sub)
	}

	allFields := append(jsonFields, subqueries...)
	if len(allFields) == 0 {
		// fallback: select all columns
		for _, col := range tbl.ColumnOrder {
			allFields = append(allFields, fmt.Sprintf("'%s', %s.%s", col, quoteIdent(table), quoteIdent(col)))
		}
	}

	// WHERE clauses from filters
	var whereClauses []string
	for key, val := range params {
		if key == "select" || key == "order" || key == "limit" || key == "offset" {
			continue
		}
		col, op, filterVal := parseFilter(key, val)
		if col == "" {
			continue
		}
		if _, exists := tbl.Columns[col]; !exists {
			continue
		}
		whereClauses = append(whereClauses, fmt.Sprintf("%s.%s %s $%d", quoteIdent(table), quoteIdent(col), op, argIdx))
		args = append(args, filterVal)
		argIdx++
	}

	// Build final SQL
	sql := fmt.Sprintf("SELECT json_build_object(%s) AS payload FROM %s",
		strings.Join(allFields, ", "),
		quoteIdent(table),
	)

	if len(whereClauses) > 0 {
		sql += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	if order, ok := params["order"]; ok {
		parts := strings.SplitN(order, ".", 2)
		col := parts[0]
		dir := "ASC"
		if len(parts) == 2 && strings.EqualFold(parts[1], "desc") {
			dir = "DESC"
		}
		if _, exists := tbl.Columns[col]; exists {
			sql += fmt.Sprintf(" ORDER BY %s.%s %s", quoteIdent(table), quoteIdent(col), dir)
		}
	}

	if lim, ok := params["limit"]; ok {
		sql += fmt.Sprintf(" LIMIT %s", sanitizeInt(lim))
	}
	if off, ok := params["offset"]; ok {
		sql += fmt.Sprintf(" OFFSET %s", sanitizeInt(off))
	}

	_ = selectCols // used above in parsedCols

	return &SelectQuery{SQL: sql, Args: args, Table: table}, nil
}

// CountQuery holds a compiled SELECT count(*) statement.
type CountQuery struct {
	SQL   string
	Args  []any
	Table string
}

// CompileCount builds a SELECT count(*) statement with the same WHERE filters.
func (c *Compiler) CompileCount(table string, params map[string]string) (*CountQuery, error) {
	tbl, ok := c.graph.GetTable(table)
	if !ok {
		return nil, fmt.Errorf("table %q not found", table)
	}

	var whereClauses []string
	var args []any
	argIdx := 1

	for key, val := range params {
		if key == "select" || key == "order" || key == "limit" || key == "offset" || key == "page" {
			continue
		}
		col, op, filterVal := parseFilter(key, val)
		if col == "" {
			continue
		}
		if _, exists := tbl.Columns[col]; !exists {
			continue
		}
		whereClauses = append(whereClauses, fmt.Sprintf("%s.%s %s $%d", quoteIdent(table), quoteIdent(col), op, argIdx))
		args = append(args, filterVal)
		argIdx++
	}

	sql := fmt.Sprintf("SELECT count(*) FROM %s", quoteIdent(table))
	if len(whereClauses) > 0 {
		sql += " WHERE " + strings.Join(whereClauses, " AND ")
	}

	return &CountQuery{SQL: sql, Args: args, Table: table}, nil
}

// CompileInsert builds an INSERT ... RETURNING json_build_object(...) statement.
func (c *Compiler) CompileInsert(table string, columns []string, values []any) (*SelectQuery, error) {
	tbl, ok := c.graph.GetTable(table)
	if !ok {
		return nil, fmt.Errorf("table %q not found", table)
	}

	var validCols []string
	var placeholders []string
	var validVals []any
	idx := 1
	for i, col := range columns {
		if _, exists := tbl.Columns[col]; exists {
			validCols = append(validCols, quoteIdent(col))
			placeholders = append(placeholders, fmt.Sprintf("$%d", idx))
			validVals = append(validVals, values[i])
			idx++
		}
	}

	if len(validCols) == 0 {
		return nil, fmt.Errorf("no valid columns for insert")
	}

	// RETURNING clause
	var retFields []string
	for _, col := range tbl.ColumnOrder {
		retFields = append(retFields, fmt.Sprintf("'%s', %s.%s", col, quoteIdent(table), quoteIdent(col)))
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) RETURNING json_build_object(%s) AS payload",
		quoteIdent(table),
		strings.Join(validCols, ", "),
		strings.Join(placeholders, ", "),
		strings.Join(retFields, ", "),
	)

	return &SelectQuery{SQL: sql, Args: validVals, Table: table}, nil
}

// CompileUpdate builds an UPDATE ... RETURNING json_build_object(...) statement.
func (c *Compiler) CompileUpdate(table string, setCols []string, setVals []any, whereCols []string, whereVals []any) (*SelectQuery, error) {
	tbl, ok := c.graph.GetTable(table)
	if !ok {
		return nil, fmt.Errorf("table %q not found", table)
	}

	idx := 1
	var setClauses []string
	var allArgs []any
	for i, col := range setCols {
		if _, exists := tbl.Columns[col]; exists {
			setClauses = append(setClauses, fmt.Sprintf("%s = $%d", quoteIdent(col), idx))
			allArgs = append(allArgs, setVals[i])
			idx++
		}
	}

	var wClauses []string
	for i, col := range whereCols {
		if _, exists := tbl.Columns[col]; exists {
			wClauses = append(wClauses, fmt.Sprintf("%s.%s = $%d", quoteIdent(table), quoteIdent(col), idx))
			allArgs = append(allArgs, whereVals[i])
			idx++
		}
	}

	var retFields []string
	for _, col := range tbl.ColumnOrder {
		retFields = append(retFields, fmt.Sprintf("'%s', %s.%s", col, quoteIdent(table), quoteIdent(col)))
	}

	sql := fmt.Sprintf("UPDATE %s SET %s", quoteIdent(table), strings.Join(setClauses, ", "))
	if len(wClauses) > 0 {
		sql += " WHERE " + strings.Join(wClauses, " AND ")
	}
	sql += fmt.Sprintf(" RETURNING json_build_object(%s) AS payload", strings.Join(retFields, ", "))

	return &SelectQuery{SQL: sql, Args: allArgs, Table: table}, nil
}

// CompileDelete builds a DELETE ... RETURNING statement.
func (c *Compiler) CompileDelete(table string, whereCols []string, whereVals []any) (*SelectQuery, error) {
	tbl, ok := c.graph.GetTable(table)
	if !ok {
		return nil, fmt.Errorf("table %q not found", table)
	}

	idx := 1
	var wClauses []string
	var args []any
	for i, col := range whereCols {
		if _, exists := tbl.Columns[col]; exists {
			wClauses = append(wClauses, fmt.Sprintf("%s.%s = $%d", quoteIdent(table), quoteIdent(col), idx))
			args = append(args, whereVals[i])
			idx++
		}
	}

	var retFields []string
	for _, col := range tbl.ColumnOrder {
		retFields = append(retFields, fmt.Sprintf("'%s', %s.%s", col, quoteIdent(table), quoteIdent(col)))
	}

	sql := fmt.Sprintf("DELETE FROM %s", quoteIdent(table))
	if len(wClauses) > 0 {
		sql += " WHERE " + strings.Join(wClauses, " AND ")
	}
	sql += fmt.Sprintf(" RETURNING json_build_object(%s) AS payload", strings.Join(retFields, ", "))

	return &SelectQuery{SQL: sql, Args: args, Table: table}, nil
}

// --- helpers ---

type resolvedRelation struct {
	RelTableColumn   string
	LocalTableColumn string
}

func findRelation(tbl *catalog.TableDefinition, relName string, graph *catalog.SchemaGraph) *resolvedRelation {
	// Check if current table has FK pointing to relName
	// tbl (local table) has column r.ColumnName pointing to relName's r.ForeignColumn
	for _, r := range tbl.Relations {
		if r.ForeignTable == relName {
			return &resolvedRelation{
				RelTableColumn:   r.ForeignColumn,
				LocalTableColumn: r.ColumnName,
			}
		}
	}
	// Check reverse: relName table has FK pointing to current table
	// relTbl has column r.ColumnName pointing to tbl's r.ForeignColumn
	if relTbl, ok := graph.GetTable(relName); ok {
		for _, r := range relTbl.Relations {
			if r.ForeignTable == tbl.Name {
				return &resolvedRelation{
					RelTableColumn:   r.ColumnName,
					LocalTableColumn: r.ForeignColumn,
				}
			}
		}
	}
	return nil
}

// parseSelectWithRelations parses "id,name,orders(id,total)" into flat cols and nested relations.
func parseSelectWithRelations(sel string) ([]string, map[string][]string) {
	if sel == "" {
		return nil, nil
	}

	var cols []string
	nested := make(map[string][]string)

	i := 0
	for i < len(sel) {
		// find next comma or paren
		j := i
		for j < len(sel) && sel[j] != ',' && sel[j] != '(' {
			j++
		}
		token := strings.TrimSpace(sel[i:j])

		if j < len(sel) && sel[j] == '(' {
			// nested relation
			relName := token
			k := j + 1
			depth := 1
			for k < len(sel) && depth > 0 {
				if sel[k] == '(' {
					depth++
				} else if sel[k] == ')' {
					depth--
				}
				k++
			}
			innerCols := strings.Split(sel[j+1:k-1], ",")
			for idx := range innerCols {
				innerCols[idx] = strings.TrimSpace(innerCols[idx])
			}
			nested[relName] = innerCols
			i = k
			if i < len(sel) && sel[i] == ',' {
				i++
			}
		} else {
			if token != "" {
				cols = append(cols, token)
			}
			i = j
			if i < len(sel) && sel[i] == ',' {
				i++
			}
		}
	}

	return cols, nested
}

// parseFilter extracts column, operator, value from "col=op.value" style params.
func parseFilter(key, val string) (col string, op string, filterVal string) {
	// key is the column name, val is "op.value"
	parts := strings.SplitN(val, ".", 2)
	if len(parts) != 2 {
		return "", "", ""
	}
	opStr := parts[0]
	filterVal = parts[1]

	switch opStr {
	case "eq":
		op = "="
	case "neq":
		op = "!="
	case "gt":
		op = ">"
	case "gte":
		op = ">="
	case "lt":
		op = "<"
	case "lte":
		op = "<="
	case "like":
		op = "LIKE"
	case "ilike":
		op = "ILIKE"
	case "is":
		op = "IS"
	default:
		return "", "", ""
	}

	return key, op, filterVal
}

// quoteIdent quotes a SQL identifier to prevent injection.
func quoteIdent(s string) string {
	// Strip any existing quotes, disallow special chars
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, ";", "")
	s = strings.ReplaceAll(s, "--", "")
	return "\"" + s + "\""
}

// sanitizeInt ensures a value is a plain integer string.
func sanitizeInt(s string) string {
	var result []byte
	for _, c := range s {
		if c >= '0' && c <= '9' {
			result = append(result, byte(c))
		}
	}
	if len(result) == 0 {
		return "0"
	}
	return string(result)
}
