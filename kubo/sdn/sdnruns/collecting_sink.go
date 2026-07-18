package sdnruns

// collecting_sink.go — the run-record bridge for the flow cut-over. The OD flow's
// host store node persists results through a flowrt.StoreSink; this wraps the node
// record store so it ALSO reports each produced $OMM back to the runner (NORAD /
// OBJECT_ID / EPOCH), which appends an ObjectResult per fit so the run record's
// object count (recompute() -> objects_done) reflects the constellation. Ephemeris
// ($OEM) is never seen here — only the persisted RESULTS.

import (
	"context"
	"sync"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OMM"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/kubo/sdn/flowrt"
)

// CollectingSink persists each fit-result record via the node record store and
// collects the produced $OMM object rows for the current run. Resettable per run;
// runs are serial (cron), so Reset/Collected need no cross-run coordination.
type CollectingSink struct {
	inner flowrt.StoreSink // *sdnstore.Store

	mu   sync.Mutex
	rows []ObjectResult
}

func NewCollectingSink(inner flowrt.StoreSink) *CollectingSink {
	return &CollectingSink{inner: inner}
}

// Store persists fb (the content-addressed, non-size-prefixed record the store
// node hands us) and, when it is an $OMM, records an object row for the run.
func (s *CollectingSink) Store(ctx context.Context, source, sdsType string, fb []byte) (cid.Cid, error) {
	c, err := s.inner.Store(ctx, source, sdsType, fb)
	if err == nil && sdsType == "OMM" {
		rec := OMM.GetRootAsOMM(fb, 0)
		s.mu.Lock()
		s.rows = append(s.rows, ObjectResult{
			Norad:     rec.NORAD_CAT_ID(),
			ObjectID:  string(rec.OBJECT_ID()),
			Epoch:     string(rec.EPOCH()),
			Converged: true,
			OMMCid:    c.String(),
		})
		s.mu.Unlock()
	}
	return c, err
}

// Reset clears the collected rows (call before each run).
func (s *CollectingSink) Reset() {
	s.mu.Lock()
	s.rows = s.rows[:0]
	s.mu.Unlock()
}

// Collected returns a copy of the object rows produced since the last Reset.
func (s *CollectingSink) Collected() []ObjectResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ObjectResult, len(s.rows))
	copy(out, s.rows)
	return out
}
