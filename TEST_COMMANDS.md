# Dynago Test Suite & Command Reference

This file contains the complete test setup, SQL schema, seed data, and commands for testing Dynago.

---

## 0. Setup: Test Database Schema & Seed Data

Run this SQL script against your PostgreSQL database (via `psql`, pgAdmin, or your Neon Console SQL editor) before running the test commands.

```sql
-- Clean up existing tables if any
DROP TABLE IF EXISTS reviews CASCADE;
DROP TABLE IF EXISTS order_items CASCADE;
DROP TABLE IF EXISTS orders CASCADE;
DROP TABLE IF EXISTS products CASCADE;
DROP TABLE IF EXISTS categories CASCADE;
DROP TABLE IF EXISTS customers CASCADE;

-- 1. Categories
CREATE TABLE categories (
    id SERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL UNIQUE,
    slug VARCHAR(100) NOT NULL,
    description TEXT,
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 2. Products (Foreign key to categories, JSONB, check constraints)
CREATE TABLE products (
    id SERIAL PRIMARY KEY,
    category_id INT REFERENCES categories(id) ON DELETE SET NULL,
    sku VARCHAR(50) NOT NULL UNIQUE,
    title VARCHAR(255) NOT NULL,
    price NUMERIC(10, 2) NOT NULL CHECK (price >= 0),
    stock_quantity INT DEFAULT 0 CHECK (stock_quantity >= 0),
    metadata JSONB DEFAULT '{}'::jsonb,
    is_published BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 3. Customers
CREATE TABLE customers (
    id SERIAL PRIMARY KEY,
    email VARCHAR(255) NOT NULL UNIQUE,
    full_name VARCHAR(150) NOT NULL,
    tier VARCHAR(50) DEFAULT 'standard', -- standard, silver, gold
    loyalty_points INT DEFAULT 0,
    is_active BOOLEAN DEFAULT TRUE,
    preferences JSONB DEFAULT '{"notifications": true}'::jsonb,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- 4. Orders (Foreign key to customers)
CREATE TABLE orders (
    id SERIAL PRIMARY KEY,
    customer_id INT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    order_number VARCHAR(50) NOT NULL UNIQUE,
    status VARCHAR(50) DEFAULT 'pending', -- pending, processing, shipped, completed, cancelled
    total_amount NUMERIC(10, 2) NOT NULL DEFAULT 0.00,
    shipping_address JSONB,
    placed_at TIMESTAMPTZ DEFAULT NOW()
);

-- 5. Order Items (Bridge table: FK to orders and products)
CREATE TABLE order_items (
    id SERIAL PRIMARY KEY,
    order_id INT NOT NULL REFERENCES orders(id) ON DELETE CASCADE,
    product_id INT NOT NULL REFERENCES products(id) ON DELETE RESTRICT,
    quantity INT NOT NULL CHECK (quantity > 0),
    unit_price NUMERIC(10, 2) NOT NULL
);

-- 6. Product Reviews (FK to products and customers)
CREATE TABLE reviews (
    id SERIAL PRIMARY KEY,
    product_id INT NOT NULL REFERENCES products(id) ON DELETE CASCADE,
    customer_id INT NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    rating INT NOT NULL CHECK (rating BETWEEN 1 AND 5),
    comment TEXT,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

-- ============================================================================
-- Seed Sample Data
-- ============================================================================

INSERT INTO categories (name, slug, description) VALUES
('Electronics', 'electronics', 'Gadgets, devices, and accessories'),
('Audio', 'audio', 'Headphones, speakers, and audio equipment'),
('Office', 'office', 'Desks, chairs, and workspace gear');

INSERT INTO products (category_id, sku, title, price, stock_quantity, metadata, is_published) VALUES
(1, 'ELEC-KB-01', 'Pro Wireless Mechanical Keyboard', 129.99, 45, '{"switch": "brown", "wireless": true}', TRUE),
(1, 'ELEC-MO-02', 'Ergonomic Precision Mouse', 79.50, 110, '{"dpi": 16000, "bluetooth": true}', TRUE),
(2, 'AUD-HP-01', 'Studio Noise-Cancelling Headphones', 249.00, 25, '{"battery_hours": 30, "anc": true}', TRUE),
(2, 'AUD-SP-02', 'Portable Waterproof Speaker', 59.99, 0, '{"rating": "IPX7", "output": "20W"}', FALSE),
(3, 'OFF-DK-01', 'Dual-Motor Standing Desk Frame', 399.00, 12, '{"motor_count": 2, "max_load_kg": 120}', TRUE);

INSERT INTO customers (email, full_name, tier, loyalty_points, preferences) VALUES
('alice@example.com', 'Alice Chen', 'gold', 1450, '{"newsletter": true, "dark_mode": true}'),
('bob@example.com', 'Bob Smith', 'standard', 120, '{"newsletter": false}'),
('charlie@example.com', 'Charlie Davis', 'silver', 680, '{"newsletter": true}');

INSERT INTO orders (customer_id, order_number, status, total_amount, shipping_address) VALUES
(1, 'ORD-2026-001', 'shipped', 209.49, '{"street": "123 Tech Blvd", "city": "San Francisco", "zip": "94105"}'),
(1, 'ORD-2026-002', 'completed', 249.00, '{"street": "123 Tech Blvd", "city": "San Francisco", "zip": "94105"}'),
(2, 'ORD-2026-003', 'pending', 399.00, '{"street": "456 Market St", "city": "Seattle", "zip": "98101"}');

INSERT INTO order_items (order_id, product_id, quantity, unit_price) VALUES
(1, 1, 1, 129.99),
(1, 2, 1, 79.50),
(2, 3, 1, 249.00),
(3, 5, 1, 399.00);

INSERT INTO reviews (product_id, customer_id, rating, comment) VALUES
(1, 1, 5, 'Best mechanical keyboard I have ever used. Great tactile feel.'),
(2, 1, 4, 'Very comfortable for long coding sessions.'),
(3, 2, 5, 'Incredible sound quality and ANC.');
```

