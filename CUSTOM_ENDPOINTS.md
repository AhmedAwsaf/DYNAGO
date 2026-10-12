# Custom Endpoints Manual: Adding Custom Code to Dynago

Dynago allows you to attach custom business logic to dedicated HTTP endpoints without modifying the core server code. You can implement custom endpoints using:

1. **WebAssembly (Wasm)**: Write custom code in **Go (TinyGo)**, **Rust**, or **TypeScript (AssemblyScript)**. Modules run sandboxed in-process with sub-millisecond execution and optional outbound HTTP host calls.
2. **PostgreSQL Stored Procedures (RPC)**: Write custom SQL or PL/pgSQL functions that are discovered automatically without code generation.

---

## 1. WebAssembly (Wasm) Custom Endpoints

### 1.1 Naming and Routing Convention

Any `.wasm` binary placed in the `plugins/` directory (configured via `DYNAGO_PLUGIN_DIR` in `.env`) matching the pattern `{name}_route.wasm` or `{name}_function.wasm` is loaded automatically at startup and registered under:

- `POST /api/v1/functions/{name}`
- `GET /api/v1/functions/{name}`

For example, `plugins/calculate_shipping_route.wasm` creates the endpoint `/api/v1/functions/calculate_shipping`.

---

### 1.2 Calling Convention & ABI

Every Wasm plugin must export:
- Linear memory (`memory`)
- `alloc(size uint32) uint32`: Allocates a memory buffer in the guest and returns its pointer.
- `handle(ptr uint32, len uint32) uint64`: Receives the input JSON payload and returns a packed `uint64` where:
  - Upper 32 bits (`result >> 32`): Pointer to output JSON bytes.
  - Lower 32 bits (`result & 0xFFFFFFFF`): Length of output JSON bytes.

Dynago host imports available to plugins (under module namespace `"env"`):
- `host_log(ptr, len uint32)`: Print debug logs to Dynago stdout.
- `http_get(urlPtr, urlLen uint32) uint64`: Outbound HTTP GET request.
- `http_post(urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64`: Outbound HTTP POST request.

---

### 1.3 Implementation in Go (TinyGo)

#### Step 1: Write the Go Code
Create a file, e.g. `plugins/shipping/main.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// Declare host logger
//go:wasmimport env host_log
func hostLog(ptr, len uint32)

func log(msg string) {
	bytes := []byte(msg)
	if len(bytes) > 0 {
		hostLog(uint32(uintptr(unsafe.Pointer(&bytes[0]))), uint32(len(bytes)))
	}
}

// Global buffer for guest-host communication
var buffer []byte

//export alloc
func alloc(size uint32) uint32 {
	buffer = make([]byte, size)
	return uint32(uintptr(unsafe.Pointer(&buffer[0])))
}

// Request and Response structures
type ShippingRequest struct {
	WeightKg float64 `json:"weight_kg"`
	Country  string  `json:"country"`
	Express  bool    `json:"express"`
}

type ShippingResponse struct {
	BaseCost float64 `json:"base_cost"`
	Tax      float64 `json:"tax"`
	Total    float64 `json:"total"`
	Currency string  `json:"currency"`
}

//export handle
func handle(ptr, length uint32) uint64 {
	inBytes := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	log(fmt.Sprintf("[calculate_shipping] Processing request (%d bytes)", length))

	var req ShippingRequest
	if err := json.Unmarshal(inBytes, &req); err != nil {
		errResp := []byte(`{"error": "invalid json input"}`)
		outPtr := alloc(uint32(len(errResp)))
		copy(buffer, errResp)
		return (uint64(outPtr) << 32) | uint64(len(errResp))
	}

	// Custom business logic
	ratePerKg := 5.0
	if req.Country != "US" && req.Country != "CA" {
		ratePerKg = 12.0
	}
	base := req.WeightKg * ratePerKg
	if req.Express {
		base += 25.0
	}
	tax := base * 0.08
	total := base + tax

	res := ShippingResponse{
		BaseCost: base,
		Tax:      tax,
		Total:    total,
		Currency: "USD",
	}

	outBytes, _ := json.Marshal(res)
	outPtr := alloc(uint32(len(outBytes)))
	copy(buffer, outBytes)

	return (uint64(outPtr) << 32) | uint64(len(outBytes))
}

func main() {}
```

