package main

import (
	"encoding/json"
	"fmt"
	"unsafe"
)

// Declare Dynago host functions imported from the host runtime environment.
// These are linked automatically by Wazero when instantiating the module.
//
//go:wasmimport env host_log
func hostLog(ptr, len uint32)

//go:wasmimport env http_post
func httpPost(urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64

//go:wasmimport env http_get
func httpGet(urlPtr, urlLen uint32) uint64

// log sends a debug message to Dynago's host stdout.
func log(msg string) {
	bytes := []byte(msg)
	if len(bytes) == 0 {
		return
	}
	hostLog(uint32(uintptr(unsafe.Pointer(&bytes[0]))), uint32(len(bytes)))
}

// post sends an outbound HTTP POST via Dynago's host HTTP client.
func post(url string, body []byte) []byte {
	urlBytes := []byte(url)
	var bodyPtr uint32
	if len(body) > 0 {
		bodyPtr = uint32(uintptr(unsafe.Pointer(&body[0])))
	}
	packed := httpPost(
		uint32(uintptr(unsafe.Pointer(&urlBytes[0]))),
		uint32(len(urlBytes)),
		bodyPtr,
		uint32(len(body)),
	)
	if packed == 0 {
		return nil
	}
	resPtr := uint32(packed >> 32)
	resLen := uint32(packed & 0xFFFFFFFF)
	return unsafe.Slice((*byte)(unsafe.Pointer(uintptr(resPtr))), resLen)
}

// Global memory buffer used for guest-host communication
var buffer []byte

//export alloc
func alloc(size uint32) uint32 {
	buffer = make([]byte, size)
	return uint32(uintptr(unsafe.Pointer(&buffer[0])))
}

//export handle
func handle(ptr, length uint32) uint64 {
	// Read input JSON payload from guest memory
	inBytes := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), length)
	log(fmt.Sprintf("[orders_prehook] Received input payload (%d bytes)", length))

	var order map[string]interface{}
	if err := json.Unmarshal(inBytes, &order); err != nil {
		log(fmt.Sprintf("[orders_prehook] Error parsing JSON: %v", err))
		// Return original input unchanged on parse error
		return (uint64(ptr) << 32) | uint64(length)
	}

	// Example: Check order total and enrich or validate via outbound HTTP call
	if total, ok := order["total"].(float64); ok && total > 500 {
		log(fmt.Sprintf("[orders_prehook] High-value order detected ($%.2f), calling fraud validation service...", total))

		reqPayload, _ := json.Marshal(map[string]interface{}{
			"order_total": total,
			"status":      "pending_verification",
		})
		resp := post("https://httpbin.org/post", reqPayload)
		if len(resp) > 0 {
			log("[orders_prehook] Received response from fraud check API")
			order["fraud_checked"] = true
			order["verification_source"] = "dynago_wasm_prehook"
		}
	}

	// Ensure default status if missing
	if _, ok := order["status"]; !ok {
		order["status"] = "processing"
	}

	outBytes, err := json.Marshal(order)
	if err != nil {
		log(fmt.Sprintf("[orders_prehook] Error marshaling output: %v", err))
		return (uint64(ptr) << 32) | uint64(length)
	}

	// Allocate space for output and copy
	outPtr := alloc(uint32(len(outBytes)))
	copy(buffer, outBytes)

	log(fmt.Sprintf("[orders_prehook] Output payload ready (%d bytes)", len(outBytes)))
	return (uint64(outPtr) << 32) | uint64(len(outBytes))
}

func main() {
	// TinyGo requires main() entry point when compiling to WebAssembly
}
