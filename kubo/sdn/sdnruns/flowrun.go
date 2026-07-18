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
//
// SCALE (full constellation): a large provider (Starlink ~10.8k objects) cannot be
// pulled in one wasm invocation — a whole-constellation $OEM stream blows the guest
// heap, and one serial fetch of every object takes hours. Such providers are driven
// as HOST-CONCURRENT BATCHES: the engine probes the provider for its object count,
// then fans many small [offset,count) pulls across a bounded worker pool. Each pull
// range-fetches only the fit window per object (provider side), so memory stays
// bounded per batch and the fetches run in parallel. See PullOpts / runBatched.

import (
	"context"
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/ipfs/kubo/sdn/flowrt"
)

// PullOpts controls one provider `pull` invocation.
//
// ObjectCap bounds the total objects the provider will consider (0 = the module's
// built-in default). Offset/Count select a batch window into the provider's object
// list (both < 0 => the whole span, i.e. legacy single-shot). Probe asks the
// provider to return ONLY its total object count (a bare u32le), so the host can
// schedule concurrent batches without fetching anything.
type PullOpts struct {
	ObjectCap int
	Offset    int
	Count     int
	Probe     bool
}

// ProviderInvoker runs a data-source provider module's `pull` method and returns
// its raw output: the $OEM STREAM ([u32le count] then N × [u32le len][$OEM]) the
// module emits in memory, or — in Probe mode — a bare u32le object count. Backed by
// modulert in production; a stub in tests.
type ProviderInvoker interface {
	InvokePull(ctx context.Context, provider string, opts PullOpts) ([]byte, error)
}

// FlowRunEngine fits a provider's objects through the baked OD flow + FlowPool.
type FlowRunEngine struct {
	pool   *flowrt.FlowPool
	invoke ProviderInvoker
	sink   flowrt.StoreSink
	batch  flowrt.OEMBatchConfig

	// batched providers are pulled as host-concurrent [offset,count) batches
	// (probe -> fan-out) rather than one single-shot invocation. Only providers
	// whose module understands the probe/offset/count pull config belong here.
	batched   map[string]bool
	batchSize int // objects per batch pull
	batchConc int // concurrent batch pulls in flight
	logf      func(string, ...interface{})
}

// odFlowFeederPluginID / odFlowStorePluginID name the deploy OD flow's host nodes;
// they MUST match the baked runtime.wasm asset (see flowrt TestBakeODRuntimeAsset).
const (
	odFlowFeederPluginID = "io.spacedatanetwork.object-feeder"
	odFlowStorePluginID  = "io.spacedatanetwork.store"
)

// Default batch tuning for large providers (Starlink). batchSize keeps each pull's
// in-guest $OEM stream small; batchConc sets the fetch parallelism (I/O-bound —
// api.starlink.com is unthrottled) while the pool bounds fit parallelism.
const (
	defaultBatchSize = 64
	defaultBatchConc = 12
)

// NewFlowRunEngineForOD builds the engine with the standard OD-flow node config
// (feeder/store ids, "oem" port, produced-source lane), so callers need not import
// flowrt. poolSize<=0 selects NumCPU. sink persists results + collects $OMM rows.
func NewFlowRunEngineForOD(runtimeWasm []byte, poolSize int, invoke ProviderInvoker, sink *CollectingSink) (*FlowRunEngine, error) {
	e, err := NewFlowRunEngine(runtimeWasm, poolSize, 2048, invoke, sink, flowrt.OEMBatchConfig{
		FeederPluginID: odFlowFeederPluginID,
		FeederPort:     "oem",
		StorePluginID:  odFlowStorePluginID,
		StoreSource:    DefaultProducedSource,
		Drain:          flowrt.DrainOptions{MaxIterations: 256},
	})
	if err != nil {
		return nil, err
	}
	// Providers large enough to require host-concurrent batching. Their modules
	// understand the probe/offset/count pull config (spacex-starlink-source).
	e.batched = map[string]bool{"spacex": true, "spacex-starlink": true, "starlink": true}
	e.batchSize = defaultBatchSize
	e.batchConc = defaultBatchConc
	return e, nil
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
		pool:      flowrt.NewFlowPool(runtimeWasm, poolSize, maxMemoryPages),
		invoke:    invoke,
		sink:      sink,
		batch:     batch,
		batchSize: defaultBatchSize,
		batchConc: defaultBatchConc,
	}, nil
}

