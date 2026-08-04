package api

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
)

// recordingPublicationService captures every publication the trigger requests.
type recordingPublicationService struct {
	mu       sync.Mutex
	requests []DatasetPublicationRequest
	err      error
	fired    chan struct{}
}

func newRecordingPublicationService() *recordingPublicationService {
	return &recordingPublicationService{fired: make(chan struct{}, 16)}
}

func (s *recordingPublicationService) PublishDatasetUpdate(_ context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req)
	err := s.err
	s.mu.Unlock()
	s.fired <- struct{}{}
	if err != nil {
		return nil, err
	}
	return &DatasetPublicationResult{Schema: req.Schema, RecordCount: 1, ManifestCID: "bafytest"}, nil
}

func (s *recordingPublicationService) snapshot() []DatasetPublicationRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DatasetPublicationRequest, len(s.requests))
	copy(out, s.requests)
	return out
}

func (s *recordingPublicationService) waitForPublications(t *testing.T, n int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for i := 0; i < n; i++ {
		select {
		case <-s.fired:
		case <-deadline:
			t.Fatalf("timed out waiting for publication %d of %d (got %d)", i+1, n, len(s.snapshot()))
		}
	}
}

func startAutoPublisher(t *testing.T, service DatasetPublicationService, lanes []config.AutoPublishLane) *AutoPublisher {
	t.Helper()
	publisher := NewAutoPublisher(service, lanes)
	if publisher == nil {
		t.Fatal("NewAutoPublisher returned nil for a configured lane")
	}
	publisher.Start(context.Background())
	t.Cleanup(publisher.Stop)
	return publisher
}

// THE DEFECT (sdn-rfb-publish-to-consumer-node): host-02 ingested 5,289 $RFB
// records and host-01 never saw one, because nothing fired a publication for
// an ingest lane whose module carries no publish node.

func TestAutoPublisherPublishesAConfiguredLane(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{
		{Schema: "RFB.fbs", ProviderID: "space-data-network-02", SourceName: "satnogs-db"},
	})

	publisher.ObserveIngest(IngestedBatch{
		Schema:     "RFB.fbs",
		ProviderID: "space-data-network-02",
		SourceName: "satnogs-db",
		BatchID:    "e6fad530",
		Inserted:   5289,
	})

	service.waitForPublications(t, 1)
	got := service.snapshot()
	if len(got) != 1 {
		t.Fatalf("publications = %d, want 1", len(got))
	}
	req := got[0]
	if req.Schema != "RFB.fbs" || req.ProviderID != "space-data-network-02" ||
		req.SourceName != "satnogs-db" || req.BatchID != "e6fad530" {
		t.Fatalf("publication request = %+v, want the observed RFB batch identity", req)
	}
	// The trigger names a WINDOW, never a policy: no licence override, no
	// full-catalog flag, no announcement-schema list invented by the host.
	if req.FullCatalog || req.AnnounceExisting || req.Limit != 0 ||
		req.ChunkSize != 0 || req.DatasetID != "" || len(req.AnnouncementSchemas) != 0 {
		t.Fatalf("publication request carries host-invented policy: %+v", req)
	}
}

// The lane may be written as the standard code; the request always carries the
// canonical schema file name the publication service expects.
func TestAutoPublisherNormalizesTheSchema(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{{Schema: "rfb"}})

	publisher.ObserveIngest(IngestedBatch{
		Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b", Inserted: 1,
	})

	service.waitForPublications(t, 1)
	if got := service.snapshot()[0].Schema; got != "RFB.fbs" {
		t.Fatalf("schema = %q, want RFB.fbs", got)
	}
}

// FAIL-CLOSED: no configuration is not permission to republish. This matters
// most for the share-alike source that motivated the lane.
func TestAutoPublisherIsNilWithoutConfiguredLanes(t *testing.T) {
	if publisher := NewAutoPublisher(newRecordingPublicationService(), nil); publisher != nil {
		t.Fatal("NewAutoPublisher must return nil when no lane is configured")
	}
	// A lane with no schema would match everything; it is dropped, not honoured.
	if publisher := NewAutoPublisher(newRecordingPublicationService(), []config.AutoPublishLane{{ProviderID: "p"}}); publisher != nil {
		t.Fatal("a schema-less lane must not arm the publisher")
	}
	// The nil publisher must stay safe to drive.
	var publisher *AutoPublisher
	publisher.Start(context.Background())
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", Inserted: 1})
	publisher.Stop()
}

