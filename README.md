# Dynago

**Dynamic PostgreSQL REST API Engine** — connect to any Postgres database and instantly get fully dynamic, cached REST endpoints for every table. Zero ORM, zero code generation, zero boilerplate.

Dynago introspects your database schema at startup, builds an in-memory graph of tables, columns, primary keys, and foreign key relationships, then exposes CRUD endpoints that compile HTTP requests directly into optimized `json_build_object` SQL queries. Responses stream raw JSON bytes from Postgres straight to the client — no marshaling/unmarshaling overhead.

---

## Features

- **Dynamic endpoint generation** — every table becomes a REST API automatically
- **Single-query JSON compilation** — nested relations resolved in one SQL statement (anti-N+1)
- **Built-in caching** — in-memory TTL cache (or Redis) with `X-Dynago-Cache: HIT/MISS` headers
- **Interactive Web UI Dashboard** — zero-dependency embedded dashboard at `/` and `/ui`
- **Dynamic Example JSON Generator** — realistic request & response payloads generated per table
- **In-Browser SQL Query Runner** — execute queries with sub-millisecond timer & CSV export
- **Data Explorer** — interactive table viewer with dynamic multi-filtering, sorting & pagination
- **JWT Authentication & Login API** — built-in token issuance (`POST /api/v1/_auth/login`) & session handling
- **Table-Level RBAC & Public/Private Visibility** — designate tables as Public (accessible anonymously without logging in) or Private (requires JWT Bearer token)
- **Custom Endpoints & Functions (RPC)** — execute polyglot Wasm virtual routes & PostgreSQL stored functions via `/api/v1/functions/{name}` and `/api/v1/rpc/{name}` with Public/Private access control
- **Real-Time SSE Streaming** — live Server-Sent Events stream per table (`/api/v1/stream/{table}`) or globally (`/api/v1/stream`)
- **Schema refresh endpoint** — `POST /api/v1/_refresh` to hot-reload after schema changes
- **Endpoint checklist** — `GET /api/v1/_endpoints` lists every generated route
- **WebAssembly plugins** — pre/post hooks per table via `.wasm` files (wazero, zero-CGO)
- **PG Hook webhook** — fires an HTTP POST to an external URL on every mutation
- **CORS** — enabled by default for browser clients
- **Docker Compose** — one command boots Postgres + Redis + Dynago

---

## Quick Start

### Prerequisites