// SetLog installs a progress logger (batch completion for large providers).
func (e *FlowRunEngine) SetLog(logf func(string, ...interface{})) { e.logf = logf }

// SetBatchTuning overrides the batch size / concurrency (operator-tunable). Values
// <= 0 leave the current setting.
func (e *FlowRunEngine) SetBatchTuning(size, concurrency int) {
	if size > 0 {
		e.batchSize = size
	}
	if concurrency > 0 {
		e.batchConc = concurrency
	}
}

func (e *FlowRunEngine) log(format string, args ...interface{}) {
	if e.logf != nil {
		e.logf(format, args...)
	}
}

// RunProvider invokes one provider and fits every object through the pool (results
// persisted by the flow's store node). A large provider is driven as host-concurrent
// batches (see runBatched); every other provider is a single in-memory pull. A
// provider that yields no objects is a no-op (empty result, no error).
func (e *FlowRunEngine) RunProvider(ctx context.Context, provider string, objectCap int) (*flowrt.OEMBatchResult, error) {
	if e.batched[provider] {
		return e.runBatched(ctx, provider, objectCap)
	}
	stream, err := e.invoke.InvokePull(ctx, provider, PullOpts{ObjectCap: objectCap, Offset: -1, Count: -1})
	if err != nil {
		return nil, fmt.Errorf("sdnruns: provider %q invoke: %w", provider, err)
	}
	res, err := flowrt.RunOEMStream(ctx, e.pool, stream, e.sink, e.batch)
	if err != nil {
		return nil, fmt.Errorf("sdnruns: provider %q: %w", provider, err)
	}
	return res, nil
}

// runBatched probes the provider for its object count, then fans concurrent
// [offset,count) batch pulls across a bounded worker pool, fitting each batch's
// objects through the shared FlowPool as it arrives. Memory stays bounded to
// batchConc × batchSize $OEMs in flight; fetches run in parallel. A batch that
// fails is logged and skipped (the run still records every other object).
func (e *FlowRunEngine) runBatched(ctx context.Context, provider string, objectCap int) (*flowrt.OEMBatchResult, error) {
	probe, err := e.invoke.InvokePull(ctx, provider, PullOpts{ObjectCap: objectCap, Probe: true})
	if err != nil {
		return nil, fmt.Errorf("sdnruns: provider %q probe: %w", provider, err)
	}
	if len(probe) < 4 {
		return nil, fmt.Errorf("sdnruns: provider %q probe returned %d bytes, want a u32le count", provider, len(probe))
	}
	total := int(binary.LittleEndian.Uint32(probe[:4]))
	if total <= 0 {
		return &flowrt.OEMBatchResult{}, nil
	}
	e.log("sdnruns: provider %q: %d objects, batching %d-wide x %d concurrent", provider, total, e.batchSize, e.batchConc)

	var (
		agg  flowrt.OEMBatchResult
		mu   sync.Mutex
		wg   sync.WaitGroup
		sem  = make(chan struct{}, e.batchConc)
		done int
	)
	for off := 0; off < total; off += e.batchSize {
		select {
		case <-ctx.Done():
			wg.Wait()
			return &agg, ctx.Err()
		case sem <- struct{}{}:
		}
		off := off
		cnt := e.batchSize
		if off+cnt > total {
			cnt = total - off
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			stream, err := e.invoke.InvokePull(ctx, provider, PullOpts{ObjectCap: objectCap, Offset: off, Count: cnt})
			if err != nil {
				e.log("sdnruns: provider %q batch @%d(%d) pull failed: %v", provider, off, cnt, err)
				return
			}
			res, err := flowrt.RunOEMStream(ctx, e.pool, stream, e.sink, e.batch)
			if err != nil {
				e.log("sdnruns: provider %q batch @%d(%d) fit failed: %v", provider, off, cnt, err)
				return
			}
			mu.Lock()
			agg.Objects += res.Objects
			agg.Fitted += res.Fitted
			done += cnt
			d := done
			mu.Unlock()
			if d%(e.batchSize*e.batchConc) < e.batchSize {
				e.log("sdnruns: provider %q progress: ~%d/%d objects processed (%d fitted)", provider, d, total, agg.Fitted)
			}
		}()
	}
	wg.Wait()
	return &agg, nil
}
