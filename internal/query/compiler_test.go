package query

import (
	"strings"
	"testing"

	"github.com/dynago/dynago/internal/catalog"
)

func buildTestGraph() *catalog.SchemaGraph {
	customers := &catalog.TableDefinition{
		Schema:      "public",
		Name:        "customers",
		Columns:     map[string]*catalog.ColumnDefinition{
			"id":        {Name: "id", DataType: "integer", IsPrimaryKey: true},
			"full_name": {Name: "full_name", DataType: "text"},
			"email":     {Name: "email", DataType: "text"},
		},
		ColumnOrder: []string{"id", "full_name", "email"},
		PrimaryKeys: []string{"id"},
	}

	orders := &catalog.TableDefinition{
		Schema:      "public",
		Name:        "orders",
		Columns:     map[string]*catalog.ColumnDefinition{
			"id":           {Name: "id", DataType: "integer", IsPrimaryKey: true},
			"customer_id":  {Name: "customer_id", DataType: "integer"},
			"order_number": {Name: "order_number", DataType: "text"},
		},
		ColumnOrder: []string{"id", "customer_id", "order_number"},
		PrimaryKeys: []string{"id"},
		Relations: []catalog.RelationDefinition{
			{
				ColumnName:    "customer_id",
				ForeignTable:  "customers",
				ForeignColumn: "id",
			},
		},
	}

	return &catalog.SchemaGraph{
		Tables: map[string]*catalog.TableDefinition{
			"customers": customers,
			"orders":    orders,
		},
		Functions: make(map[string]*catalog.FunctionDefinition),
	}
}

func TestNestedRelationsSQL(t *testing.T) {
	graph := buildTestGraph()
	compiler := NewCompiler(graph)

	t.Run("Reverse FK: customers embedding orders", func(t *testing.T) {
		q, err := compiler.CompileSelect("customers", map[string]string{
			"select": "id,full_name,orders(id,order_number)",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedSubquery := `"orders"."customer_id" = "customers"."id"`
		if !strings.Contains(q.SQL, expectedSubquery) {
			t.Errorf("expected SQL to contain %q, but got:\n%s", expectedSubquery, q.SQL)
		}
	})

	t.Run("Direct FK: orders embedding customers", func(t *testing.T) {
		q, err := compiler.CompileSelect("orders", map[string]string{
			"select": "id,order_number,customers(id,full_name)",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		expectedSubquery := `"customers"."id" = "orders"."customer_id"`
		if !strings.Contains(q.SQL, expectedSubquery) {
			t.Errorf("expected SQL to contain %q, but got:\n%s", expectedSubquery, q.SQL)
		}
	})

	t.Run("CompileCount with filters", func(t *testing.T) {
		cq, err := compiler.CompileCount("customers", map[string]string{
			"email": "eq.test@example.com",
			"limit": "10",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(cq.SQL, `SELECT count(*) FROM "customers" WHERE "customers"."email" = $1`) {
			t.Errorf("unexpected count SQL: %s", cq.SQL)
		}
		if len(cq.Args) != 1 || cq.Args[0] != "test@example.com" {
			t.Errorf("unexpected args: %v", cq.Args)
		}
	})
}
