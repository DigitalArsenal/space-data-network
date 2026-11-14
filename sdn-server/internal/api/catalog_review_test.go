package api

import (
	"context"
	"errors"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"reflect"
	"testing"
	"time"
)

func TestCatalogProductionSelection(t *testing.T) {
	s := pairedStore(t, t.TempDir())
	defer s.Close()
	catalogEnableRetention(t, s)
	catalogPutDay(t, s, 0, "OCM", "CAT")
	first, err := s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil {
		t.Fatal(err)
	}
	service := &ConcreteDatasetPublicationService{store: s, hygiene: newPublicationHygiene(), now: time.Now, providerPeerID: "synthetic"}
	var order []string
	consumer := newCatalogConsumer()
	publish := func(active *ConcreteDatasetPublicationService, ctx context.Context, req DatasetPublicationRequest, schema string) (*DatasetPublicationResult, []string, error) {
		order = append(order, schema)
		if active.selection == nil || active.selection.Cutoff != first.Cutoff {
			t.Fatal("production scopes did not share one capture")
		}
		if schema == "OCM.fbs" {
			catalogPutDay(t, s, 1, "OCM", "CAT")
		}
		ex, err := s.ExportDatasetWindow(t.TempDir(), storage.IndexedRecordQuery{SchemaName: schema, ProviderID: req.ProviderID, SourceName: req.SourceName, Selection: active.selection})
		if err != nil {
			return nil, nil, err
		}
		consumer.apply(t, ex)
		return &DatasetPublicationResult{RecordCount: ex.RecordCount, ManifestCID: ex.ShardCID, ShardCID: ex.ShardCID, IndexCID: ex.IndexCID}, nil, nil
	}
	req := DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true}
	if _, err = service.publishSelectedDatasetUpdate(context.Background(), req, "CAT.fbs", publish); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"OCM.fbs", "CAT.fbs"}) {
		t.Fatal("production publication order", order)
	}
	consumer.validate(t)
	if len(consumer.products) != 32 || len(consumer.selected) != 32 {
		t.Fatal("production closure changed during concurrent ingestion")
	}
	order = nil
	_, err = service.publishSelectedDatasetUpdate(context.Background(), req, "CAT.fbs", func(_ *ConcreteDatasetPublicationService, _ context.Context, _ DatasetPublicationRequest, schema string) (*DatasetPublicationResult, []string, error) {
		order = append(order, schema)
		return nil, nil, errors.New("inert product failure")
	})
	if err == nil || !reflect.DeepEqual(order, []string{"OCM.fbs"}) {
		t.Fatal("index exposed after product failure")
	}
}
func TestCatalogReviewIncompleteGenerationFallback(t *testing.T) {
	s := pairedStore(t, t.TempDir())
	defer s.Close()
	catalogEnableRetention(t, s)
	catalogPutDay(t, s, 0, "OCM", "CAT")
	before, err := s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil {
		t.Fatal(err)
	}
	rows := catalogReadShard(t, "testdata/catalog-history/day1-CAT.sds")
	if _, err = s.StoreBatchWithSourceTags("CAT.fbs", [][]byte{rows[0][4:]}, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "incomplete"}); err != nil {
		t.Fatal(err)
	}
	after, err := s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil || after.Cutoff != before.Cutoff {
		t.Fatal("partial generation exposed", err)
	}
	if _, err = s.CompleteCatalogGeneration("synthetic-node", "catalog-index"); err == nil {
		t.Fatal("unresolved generation completed")
	}
	catalogPutDay(t, s, 1, "OCM")
	after, err = s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil || after.Cutoff != before.Cutoff {
		t.Fatal("product write committed incomplete index", err)
	}
	catalogPutDay(t, s, 1, "CAT")
	after, err = s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil || after.Cutoff <= before.Cutoff {
		t.Fatal("completed generation unavailable", err)
	}
}
func TestCatalogReviewExclusionsAndStableCutoff(t *testing.T) {
	s := pairedStore(t, t.TempDir())
	defer s.Close()
	catalogEnableRetention(t, s)
	catalogPutDay(t, s, 0, "OCM", "CAT")
	before, err := s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil {
		t.Fatal(err)
	}
	catalogPutDay(t, s, 1, "OCM")
	after, err := s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil || before.Cutoff != after.Cutoff {
		t.Fatal("unrelated writes changed generation", err)
	}
	record, err := before.Index.Record(0)
	if err != nil {
		t.Fatal(err)
	}
	id := record.CID
	if err = s.ExcludeCatalogRecords("synthetic-node", "catalog-index", []string{id}); err != nil {
		t.Fatal(err)
	}
	after, err = s.CaptureCatalogSnapshot("synthetic-node", "catalog-index", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, count := after.Index.Fingerprint(); count != 31 {
		t.Fatal("CAT exclusion not applied")
	}
	if _, count := after.Products.Fingerprint(); count != 31 {
		t.Fatal("excluded product remained selected")
	}
	if _, count := before.Index.Fingerprint(); count != 32 {
		t.Fatal("immutable capture changed")
	}
}
func TestPairedPublicationOrderedBurst(t *testing.T) {
	service := newRecordingPublicationService()
	p := NewAutoPublisher(service, pairedRules())
	now := time.Now()
	p.now = func() time.Time { return now }
	for _, id := range []string{"first", "second", "third"} {
		p.ObserveIngest(pairedObservation(id))
	}
	for len(p.queue) > 0 {
		p.publishPairedBatch(context.Background(), <-p.queue)
	}
	var order []string
	for _, req := range service.snapshot() {
		order = append(order, req.BatchID)
	}
	if !reflect.DeepEqual(order, []string{"first", "second", "third"}) {
		t.Fatal("burst is unordered or throttled per batch", order)
	}
	p.ObserveIngest(pairedObservation("later"))
	if len(p.queue) != 0 {
		t.Fatal("new burst ignored interval")
	}
	now = now.Add(time.Hour)
	p.queueReadyPairedBatches()
	p.publishPairedBatch(context.Background(), <-p.queue)
	if len(service.snapshot()) != 4 {
		t.Fatal("later burst not published")
	}
}
func TestCatalogReviewSnapshotCheckpoint(t *testing.T) {
	s := pairedStore(t, t.TempDir())
	defer s.Close()
	catalogEnableRetention(t, s)
	catalogPutDay(t, s, 0, "OCM", "CAT")
	service := &catalogExportService{ConcreteDatasetPublicationService: &ConcreteDatasetPublicationService{store: s}, dir: t.TempDir()}
	p := NewAutoPublisher(service, catalogRules())
	p.seedSourcePublicationsLocked()
	p.publishReadySource(context.Background())
	p.publishReadySource(context.Background())
	before := len(service.events)
	p = NewAutoPublisher(service, catalogRules())
	p.now = func() time.Time { return time.Now().Add(-time.Hour) }
	p.seedSourcePublicationsLocked()
	p.publishReadySource(context.Background())
	if len(service.events) != before {
		t.Fatal("restart ignored persisted time")
	}
	p.now = func() time.Time { return time.Now().Add(time.Hour) }
	p.publishReadySource(context.Background())
	p.publishReadySource(context.Background())
	if len(service.events) != before {
		t.Fatal("unchanged periodic snapshot republished")
	}
}

type orderedRetryService struct {
	attempts []string
	fail     bool
}

func (s *orderedRetryService) PublishDatasetUpdate(_ context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	s.attempts = append(s.attempts, req.BatchID)
	if s.fail {
		s.fail = false
		return nil, errors.New("inert retry")
	}
	return &DatasetPublicationResult{ManifestCID: "synthetic"}, nil
}
func TestPairedPublicationRetryPreservesOrder(t *testing.T) {
	service := &orderedRetryService{fail: true}
	p := NewAutoPublisher(service, pairedRules())
	now := time.Now()
	p.now = func() time.Time { return now }
	p.retryBackoff = func(int) time.Duration { return time.Second }
	p.ObserveIngest(pairedObservation("first"))
	p.ObserveIngest(pairedObservation("second"))
	for len(p.queue) > 0 {
		p.publishPairedBatch(context.Background(), <-p.queue)
	}
	if !reflect.DeepEqual(service.attempts, []string{"first"}) {
		t.Fatal("newer batch passed failed predecessor")
	}
	now = now.Add(time.Second)
	p.queueReadyPairedBatches()
	for len(p.queue) > 0 {
		p.publishPairedBatch(context.Background(), <-p.queue)
	}
	if !reflect.DeepEqual(service.attempts, []string{"first", "first", "second"}) {
		t.Fatal("retry changed batch order", service.attempts)
	}
}

func TestPairedPublicationIndependentSourceClocks(t *testing.T) {
	service := newRecordingPublicationService()
	rules := pairedRules()
	rules[0].ProviderID = ""
	rules[0].SourceName = ""
	other := rules[1]
	other.SourceName = "another"
	rules = append(rules, other)
	p := NewAutoPublisher(service, rules)
	now := time.Now()
	p.now = func() time.Time { return now }
	p.ObserveIngest(pairedObservation("first"))
	p.publishPairedBatch(context.Background(), <-p.queue)
	observation := pairedObservation("second")
	observation.SourceName = "another"
	p.ObserveIngest(observation)
	if len(p.queue) != 1 {
		t.Fatal("one source consumed another source's interval")
	}
	p.publishPairedBatch(context.Background(), <-p.queue)
	if len(service.snapshot()) != 2 {
		t.Fatal("second source did not publish")
	}
}
