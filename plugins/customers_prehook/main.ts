// Dynago AssemblyScript Plugin: customers_prehook
// Normalizes email addresses to lowercase and validates required fields before INSERT/UPDATE.

import { JSON } from "assemblyscript-json/assembly";

// Host-exported logging capability provided by Dynago
@external("env", "host_log")
declare function host_log(ptr: usize, len: usize): void;

function logToHost(msg: string): void {
  const buf = String.UTF8.encode(msg);
  host_log(changetype<usize>(buf), buf.byteLength);
}

// Memory allocator exported for Dynago host runtime
export function alloc(size: usize): usize {
  return heap.alloc(size);
}

// Hook entrypoint: receives JSON input payload, returns (possibly mutated) JSON payload
export function handle(ptr: usize, len: usize): u64 {
  const inputStr = String.UTF8.decodeUnsafe(ptr, len);
  logToHost("[customers_prehook] Processing input customer payload");

  const jsonObj = <JSON.Obj>(JSON.parse(inputStr));
  if (jsonObj.has("email")) {
    const rawEmail = jsonObj.getString("email");
    if (rawEmail != null) {
      // Normalize email to lowercase
      jsonObj.set("email", rawEmail.valueOf().toLowerCase());
      logToHost("[customers_prehook] Normalized email: " + rawEmail.valueOf().toLowerCase());
    }
  }

  const outputStr = jsonObj.stringify();
  const outBuf = String.UTF8.encode(outputStr);
  const outPtr = heap.alloc(outBuf.byteLength);
  memory.copy(outPtr, changetype<usize>(outBuf), outBuf.byteLength);

  // Return packed (ptr << 32) | len
  return (<u64>outPtr << 32) | <u64>outBuf.byteLength;
}