---

## 1. Start Server

```powershell
# Run directly with Go
go run ./cmd/dynago

# Or build binary first
go build -o dynago.exe ./cmd/dynago
./dynago.exe
```

---

## 2. Phase 1: System Introspection & Metadata

```powershell
# 1. Health check
curl.exe -s http://localhost:8080/healthz

# 2. List all dynamic routes auto-generated by Dynago
curl.exe -s http://localhost:8080/api/v1/_endpoints

# 3. Dump complete schema metadata (tables, columns, types, nullability, PKs, FKs)
curl.exe -s http://localhost:8080/api/v1/_schema

# 4. Deep inspect a single table's metadata and relations
curl.exe -s http://localhost:8080/api/v1/_schema/products
curl.exe -s http://localhost:8080/api/v1/_schema/orders

# 5. Hot-refresh database schema on the fly (re-introspects without restarting server)
curl.exe -X POST http://localhost:8080/api/v1/_refresh
```

---

## 3. Phase 2: Advanced Queries & Anti-N+1 Nested Relations

Dynago compiles nested relation requests into a single Postgres JSON query:

```powershell
# 1. Reverse FK: Customers with their Orders embedded
curl.exe -s "http://localhost:8080/api/v1/customers?select=id,full_name,tier,orders(id,order_number,status,total_amount)"

# 2. Reverse FK: Orders with their Line Items embedded
curl.exe -s "http://localhost:8080/api/v1/orders?select=id,order_number,status,order_items(id,product_id,quantity,unit_price)"

# 3. Products with all Customer Reviews embedded
curl.exe -s "http://localhost:8080/api/v1/products?select=id,title,price,reviews(rating,comment)&id=eq.1"

# 4. Selective column projection (only return specific fields)
curl.exe -s "http://localhost:8080/api/v1/products?select=sku,title,price"
```

---

## 4. Phase 3: Filtering & Comparison Operators

Supported operators: `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `like`, `ilike`, `is`:

```powershell
# 1. Greater than (gt) + In-stock check: products over $100
curl.exe -s "http://localhost:8080/api/v1/products?price=gt.100&stock_quantity=gt.0"

# 2. Range filter (gte + lte): products between $50 and $150
curl.exe -s "http://localhost:8080/api/v1/products?price=gte.50&price=lte.150"