func TestAutoPublisherIgnoresUnconfiguredLanes(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{
		{Schema: "RFB.fbs", ProviderID: "space-data-network-02", SourceName: "satnogs-db"},
	})

	// Wrong schema, wrong provider, wrong source, and nothing inserted.
	publisher.ObserveIngest(IngestedBatch{Schema: "OMM.fbs", ProviderID: "space-data-network-02", SourceName: "satnogs-db", BatchID: "b1", Inserted: 10})
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "someone-else", SourceName: "satnogs-db", BatchID: "b2", Inserted: 10})
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "space-data-network-02", SourceName: "other-source", BatchID: "b3", Inserted: 10})
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "space-data-network-02", SourceName: "satnogs-db", BatchID: "b4", Inserted: 0})
	// Incomplete provenance can never name a publication window.
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "space-data-network-02", SourceName: "satnogs-db", Inserted: 10})

	select {
	case <-service.fired:
		t.Fatalf("published an unconfigured lane: %+v", service.snapshot())
	case <-time.After(150 * time.Millisecond):
	}
}

// An empty provider/source in the lane is a wildcard for that field only.
func TestAutoPublisherLaneWildcardsMatchAnyProviderAndSource(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{{Schema: "RFB.fbs"}})

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "anyone", SourceName: "any-source", BatchID: "b1", Inserted: 1})

	service.waitForPublications(t, 1)
	if got := service.snapshot()[0].ProviderID; got != "anyone" {
		t.Fatalf("providerId = %q, want the observed provider", got)
	}
}

// Re-ingesting the SAME batch id (the debounce-replay shape) must not
// republish it.
func TestAutoPublisherDoesNotRepublishTheSameBatch(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{{Schema: "RFB.fbs"}})

	batch := IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "same", Inserted: 1}
	publisher.ObserveIngest(batch)
	service.waitForPublications(t, 1)
	publisher.ObserveIngest(batch)

	select {
	case <-service.fired:
		t.Fatal("republished an already-published batch")
	case <-time.After(150 * time.Millisecond):
	}
}

// A publication exports, pins and announces a whole shard: an aggressive ingest
// timer must not become a publication storm.
func TestAutoPublisherRateLimitsALane(t *testing.T) {
	service := newRecordingPublicationService()
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{
		{Schema: "RFB.fbs", MinInterval: time.Hour},
	})

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b1", Inserted: 1})
	service.waitForPublications(t, 1)
	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b2", Inserted: 1})

	select {
	case <-service.fired:
		t.Fatal("published a second batch inside the lane interval")
	case <-time.After(150 * time.Millisecond):
	}

	// Once the interval has passed, the next batch publishes.
	publisher.mu.Lock()
	for key := range publisher.lastPublished {
		publisher.lastPublished[key] = time.Now().UTC().Add(-2 * time.Hour)
	}
	publisher.mu.Unlock()

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b3", Inserted: 1})
	service.waitForPublications(t, 1)
	if got := service.snapshot()[1].BatchID; got != "b3" {
		t.Fatalf("second publication batch = %q, want b3", got)
	}
}

// THE DEADLOCK LESSON: the in-flow publish trigger blocked the GP flow for 100
// minutes with zero store writes. ObserveIngest runs on the INGESTING
// goroutine, so it must return immediately even while a publication is stuck.
func TestAutoPublisherNeverBlocksTheIngestingGoroutine(t *testing.T) {
	release := make(chan struct{})
	service := &blockingPublicationService{release: release, entered: make(chan struct{}, 1)}
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{{Schema: "RFB.fbs"}})
	defer close(release)

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "stuck", Inserted: 1})
	<-service.entered // the worker is now wedged inside PublishDatasetUpdate

	done := make(chan struct{})
	go func() {
		for i := 0; i < 64; i++ {
			publisher.ObserveIngest(IngestedBatch{
				Schema: "RFB.fbs", ProviderID: "p", SourceName: "s",
				BatchID: string(rune('a'+i%26)) + "batch", Inserted: 1,
			})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ObserveIngest blocked behind an in-flight publication")
	}
}

// A failed publication is logged and never propagated: the ingest already
// succeeded, and the batch must not be lost because the announcement failed.
func TestAutoPublisherSurvivesAFailedPublication(t *testing.T) {
	service := newRecordingPublicationService()
	service.err = errors.New("ipfs api url is required")
	publisher := startAutoPublisher(t, service, []config.AutoPublishLane{{Schema: "RFB.fbs"}})

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b1", Inserted: 1})
	service.waitForPublications(t, 1)

	service.mu.Lock()
	service.err = nil
	service.mu.Unlock()

	publisher.mu.Lock()
	for key := range publisher.lastPublished {
		publisher.lastPublished[key] = time.Now().UTC().Add(-2 * time.Hour)
	}
	publisher.mu.Unlock()

	publisher.ObserveIngest(IngestedBatch{Schema: "RFB.fbs", ProviderID: "p", SourceName: "s", BatchID: "b2", Inserted: 1})
	service.waitForPublications(t, 1)
	if got := len(service.snapshot()); got != 2 {
		t.Fatalf("publications = %d, want 2 (the publisher stopped after a failure)", got)
	}
}

type blockingPublicationService struct {
	release chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (s *blockingPublicationService) PublishDatasetUpdate(_ context.Context, _ DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	s.once.Do(func() { s.entered <- struct{}{} })
	<-s.release
	return &DatasetPublicationResult{}, nil
}