| Tool | Version | Purpose |
|------|---------|---------|
| [Go](https://go.dev/dl/) | 1.22+ | Build from source |
| [PostgreSQL](https://www.postgresql.org/) | 14+ | Database |
| [Docker](https://docs.docker.com/get-docker/) | 20+ | (Optional) Run everything containerized |
| [Redis](https://redis.io/) | 7+ | (Optional) External cache layer |

### Install Dependencies & Build

```bash
# Clone the repository
git clone https://github.com/dynago/dynago.git
cd dynago

# Download Go dependencies
go mod tidy

# Build the binary
go build -ldflags="-s -w" -o dynago ./cmd/dynago

# Or use Make
make build
```

### Configure Environment

Copy the example env file and edit it:

```bash
cp .env.example .env
```

| Variable | Default | Description |
|----------|---------|-------------|
| `DATABASE_URL` | `postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable` | PostgreSQL connection string |
| `REDIS_URL` | _(empty)_ | Redis URL. Leave empty for in-memory cache |
| `DYNAGO_PORT` | `8080` | HTTP server port |
| `DYNAGO_CACHE_TTL` | `5m` | Cache time-to-live (Go duration format) |
| `DYNAGO_PLUGIN_DIR` | `./plugins` | Directory containing `.wasm` plugin files |
| `DYNAGO_JWT_SECRET` | _(empty)_ | JWT signing secret. Leave empty to disable auth |
| `DYNAGO_PG_HOOK_URL` | _(empty)_ | Webhook URL called on every INSERT/UPDATE/DELETE |
| `DYNAGO_ENABLE_UI` | `false` | Enable interactive web UI dashboard at `/` and `/ui` |
| `DYNAGO_ENABLE_SQL` | `false` | Enable interactive SQL Query Runner in UI mode |
| `DYNAGO_ENABLE_DATA_VIEW` | `false` | Enable Data Explorer with dynamic filters & pagination |
| `DYNAGO_DATA_LIMIT` | `50` | Maximum & default row limit for Data View pagination |

### Start the Server

**Option A — Run directly:**
```bash
# Load env vars (Linux/Mac)
export $(cat .env | xargs)

# Or on Windows PowerShell
Get-Content .env | ForEach-Object { if ($_ -match '^([^#].+?)=(.*)$') { [System.Environment]::SetEnvironmentVariable($Matches[1], $Matches[2]) } }

# Start
./dynago
```

**Option B — Docker Compose (recommended):**
```bash
docker compose up --build
```

This boots PostgreSQL (with `wal_level=logical`), Redis, and Dynago. Access the API at `http://localhost:8080`.

**Option C — Make:**
```bash
make run        # build + run
make docker-up  # docker compose up
```

---

## API Reference

Once running, Dynago auto-generates these endpoints:

### System Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/healthz` | Health check |
| `GET` | `/api/v1/_endpoints` | List all generated endpoints |
| `GET` | `/api/v1/_schema` | List all tables with metadata |
| `GET` | `/api/v1/_schema/{table}` | Detailed schema for one table |
| `POST` | `/api/v1/_refresh` | Hot-reload schema from database |

### Dynamic Table Endpoints

For every table discovered in your database:

| Method | Path | Description |
|--------|------|-------------|
| `GET` | `/api/v1/{table}` | Select rows (with filters, ordering, pagination) |
| `POST` | `/api/v1/{table}` | Insert a row |
| `PATCH` | `/api/v1/{table}` | Update rows matching filters |
| `DELETE` | `/api/v1/{table}` | Delete rows matching filters |

### Query Parameters (GET)

| Parameter | Example | Description |
|-----------|---------|-------------|
| `select` | `?select=id,name,email` | Choose specific columns |
| `{column}` | `?status=eq.active` | Filter: `eq`, `neq`, `gt`, `gte`, `lt`, `lte`, `like`, `ilike` |
| `order` | `?order=created_at.desc` | Sort results |
| `limit` | `?limit=10` | Limit rows |
| `offset` | `?offset=20` | Pagination offset |
| Nested | `?select=id,name,orders(id,total)` | Embed related table data |

### Examples

```bash
# List all users
curl http://localhost:8080/api/v1/users

# Get user by ID
curl "http://localhost:8080/api/v1/users?id=eq.1"

# Select specific columns with nested orders
curl "http://localhost:8080/api/v1/users?select=id,name,orders(id,total,status)&id=eq.1"

# Filter, sort, paginate
curl "http://localhost:8080/api/v1/orders?status=eq.shipped&order=created_at.desc&limit=10"

# Insert a user
curl -X POST http://localhost:8080/api/v1/users \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice", "email": "alice@example.com"}'

# Update a user
curl -X PATCH "http://localhost:8080/api/v1/users?id=eq.1" \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice Updated"}'

# Delete a user
curl -X DELETE "http://localhost:8080/api/v1/users?id=eq.1"

# Refresh schema after DDL changes
curl -X POST http://localhost:8080/api/v1/_refresh

# List all endpoints
curl http://localhost:8080/api/v1/_endpoints

# View table schema
curl http://localhost:8080/api/v1/_schema/users
```

---

## Web UI Dashboard & Explorers

Dynago includes a built-in, zero-dependency, rich web dashboard accessible in your browser at `http://localhost:8080/` or `http://localhost:8080/ui`.

The dashboard and its capabilities are gated by three environment configuration flags:

### 1. Web UI Mode (`DYNAGO_ENABLE_UI=true`)
Enables the single-page application dashboard:
- **Endpoints Catalog**: Lists all auto-discovered table endpoints (`GET`, `POST`, `PATCH`, `DELETE`) and system routes (`/healthz`, `/_refresh`, `/_schema`).
- **Example JSON Payloads**: Auto-generates realistic example JSON for each table:
  - Example `GET` response JSON
  - Example `POST` insert request body
  - Example `PATCH` update request body
- **Schema Inspector**: View column types, primary keys, nullability, defaults, and foreign keys.
- **Interactive Playground**: Test requests and copy ready-to-run `curl` commands with 1 click.

### 2. SQL Query Runner in UI Mode (`DYNAGO_ENABLE_SQL=true`)
Enables an interactive in-browser SQL console:
- Run arbitrary `SELECT`, `INSERT`, `UPDATE`, `DELETE`, or schema queries.
- Instant execution metrics: runtime in milliseconds and row count.
- Results table view, formatted JSON toggle, and 1-click CSV export.
- When disabled, the tab displays an informative security lock notice.

### 3. Data Explorer with Dynamic Filters & Pagination (`DYNAGO_ENABLE_DATA_VIEW=true`)
Enables an interactive table data viewer:
- **Dynamic Multi-Filter Builder**: Add multiple simultaneous filters using all supported operators:
  - `=` (`eq`), `!=` (`neq`)
  - `>` (`gt`), `>=` (`gte`), `<` (`lt`), `<=` (`lte`)
  - `LIKE` (`like`), `ILIKE` (`ilike`), `IS NULL` (`is.null`)
- **Sorting**: Sort by any column ascending or descending.
- **Pagination & Row Limits**:
  - Configurable page size (10, 25, 50 rows per page).
  - Safety limit enforced by `DYNAGO_DATA_LIMIT` (default: `50`).
  - Total records count and page navigation controls.
  - Live REST API URL preview with 1-click copy.

---

## Caching & Deterministic Invalidation

Dynago caches every `GET` response using the request URI as the cache key (`dynago:select:/api/v1/{table}...`).

- **In-memory cache** is used by default (zero external dependencies).
- Set `REDIS_URL` to use Redis instead.
- Response headers include `X-Dynago-Cache: HIT` or `X-Dynago-Cache: MISS`.
- **Targeted Table Eviction:** When a table is mutated (`POST`, `PATCH`, `DELETE`), Dynago invalidates only cache entries belonging to that table (`dynago:select:/api/v1/{table}*`), leaving other cached tables intact.
- Call `POST /api/v1/_refresh` to flush the entire cache and re-introspect schemas.
- Cache TTL is controlled by `DYNAGO_CACHE_TTL` (default: `5m`).

---

## PostgreSQL Logical Replication & Change Data Capture (CDC)

Dynago includes a built-in PostgreSQL logical replication consumer in `internal/cdc` that taps directly into the database Write-Ahead Log (WAL).

### How CDC Works:
1. **Replication Slot:** Dynago creates and consumes a PostgreSQL logical replication slot (`dynago_cdc_slot`) using the standard `test_decoding` plugin.
2. **External Mutations Captured:** When rows are inserted, updated, or deleted directly in PostgreSQL (via `psql`, the Neon Console, background jobs, or third-party services), the CDC engine intercepts the raw WAL records.
3. **Automatic Cache Eviction:** Decoded mutations automatically invalidate the cached `GET` responses for that specific table in Redis and in-memory cache.
4. **Instant Fanout to SSE:** Decoded mutations are dispatched to all connected SSE clients (`/api/v1/stream/{table}`) without requiring modifications to go through Dynago's REST API.
5. **Graceful Fallback:** If the database user does not have replication privileges or `wal_level` is not `logical`, Dynago automatically operates in application-level change capture mode without crashing.

### CDC Configuration (.env)
```env
# Enable PostgreSQL logical replication CDC engine
DYNAGO_CDC_ENABLED=true

# Replication slot name
DYNAGO_CDC_SLOT=dynago_cdc_slot

# Decoding plugin (test_decoding or pgoutput)
DYNAGO_CDC_PLUGIN=test_decoding

# Interval between polling replication slot changes
DYNAGO_CDC_POLL_INTERVAL=250ms
```

---

## PG Hook (Webhook)

Set `DYNAGO_PG_HOOK_URL` to an external webhook URL. Dynago will fire an HTTP POST to it on every mutation:

```json
{
  "operation": "INSERT",
  "table": "users",
  "body": {"name": "Alice", "email": "alice@example.com"},
  "timestamp": "2026-10-11T02:30:00Z"
}
```

Use this for audit logging, event sourcing, triggering external workflows, or forwarding to message brokers.

---

## Real-Time Server-Sent Events (SSE) Streaming

Dynago includes a built-in event pub/sub broker that broadcasts database mutations in real time over HTTP Server-Sent Events (SSE).

### Endpoints

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/v1/stream` | Stream all mutation events across all tables | Admin (when auth enabled) |
| `GET` | `/api/v1/stream/{table}` | Stream mutation events for a specific table | Table read permission |

### 1. Subscribe via cURL
```bash
# Listen to live changes on the products table
curl -N http://localhost:8080/api/v1/stream/products
```

**Stream Output Example:**
```text
event: connected
data: {"connected":true,"table":"products","message":"subscribed to real-time mutation stream","timestamp":"2026-10-11T04:30:00Z"}

event: INSERT
data: {"table":"products","action":"INSERT","record":{"id":12,"title":"New Keyboard","price":99.99},"timestamp":"2026-10-11T04:30:15Z"}

event: UPDATE
data: {"table":"products","action":"UPDATE","record":{"id":12,"price":89.99},"timestamp":"2026-10-11T04:30:28Z"}
```

### 2. Subscribe in JavaScript / Browser
```javascript
const sse = new EventSource('http://localhost:8080/api/v1/stream/orders');

sse.addEventListener('INSERT', event => {
  const data = JSON.parse(event.data);
  console.log('New Order Placed:', data.record);
});

sse.addEventListener('UPDATE', event => {
  const data = JSON.parse(event.data);
  console.log('Order Status Changed:', data.record);
});
```

### 3. Web UI Live Event Monitor
In the Web UI dashboard (`http://localhost:8080/ui`):
- Click the **Live Stream** tab in the navigation bar.
- Choose whether to stream all tables or a specific table.
- Click **Connect Stream**.
- Perform mutations in another tab or in the SQL console, and watch real-time event cards arrive in the live feed with timestamps, status badges, and formatted JSON payloads!

---

## WebAssembly Plugins & Outbound HTTP Capabilities

Dynago embeds [Wazero](https://wazero.io) to execute sandboxed WebAssembly plugins in-process with nanosecond cold starts and zero external daemon overhead.

### How It Works

1. Place `.wasm` files in the `plugins/` directory (configured via `DYNAGO_PLUGIN_DIR`, default `./plugins`).
2. Follow the naming convention: `{tablename}_{hooktype}.wasm`.
3. If no plugins are present on startup, Dynago automatically seeds pre-compiled sample binaries (`customers_prehook.wasm`, `orders_prehook.wasm`) so you can test Wasm execution immediately out of the box.

### Hook Types

| Hook | Execution Point | Signature | Use Case |
|------|-----------------|-----------|----------|
| `prehook` | Before INSERT / UPDATE | `handle(ptr, len) -> u64` | Validate schema, normalize fields, call external APIs (fraud check, geocoding) |
| `posthook` | After SELECT / commit | `handle(ptr, len) -> u64` | Mask sensitive fields, enrich response, emit audit logs |
| `route` | `/api/v1/plugins/{name}` | `handle(ptr, len) -> u64` | Custom polyglot microservice endpoints |

### Host Capabilities (Outbound HTTP & Logging)

Dynago provides host functions to your Wasm guest modules under both the `"env"` and `"dynago"` namespaces:

| Host Function | Signature | Description |
|---------------|-----------|-------------|
| `host_log` | `(ptr: u32, len: u32) -> void` | Prints logs to Dynago stdout/stderr with `[wasm:log]` prefix |
| `http_get` | `(url_ptr: u32, url_len: u32) -> u64` | Outbound HTTP GET request (5s timeout); returns packed `(res_ptr << 32) \| res_len` |
| `http_post` | `(url_ptr, url_len, body_ptr, body_len) -> u64` | Outbound HTTP POST with JSON body; returns packed response buffer |
| `http_request` | `(method_ptr, method_len, url_ptr, url_len, body_ptr, body_len) -> u64` | Arbitrary outbound HTTP request (PUT, DELETE, PATCH, etc.) |

### ABI & Calling Convention

Every plugin exports:
1. `memory`: Linear memory buffer.
2. `alloc(size: u32) -> u32`: Allocates memory in the guest for Dynago to write incoming payloads or host HTTP responses.
3. `handle(ptr: u32, len: u32) -> u64`: Processes input JSON at `[ptr, ptr+len)`. Returns packed `uint64` where:
   - High 32 bits (`result >> 32`): Pointer to the output buffer.
   - Low 32 bits (`result & 0xFFFFFFFF`): Byte length of the output buffer.

### Included Sample Plugins

- **`plugins/customers_prehook/main.ts`** (AssemblyScript):
  Normalizes customer email addresses to lowercase and logs audit messages via `host_log`.
- **`plugins/orders_prehook/main.go`** (TinyGo):
  Detects high-value orders (`total > 500`), triggers an outbound HTTP fraud verification call via `http_post`, and enriches the order before database insertion.

See [`plugins/README.md`](file:///d:/GithubProject/DYNAGO/plugins/README.md) for full compilation steps across AssemblyScript, TinyGo, and Rust.

---

## Custom Endpoints & Functions (RPC)

Dynago provides a unified custom function execution engine that enables running custom business logic on dedicated HTTP endpoints:
- **Polyglot WebAssembly Functions**: Drop a `.wasm` file into `./plugins/` named `{function_name}_route.wasm` or `{function_name}_function.wasm`. It runs sandboxed in Wazero with nanosecond execution, memory isolation, and host outbound HTTP capabilities.
- **PostgreSQL Stored Procedures (RPC)**: Any user-defined function in PostgreSQL (e.g., `CREATE FUNCTION calculate_tax(...)`) is automatically discovered and callable without writing any boilerplate code.

### Endpoint Matrix

| Method | Endpoint | Description | Auth Required |
|--------|----------|-------------|---------------|
| `GET` | `/api/v1/functions` | List all discovered functions with public/private status | Public |
| `POST` | `/api/v1/functions/{name}` | Execute custom function `{name}` with JSON body | Public if function is public, otherwise Yes |
| `GET` | `/api/v1/functions/{name}` | Execute custom function `{name}` with query parameters | Public if function is public, otherwise Yes |
| `POST` | `/api/v1/rpc/{name}` | Alias for compatibility with PostgREST/Supabase RPC standards | Public if function is public, otherwise Yes |
| `PUT` | `/api/v1/_auth/functions/{name}` | Configure public or private visibility for function `{name}` | Admin only |

### Public vs Private Access Control
Functions can be explicitly configured as **Public** (callable by unauthenticated clients without a JWT token) or **Private** (requiring authentication):
- **Web UI Toggle:** In the **Functions** tab, Admins can toggle any function between **Public** and **Private** with one click.
- **REST API:**
  ```bash
  # Mark function as public (accessible without login)
  curl -X PUT http://localhost:8080/api/v1/_auth/functions/currency_convert \
    -H "Authorization: Bearer $adminToken" \
    -H "Content-Type: application/json" \
    -d '{"is_public": true}'
  ```
- **Environment Variable Default:** Set `DYNAGO_PUBLIC_FUNCTIONS=currency_convert,calculate_discount` in `.env`.
- By default, sample function `currency_convert` is initialized as **Public** so unauthenticated clients can verify execution immediately without logging in.

### 1. Wasm Custom Functions Example
Place a compiled module like `currency_convert_route.wasm` into `./plugins/`:
```bash
# Call public function anonymously (no token required!):
curl -X POST http://localhost:8080/api/v1/functions/currency_convert \
  -H "Content-Type: application/json" \
  -d '{"amount": 100, "from": "USD", "to": "EUR"}'
```

**Response:**
```json
{
  "source": "wasm",
  "function": "currency_convert",
  "is_public": true,
  "data": {
    "converted": 92.50,
    "rate": 0.925
  }
}
```

### 2. PostgreSQL Stored Procedure (RPC) Example
Create a standard function in PostgreSQL:
```sql
CREATE OR REPLACE FUNCTION calculate_discount(price NUMERIC, discount_percent NUMERIC)
RETURNS NUMERIC AS $$
BEGIN
    RETURN ROUND(price * (1 - discount_percent / 100), 2);
END;
$$ LANGUAGE plpgsql;
```

Invoke it via Dynago (with token if private, or anonymously if marked public):
```bash
curl -X POST http://localhost:8080/api/v1/rpc/calculate_discount \
  -H "Authorization: Bearer $token" \
  -H "Content-Type: application/json" \
  -d '{"price": 149.99, "discount_percent": 20}'
```

**Response:**
```json
{
  "source": "database",
  "function": "calculate_discount",
  "result": 119.99
}
```

---

### Manual: How to Add a Custom Endpoint with Custom Code

Dynago provides two approaches to adding custom endpoints. For the full reference guide with multi-language code samples, see [CUSTOM_ENDPOINTS.md](file:///d:/GithubProject/DYNAGO/CUSTOM_ENDPOINTS.md).

#### Approach A: Custom Code via WebAssembly (Wasm)
Write business logic in Go, Rust, or TypeScript and deploy it directly into Dynago without restarting the server:

1. **Write the code** (Go / TinyGo example):
   ```go
   package main

   import (
       "encoding/json"
       "unsafe"
   )

   var buffer []byte

   //export alloc
   func alloc(size uint32) uint32 {
       buffer = make([]byte, size)
       return uint32(uintptr(unsafe.Pointer(&buffer[0])))
   }

   //export handle
   func handle(ptr, length uint32) uint64 {
       inBytes := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)

       var req struct {
           Amount float64 `json:"amount"`
           Rate   float64 `json:"rate"`
       }
       _ = json.Unmarshal(inBytes, &req)

       outBytes, _ := json.Marshal(map[string]any{
           "converted": req.Amount * req.Rate,
           "status":    "success",
       })

       outPtr := alloc(uint32(len(outBytes)))
       copy(buffer, outBytes)
       return (uint64(outPtr) << 32) | uint64(len(outBytes))
   }

   func main() {}
   ```

2. **Compile to WebAssembly:**
   ```bash
   tinygo build -o ./plugins/my_calc_route.wasm -target=wasi main.go
   ```

3. **Call your new endpoint:**
   Dynago auto-registers any `plugins/{name}_route.wasm` file:
   ```bash
   curl -X POST http://localhost:8080/api/v1/functions/my_calc \
     -H "Content-Type: application/json" \
     -d '{"amount": 50, "rate": 1.25}'
   ```

#### Approach B: Custom Code via PostgreSQL Stored Procedure (RPC)
Write business logic directly in PostgreSQL and Dynago will discover and expose it over HTTP:

1. **Create the stored function in PostgreSQL:**
   ```sql
   CREATE OR REPLACE FUNCTION get_order_summary(cust_id INT)
   RETURNS JSON AS $$
   BEGIN
       RETURN (
           SELECT json_build_object(
               'customer_id', cust_id,
               'total_spent', COALESCE(SUM(total_amount), 0),
               'order_count', COUNT(id)
           )
           FROM orders
           WHERE customer_id = cust_id
       );
   END;
   $$ LANGUAGE plpgsql;
   ```

2. **Sync the schema:**
   ```bash
   curl -X POST http://localhost:8080/api/v1/_refresh
   ```
   *(Or click "Sync" in the top bar of the web dashboard).*

3. **Call your new endpoint:**
   ```bash
   curl -X POST http://localhost:8080/api/v1/rpc/get_order_summary \
     -H "Authorization: Bearer $token" \
     -H "Content-Type: application/json" \
     -d '{"cust_id": 1}'
   ```

4. **Example: Mutate DB via RPC (`update_order_status`):**
   ```sql
   CREATE OR REPLACE FUNCTION update_order_status(target_order_id INT, new_status VARCHAR)
   RETURNS JSON AS $$
   DECLARE v_order RECORD;
   BEGIN
       UPDATE orders SET status = new_status WHERE id = target_order_id
       RETURNING id, order_number, status, total_amount INTO v_order;
       RETURN json_build_object('success', true, 'order_id', v_order.id, 'status', v_order.status);
   END;
   $$ LANGUAGE plpgsql VOLATILE;
   ```
   Invoke via HTTP:
   ```bash
   curl -X POST http://localhost:8080/api/v1/rpc/update_order_status \
     -H "Authorization: Bearer $token" \
     -H "Content-Type: application/json" \
     -d '{"target_order_id": 1, "new_status": "completed"}'
   ```

5. **Example: Create and bring persistent receipts on orders (`create_order_receipt`):**
   Creates an itemized receipt entry in the `receipts` table and returns the complete receipt object:
   ```bash
   # Create receipt for order 1:
   curl -X POST http://localhost:8080/api/v1/rpc/create_order_receipt \
     -H "Authorization: Bearer $token" \
     -H "Content-Type: application/json" \
     -d '{"target_order_id": 1}'

   # Fetch receipt for order 1:
   curl -X POST http://localhost:8080/api/v1/rpc/get_order_receipt \
     -H "Authorization: Bearer $token" \
     -H "Content-Type: application/json" \
     -d '{"target_order_id": 1}'
   ```

#### Configure Endpoint Visibility (Public vs. Private)
To allow unauthenticated clients to access your custom endpoint:
```bash
curl -X PUT http://localhost:8080/api/v1/_auth/functions/my_calc \
  -H "Authorization: Bearer $adminToken" \
  -H "Content-Type: application/json" \
  -d '{"is_public": true}'
```
You can also toggle visibility with one click in the web dashboard under the **Functions** tab.

---

## JWT Authentication & Login

When `DYNAGO_JWT_SECRET` is set, Dynago enforces JWT Bearer token authentication on all data and mutation endpoints.

### Configuration
```env
# Secret key used to sign and verify HMAC-SHA256 JWTs
DYNAGO_JWT_SECRET=supersecretjwtkey12345

# Administrative credentials for obtaining tokens
DYNAGO_AUTH_USER=admin
DYNAGO_AUTH_PASS=dynago123

# Token lifetime (Go duration: 24h, 1h, 30m)
DYNAGO_AUTH_TOKEN_TTL=24h
```

### 1. Create Account (Self-Service Registration)
```bash
curl -X POST http://localhost:8080/api/v1/_auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"newuser","password":"mypassword"}'
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 86400,
  "expires_at": "2026-10-13T03:30:00Z",
  "user": {
    "username": "newuser",
    "role": "viewer"
  }
}
```

### 2. Authenticate & Obtain Token
```bash
curl -X POST http://localhost:8080/api/v1/_auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"dynago123"}'
```

**Response:**
```json
{
  "token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 86400,
  "expires_at": "2026-10-12T03:30:00Z",
  "user": {
    "username": "admin",
    "role": "admin"
  }
}
```

### 3. Verify Identity
```bash
curl http://localhost:8080/api/v1/_auth/me \
  -H "Authorization: Bearer <your-jwt-token>"
```

### 4. Query Protected Endpoints
```bash
curl -H "Authorization: Bearer <your-jwt-token>" http://localhost:8080/api/v1/products
```

### 5. Schema, Endpoint & Stream Privacy (Hiding Inaccessible Resources)
Dynago enforces strict resource visibility based on caller credentials:
- **Endpoint Catalog (`GET /api/v1/_endpoints`)**: Only endpoints for public tables or tables permitted for the caller's role are listed. Private table routes and administrative functions are omitted for unauthenticated callers.
- **Schema Introspection (`GET /api/v1/_schema`)**: Returns schemas exclusively for accessible tables. Direct inspection of private tables via `/api/v1/_schema/{table}` returns `401 Unauthorized` without a valid token.
- **Real-Time Stream**: Global SSE stream (`/api/v1/stream`) requires admin credentials. Per-table streams (`/api/v1/stream/{table}`) require read permissions on that specific table.
- **SQL Console (`POST /api/v1/_ui/sql`)**: Locked to administrator role only when authentication is enabled.

### 6. Web UI Dashboard Integration
When authentication is enabled:
- The top navigation bar displays an interactive **Auth Button** indicating session state (`admin (admin)` or `Sign In`).
- Clicking it opens an authentication modal with segmented tabs for **Sign In** and **Create Account**.
- Registering or signing in automatically updates the dashboard, refreshing table sidebars, schema views, function catalogs, and permissions without manual page reloads.
- The SQL Console is locked with an "Admin Only" badge for non-admin sessions.

---

## User Management & Table-Level Permissions (RBAC)

Dynago provides a built-in, database-backed Role-Based Access Control (RBAC) system that manages users and enforces fine-grained permissions per database table.

### Architecture

Two internal system tables are automatically created and maintained in PostgreSQL:
- `_dynago_users` — stores usernames, salted SHA-256 password hashes (`salt$hash`), roles (`admin`, `editor`, `viewer`), and active status.
- `_dynago_permissions` — stores granular table-level permission overrides (`can_read`, `can_insert`, `can_update`, `can_delete`) for each role and table.

*(Note: Internal `_dynago_*` metadata tables are automatically excluded from public REST CRUD introspection and schema listings).*

### Default Roles & Permission Hierarchy

| Role | Default Access | Table Overrides |
|------|----------------|-----------------|
| `admin` | Full read, insert, update, and delete access across all tables + access to user & permission APIs | Admin role always bypasses checks |
| `editor` | Can read, insert, and update on all tables; cannot delete | Can be customized per table |
| `viewer` | Read-only access across all tables | Can be granted or restricted per table |

### RBAC Management Endpoints

All management endpoints require an authenticated `admin` token in the `Authorization: Bearer <token>` header:

| Method | Path | Description | Access |
|--------|------|-------------|--------|
| `GET` | `/api/v1/_auth/users` | List all registered users | Admin |
| `POST` | `/api/v1/_auth/users` | Create a new user with role | Admin |
| `DELETE` | `/api/v1/_auth/users/{username}` | Delete a user account | Admin |
| `GET` | `/api/v1/_auth/permissions` | Get table permissions matrix | Authenticated |
| `PUT` | `/api/v1/_auth/permissions` | Set permissions for a role & table | Admin |

### Examples

#### 1. Create a New User
```bash
curl -X POST http://localhost:8080/api/v1/_auth/users \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"username":"john_doe","password":"secretpassword","role":"editor"}'
```

**Response:**
```json
{
  "id": 2,
  "username": "john_doe",
  "role": "editor",
  "message": "user created successfully"
}
```

#### 2. List Users
```bash
curl http://localhost:8080/api/v1/_auth/users \
  -H "Authorization: Bearer <admin-token>"
```

#### 3. Configure Table Permission
Set specific table permissions for a role (e.g., allow `viewer` role to insert into the `orders` table):

```bash
curl -X PUT http://localhost:8080/api/v1/_auth/permissions \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{
    "role": "viewer",
    "table_name": "orders",
    "can_read": true,
    "can_insert": true,
    "can_update": false,
    "can_delete": false
  }'
```

#### 4. Delete a User
```bash
curl -X DELETE http://localhost:8080/api/v1/_auth/users/john_doe \
  -H "Authorization: Bearer <admin-token>"
```

#### 5. Interactive UI Management Tab
In the Web UI dashboard (`http://localhost:8080/ui`), admins can access the **Users & Permissions** tab to:
- View all registered users with role badges and creation dates.
- Create new users directly with custom roles.
- Delete user accounts with single-click confirmation.
- Configure granular per-table permissions (`Read`, `Insert`, `Update`, `Delete`) using an interactive matrix with instant auto-save.
- Toggle tables between **Public** (accessible without logging in) and **Private** (requires JWT Bearer token).

---

## Public vs. Private Tables (Anonymous Access Control)

Dynago supports designating tables as **Public** or **Private**, providing fine-grained access control for unauthenticated visitors (such as public e-commerce catalogs, blogs, or pricing plans) while securing sensitive business data (such as orders, users, or internal analytics).

### Access Rules

| Table Type | Unauthenticated (Not Logged In) | Authenticated (Logged In) |
|------------|---------------------------------|---------------------------|
| **Public** | Allowed to query and read data (`GET /api/v1/{table}`) anonymously without a Bearer token. Mutation operations (`POST`, `PATCH`, `DELETE`) require logging in unless explicit permissions are assigned to the `anon` role. | Enforces user role permissions (`admin`, `editor`, `viewer`). |
| **Private** | Blocked with `HTTP 401 Unauthorized` (`"authentication required: table is private"`). | Enforces role permissions per `_dynago_permissions`. |

### Determining Table Visibility

Dynago determines whether a table is public using a three-tier check:
1. **Database Table Permissions**: The internal `_dynago_table_perms (table_name VARCHAR PRIMARY KEY, is_public BOOLEAN)` metadata table.
2. **Environment Variable Configuration**: Comma-separated list in `.env`:
   ```env
   DYNAGO_PUBLIC_TABLES=products,categories
   ```
3. **Naming Convention Fallback**: Tables named with `public_` prefix or `_public` suffix (e.g. `public_announcements`) are automatically treated as public.

### Dynamically Toggling Table Visibility

Admins can toggle any table between public and private at runtime via REST API or the Web UI:

```bash
# Mark table as PUBLIC (allows anonymous callers to read):
curl -X PUT http://localhost:8080/api/v1/_auth/tables/products \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"is_public": true}'

# Mark table as PRIVATE (requires valid JWT token):
curl -X PUT http://localhost:8080/api/v1/_auth/tables/products \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"is_public": false}'
```

Anonymous callers accessing a private table receive:
```json
{
  "error": "authentication required: table \"orders\" is private"
}
```
with `HTTP 401 Unauthorized`.


---

## Project Structure

```
dynago/
├── cmd/dynago/main.go              # Entry point
├── internal/
│   ├── config/config.go            # Env-based configuration
│   ├── catalog/introspect.go       # PG schema discovery & graph
│   ├── query/compiler.go           # HTTP → SQL compiler
│   ├── cache/
│   │   ├── memory.go               # In-memory TTL cache
│   │   └── redis.go                # Redis cache adapter
│   ├── server/
│   │   ├── router.go               # Dynamic routing & CRUD handlers
│   │   └── middleware.go           # CORS, logging, JWT
│   └── wasm/manager.go             # Wazero plugin loader
├── plugins/                        # .wasm plugin files
├── docker-compose.yml              # Postgres + Redis + Dynago
├── Dockerfile                      # Multi-stage build
├── Makefile                        # Build targets
├── .env.example                    # Config reference
├── go.mod
└── README.md
```

---

## Full Dependency Install Commands

```bash
# Install Go (if not installed) — https://go.dev/dl/
# Then:

# Clone and enter project
git clone https://github.com/dynago/dynago.git
cd dynago

# Install all Go dependencies
go mod tidy

# This pulls:
#   github.com/jackc/pgx/v5          — PostgreSQL driver & connection pool
#   github.com/redis/go-redis/v9     — Redis client
#   github.com/tetratelabs/wazero    — WebAssembly runtime (zero CGO)
#   github.com/golang-jwt/jwt/v5     — JWT parsing & validation

# Build
go build -ldflags="-s -w" -o dynago ./cmd/dynago

# Or with Docker (no Go required):
docker compose up --build
```

---

## License

MIT
