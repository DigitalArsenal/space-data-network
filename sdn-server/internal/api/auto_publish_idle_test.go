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

type blockedCheckpointService struct {
	*recordingPublicationService
	entered chan struct{}
}

func (s *blockedCheckpointService) LastSourcePublication(ctx context.Context, _ DatasetPublicationRequest) (time.Time, error) {
	close(s.entered)
	<-ctx.Done()
	return time.Time{}, ctx.Err()
}
func TestPairedPublicationCheckpointDoesNotHoldMutex(t *testing.T) {
	service := &blockedCheckpointService{newRecordingPublicationService(), make(chan struct{})}
	p := NewAutoPublisher(service, pairedRules())
	p.seedSourcePublicationsLocked()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { p.publishReadySource(ctx); close(done) }()
	<-service.entered
	observed := make(chan struct{})
	go func() { p.Stats(); close(observed) }()
	select {
	case <-observed:
	case <-time.After(time.Second):
		t.Error("checkpoint held publisher mutex")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("checkpoint ignored caller cancellation")
	}
}