# 3. Case-insensitive pattern match (ilike): titles matching "*desk*"
curl.exe -s "http://localhost:8080/api/v1/products?title=ilike.%25desk%25"

# 4. Inequality (neq): all orders except 'cancelled'
curl.exe -s "http://localhost:8080/api/v1/orders?status=neq.cancelled"

# 5. Null checks (is): categories where description is null
curl.exe -s "http://localhost:8080/api/v1/categories?description=is.null"
```

---

## 5. Phase 4: Sorting & Pagination

```powershell
# 1. Descending sort by price with top 3 limit
curl.exe -s "http://localhost:8080/api/v1/products?order=price.desc&limit=3"

# 2. Pagination (Page 2: offset 2, limit 2)
curl.exe -s "http://localhost:8080/api/v1/products?order=id.asc&limit=2&offset=2"

# 3. Sort orders by placement date descending
curl.exe -s "http://localhost:8080/api/v1/orders?order=placed_at.desc&limit=5"
```

---

## 6. Phase 5: Complete CRUD Lifecycle & Cache Invalidation

```powershell
# 1. Warm cache on customers (Observe X-Dynago-Cache: MISS -> HIT)
curl.exe -i "http://localhost:8080/api/v1/customers?id=eq.1"
curl.exe -i "http://localhost:8080/api/v1/customers?id=eq.1"

# 2. CREATE: Insert a new customer
curl.exe -s -X POST http://localhost:8080/api/v1/customers `
  -H "Content-Type: application/json" `
  -d '{"email":"diana@test.com","full_name":"Diana Prince","tier":"gold","loyalty_points":500}'

# 3. READ: Verify the newly created customer
curl.exe -s "http://localhost:8080/api/v1/customers?email=eq.diana@test.com"

# 4. UPDATE: Upgrade loyalty points and tier via PATCH
curl.exe -s -X PATCH "http://localhost:8080/api/v1/customers?email=eq.diana@test.com" `
  -H "Content-Type: application/json" `
  -d '{"tier":"vip","loyalty_points":2500}'

# 5. READ: Verify the updated row
curl.exe -s "http://localhost:8080/api/v1/customers?email=eq.diana@test.com"

# 6. DELETE: Delete the customer
curl.exe -s -X DELETE "http://localhost:8080/api/v1/customers?email=eq.diana@test.com"

# 7. READ: Confirm deletion (should return empty array [])
curl.exe -s "http://localhost:8080/api/v1/customers?email=eq.diana@test.com"
```

---

## 7. Phase 6: Error Handling & Edge Cases

```powershell
# 1. Non-existent table (Expect 404 Not Found)
curl.exe -i "http://localhost:8080/api/v1/non_existent_table"

# 2. Database CHECK constraint violation (Negative price fails with 500/insert error)
curl.exe -i -X POST http://localhost:8080/api/v1/products `
  -H "Content-Type: application/json" `
  -d '{"sku":"BAD-SKU","title":"Invalid Product","price":-50.00}'

# 3. Unique constraint violation (Duplicate customer email)
curl.exe -i -X POST http://localhost:8080/api/v1/customers `
  -H "Content-Type: application/json" `
  -d '{"email":"alice@example.com","full_name":"Duplicate Alice"}'

# 4. Malformed JSON payload (Expect 400 Bad Request)
curl.exe -i -X POST http://localhost:8080/api/v1/customers `
  -H "Content-Type: application/json" `
  -d '{"email": bad_json}'
```

---

## 8. Phase 7: Throughput & Cache Speed Benchmark

```powershell
# Measure round-trip time for 100 requests (hitting in-memory cache)
Measure-Command {
    1..100 | ForEach-Object {
        curl.exe -s "http://localhost:8080/api/v1/products?limit=5" > $null
    }
} | Select-Object TotalMilliseconds, Milliseconds
```

---

## 9. Phase 8: Web UI & Flag Controls

### 9.1 Environment Flags Configuration (.env)
```env
# Flag 1: Enable UI mode
DYNAGO_ENABLE_UI=true

# Flag 2: Enable SQL execution in UI mode
DYNAGO_ENABLE_SQL=true

# Flag 3: Enable Data Explorer with dynamic filters & pagination
DYNAGO_ENABLE_DATA_VIEW=true

# Safety limit for data view pagination
DYNAGO_DATA_LIMIT=50
```

