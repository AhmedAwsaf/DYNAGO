# Dynago WebAssembly (Wasm) Plugins

Dynago features an in-process, sandboxed WebAssembly execution engine powered by [Wazero](https://wazero.io). Plugins execute with nanosecond cold starts and zero external daemon overhead.

---

## 1. Directory Structure & Naming Convention

Plugins placed inside this directory are loaded automatically by Dynago on boot and reloaded dynamically.

File naming convention:
```
{tablename}_{hooktype}.wasm
```

### Supported Hook Types:
| Hook Type | Execution Point | Signature | Return Value |
|-----------|-----------------|-----------|--------------|
| `prehook` | Before `INSERT` or `UPDATE` | `handle(ptr, len) -> u64` | Mutated or validated JSON payload |
| `posthook`| After database commit | `handle(ptr, len) -> u64` | Status / audit confirmation |
| `route`   | Custom endpoint: `/api/v1/plugins/{name}` | `handle(ptr, len) -> u64` | Arbitrary HTTP response JSON |

---

## 2. ABI & Calling Convention

All Dynago plugins must export two WebAssembly functions and memory:

### Exported by Guest Plugin:
1. **`memory`**: Standard WebAssembly linear memory.
2. **`alloc(size: u32) -> u32`**:
   Allocates a buffer of `size` bytes in the guest's linear memory and returns the starting pointer. Used by Dynago host to write payloads and HTTP responses.
3. **`handle(ptr: u32, len: u32) -> u64`**:
   The hook handler. Receives the input JSON payload at `[ptr, ptr+len)`.
   Returns a packed `uint64` where:
   - High 32 bits (`result >> 32`): Pointer to the output byte buffer in guest memory.
   - Low 32 bits (`result & 0xFFFFFFFF`): Length in bytes of the output buffer.

---

## 3. Host Capabilities (Host Functions)

Dynago exposes host functions to the guest module under both `"env"` and `"dynago"` module namespaces:

### Available Host Functions:

```c
// Send a log message to Dynago's host stdout/stderr
void host_log(uint32_t ptr, uint32_t len);

// Perform an outbound HTTP GET request (5s timeout)
// Returns packed u64: (res_ptr << 32) | res_len
uint64_t http_get(uint32_t url_ptr, uint32_t url_len);

// Perform an outbound HTTP POST request with JSON payload (5s timeout)
// Returns packed u64: (res_ptr << 32) | res_len
uint64_t http_post(uint32_t url_ptr, uint32_t url_len, uint32_t body_ptr, uint32_t body_len);

// Perform an arbitrary outbound HTTP request (GET, POST, PUT, DELETE, PATCH)
// Returns packed u64: (res_ptr << 32) | res_len
uint64_t http_request(
    uint32_t method_ptr, uint32_t method_len,
    uint32_t url_ptr, uint32_t url_len,
    uint32_t body_ptr, uint32_t body_len
);
```

---

## 4. Sample Plugins Included

- **`customers_prehook/`** (AssemblyScript / TypeScript):
  Normalizes customer email addresses to lowercase and validates required schema fields.
- **`orders_prehook/`** (TinyGo / Go):
  Intercepts high-value orders (`total > 500`), performs an outbound HTTP fraud check via `http_post`, and tags verified orders before insertion.

---

## 5. Compiling Plugins

### Option A: AssemblyScript (TypeScript)
```bash
cd plugins/customers_prehook
npm install
npx asc main.ts -o ../customers_prehook.wasm --optimize --exportRuntime
```

### Option B: TinyGo
```bash
cd plugins/orders_prehook
tinygo build -o ../orders_prehook.wasm -target=wasi main.go
```

### Option C: Rust
```rust
// Cargo.toml: crate-type = ["cdylib"]
#[link(wasm_import_module = "env")]
extern "C" {
    fn host_log(ptr: *const u8, len: usize);
    fn http_post(url_ptr: *const u8, url_len: usize, body_ptr: *const u8, body_len: usize) -> u64;
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
    // Process input, call host functions, return packed pointer and length
    ((ptr as u64) << 32) | (len as u64)
}
```
Compile with Cargo:
```bash
cargo build --target wasm32-wasi --release
cp target/wasm32-wasi/release/my_plugin.wasm ../orders_prehook.wasm
```

---

## 6. Out-of-the-Box Zero-Dependency Seeding

When Dynago starts with `WASM_ENABLED=true`, if no `.wasm` files exist in `plugins/`, Dynago automatically generates pre-compiled valid WebAssembly 1.0 binaries (`customers_prehook.wasm` and `orders_prehook.wasm`). This allows testing and running the Wasm pipeline immediately without requiring local toolchains (TinyGo, Rust, or Node.js).
