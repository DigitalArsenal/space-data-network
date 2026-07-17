package flowrt

// oem_batch.go — RunOEMBatch drives a whole provider's transient $OEM set through
// the baked OD flow (feeder -> od.fit -> store) across a FlowPool, concurrently.
// This is the reusable core the OD run engine calls after it splits a provider's
// $OEM stream: seed the feeder with the per-object records, then fan len(records)
// drains across the pool's resident instances, each fitting one object and letting
// the host store node persist its $OMM/$OCM/$OBD. Ephemeris is in-memory only (the
// records live only for the batch); nothing writes $OEM to the store.
//
// It is the extraction of the proven od_multiobject_test drive loop into a single
// entry point so the runner (sdn/sdnruns) does not re-implement pool orchestration.

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
)

// OEMBatchConfig names the flow's feeder + store nodes and the store lane. The
// method ids match the OD flow node methods ("emit" on the feeder source,
// "persist" on the store sink).
type OEMBatchConfig struct {
	FeederPluginID string       // host-model source node plugin id
	FeederPort     string       // its $OEM output port (e.g. "oem")
	StorePluginID  string       // host-model store sink node plugin id
	StoreSource    string       // provenance lane for persisted results (e.g. "supplemental-omm")
	TriggerIndex   uint32       // flow trigger to fire per object (usually 0)
	Drain          DrainOptions // per-drain bound
}

// OEMBatchResult summarizes a batch fit.
type OEMBatchResult struct {
	Objects int // records submitted
	Fitted  int // drains that completed without error
}

// RunOEMBatch fits every $OEM record in `records` through `pool` (feeder ->
// od.fit -> store), persisting results via `sink`. Safe to call repeatedly on the
// same pool (each call seeds a fresh feeder). The pool bounds concurrency; this
// fans one goroutine per record and blocks until all complete.
func RunOEMBatch(ctx context.Context, pool *FlowPool, records [][]byte, sink StoreSink, cfg OEMBatchConfig) (*OEMBatchResult, error) {
	if pool == nil {
		return nil, fmt.Errorf("flowrt: RunOEMBatch requires a non-nil FlowPool")
	}
	if cfg.FeederPluginID == "" || cfg.StorePluginID == "" {
		return nil, fmt.Errorf("flowrt: RunOEMBatch requires FeederPluginID and StorePluginID")
	}
	feed := NewObjectFeeder(records)
	handlers := HandlerMap{
		cfg.FeederPluginID + ":emit":   NewObjectFeederHandler(feed, cfg.FeederPort),
		cfg.StorePluginID + ":persist": NewStoreHandler(sink, cfg.StoreSource),
	}

	var fitted int64
	var wg sync.WaitGroup
	for range records {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := pool.Run(ctx, cfg.TriggerIndex, handlers, cfg.Drain); err == nil {
				atomic.AddInt64(&fitted, 1)
			}
		}()
	}
	wg.Wait()

	return &OEMBatchResult{Objects: len(records), Fitted: int(fitted)}, nil
}