### 9.2 Test Commands

```powershell
# 1. Access Web Dashboard in browser (returns HTML dashboard)
curl.exe -i "http://localhost:8080/"
curl.exe -i "http://localhost:8080/ui"

# 2. Inspect UI Metadata (shows all flag states, tables, schema, and generated example JSON)
curl.exe -s "http://localhost:8080/api/v1/_ui/meta"

# 3. Test SQL Query Runner via UI API (when DYNAGO_ENABLE_SQL=true)
curl.exe -i -X POST "http://localhost:8080/api/v1/_ui/sql" `
  -H "Content-Type: application/json" `
  -d '{"query":"SELECT id, title, price, stock_quantity FROM products ORDER BY price DESC LIMIT 5"}'

# 4. Test Data Explorer API with Dynamic Filter & Pagination (when DYNAGO_ENABLE_DATA_VIEW=true)
# Filter: price >= 50, page 1, limit 10
curl.exe -s "http://localhost:8080/api/v1/_ui/data/products?price=gte.50&limit=10&page=1&order=price.desc"

# 5. Test multiple dynamic filters: is_active=true AND loyalty_points > 100
curl.exe -s "http://localhost:8080/api/v1/_ui/data/customers?is_active=eq.true&loyalty_points=gt.100&limit=5&page=1"

# 6. Verify safety limit enforcement (limit=500 is capped at DYNAGO_DATA_LIMIT)
curl.exe -s "http://localhost:8080/api/v1/_ui/data/products?limit=500"
```

---

## 10. Phase 9: Authentication & JWT Testing

```powershell
# 1. Access protected endpoint without token (Expect 401 Unauthorized)
curl.exe -i "http://localhost:8080/api/v1/products?limit=2"

# 2. Login with credentials to obtain JWT token
$loginResp = curl.exe -s -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{"username":"admin","password":"dynago123"}' | ConvertFrom-Json

$token = $loginResp.token
Write-Host "Obtained JWT Token: $token"

# 3. Verify authenticated identity
curl.exe -i -H "Authorization: Bearer $token" "http://localhost:8080/api/v1/_auth/me"

# 4. Standard GET: Query protected table with Bearer token
curl.exe -i -H "Authorization: Bearer $token" "http://localhost:8080/api/v1/products?limit=3"

# 5. GET with Field Selection (Projection): Select only specific columns
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/products?select=id,sku,title,price,stock_quantity&limit=3"

# 6. GET with Filtering & Comparison: Products with price > 50 and is_published = true
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/products?price=gt.50&is_published=eq.true"

# 7. GET with Pattern Matching: Filter customers by email domain using 'like'
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/customers?email=like.%25example.com"

# 8. GET with Sorting & Pagination: Order by price descending with limit and offset
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/products?order=price.desc&limit=2&offset=1"

# 9. GET Single Resource: Lookup by Primary Key
curl.exe -s -H "Authorization: Bearer $token" "http://localhost:8080/api/v1/products/1"

# 10. GET with Relational Embedding: Category including nested products
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/categories?include=products&limit=2"

# 11. GET with Multi-Table Embedding: Orders including customer info and order items
curl.exe -s -H "Authorization: Bearer $token" `
  "http://localhost:8080/api/v1/orders?include=customers,order_items&limit=2"

# 12. GET with Query Parameter Token (?token=...): Authenticate without custom headers
# (Useful for browsers, SSE EventSource, WebSockets, or embedded links)
curl.exe -i "http://localhost:8080/api/v1/products?token=$token&limit=2"

# 13. GET with In-Memory Cache Verification:
# First call produces MISS, second identical call produces HIT
curl.exe -i -H "Authorization: Bearer $token" "http://localhost:8080/api/v1/customers?tier=eq.gold"
curl.exe -i -H "Authorization: Bearer $token" "http://localhost:8080/api/v1/customers?tier=eq.gold"

# 14. Invalid credentials test (Expect 401 Unauthorized)
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{"username":"admin","password":"wrongpassword"}'

