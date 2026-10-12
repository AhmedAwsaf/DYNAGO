package catalog

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ColumnDefinition describes a single column in a table.
type ColumnDefinition struct {
	Name         string
	DataType     string
	IsNullable   bool
	DefaultValue string
	IsPrimaryKey bool
}

// RelationDefinition describes a foreign key relationship.
type RelationDefinition struct {
	ColumnName    string
	ForeignTable  string
	ForeignColumn string
}

// TableDefinition holds full metadata for a single table.
type TableDefinition struct {
	Schema      string
	Name        string
	Columns     map[string]*ColumnDefinition
	ColumnOrder []string // preserves ordinal position
	PrimaryKeys []string
	Relations   []RelationDefinition
}

// FunctionDefinition describes a stored procedure / function in PostgreSQL.
type FunctionDefinition struct {
	Name        string `json:"name"`
	Arguments   string `json:"arguments"`
	ReturnType  string `json:"return_type"`
	Description string `json:"description,omitempty"`
}

// SchemaGraph is the in-memory representation of the entire database schema and functions.
type SchemaGraph struct {
	Tables    map[string]*TableDefinition
	Functions map[string]*FunctionDefinition
	mu        sync.RWMutex
}

// NewPool creates a pgx connection pool.
func NewPool(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	cfg.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping: %w", err)
	}
	return pool, nil
}

// introspection SQL — single query gets columns, PKs, and FKs.
const introspectSQL = `
SELECT 
    c.table_schema,
    c.table_name,
    c.column_name,
    c.data_type,
    c.is_nullable,
    COALESCE(c.column_default, ''),
    COALESCE(tc.constraint_type, ''),
    COALESCE(kcu2.table_name, ''),
    COALESCE(kcu2.column_name, '')
FROM information_schema.columns c
LEFT JOIN information_schema.key_column_usage kcu
    ON c.table_schema = kcu.table_schema 
    AND c.table_name = kcu.table_name 
    AND c.column_name = kcu.column_name
LEFT JOIN information_schema.table_constraints tc 
    ON kcu.constraint_name = tc.constraint_name
    AND kcu.table_schema = tc.table_schema
LEFT JOIN information_schema.referential_constraints rc 
    ON tc.constraint_name = rc.constraint_name
    AND tc.constraint_schema = rc.constraint_schema
LEFT JOIN information_schema.key_column_usage kcu2 
    ON rc.unique_constraint_name = kcu2.constraint_name
    AND rc.unique_constraint_schema = kcu2.constraint_schema
WHERE c.table_schema NOT IN ('pg_catalog', 'information_schema')
  AND c.table_name NOT LIKE '_dynago_%'
ORDER BY c.table_name, c.ordinal_position;
`

// Introspect queries PostgreSQL catalog and builds the SchemaGraph.
func Introspect(ctx context.Context, pool *pgxpool.Pool) (*SchemaGraph, error) {
	rows, err := pool.Query(ctx, introspectSQL)
	if err != nil {
		return nil, fmt.Errorf("introspect query: %w", err)
	}
	defer rows.Close()

	graph := &SchemaGraph{
		Tables:    make(map[string]*TableDefinition),
		Functions: make(map[string]*FunctionDefinition),
	}

	for rows.Next() {
		var (
			schema, table, column, dataType string
			isNullable, colDefault          string
			constraintType                  string
			foreignTable, foreignColumn     string
		)
		if err := rows.Scan(&schema, &table, &column, &dataType, &isNullable, &colDefault,
			&constraintType, &foreignTable, &foreignColumn); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}

		tbl, ok := graph.Tables[table]
		if !ok {
			tbl = &TableDefinition{
				Schema:  schema,
				Name:    table,
				Columns: make(map[string]*ColumnDefinition),
			}
			graph.Tables[table] = tbl
		}

		// Add column if not already present (joins may duplicate rows)
		if _, exists := tbl.Columns[column]; !exists {
			col := &ColumnDefinition{
				Name:         column,
				DataType:     dataType,
				IsNullable:   isNullable == "YES",
				DefaultValue: colDefault,
			}
			tbl.Columns[column] = col
			tbl.ColumnOrder = append(tbl.ColumnOrder, column)
		}

		if constraintType == "PRIMARY KEY" {
			tbl.Columns[column].IsPrimaryKey = true
			// deduplicate
			found := false
			for _, pk := range tbl.PrimaryKeys {
				if pk == column {
					found = true
					break
				}
			}
			if !found {
				tbl.PrimaryKeys = append(tbl.PrimaryKeys, column)
			}
		}

		if constraintType == "FOREIGN KEY" && foreignTable != "" {
			// deduplicate relations
			dup := false
			for _, r := range tbl.Relations {
				if r.ColumnName == column && r.ForeignTable == foreignTable {
					dup = true
					break
				}
			}
			if !dup {
				tbl.Relations = append(tbl.Relations, RelationDefinition{
					ColumnName:    column,
					ForeignTable:  foreignTable,
					ForeignColumn: foreignColumn,
				})
			}
		}
	}

	if rows.Err() != nil {
		return graph, rows.Err()
	}

	// Introspect PostgreSQL stored functions & procedures
	funcRows, funcErr := pool.Query(ctx, introspectFunctionsSQL)
	if funcErr == nil {
		defer funcRows.Close()
		for funcRows.Next() {
			var fn FunctionDefinition
			if err := funcRows.Scan(&fn.Name, &fn.Arguments, &fn.ReturnType, &fn.Description); err == nil {
				graph.Functions[fn.Name] = &fn
			}
		}
	}

	return graph, nil
}

const introspectFunctionsSQL = `
SELECT 
    p.proname AS function_name,
    COALESCE(pg_get_function_arguments(p.oid), '') AS argument_list,
    COALESCE(pg_get_function_result(p.oid), '') AS return_type,
    COALESCE(d.description, '') AS description
FROM pg_proc p
JOIN pg_namespace n ON p.pronamespace = n.oid
LEFT JOIN pg_description d ON d.objoid = p.oid
WHERE n.nspname = 'public'
  AND p.prokind IN ('f', 'p')
  AND p.proname NOT LIKE '_dynago_%'
ORDER BY p.proname;
`

// Refresh re-introspects the schema and replaces the graph atomically.
func (g *SchemaGraph) Refresh(ctx context.Context, pool *pgxpool.Pool) error {
	newGraph, err := Introspect(ctx, pool)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.Tables = newGraph.Tables
	g.Functions = newGraph.Functions
	g.mu.Unlock()
	return nil
}

// GetTable returns a table definition (thread-safe).
func (g *SchemaGraph) GetTable(name string) (*TableDefinition, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	tbl, ok := g.Tables[name]
	return tbl, ok
}

// TableNames returns all table names (thread-safe).
func (g *SchemaGraph) TableNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.Tables))
	for name := range g.Tables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// GetFunction returns a stored procedure/function definition (thread-safe).
func (g *SchemaGraph) GetFunction(name string) (*FunctionDefinition, bool) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fn, ok := g.Functions[name]
	return fn, ok
}

// FunctionNames returns all function names (thread-safe).
func (g *SchemaGraph) FunctionNames() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	names := make([]string, 0, len(g.Functions))
	for name := range g.Functions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
