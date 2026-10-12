package wasm

import (
	"bytes"
	"context"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// RegisterHostFunctions exposes host capabilities (outbound HTTP, logging) to guest Wasm modules.
// Exposes to both "env" and "dynago" namespaces for compatibility with AssemblyScript, TinyGo, and Rust.
func RegisterHostFunctions(ctx context.Context, rt wazero.Runtime) error {
	registerModule := func(moduleName string) error {
		_, err := rt.NewHostModuleBuilder(moduleName).
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, ptr, len uint32) {
				msgBytes, ok := m.Memory().Read(ptr, len)
				if ok {
					log.Printf("[wasm:log] %s", string(msgBytes))
				}
			}).
			Export("host_log").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, urlPtr, urlLen uint32) uint64 {
				return executeHostHTTP(ctx, m, "GET", urlPtr, urlLen, 0, 0)
			}).
			Export("http_get").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
				return executeHostHTTP(ctx, m, "POST", urlPtr, urlLen, bodyPtr, bodyLen)
			}).
			Export("http_post").
			NewFunctionBuilder().
			WithFunc(func(ctx context.Context, m api.Module, methodPtr, methodLen, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
				methodBytes, ok := m.Memory().Read(methodPtr, methodLen)
				if !ok {
					return 0
				}
				return executeHostHTTP(ctx, m, string(methodBytes), urlPtr, urlLen, bodyPtr, bodyLen)
			}).
			Export("http_request").
			Instantiate(ctx)
		return err
	}

	if err := registerModule("env"); err != nil {
		return err
	}
	return registerModule("dynago")
}

// executeHostHTTP handles outbound HTTP calls initiated by a Wasm guest module.
// Returns a packed uint64 where upper 32 bits = guest memory pointer, lower 32 bits = length.
func executeHostHTTP(ctx context.Context, m api.Module, method string, urlPtr, urlLen, bodyPtr, bodyLen uint32) uint64 {
	urlBytes, ok := m.Memory().Read(urlPtr, urlLen)
	if !ok {
		log.Printf("[wasm:http] failed to read URL at offset %d", urlPtr)
		return 0
	}
	urlStr := string(urlBytes)

	var reqBody io.Reader
	if bodyLen > 0 {
		bodyBytes, ok := m.Memory().Read(bodyPtr, bodyLen)
		if ok {
			reqBody = bytes.NewReader(bodyBytes)
		}
	}

	httpCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(httpCtx, method, urlStr, reqBody)
	if err != nil {
		log.Printf("[wasm:http] request build error: %v", err)
		return 0
	}
	req.Header.Set("User-Agent", "Dynago-Wasm-Plugin/1.0")
	if bodyLen > 0 {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[wasm:http] request to %s failed: %v", urlStr, err)
		return 0
	}
	defer resp.Body.Close()

	respBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit
	if err != nil {
		log.Printf("[wasm:http] read response error: %v", err)
		return 0
	}

	// Allocate space in the guest module's memory to return the response payload
	allocFunc := m.ExportedFunction("alloc")
	if allocFunc == nil {
		log.Printf("[wasm:http] guest module missing 'alloc' function to receive response")
		return 0
	}

	resAlloc, err := allocFunc.Call(ctx, uint64(len(respBytes)))
	if err != nil || len(resAlloc) == 0 {
		log.Printf("[wasm:http] guest alloc call failed: %v", err)
		return 0
	}

	resPtr := uint32(resAlloc[0])
	if !m.Memory().Write(resPtr, respBytes) {
		log.Printf("[wasm:http] failed to write response into guest memory")
		return 0
	}

	return (uint64(resPtr) << 32) | uint64(len(respBytes))
}