#### Step 2: Compile with TinyGo
```bash
tinygo build -o ./plugins/calculate_shipping_route.wasm -target=wasi ./plugins/shipping/main.go
```

#### Step 3: Call the Endpoint
```bash
curl -X POST http://localhost:8080/api/v1/functions/calculate_shipping \
  -H "Content-Type: application/json" \
  -d '{"weight_kg": 4.5, "country": "US", "express": true}'
```

**Response:**
```json
{
  "source": "wasm",
  "function": "calculate_shipping",
  "is_public": false,
  "duration_ms": 0.35,
  "result": {
    "base_cost": 47.5,
    "tax": 3.8,
    "total": 51.3,
    "currency": "USD"
  }
}
```

---

### 1.4 Implementation in TypeScript (AssemblyScript)

#### Step 1: Write `main.ts`
```typescript
import { JSON } from "assemblyscript-json/assembly";

declare function host_log(ptr: usize, len: usize): void;

let buffer: Uint8Array = new Uint8Array(0);

export function alloc(size: usize): usize {
  buffer = new Uint8Array(size as i32);
  return buffer.dataStart;
}

export function handle(ptr: usize, len: usize): u64 {
  // Read payload from memory
  let inStr = String.UTF8.decodeUnsafe(ptr, len);

  // Return formatted JSON string
  let outStr = '{"status":"ok","processed":true}';
  let outBytes = Uint8Array.wrap(String.UTF8.encode(outStr));
  let outPtr = alloc(outBytes.length);
  memory.copy(outPtr, outBytes.dataStart, outBytes.length);

  return ((outPtr as u64) << 32) | (outBytes.length as u64);
}
```

#### Step 2: Compile with AssemblyScript
```bash
npx asc main.ts -o ./plugins/my_ts_endpoint_route.wasm --optimize --exportRuntime
```

---

### 1.5 Implementation in Rust

#### Step 1: Write `lib.rs`
```rust
#[link(wasm_import_module = "env")]
extern "C" {
    fn host_log(ptr: *const u8, len: usize);
}

#[no_mangle]
pub extern "C" fn alloc(size: usize) -> *mut u8 {
    let mut buf = Vec::with_capacity(size);
    let ptr = buf.as_mut_ptr();
    std::mem::forget(buf);
    ptr
}

#[no_mangle]
pub extern "C" fn handle(ptr: *const u8, len: usize) -> u64 {
    let input = unsafe { std::slice::from_raw_parts(ptr, len) };
    let input_str = std::str::from_utf8(input).unwrap_or("{}");

    let response = format!(r#"{{"echo_length":{},"status":"processed"}}"#, input_str.len());
    let bytes = response.into_bytes();
    let out_len = bytes.len();
    let out_ptr = alloc(out_len);

    unsafe {
        std::ptr::copy_nonoverlapping(bytes.as_ptr(), out_ptr, out_len);
    }

    ((out_ptr as u64) << 32) | (out_len as u64)
}
```

#### Step 2: Compile with Cargo
```bash
cargo build --target wasm32-wasi --release
cp target/wasm32-wasi/release/my_endpoint.wasm ./plugins/rust_service_route.wasm
```

---

## 2. PostgreSQL Stored Procedure (RPC) Custom Endpoints

Dynago automatically introspects the PostgreSQL `public` schema. Any function defined with `CREATE OR REPLACE FUNCTION` is immediately exposed over HTTP.

### 2.1 Scalar Function Example

