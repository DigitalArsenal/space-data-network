// Package wasmrt provides a shared WasmEdge WASM runtime wrapper for SDN.
// It abstracts VM lifecycle, WASI setup, memory management (malloc/free),
// host module registration, and function execution.
package wasmrt

import (
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/second-state/WasmEdge-go/wasmedge"
)

var (
	ErrNoModule    = errors.New("WASM module not loaded")
	ErrAllocFailed = errors.New("WASM memory allocation failed")
	ErrMemory      = errors.New("WASM memory access failed")
)

// HostFunc describes a single host function to register in a host module.
type HostFunc struct {
	Name    string
	Func    func(data interface{}, callframe *wasmedge.CallingFrame, params []interface{}) ([]interface{}, wasmedge.Result)
	Params  []*wasmedge.ValType
	Returns []*wasmedge.ValType
	Cost    uint
}

// Option configures a Module during creation.
type Option func(*config)

type config struct {
	maxMemoryPages    uint32
	enableWASI        bool
	wasiArgs          []string
	wasiEnvs          []string
	wasiPreopens      []string
	mallocName        string
	freeName          string
	secureDeallocName string
	hostModules       []hostModuleSpec
}

type hostModuleSpec struct {
	name  string
	funcs []HostFunc
}

func WithMaxMemoryPages(pages uint32) Option {
	return func(c *config) { c.maxMemoryPages = pages }
}

func WithWASI() Option {
	return func(c *config) { c.enableWASI = true }
}

func WithWASIArgs(args []string, envs []string, preopens []string) Option {
	return func(c *config) {
		c.enableWASI = true
		c.wasiArgs = args
		c.wasiEnvs = envs
		c.wasiPreopens = preopens
	}
}

func WithMallocName(name string) Option {
	return func(c *config) { c.mallocName = name }
}

func WithFreeName(name string) Option {
	return func(c *config) { c.freeName = name }
}

func WithSecureDealloc(name string) Option {
	return func(c *config) { c.secureDeallocName = name }
}

func WithHostModule(name string, funcs []HostFunc) Option {
	return func(c *config) {
		c.hostModules = append(c.hostModules, hostModuleSpec{name: name, funcs: funcs})
	}
}

// Module wraps a WasmEdge VM with convenience methods for memory management
// and function execution.
type Module struct {
	conf     *wasmedge.Configure
	vm       *wasmedge.VM
	hostMods []*wasmedge.Module
	mu       sync.Mutex

	mallocName        string
	freeName          string
	secureDeallocName string
}

// NewModule creates a WasmEdge VM, optionally enables WASI and host modules,
// loads the WASM bytes, and instantiates the module.
func NewModule(wasmBytes []byte, opts ...Option) (*Module, error) {
	cfg := &config{
		mallocName: "malloc",
		freeName:   "free",
	}
	for _, o := range opts {
		o(cfg)
	}

	conf := wasmedge.NewConfigure()
	conf.AddConfig(wasmedge.THREADS)
	conf.AddConfig(wasmedge.EXCEPTION_HANDLING)
	if cfg.enableWASI {
		conf.AddConfig(wasmedge.WASI)
	}
	if cfg.maxMemoryPages > 0 {
		conf.SetMaxMemoryPage(uint(cfg.maxMemoryPages))
	}

	vm := wasmedge.NewVMWithConfig(conf)
	if vm == nil {
		conf.Release()
		return nil, errors.New("failed to create WasmEdge VM")
	}

	m := &Module{
		conf:              conf,
		vm:                vm,
		mallocName:        cfg.mallocName,
		freeName:          cfg.freeName,
		secureDeallocName: cfg.secureDeallocName,
	}

	// Initialize WASI module if enabled.
	// When WASI is in the Configure, the VM auto-registers a WASI module.
	// We just need to call InitWasi() on it to set args/env/preopens.
	if cfg.enableWASI {
		wasiMod := vm.GetImportModule(wasmedge.WASI)
		if wasiMod == nil {
			m.Release()
			return nil, errors.New("failed to get WASI import module from VM")
		}
		args := cfg.wasiArgs
		if args == nil {
			args = []string{}
		}
		envs := cfg.wasiEnvs
		if envs == nil {
			envs = []string{}
		}
		preopens := cfg.wasiPreopens
		if preopens == nil {
			preopens = []string{}
		}
		wasiMod.InitWasi(args, envs, preopens)
	}

	// Register host modules
	for _, spec := range cfg.hostModules {
		hostMod := wasmedge.NewModule(spec.name)
		if hostMod == nil {
			m.Release()
			return nil, fmt.Errorf("failed to create host module %q", spec.name)
		}
		for _, hf := range spec.funcs {
			ft := wasmedge.NewFunctionType(hf.Params, hf.Returns)
			fn := wasmedge.NewFunction(ft, hf.Func, nil, hf.Cost)
			ft.Release()
			hostMod.AddFunction(hf.Name, fn)
		}
		err := vm.RegisterModule(hostMod)
		if err != nil {
			hostMod.Release()
			m.Release()
			return nil, fmt.Errorf("failed to register host module %q: %w", spec.name, err)
		}
		m.hostMods = append(m.hostMods, hostMod)
	}

	// Load, validate, and instantiate the WASM module
	if err := vm.LoadWasmBuffer(wasmBytes); err != nil {
		m.Release()
		return nil, fmt.Errorf("failed to load WASM: %w", err)
	}
	if err := vm.Validate(); err != nil {
		m.Release()
		return nil, fmt.Errorf("failed to validate WASM: %w", err)
	}
	if err := vm.Instantiate(); err != nil {
		m.Release()
		return nil, fmt.Errorf("failed to instantiate WASM: %w", err)
	}

	return m, nil
}

