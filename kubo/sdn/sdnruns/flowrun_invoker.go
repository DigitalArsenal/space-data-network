package sdnruns

// flowrun_invoker.go — the production ProviderInvoker. It resolves a provider's
// installed data-source module, loads it with the node's capability registry
// (http, etc.), and invokes its command `pull` via the RAW plugin_invoke_stream
// path (modulert.InvokeMethodRaw) — providers return a raw $OEM STREAM, not a
// PIV-framed response. The load + resolve seams are injected so this package does
// not depend on the node's service bundle: the sdnruntime plugin supplies
// sdnservices.Services.LoadModule and a blockstore/installer-backed resolver.

import (
	"context"
	"fmt"

	"github.com/ipfs/kubo/sdn/modulert"
)

// ProviderModuleLoader loads a provider's WASM into a modulert.Module with the
// node's capability registry. *sdnservices.Services.LoadModule satisfies this.
type ProviderModuleLoader func(wasm []byte) (*modulert.Module, error)

// ProviderWasmResolver resolves a provider token (e.g. "spacex") to its installed
// data-source module's WASM bytes (via the installer + blockstore).
type ProviderWasmResolver func(ctx context.Context, provider string) ([]byte, error)

// ModulertProviderInvoker is the production ProviderInvoker.
type ModulertProviderInvoker struct {
	load    ProviderModuleLoader
	resolve ProviderWasmResolver
	config  []byte // optional raw JSON pull config (objectCap/manifestUrl); nil = module defaults
}

// NewModulertProviderInvoker wires the loader + resolver. config is an optional raw
// pull-config payload passed to every provider (nil uses each module's defaults).
func NewModulertProviderInvoker(load ProviderModuleLoader, resolve ProviderWasmResolver, config []byte) (*ModulertProviderInvoker, error) {
	if load == nil || resolve == nil {
		return nil, fmt.Errorf("sdnruns: ModulertProviderInvoker requires a loader and a resolver")
	}
	return &ModulertProviderInvoker{load: load, resolve: resolve, config: config}, nil
}

// InvokePull resolves + loads the provider module and runs its `pull`, returning
// the in-memory $OEM stream. The module instance is closed after the call.
func (p *ModulertProviderInvoker) InvokePull(ctx context.Context, provider string) ([]byte, error) {
	wasm, err := p.resolve(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve %q module: %w", provider, err)
	}
	mod, err := p.load(wasm)
	if err != nil {
		return nil, fmt.Errorf("load %q module: %w", provider, err)
	}
	defer func() { _ = mod.Close() }()
	out, err := mod.InvokeMethodRaw(ctx, p.config)
	if err != nil {
		return nil, fmt.Errorf("invoke %q pull: %w", provider, err)
	}
	return out, nil
}