#### Step 1: Define the Function in PostgreSQL
```sql
CREATE OR REPLACE FUNCTION calculate_discount(price NUMERIC, discount_percent NUMERIC)
RETURNS NUMERIC AS $$
BEGIN
    RETURN ROUND(price * (1 - discount_percent / 100), 2);
END;
$$ LANGUAGE plpgsql;
```

#### Step 2: Refresh Dynago Schema
If Dynago is already running, notify it to re-introspect:
```bash
curl -X POST http://localhost:8080/api/v1/_refresh
```
*(Or click the "Sync" button in the web dashboard header).*

#### Step 3: Invoke the Endpoint
Call via `POST /api/v1/rpc/{name}` or `GET /api/v1/rpc/{name}`:

```bash
# JSON Body:
curl -X POST http://localhost:8080/api/v1/rpc/calculate_discount \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"price": 199.99, "discount_percent": 15}'

# URL Query Parameters:
curl -H "Authorization: Bearer <token>" \
  "http://localhost:8080/api/v1/rpc/calculate_discount?price=199.99&discount_percent=15"
```

**Response:**
```json
{
  "source": "database",
  "function": "calculate_discount",
  "is_public": false,
  "duration_ms": 1.15,
  "result": 169.99
}
```

---

### 2.2 Table-Valued / Set-Returning Function Example

Dynago also handles functions returning rows or tables:

```sql
CREATE OR REPLACE FUNCTION get_top_customers(min_orders INT)
RETURNS TABLE (customer_id INT, full_name VARCHAR, order_count BIGINT) AS $$
BEGIN
    RETURN QUERY
    SELECT c.id, c.full_name, COUNT(o.id)
    FROM customers c
    JOIN orders o ON o.customer_id = c.id
    GROUP BY c.id, c.full_name
    HAVING COUNT(o.id) >= min_orders
    ORDER BY COUNT(o.id) DESC;
END;
$$ LANGUAGE plpgsql;
```

Invocation:
```bash
curl -X POST http://localhost:8080/api/v1/rpc/get_top_customers \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"min_orders": 2}'
```

**Response:**
```json
{
  "source": "database",
  "function": "get_top_customers",
  "is_public": false,
  "duration_ms": 2.45,
  "result": [
    {"customer_id": 1, "full_name": "Alice Chen", "order_count": 5},
    {"customer_id": 2, "full_name": "Bob Smith", "order_count": 3}
  ]
}
```

---

### 2.3 Database Mutation Function: Modifying DB Records via RPC

Stored procedures can execute `UPDATE`, `INSERT`, or `DELETE` statements inside the database transaction and return the result:

```sql
CREATE OR REPLACE FUNCTION update_order_status(target_order_id INT, new_status VARCHAR)
RETURNS JSON AS $$
DECLARE
    v_order RECORD;
BEGIN
    UPDATE orders
    SET status = new_status
    WHERE id = target_order_id
    RETURNING id, order_number, status, total_amount INTO v_order;

    IF NOT FOUND THEN
        RETURN json_build_object(
            'success', false,
            'error', 'Order not found',
            'order_id', target_order_id
        );
    END IF;

    RETURN json_build_object(
        'success', true,
        'message', 'Order status updated successfully in database',
        'order_id', v_order.id,
        'order_number', v_order.order_number,
        'new_status', v_order.status,
        'total_amount', v_order.total_amount,
        'updated_at', NOW()
    );
END;
$$ LANGUAGE plpgsql VOLATILE;
```

#### Calling the Mutation Function:
```bash
curl -X POST http://localhost:8080/api/v1/rpc/update_order_status \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"target_order_id": 1, "new_status": "processing"}'
```

**Response:**
```json
{
  "source": "database",
  "function": "update_order_status",
  "is_public": false,
  "duration_ms": 1.4,
  "result": {
    "success": true,
    "message": "Order status updated successfully in database",
    "order_id": 1,
    "order_number": "ORD-2026-001",
    "new_status": "processing",
    "total_amount": 209.49,
    "updated_at": "2026-10-12T13:22:00Z"
  }
}
```

---