# 15. Invalid / Expired Token test (Expect 401 Unauthorized)
curl.exe -i -H "Authorization: Bearer invalid_jwt_token_xyz" "http://localhost:8080/api/v1/products"
```

---

## 11. Phase 10: User Management & Table-Level Permissions (RBAC)

```powershell
# Prerequisites: Login as admin to obtain admin token
$adminLogin = curl.exe -s -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{"username":"admin","password":"dynago123"}' | ConvertFrom-Json
$adminToken = $adminLogin.token

# 1. Create an 'editor' user
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/users" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"username":"john_editor","password":"editorpass123","role":"editor"}'

# 2. Create a 'viewer' user
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/users" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"username":"alice_viewer","password":"viewerpass123","role":"viewer"}'

# 3. List all registered users (Admin only)
curl.exe -s -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/_auth/users"

# 4. View permissions matrix
curl.exe -s -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/_auth/permissions"

# 5. Log in as 'alice_viewer' to get viewer token
$viewerLogin = curl.exe -s -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{"username":"alice_viewer","password":"viewerpass123"}' | ConvertFrom-Json
$viewerToken = $viewerLogin.token

# 6. Viewer reads products (Expect 200 OK)
curl.exe -i -H "Authorization: Bearer $viewerToken" "http://localhost:8080/api/v1/products?limit=2"

# 7. Viewer attempts INSERT into products (Expect 403 Forbidden: read-only)
curl.exe -i -X POST "http://localhost:8080/api/v1/products" `
  -H "Authorization: Bearer $viewerToken" `
  -H "Content-Type: application/json" `
  -d '{"sku":"TEST-SKU","title":"Unauthorized Product","price":19.99}'

# 8. Admin configures custom permission: grant 'viewer' insert access on products
curl.exe -i -X PUT "http://localhost:8080/api/v1/_auth/permissions" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"role":"viewer","table_name":"products","can_read":true,"can_insert":true,"can_update":false,"can_delete":false}'

# 9. Viewer retries INSERT into products (Expect 201 Created now!)
curl.exe -i -X POST "http://localhost:8080/api/v1/products" `
  -H "Authorization: Bearer $viewerToken" `
  -H "Content-Type: application/json" `
  -d '{"sku":"VIEWER-PERM-01","title":"Permitted Product","price":29.99}'

# 10. Admin deletes user account 'john_editor'
curl.exe -i -X DELETE "http://localhost:8080/api/v1/_auth/users/john_editor" `
  -H "Authorization: Bearer $adminToken"

# 11. Verify deleted user cannot log in (Expect 401 Unauthorized)
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{"username":"john_editor","password":"editorpass123"}'
```

---

## 12. Phase 11: Real-Time Server-Sent Events (SSE) Stream

```powershell
# 1. Start streaming real-time events for 'products' table:
# Option A: Authenticate with Bearer token header
curl.exe -N -H "Authorization: Bearer $token" http://localhost:8080/api/v1/stream/products

# Option B: Authenticate with Query Parameter (?token=...) — browser EventSource & WebSocket standard
curl.exe -N "http://localhost:8080/api/v1/stream/products?token=$token"

# 2. In a second terminal window, trigger an INSERT mutation:
curl.exe -s -X POST http://localhost:8080/api/v1/products `
  -H "Content-Type: application/json" `
  -d '{"sku":"STREAM-ITEM-01","title":"Live Stream Keyboard","price":149.00,"stock_quantity":20}'

# -> Output in the first terminal immediately shows:
# event: INSERT
# data: {"table":"products","action":"INSERT","record":{...},"timestamp":"..."}

# 3. Trigger a PATCH update mutation:
curl.exe -s -X PATCH "http://localhost:8080/api/v1/products?sku=eq.STREAM-ITEM-01" `
  -H "Content-Type: application/json" `
  -d '{"price":129.00}'

# -> Output in the first terminal immediately shows:
# event: UPDATE
# data: {"table":"products","action":"UPDATE","record":{...},"timestamp":"..."}

# 4. Trigger a DELETE mutation:
curl.exe -s -X DELETE "http://localhost:8080/api/v1/products?sku=eq.STREAM-ITEM-01"

# -> Output in the first terminal immediately shows:
# event: DELETE
# data: {"table":"products","action":"DELETE","timestamp":"..."}

# 5. Global stream across all tables (requires admin token when auth enabled):
curl.exe -N -H "Authorization: Bearer $adminToken" http://localhost:8080/api/v1/stream
```

