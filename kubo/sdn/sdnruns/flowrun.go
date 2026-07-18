package sdnruns

// flowrun.go — the OD run engine cut over to the baked $PIV flow. It drives each
// provider's transient in-memory $OEM set through a FlowPool over the baked OD
// runtime.wasm (feeder -> od.fit -> store), REPLACING the store-backed
// StoreEphemerisSource + ReactorFitter + Go buildOMM path:
//
//	provider.pull ──(modulert)──▶ $OEM STREAM ──splitOEMStream──▶ [][]byte
//	                                                     │
//	                             flowrt.RunOEMBatch(pool, records, sink) ──▶ $OMM/$OCM/$OBD stored
//
// Ephemeris is never stored (the records live only for the batch); only the fit
// RESULTS are persisted, by the flow's host store node. The baked runtime.wasm is
// produced at build time (Docker) and shipped as a node asset, so no flowcc
// toolchain is needed on the node.

import (
	"context"
	"fmt"

	"github.com/ipfs/kubo/sdn/flowrt"
)

// ProviderInvoker runs a data-source provider module's `pull` method and returns
// its raw output: the $OEM STREAM ([u32le count] then N × [u32le len][$OEM]) the
// module emits in memory. Backed by modulert in production; a stub in tests.
type ProviderInvoker interface {
	InvokePull(ctx context.Context, provider string) ([]byte, error)
}

// FlowRunEngine fits a provider's objects through the baked OD flow + FlowPool.
type FlowRunEngine struct {
	pool   *flowrt.FlowPool
	invoke ProviderInvoker
	sink   flowrt.StoreSink
	batch  flowrt.OEMBatchConfig
}

// odFlowFeederPluginID / odFlowStorePluginID name the deploy OD flow's host nodes;
// they MUST match the baked runtime.wasm asset (see flowrt TestBakeODRuntimeAsset).
const (
	odFlowFeederPluginID = "io.spacedatanetwork.object-feeder"
	odFlowStorePluginID  = "io.spacedatanetwork.store"
)

// NewFlowRunEngineForOD builds the engine with the standard OD-flow node config
// (feeder/store ids, "oem" port, produced-source lane), so callers need not import
// flowrt. poolSize<=0 selects NumCPU. sink persists results + collects $OMM rows.
func NewFlowRunEngineForOD(runtimeWasm []byte, poolSize int, invoke ProviderInvoker, sink *CollectingSink) (*FlowRunEngine, error) {
	return NewFlowRunEngine(runtimeWasm, poolSize, 2048, invoke, sink, flowrt.OEMBatchConfig{
		FeederPluginID: odFlowFeederPluginID,
		FeederPort:     "oem",
		StorePluginID:  odFlowStorePluginID,
		StoreSource:    DefaultProducedSource,
		Drain:          flowrt.DrainOptions{MaxIterations: 256},
	})
}

// NewFlowRunEngine builds the engine over a baked OD runtime.wasm. poolSize resident
// instances fit objects in parallel; maxMemoryPages bounds each instance's guest
// heap. invoke supplies provider $OEM streams; sink persists the fit results.
func NewFlowRunEngine(runtimeWasm []byte, poolSize int, maxMemoryPages uint32, invoke ProviderInvoker, sink flowrt.StoreSink, batch flowrt.OEMBatchConfig) (*FlowRunEngine, error) {
	if len(runtimeWasm) == 0 {
		return nil, fmt.Errorf("sdnruns: FlowRunEngine requires a baked OD runtime.wasm")
	}
	if invoke == nil {
		return nil, fmt.Errorf("sdnruns: FlowRunEngine requires a ProviderInvoker")
	}
	return &FlowRunEngine{
		pool:   flowrt.NewFlowPool(runtimeWasm, poolSize, maxMemoryPages),
		invoke: invoke,
		sink:   sink,
		batch:  batch,
	}, nil
}

// RunProvider invokes one provider, splits its in-memory $OEM stream, and fits every
// object through the pool (results persisted by the flow's store node). A provider
// that yields no objects is a no-op (empty result, no error).
func (e *FlowRunEngine) RunProvider(ctx context.Context, provider string) (*flowrt.OEMBatchResult, error) {
	stream, err := e.invoke.InvokePull(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: provider %q invoke: %w", provider, err)
	}
	res, err := flowrt.RunOEMStream(ctx, e.pool, stream, e.sink, e.batch)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: provider %q: %w", provider, err)
	}
	return res, nil
}
