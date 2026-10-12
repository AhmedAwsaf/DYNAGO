package wasm

import (
	"log"
	"os"
	"path/filepath"
)

// MinimalValidWasm produces a valid WebAssembly 1.0 binary module that exports
// "memory", "alloc", and "handle", executable in Dynago's Wazero sandbox runtime.
func MinimalValidWasm() []byte {
	return []byte{
		// Magic header ('\0asm') & WebAssembly version 1
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,

		// Section 1: Type section (12 bytes: 1 byte count + 5 bytes type0 + 6 bytes type1)
		0x01, 0x0c, 0x02,
		0x60, 0x01, 0x7f, 0x01, 0x7f, // Type 0: func(i32) -> i32 (alloc)
		0x60, 0x02, 0x7f, 0x7f, 0x01, 0x7e, // Type 1: func(i32, i32) -> i64 (handle)

		// Section 3: Function section
		0x03, 0x03, 0x02,
		0x00, // func 0 has type 0
		0x01, // func 1 has type 1

		// Section 5: Memory section (1 memory, min 1 page = 64KB)
		0x05, 0x03, 0x01, 0x00, 0x01,

		// Section 7: Export section (3 exports: memory, alloc, handle)
		0x07, 0x1b, 0x03,
		0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00, // export memory 0
		0x05, 'a', 'l', 'l', 'o', 'c', 0x00, 0x00, // export func 0 as "alloc"
		0x06, 'h', 'a', 'n', 'd', 'l', 'e', 0x00, 0x01, // export func 1 as "handle"

		// Section 10: Code section (implementations of alloc & handle)
		0x0a, 0x14, 0x02,
		// Func 0: alloc(size: i32) -> i32 (returns fixed memory buffer at offset 1024)
		0x05, 0x00, 0x41, 0x80, 0x08, 0x0b,
		// Func 1: handle(ptr: i32, len: i32) -> i64 (returns packed (ptr << 32) | len)
		0x0c, 0x00, 0x20, 0x00, 0xad, 0x42, 0x20, 0x86, 0x20, 0x01, 0xad, 0x84, 0x0b,
	}
}

// EnsureSamplePlugins writes ready-to-run sample Wasm plugin binaries into pluginDir
// if no plugins currently exist, ensuring out-of-the-box Wasm functionality.
func EnsureSamplePlugins(pluginDir string) error {
	if err := os.MkdirAll(pluginDir, 0755); err != nil {
		return err
	}

	samples := []string{
		"customers_prehook.wasm",
		"orders_prehook.wasm",
		"currency_convert_route.wasm",
	}

	wasmBytes := MinimalValidWasm()
	for _, filename := range samples {
		path := filepath.Join(pluginDir, filename)
		data, err := os.ReadFile(path)
		// Overwrite if doesn't exist or has the legacy length byte (0x0b instead of 0x0c)
		if os.IsNotExist(err) || (err == nil && len(data) >= 10 && data[8] == 0x01 && data[9] == 0x0b) {
			if err := os.WriteFile(path, wasmBytes, 0644); err != nil {
				log.Printf("[wasm] error writing sample plugin %s: %v", filename, err)
			} else {
				log.Printf("[wasm] updated sample plugin with valid bytecode: %s", path)
			}
		}
	}

	return nil
}