---

## 13. Phase 12: WebAssembly (Wasm) Plugins & Host Outbound HTTP Testing

Dynago loads `.wasm` plugins from the `./plugins` directory on startup. If no `.wasm` files are present, Dynago automatically seeds `customers_prehook.wasm` and `orders_prehook.wasm`.

```powershell
# 1. Verify Wasm plugin files are present in ./plugins
Get-ChildItem -Path ./plugins -Filter *.wasm

# 2. Test customers_prehook: Insert customer (triggers customers_prehook.wasm)
# The hook intercepts the payload and normalizes the email address
curl.exe -s -X POST http://localhost:8080/api/v1/customers `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"email":"WASM_USER@Example.COM","full_name":"Wasm Test User","tier":"standard"}'

# 3. Read back inserted customer to verify prehook execution
curl.exe -s -H "Authorization: Bearer $adminToken" `
  "http://localhost:8080/api/v1/customers?full_name=eq.Wasm%20Test%20User"

# 4. Test orders_prehook: Insert high-value order (triggers orders_prehook.wasm with outbound HTTP)
# The hook calls the host function http_post to perform an outbound verification check
curl.exe -s -X POST http://localhost:8080/api/v1/orders `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"customer_id":1,"order_number":"ORD-WASM-999","total_amount":750.00,"status":"pending"}'

# 5. Check Dynago server logs:
# Notice the [wasm:log] and [wasm:http] messages demonstrating the Wasm plugin execution:
# [wasm:log] [orders_prehook] Received input payload
# [wasm:log] [orders_prehook] High-value order detected ($750.00), calling fraud validation service...
```

---

## 14. Phase 13: PostgreSQL Logical Replication (CDC) & Targeted Cache Invalidation

This test phase verifies that the CDC logical replication engine intercepts raw Write-Ahead Log mutations and automatically invalidates the cache while streaming real-time events to SSE clients.

```powershell
# 1. Start real-time SSE stream for 'products' in a separate terminal:
curl.exe -N http://localhost:8080/api/v1/stream/products

# 2. Warm cache on 'products' and 'customers':
# Both requests will return X-Dynago-Cache: MISS on 1st call, then HIT on 2nd call
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/products?limit=2"
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/products?limit=2"
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/customers?limit=2"
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/customers?limit=2"

# 3. Perform a mutation directly on 'products' (via SQL console or psql):
# Notice: Even when executed directly against the database, the CDC engine intercepts the WAL event!
curl.exe -s -X POST "http://localhost:8080/api/v1/_ui/sql" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"query":"UPDATE products SET price = 119.99 WHERE sku = '\''ELEC-KB-01'\''"}'

# -> Output in the first terminal (SSE stream) receives the UPDATE event:
# event: UPDATE
# data: {"schema":"public","table":"products","action":"UPDATE", ...}

# 4. Verify targeted cache eviction:
# The 'products' cache is invalidated (returns X-Dynago-Cache: MISS)
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/products?limit=2"

# The 'customers' cache is still warm and valid (returns X-Dynago-Cache: HIT)
curl.exe -i -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/customers?limit=2"

# 5. Check replication slot status in PostgreSQL:
curl.exe -s -X POST "http://localhost:8080/api/v1/_ui/sql" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"query":"SELECT slot_name, plugin, active, wal_status FROM pg_replication_slots WHERE slot_name = '\''dynago_cdc_slot'\''"}'
```

---

## 15. Phase 14: Custom Endpoints & RPC Functions (Wasm & PostgreSQL)

```powershell
# 1. Discover all registered Custom Functions (Wasm routes + Database stored procedures):
curl.exe -s -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/functions"

# 2. Invoke sandboxed Wasm Custom Function (currency_convert):
curl.exe -i -X POST "http://localhost:8080/api/v1/functions/currency_convert" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"amount\":100,\"from\":\"USD\",\"to\":\"EUR\"}'