### 2.4 Creating Persistent Receipts on Orders

This function inspects the order, joins customer and itemized product details, calculates taxes and line totals, records a receipt entry in the `receipts` table, and returns the complete receipt object:

```sql
CREATE TABLE IF NOT EXISTS receipts (
    id SERIAL PRIMARY KEY,
    receipt_number VARCHAR(100) UNIQUE NOT NULL,
    order_id INT NOT NULL,
    customer_name VARCHAR(255),
    customer_email VARCHAR(255),
    total_amount NUMERIC(10, 2) NOT NULL,
    items_count INT NOT NULL DEFAULT 1,
    receipt_data JSONB NOT NULL,
    created_at TIMESTAMPTZ DEFAULT NOW()
);

CREATE OR REPLACE FUNCTION create_order_receipt(target_order_id INT)
RETURNS JSON AS $$
DECLARE
    v_order RECORD;
    v_customer RECORD;
    v_items JSON;
    v_items_count INT;
    v_receipt_number VARCHAR(100);
    v_receipt JSON;
BEGIN
    -- 1. Fetch order
    SELECT id, customer_id, order_number, status, total_amount, placed_at
    INTO v_order
    FROM orders
    WHERE id = target_order_id;

    IF NOT FOUND THEN
        RETURN json_build_object(
            'success', false,
            'error', 'Order not found',
            'order_id', target_order_id
        );
    END IF;

    -- 2. Fetch customer details
    SELECT id, full_name, email
    INTO v_customer
    FROM customers
    WHERE id = v_order.customer_id;

    -- 3. Gather order items and calculate line totals
    SELECT 
        COALESCE(json_agg(json_build_object(
            'product_id', oi.product_id,
            'title', p.title,
            'sku', p.sku,
            'quantity', oi.quantity,
            'unit_price', oi.unit_price,
            'line_total', ROUND(oi.quantity * oi.unit_price, 2)
        )), '[]'::json),
        COALESCE(SUM(oi.quantity), 1)
    INTO v_items, v_items_count
    FROM order_items oi
    JOIN products p ON p.id = oi.product_id
    WHERE oi.order_id = target_order_id;

    v_receipt_number := 'REC-' || to_char(NOW(), 'YYYYMMDD') || '-' || LPAD(target_order_id::text, 4, '0');

    -- 4. Build full receipt JSON payload
    v_receipt := json_build_object(
        'receipt_number', v_receipt_number,
        'order_id', v_order.id,
        'order_number', v_order.order_number,
        'status', v_order.status,
        'customer', json_build_object(
            'customer_id', v_customer.id,
            'name', COALESCE(v_customer.full_name, 'Guest Customer'),
            'email', COALESCE(v_customer.email, 'unknown@example.com')
        ),
        'items', v_items,
        'total_items_count', v_items_count,
        'subtotal', v_order.total_amount,
        'tax', ROUND(v_order.total_amount * 0.08, 2),
        'total_amount', ROUND(v_order.total_amount * 1.08, 2),
        'issued_at', NOW()
    );

    -- 5. Insert or update receipts table
    INSERT INTO receipts (
        receipt_number, order_id, customer_name, customer_email, total_amount, items_count, receipt_data, created_at
    ) VALUES (
        v_receipt_number,
        v_order.id,
        COALESCE(v_customer.full_name, 'Guest Customer'),
        COALESCE(v_customer.email, 'unknown@example.com'),
        ROUND(v_order.total_amount * 1.08, 2),
        v_items_count,
        v_receipt::jsonb,
        NOW()
    )
    ON CONFLICT (receipt_number) DO UPDATE
    SET receipt_data = EXCLUDED.receipt_data,
        total_amount = EXCLUDED.total_amount;

    RETURN json_build_object(
        'success', true,
        'message', 'Receipt created and saved to database successfully',
        'receipt', v_receipt
    );
END;
$$ LANGUAGE plpgsql VOLATILE;
```