// NewModuleFromFile loads WASM from a file path.
func NewModuleFromFile(path string, opts ...Option) (*Module, error) {
	wasmBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read WASM file: %w", err)
	}
	return NewModule(wasmBytes, opts...)
}

// Execute calls a WASM exported function by name.
func (m *Module) Execute(name string, params ...interface{}) ([]interface{}, error) {
	return m.vm.Execute(name, params...)
}

// memory returns the default linear memory ("memory") from the active module.
func (m *Module) memory() (*wasmedge.Memory, error) {
	mod := m.vm.GetActiveModule()
	if mod == nil {
		return nil, ErrNoModule
	}
	mem := mod.FindMemory("memory")
	if mem == nil {
		return nil, fmt.Errorf("%w: no 'memory' export found", ErrMemory)
	}
	return mem, nil
}

// ReadMemory copies bytes from WASM linear memory at [offset, offset+length).
func (m *Module) ReadMemory(offset, length uint32) ([]byte, error) {
	mem, err := m.memory()
	if err != nil {
		return nil, err
	}
	data, err := mem.GetData(uint(offset), uint(length))
	if err != nil {
		return nil, fmt.Errorf("%w: GetData at %d len %d: %v", ErrMemory, offset, length, err)
	}
	// Copy out — the returned slice may point into WASM linear memory.
	result := make([]byte, length)
	copy(result, data)
	return result, nil
}

// WriteMemory writes data into WASM linear memory at offset.
func (m *Module) WriteMemory(offset uint32, data []byte) error {
	mem, err := m.memory()
	if err != nil {
		return err
	}
	if err := mem.SetData(data, uint(offset), uint(len(data))); err != nil {
		return fmt.Errorf("%w: SetData at %d len %d: %v", ErrMemory, offset, len(data), err)
	}
	return nil
}

// Allocate calls the module's malloc, writes data into the allocated region,
// and returns the pointer. Caller must Deallocate when done.
func (m *Module) Allocate(data []byte) (uint32, error) {
	ptr, err := m.AllocateSize(uint32(len(data)))
	if err != nil {
		return 0, err
	}
	if err := m.WriteMemory(ptr, data); err != nil {
		m.Deallocate(ptr)
		return 0, err
	}
	return ptr, nil
}

// AllocateSize calls the module's malloc for the given size, returning the pointer.
func (m *Module) AllocateSize(size uint32) (uint32, error) {
	results, err := m.vm.Execute(m.mallocName, int32(size))
	if err != nil {
		return 0, fmt.Errorf("malloc(%d) failed: %w", size, err)
	}
	ptr := toUint32(results[0])
	if ptr == 0 {
		return 0, fmt.Errorf("%w: malloc(%d) returned 0", ErrAllocFailed, size)
	}
	return ptr, nil
}

// Deallocate calls the module's free.
func (m *Module) Deallocate(ptr uint32) {
	m.vm.Execute(m.freeName, int32(ptr))
}

// SecureDeallocate wipes memory then frees. Falls back to plain Deallocate
// if no secure dealloc function was configured.
func (m *Module) SecureDeallocate(ptr, size uint32) {
	if m.secureDeallocName != "" {
		m.vm.Execute(m.secureDeallocName, int32(ptr), int32(size))
	} else {
		m.Deallocate(ptr)
	}
}

// AllocateString allocates a null-terminated C string in WASM memory.
func (m *Module) AllocateString(s string) (uint32, error) {
	data := append([]byte(s), 0)
	return m.Allocate(data)
}

// ReadCString reads a null-terminated string from WASM memory.
func (m *Module) ReadCString(ptr, maxLen uint32) (string, error) {
	data, err := m.ReadMemory(ptr, maxLen)
	if err != nil {
		return "", err
	}
	for i, b := range data {
		if b == 0 {
			return string(data[:i]), nil
		}
	}
	return string(data), nil
}

// Release frees all WasmEdge resources.
func (m *Module) Release() {
	if m.vm != nil {
		m.vm.Release()
		m.vm = nil
	}
	for _, hm := range m.hostMods {
		if hm != nil {
			hm.Release()
		}
	}
	m.hostMods = nil
	if m.conf != nil {
		m.conf.Release()
		m.conf = nil
	}
}

// Lock acquires the module mutex for thread-safe access.
func (m *Module) Lock() { m.mu.Lock() }

// Unlock releases the module mutex.
func (m *Module) Unlock() { m.mu.Unlock() }

// VM returns the underlying WasmEdge VM (for advanced use cases).
func (m *Module) VM() *wasmedge.VM { return m.vm }

// toUint32 converts a WasmEdge return value (interface{}) to uint32.
func toUint32(v interface{}) uint32 {
	switch val := v.(type) {
	case int32:
		return uint32(val)
	case uint32:
		return val
	case int64:
		return uint32(val)
	case uint64:
		return uint32(val)
	default:
		return 0
	}
}

// ToInt32 converts a WasmEdge return value to int32.
func ToInt32(v interface{}) int32 {
	switch val := v.(type) {
	case int32:
		return val
	case uint32:
		return int32(val)
	case int64:
		return int32(val)
	case uint64:
		return int32(val)
	default:
		return 0
	}
}