# 3. Create a custom PostgreSQL stored function via the SQL console:
curl.exe -s -X POST "http://localhost:8080/api/v1/_ui/sql" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{"query":"CREATE OR REPLACE FUNCTION calculate_discount(price numeric, discount_percent numeric) RETURNS numeric AS $$ BEGIN RETURN ROUND(price * (1 - discount_percent / 100), 2); END; $$ LANGUAGE plpgsql;"}'

# 4. Refresh schema to re-introspect newly created stored function:
curl.exe -s -X POST "http://localhost:8080/api/v1/_refresh" `
  -H "Authorization: Bearer $adminToken"

# 5. Invoke PostgreSQL stored function via RPC POST with JSON payload:
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/calculate_discount" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"price\":149.99,\"discount_percent\":20}'

# 6. Invoke PostgreSQL stored function via GET with query parameters:
curl.exe -s -H "Authorization: Bearer $adminToken" `
  "http://localhost:8080/api/v1/rpc/calculate_discount?price=250&discount_percent=10"

# 7. Anonymous access to Public function (no Authorization token required):
curl.exe -i -X POST "http://localhost:8080/api/v1/functions/currency_convert" `
  -H "Content-Type: application/json" `
  -d '{\"amount\":250,\"from\":\"USD\",\"to\":\"EUR\"}'

# 8. Anonymous access to Private function without token (fails with HTTP 401 Unauthorized):
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/calculate_discount" `
  -H "Content-Type: application/json" `
  -d '{\"price\":100,\"discount_percent\":15}'

# 9. Admin toggles calculate_discount to PUBLIC:
curl.exe -i -X PUT "http://localhost:8080/api/v1/_auth/functions/calculate_discount" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"is_public\":true}'

# 10. Anonymous access to calculate_discount now succeeds without any token:
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/calculate_discount" `
  -H "Content-Type: application/json" `
  -d '{\"price\":100,\"discount_percent\":15}'
```

---

## 16. Phase 15: Public vs. Private Tables (Anonymous Access Control)

```powershell
# 1. Anonymous access to a Private table without token (fails with HTTP 401 Unauthorized):
curl.exe -i "http://localhost:8080/api/v1/orders?limit=3"

# 2. Admin designates table as PUBLIC:
curl.exe -i -X PUT "http://localhost:8080/api/v1/_auth/tables/products" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"is_public\":true}'

# 3. Anonymous caller can now read from public table without any Authorization token (HTTP 200 OK):
curl.exe -i "http://localhost:8080/api/v1/products?limit=2"

# 4. Anonymous mutation on public table requires login by default (HTTP 401 Unauthorized):
curl.exe -i -X POST "http://localhost:8080/api/v1/products" `
  -H "Content-Type: application/json" `
  -d '{\"sku\":\"PUB-TEST\",\"title\":\"Public Test Product\",\"price\":19.99}'

# 5. Admin toggles table back to PRIVATE:
curl.exe -i -X PUT "http://localhost:8080/api/v1/_auth/tables/products" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"is_public\":false}'

# 6. Anonymous access to products is now blocked again with HTTP 401 Unauthorized:
curl.exe -i "http://localhost:8080/api/v1/products?limit=2"
```

---

## 15. Account Registration and Privacy Hiding System

### 15.1 Create Account (Register New User)

```powershell
# 1. Register a new user account (automatically assigned viewer role):
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/register" `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"alice\",\"password\":\"alice_secret_pass\"}'

# 2. Extract returned token:
$aliceToken = "..."

# 3. Verify user identity:
curl.exe -i "http://localhost:8080/api/v1/_auth/me" `
  -H "Authorization: Bearer $aliceToken"
```

### 15.2 Schema, Endpoints, and Stream Privacy Checks

```powershell
# 1. Anonymous endpoint discovery:
# Only public tables and public functions are exposed. Private endpoints/tables are hidden.
curl.exe -i "http://localhost:8080/api/v1/_endpoints"

# 2. Anonymous schema catalog:
# Only public tables are included in the schema list.
curl.exe -i "http://localhost:8080/api/v1/_schema"

# 3. Direct schema inspection on private table without token (401 Unauthorized):
curl.exe -i "http://localhost:8080/api/v1/_schema/orders"

# 4. Anonymous global event stream access (401 Unauthorized):
curl.exe -i "http://localhost:8080/api/v1/stream"

# 5. Non-admin SQL execution attempt (403 Forbidden):
curl.exe -i -X POST "http://localhost:8080/api/v1/_ui/sql" `
  -H "Authorization: Bearer $aliceToken" `
  -H "Content-Type: application/json" `
  -d '{\"query\":\"SELECT 1\"}'