#### Calling the Receipt Generator:
```bash
curl -X POST http://localhost:8080/api/v1/rpc/create_order_receipt \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"target_order_id": 1}'
```

**Response:**
```json
{
  "source": "database",
  "function": "create_order_receipt",
  "is_public": false,
  "duration_ms": 2.8,
  "result": {
    "success": true,
    "message": "Receipt created and saved to database successfully",
    "receipt": {
      "receipt_number": "REC-20261012-0001",
      "order_id": 1,
      "order_number": "ORD-2026-001",
      "status": "processing",
      "customer": {
        "customer_id": 1,
        "name": "Alice Chen",
        "email": "alice@example.com"
      },
      "items": [
        {
          "product_id": 1,
          "title": "Pro Wireless Mechanical Keyboard",
          "sku": "ELEC-KB-01",
          "quantity": 1,
          "unit_price": 129.99,
          "line_total": 129.99
        },
        {
          "product_id": 2,
          "title": "Ergonomic Precision Mouse",
          "sku": "ELEC-MO-02",
          "quantity": 1,
          "unit_price": 79.50,
          "line_total": 79.50
        }
      ],
      "total_items_count": 2,
      "subtotal": 209.49,
      "tax": 16.76,
      "total_amount": 226.25,
      "issued_at": "2026-10-12T13:22:15Z"
    }
  }
}
```

#### Fetching the Order Receipt:
```sql
CREATE OR REPLACE FUNCTION get_order_receipt(target_order_id INT)
RETURNS JSON AS $$
DECLARE
    v_receipt JSON;
    v_gen_result JSON;
BEGIN
    IF target_order_id IS NULL THEN
        RETURN json_build_object(
            'success', false,
            'error', 'Order not found',
            'order_id', NULL
        );
    END IF;

    -- 1. Check if receipt already exists in receipts table
    SELECT receipt_data
    INTO v_receipt
    FROM receipts
    WHERE order_id = target_order_id
    ORDER BY id DESC
    LIMIT 1;

    -- 2. If not yet created, auto-generate it from orders table
    IF v_receipt IS NULL THEN
        v_gen_result := create_order_receipt(target_order_id);
        IF (v_gen_result->>'success')::boolean = true THEN
            v_receipt := v_gen_result->'receipt';
        ELSE
            RETURN v_gen_result;
        END IF;
    END IF;

    RETURN v_receipt;
END;
$$ LANGUAGE plpgsql VOLATILE;
```

```bash
curl -X POST http://localhost:8080/api/v1/rpc/get_order_receipt \
  -H "Authorization: Bearer <token>" \
  -H "Content-Type: application/json" \
  -d '{"target_order_id": 1}'
```

## 3. Public vs. Private Access Control

By default:
- Newly discovered PostgreSQL functions are **Private** (require a valid JWT Bearer token).
- Custom Wasm endpoints are **Private** unless configured otherwise.

### Making an Endpoint Public (Anonymous Access)

An administrator can mark an endpoint as public so it can be called without any authentication token:

```bash
curl -X PUT http://localhost:8080/api/v1/_auth/functions/calculate_shipping \
  -H "Authorization: Bearer <admin-token>" \
  -H "Content-Type: application/json" \
  -d '{"is_public": true}'
```

To set default public functions on startup, set `DYNAGO_PUBLIC_FUNCTIONS` in `.env`:
```env
DYNAGO_PUBLIC_FUNCTIONS=currency_convert,calculate_shipping
```

---

## 4. Testing Endpoints via Web UI

When `DYNAGO_ENABLE_UI=true` is set:
1. Open `http://localhost:8080/` in your browser.
2. Select the **Functions** tab in the sidebar.
3. The left pane lists all available Wasm and Database functions.
4. Select your function to inspect its argument types and endpoint URL.
5. Enter a test JSON payload into the input box and click **Execute Function**.
6. Execution latency, HTTP status, and output appear instantly in the response console.
7. Click the **Toggle Visibility** button to switch between Public and Private status.
