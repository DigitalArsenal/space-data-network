package api

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestPairedPublicationRealLoopIdleBehindHead(t *testing.T) {
	service := newRecordingPublicationService()
	p := NewAutoPublisher(service, pairedRules())
	var reads atomic.Int64
	p.now = func() time.Time { reads.Add(1); return time.Now() }
	p.ObserveIngest(pairedObservation("head"))
	p.ObserveIngest(pairedObservation("later"))
	p.mu.Lock()
	for _, state := range p.pairedPending {
		if state.request.BatchID == "head" {
			state.due = time.Now().Add(time.Hour)
		}
	}
	p.mu.Unlock()
	p.Start(context.Background())
	defer p.Stop()
	time.Sleep(50 * time.Millisecond)
	before := reads.Load()
	time.Sleep(150 * time.Millisecond)
	after := reads.Load()
	if after-before > 10 {
		t.Fatalf("dispatcher spun behind blocked head: clock_reads=%d", after-before)
	}
	for _, req := range service.snapshot() {
		if !req.FullCatalog {
			t.Fatal("later batch passed head")
		}
	}
}