# 6. Admin full discovery:
curl.exe -i "http://localhost:8080/api/v1/_endpoints" `
  -H "Authorization: Bearer $adminToken"
```

---

## 17. Phase 16: User Manager, Role Updates & Admin Deletion Protection

```powershell
# 1. Admin creates a user with viewer role:
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/users" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"bob_viewer\",\"password\":\"bob_pass_123\",\"role\":\"viewer\"}'

# 2. Login as bob_viewer and verify identity (role is viewer):
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/login" `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"bob_viewer\",\"password\":\"bob_pass_123\"}'
$bobToken = "..."

curl.exe -i "http://localhost:8080/api/v1/_auth/me" `
  -H "Authorization: Bearer $bobToken"

# 3. Viewer attempts mutation on table (blocked with HTTP 403 Forbidden):
curl.exe -i -X POST "http://localhost:8080/api/v1/products" `
  -H "Authorization: Bearer $bobToken" `
  -H "Content-Type: application/json" `
  -d '{\"sku\":\"TEST-V\",\"title\":\"Viewer Product\",\"price\":10.00}'

# 4. Admin updates user role from viewer to editor:
curl.exe -i -X PATCH "http://localhost:8080/api/v1/_auth/users/bob_viewer" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"role\":\"editor\"}'

# 5. Editor attempts mutation without explicit permission grant (blocked with HTTP 403 Forbidden):
# By default, editor has read-only access until an admin explicitly grants insert/update in permissions matrix.
curl.exe -i -X POST "http://localhost:8080/api/v1/products" `
  -H "Authorization: Bearer $bobToken" `
  -H "Content-Type: application/json" `
  -d '{\"sku\":\"TEST-ED\",\"title\":\"Editor Product\",\"price\":10.00}'

# 6. Admin explicitly grants insert permission for editor on products table:
curl.exe -i -X PUT "http://localhost:8080/api/v1/_auth/permissions" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"role\":\"editor\",\"table_name\":\"products\",\"can_read\":true,\"can_insert\":true,\"can_update\":false,\"can_delete\":false}'

# 7. Admin creates another admin account:
curl.exe -i -X POST "http://localhost:8080/api/v1/_auth/users" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"username\":\"charlie_admin\",\"password\":\"charlie_pass_123\",\"role\":\"admin\"}'

# 8. Admin attempts to delete another admin (blocked with HTTP 403 Forbidden):
curl.exe -i -X DELETE "http://localhost:8080/api/v1/_auth/users/charlie_admin" `
  -H "Authorization: Bearer $adminToken"

# 9. Admin attempts to delete their own account (blocked with HTTP 400 Bad Request):
curl.exe -i -X DELETE "http://localhost:8080/api/v1/_auth/users/admin" `
  -H "Authorization: Bearer $adminToken"

# 10. Admin deletes non-admin account (succeeds with HTTP 200 OK):
curl.exe -i -X DELETE "http://localhost:8080/api/v1/_auth/users/bob_viewer" `
  -H "Authorization: Bearer $adminToken"
```

---

## 18. Phase 17: Database Mutation Functions & Order Receipts

```powershell
# 1. Mutate database: Update order status to 'processing'
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/update_order_status" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"target_order_id\":1,\"new_status\":\"processing\"}'

# 2. Verify order status in DB was actually modified:
curl.exe -s -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/orders?id=eq.1"

# 3. Create persistent itemized receipt for order 1:
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/create_order_receipt" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"target_order_id\":1}'

# 4. Fetch receipt for order 1:
curl.exe -i -X POST "http://localhost:8080/api/v1/rpc/get_order_receipt" `
  -H "Authorization: Bearer $adminToken" `
  -H "Content-Type: application/json" `
  -d '{\"target_order_id\":1}'

# 5. Query newly generated receipts table via standard REST API:
curl.exe -s -H "Authorization: Bearer $adminToken" "http://localhost:8080/api/v1/receipts?order_id=eq.1"
```






