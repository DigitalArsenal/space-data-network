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
// the in-memory $OEM stream (or, in Probe mode, a bare u32le object count). The
// module instance is closed after the call.
//
// The pull config JSON is built from opts: objectCap bounds the total objects,
// offset/count select a batch window (omitted when < 0), and probe asks for just
// the count. When opts is entirely default (cap<=0, offset<0, count<0, no probe)
// p.config is passed unchanged (nil => the module's own defaults).
func (p *ModulertProviderInvoker) InvokePull(ctx context.Context, provider string, opts PullOpts) ([]byte, error) {
	wasm, err := p.resolve(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve %q module: %w", provider, err)
	}
	mod, err := p.load(wasm)
	if err != nil {
		return nil, fmt.Errorf("load %q module: %w", provider, err)
	}
	defer func() { _ = mod.Close() }()
	config := buildPullConfig(p.config, opts)
	out, err := mod.InvokeMethodRaw(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("invoke %q pull: %w", provider, err)
	}
	return out, nil
}

// buildPullConfig renders a provider pull config from opts. Returns fallback
// unchanged when opts carries nothing to express.
func buildPullConfig(fallback []byte, opts PullOpts) []byte {
	if opts.ObjectCap <= 0 && opts.Offset < 0 && opts.Count < 0 && !opts.Probe {
		return fallback
	}
	parts := make([]string, 0, 4)
	if opts.ObjectCap > 0 {
		parts = append(parts, fmt.Sprintf(`"objectCap":%d`, opts.ObjectCap))
	}
	if opts.Probe {
		parts = append(parts, `"probe":true`)
	} else {
		if opts.Offset >= 0 {
			parts = append(parts, fmt.Sprintf(`"offset":%d`, opts.Offset))
		}
		if opts.Count >= 0 {
			parts = append(parts, fmt.Sprintf(`"count":%d`, opts.Count))
		}
	}
	out := "{"
	for i, s := range parts {
		if i > 0 {
			out += ","
		}
		out += s
	}
	out += "}"
	return []byte(out)
}
