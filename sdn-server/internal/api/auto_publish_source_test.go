package api

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

func sourceLane() config.AutoPublishLane {
	return config.AutoPublishLane{Schema: "NCD", ProviderID: "provider", SourceName: "source", PublishScope: "source", MinInterval: 80 * time.Millisecond, MaxShardBytes: 4096}
}

func sourceIngest(batch string) IngestedBatch {
	return IngestedBatch{Schema: "NCD.fbs", ProviderID: "provider", SourceName: "source", BatchID: batch, Inserted: 1}
}

func TestAutoPublisherSourceCoalescesFinalSameAndDistinctBatchEvents(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{sourceLane()})
	service.waitForPublications(t, 1) // startup catches up without a new ingest
	for _, batch := range []string{"same", "same", "another"} {
		publisher.ObserveIngest(sourceIngest(batch))
	}
	select {
	case <-service.fired:
		t.Fatal("source published before its interval")
	case <-time.After(15 * time.Millisecond):
	}
	service.waitForPublications(t, 1) // no further ingest is needed to wake it
	select {
	case <-service.fired:
		t.Fatal("coalesced source published more than once")
	case <-time.After(100 * time.Millisecond):
	}
	for _, req := range service.snapshot() {
		if req.Schema != "NCD.fbs" || req.ProviderID != "provider" || req.SourceName != "source" || req.BatchID != "" || !req.FullCatalog || req.MaxShardBytes != 4096 {
			t.Fatalf("source publication changed its configured scope: %+v", req)
		}
	}
}

type sourceBlockingService struct {
	mu      sync.Mutex
	calls   []DatasetPublicationRequest
	entered chan DatasetPublicationRequest
	release chan struct{}
}

func (s *sourceBlockingService) PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	s.mu.Lock()
	s.calls = append(s.calls, req)
	first := len(s.calls) == 1
	s.mu.Unlock()
	s.entered <- req
	if first {
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return &DatasetPublicationResult{RecordCount: 1}, nil
}

func TestAutoPublisherSourceRetainsIngestDuringPublicationAndQueuePressure(t *testing.T) {
	service := &sourceBlockingService{entered: make(chan DatasetPublicationRequest, 128), release: make(chan struct{})}
	lanes := make([]config.AutoPublishLane, autoPublishQueueDepth+8)
	for i := range lanes {
		lanes[i] = sourceLane()
		lanes[i].SourceName = fmt.Sprintf("source-%d", i)
		lanes[i].MinInterval = time.Millisecond
	}
	publisher := startAutoPublisher(t, service, lanes)
	// Cleanup must release the service before Stop waits for it.
	t.Cleanup(func() {
		select {
		case <-service.release:
		default:
			close(service.release)
		}
	})
	var first DatasetPublicationRequest
	select {
	case first = <-service.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("startup publication did not start")
	}
	done := make(chan struct{})
	go func() {
		for _, lane := range lanes {
			batch := sourceIngest("raw-hash")
			batch.SourceName = lane.SourceName
			publisher.ObserveIngest(batch)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("source observation blocked behind publication")
	}
	close(service.release)
	seen := map[string]int{first.SourceName: 1}
	deadline := time.After(3 * time.Second)
	for count := 1; count < len(lanes)+1; count++ {
		select {
		case req := <-service.entered:
			seen[req.SourceName]++
		case <-deadline:
			t.Fatalf("pending sources lost: %v", seen)
		}
	}
	if len(seen) != len(lanes) || seen[first.SourceName] != 2 {
		t.Fatalf("source events during the in-flight export were lost: %v", seen)
	}
}

func TestAutoPublisherSourceRetriesPastBatchExhaustion(t *testing.T) {
	service := newRecordingPublicationService()
	service.err = errors.New("publication fixture unavailable")
	publisher := NewAutoPublisher(service, []config.AutoPublishLane{sourceLane()})
	publisher.retryBackoff = func(int) time.Duration { return time.Millisecond }
	publisher.Start(context.Background())
	t.Cleanup(publisher.Stop)
	service.waitForPublications(t, autoPublishMaxAttempts+2)
	service.mu.Lock()
	service.err = nil
	service.mu.Unlock()
	service.waitForPublications(t, 1)
	deadline := time.Now().Add(2 * time.Second)
	for publisher.Stats().Published == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if stats := publisher.Stats(); stats.Published != 1 || stats.Failed != 0 || stats.Retrying != 0 {
		t.Fatalf("pending source did not recover: %+v", stats)
	}
}

func TestAutoPublisherSourceRequiresExactConfiguredIdentity(t *testing.T) {
	for _, lane := range []config.AutoPublishLane{
		{Schema: "NCD", PublishScope: "source"},
		{Schema: "NCD", ProviderID: "provider", PublishScope: "source"},
		{Schema: "NCD", ProviderID: "provider", SourceName: "source", PublishScope: "unknown"},
	} {
		if NewAutoPublisher(newRecordingPublicationService(), []config.AutoPublishLane{lane}) != nil {
			t.Fatalf("invalid source scope admitted: %+v", lane)
		}
	}
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{sourceLane()})
	service.waitForPublications(t, 1)
	batch := sourceIngest("another")
	batch.ProviderID = "PROVIDER"
	publisher.ObserveIngest(batch)
	select {
	case <-service.fired:
		t.Fatal("source scope matched another exact store identity")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestAutoPublisherSourceSeedsCatchupForEachNewProcess(t *testing.T) {
	for i := 0; i < 2; i++ {
		service := newRecordingPublicationService()
		publisher := NewAutoPublisher(service, []config.AutoPublishLane{sourceLane()})
		publisher.Start(context.Background())
		service.waitForPublications(t, 1)
		publisher.Stop()
	}
}
