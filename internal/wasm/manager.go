package wasm

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

// Plugin represents a loaded .wasm module.
type Plugin struct {
	Name     string
	Table    string // table this plugin hooks into, parsed from filename: "tablename_prehook.wasm"
	HookType string // "prehook", "posthook", "route"
	compiled wazero.CompiledModule
}

// Manager loads and executes Wasm plugins.
type Manager struct {
	runtime wazero.Runtime
	plugins map[string]*Plugin // key: "table:hooktype"
	mu      sync.RWMutex
}

// NewManager scans pluginDir for .wasm files and compiles them.
func NewManager(pluginDir string) (*Manager, error) {
	if _, err := os.Stat(pluginDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("plugin dir %q not found", pluginDir)
	}

	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	if err := RegisterHostFunctions(ctx, rt); err != nil {
		log.Printf("[wasm] register host functions warning: %v", err)
	}

	m := &Manager{
		runtime: rt,
		plugins: make(map[string]*Plugin),
	}

	_ = EnsureSamplePlugins(pluginDir)

	entries, err := os.ReadDir(pluginDir)
	if err != nil {
		return nil, err
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".wasm") {
			continue
		}

		wasmBytes, err := os.ReadFile(filepath.Join(pluginDir, entry.Name()))
		if err != nil {
			log.Printf("[wasm] skip %s: %v", entry.Name(), err)
			continue
		}

		compiled, err := rt.CompileModule(ctx, wasmBytes)
		if err != nil {
			log.Printf("[wasm] compile error %s: %v", entry.Name(), err)
			continue
		}

		// Parse filename: "orders_prehook.wasm" → orders:prehook
		// "currency_convert_route.wasm" → currency_convert:route
		base := strings.TrimSuffix(entry.Name(), ".wasm")
		var target, hookType string
		if strings.HasSuffix(base, "_route") {
			target = strings.TrimSuffix(base, "_route")
			hookType = "route"
		} else if strings.HasSuffix(base, "_function") {
			target = strings.TrimSuffix(base, "_function")
			hookType = "route"
		} else if strings.HasSuffix(base, "_prehook") {
			target = strings.TrimSuffix(base, "_prehook")
			hookType = "prehook"
		} else if strings.HasSuffix(base, "_posthook") {
			target = strings.TrimSuffix(base, "_posthook")
			hookType = "posthook"
		} else {
			parts := strings.SplitN(base, "_", 2)
			target = parts[0]
			hookType = "route"
			if len(parts) == 2 {
				hookType = parts[1]
			}
		}

		key := target + ":" + hookType
		m.plugins[key] = &Plugin{
			Name:     entry.Name(),
			Table:    target,
			HookType: hookType,
			compiled: compiled,
		}
		log.Printf("[wasm] loaded plugin %s → %s:%s", entry.Name(), target, hookType)
	}

	return m, nil
}

// HasHook checks if a hook exists for a table and hook type.
func (m *Manager) HasHook(table, hookType string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.plugins[table+":"+hookType]
	return ok
}

// HasFunction checks if a custom route/function plugin exists.
func (m *Manager) HasFunction(name string) bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.plugins[name+":route"]
	return ok
}

// ExecuteFunction runs a custom route Wasm function with input payload.
func (m *Manager) ExecuteFunction(ctx context.Context, name string, payload []byte) ([]byte, error) {
	return m.Execute(ctx, name, "route", payload)
}

// FunctionNames returns all loaded custom route function names.
func (m *Manager) FunctionNames() []string {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	var names []string
	for _, p := range m.plugins {
		if p.HookType == "route" {
			names = append(names, p.Table)
		}
	}
	sort.Strings(names)
	return names
}

// Execute runs a plugin's exported "handle" function with the given payload.
// Returns the (possibly mutated) payload.
func (m *Manager) Execute(ctx context.Context, table, hookType string, payload []byte) ([]byte, error) {
	if m == nil {
		return payload, nil
	}
	m.mu.RLock()
	plugin, ok := m.plugins[table+":"+hookType]
	m.mu.RUnlock()
	if !ok {
		return payload, nil // no hook = passthrough
	}

	modCfg := wazero.NewModuleConfig().WithStdout(os.Stdout).WithStderr(os.Stderr)
	mod, err := m.runtime.InstantiateModule(ctx, plugin.compiled, modCfg.WithName(""))
	if err != nil {
		return nil, fmt.Errorf("instantiate %s: %w", plugin.Name, err)
	}
	defer mod.Close(ctx)

	// Allocate memory in guest, write payload, call "handle"
	alloc := mod.ExportedFunction("alloc")
	handle := mod.ExportedFunction("handle")
	if alloc == nil || handle == nil {
		return payload, nil // module doesn't export expected functions, skip
	}

	size := uint64(len(payload))
	results, err := alloc.Call(ctx, size)
	if err != nil {
		return nil, fmt.Errorf("alloc in %s: %w", plugin.Name, err)
	}
	ptr := results[0]

	if !mod.Memory().Write(uint32(ptr), payload) {
		return nil, fmt.Errorf("memory write failed in %s", plugin.Name)
	}

	res, err := handle.Call(ctx, ptr, size)
	if err != nil {
		return nil, fmt.Errorf("handle in %s: %w", plugin.Name, err)
	}

	// Result is packed: upper 32 bits = ptr, lower 32 bits = len
	resPtr := uint32(res[0] >> 32)
	resLen := uint32(res[0] & 0xFFFFFFFF)

	out, ok := mod.Memory().Read(resPtr, resLen)
	if !ok {
		return payload, nil
	}

	return out, nil
}

// Close shuts down the wasm runtime.
func (m *Manager) Close() {
	if m != nil {
		m.runtime.Close(context.Background())
	}
}
