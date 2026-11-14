package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func pairedRules() []config.AutoPublishLane {
	return []config.AutoPublishLane{
		{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", MinInterval: time.Millisecond},
		{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", PublishScope: "source", PairedPublication: true},
	}
}
func pairedObservation(batch string) IngestedBatch {
	return IngestedBatch{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: batch, Inserted: 1}
}
func pairedStore(t *testing.T, dir string) *storage.FlatSQLStore {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewFlatSQLStore(dir, validator)
	if err != nil {
		t.Fatal(err)
	}
	return store
}
func pairedCommit(t *testing.T, store *storage.FlatSQLStore, batch string, index uint32) {
	t.Helper()
	payload := sds.NewCATBuilder().WithNoradCatID(index).WithObjectName("synthetic-publication").Build()
	tags := storage.SourceTags{ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: batch}
	if _, err := store.StoreWithSourceTags("CAT.fbs", payload, "fixture", nil, tags); err != nil {
		t.Fatal(err)
	}
}

// The inert service uses the real store and completed-series ledger. It writes
// only content identifiers to its announcement sink; no key, signature or
// transport is involved. A shard receipt is durable before announcement.
type pairedInertPublication struct {
	*ConcreteDatasetPublicationService
	t        *testing.T
	dir      string
	boundary string
}

func (s *pairedInertPublication) PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	if req.FullCatalog {
		return &DatasetPublicationResult{RecordCount: 1}, nil
	}
	fingerprint, count, err := s.store.PagedDatasetPublicationSetFingerprint(req.Schema, req.ProviderID, req.SourceName, req.BatchID)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(fingerprint)))
	receipt := filepath.Join(s.dir, "shard-"+id)
	if err := os.WriteFile(receipt, []byte(id), 0600); err != nil {
		return nil, err
	}
	s.crash("shard")
	sink, err := os.OpenFile(filepath.Join(s.dir, "announcements"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = fmt.Fprintln(sink, id)
	if err == nil {
		err = sink.Sync()
	}
	closeErr := sink.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	s.crash("announcement")
	err = s.store.RecordDatasetPublicationSeries(storage.DatasetPublicationSeries{
		SchemaName: req.Schema, ProviderID: req.ProviderID, SourceName: req.SourceName, BatchID: req.BatchID,
		SeriesID: id, ManifestCID: id, Fingerprint: fingerprint, RecordCount: count, PublishedAt: time.Now(),
	}, "fixture")
	if err != nil {
		return nil, err
	}
	s.crash("completion")
	return &DatasetPublicationResult{RecordCount: count, ManifestCID: id}, nil
}
func (s *pairedInertPublication) crash(boundary string) {
	if s.boundary == boundary {
		os.Exit(73)
	}
}
func inertPairedService(t *testing.T, dir string, store *storage.FlatSQLStore) *pairedInertPublication {
	return &pairedInertPublication{ConcreteDatasetPublicationService: &ConcreteDatasetPublicationService{store: store}, t: t, dir: dir}
}

func TestPairedPublicationCrashHelper(t *testing.T) {
	dir := os.Getenv("SDN_PAIRED_CRASH_DIR")
	if dir == "" {
		t.Skip("subprocess only")
	}
	store := pairedStore(t, filepath.Join(dir, "store"))
	service := inertPairedService(t, dir, store)
	service.boundary = os.Getenv("SDN_PAIRED_CRASH_AT")
	pairedCommit(t, store, "first", 900001)
	pairedCommit(t, store, "second", 900002)
	service.crash("commit")
	publisher := NewAutoPublisher(service, pairedRules())
	publisher.Start(context.Background())
	select {
	case <-time.After(10 * time.Second):
		t.Fatal("crash boundary was not reached")
	}
}

func TestPairedPublicationRestartAtEveryBoundary(t *testing.T) {
	for _, boundary := range []string{"commit", "shard", "announcement", "completion"} {
		t.Run(boundary, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestPairedPublicationCrashHelper$", "-test.timeout=30s")
			cmd.Env = append(os.Environ(), "SDN_PAIRED_CRASH_DIR="+dir, "SDN_PAIRED_CRASH_AT="+boundary)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 73 {
				t.Fatalf("boundary subprocess: %v\n%s", err, output)
			}
			// Reopen in a fresh instance, with no replayed observation.
			store := pairedStore(t, filepath.Join(dir, "store"))
			service := inertPairedService(t, dir, store)
			publisher := NewAutoPublisher(service, pairedRules())
			publisher.recoverPairedBatches(context.Background())
			pending := len(publisher.pairedPending)
			want := 2
			if boundary == "completion" {
				want = 1
			}
			if pending != want {
				t.Fatalf("pending=%d want=%d", pending, want)
			}
			// Drain deterministically, with time advanced beyond the rate limit.
			for len(publisher.queue) > 0 {
				req := <-publisher.queue
				publisher.mu.Lock()
				publisher.lastPublished = map[string]time.Time{}
				publisher.mu.Unlock()
				publisher.publishPairedBatch(context.Background(), req)
			}
			if len(publisher.pairedPending) != 0 {
				t.Fatal("batch did not complete")
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			store = pairedStore(t, filepath.Join(dir, "store"))
			defer store.Close()
			service = inertPairedService(t, dir, store)
			replay := NewAutoPublisher(service, pairedRules())
			replay.recoverPairedBatches(context.Background())
			if len(replay.pairedPending) != 0 {
				t.Fatal("completed series was replayed")
			}
			raw, err := os.ReadFile(filepath.Join(dir, "announcements"))
			if err != nil {
				t.Fatal(err)
			}
			deliveries := strings.Fields(string(raw))
			consumer := map[string]int{}
			for _, id := range deliveries {
				consumer[id]++
			}
			if len(consumer) != 2 {
				t.Fatalf("consumer received %d distinct contents, want two", len(consumer))
			}
			expected := 2
			if boundary == "announcement" {
				expected = 3
			}
			if len(deliveries) != expected {
				t.Fatalf("deliveries=%d want=%d", len(deliveries), expected)
			}
			for id := range consumer {
				if _, err := os.Stat(filepath.Join(dir, "shard-"+id)); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPairedPublicationDefersRateLimitAndQueuePressure(t *testing.T) {
	service := newRecordingPublicationService()
	service.fired = make(chan struct{}, 128)
	rules := pairedRules()
	rules[0].MinInterval = 3 * time.Millisecond
	publisher := NewAutoPublisher(service, rules)
	for i := 0; i < autoPublishQueueDepth+8; i++ {
		publisher.ObserveIngest(pairedObservation(fmt.Sprint(i)))
	}
	if len(publisher.queue) != 1 || len(publisher.pairedPending) != autoPublishQueueDepth+8 {
		t.Fatal("queue overflow lost pending identities")
	}
	publisher.Start(context.Background())
	defer publisher.Stop()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		count := 0
		seen := map[string]bool{}
		for _, req := range service.snapshot() {
			if !req.FullCatalog {
				count++
				seen[req.BatchID] = true
			}
		}
		if count == autoPublishQueueDepth+8 {
			if len(seen) != count {
				t.Fatal("duplicate batch")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("deferred batches did not drain without another ingest")
}

func TestPairedPublicationSnapshotIntervalAndDefaults(t *testing.T) {
	p := NewAutoPublisher(newRecordingPublicationService(), pairedRules())
	p.seedSourcePublicationsLocked()
	for _, s := range p.sources {
		if s.interval != 24*time.Hour || !s.periodic {
			t.Fatalf("snapshot interval=%s periodic=%v", s.interval, s.periodic)
		}
	}
	service := newRecordingPublicationService()
	rules := pairedRules()
	rules[1].MinInterval = 30 * time.Millisecond
	p = NewAutoPublisher(service, rules)
	p.Start(context.Background())
	defer p.Stop()
	service.waitForPublications(t, 2)
	for _, req := range service.snapshot() {
		if !req.FullCatalog {
			t.Fatal("periodic timer emitted a batch")
		}
	}
}

func TestPairedPublicationRetainsRetriesPastExhaustion(t *testing.T) {
	service := newRecordingPublicationService()
	service.err = errors.New("temporary failure")
	p := NewAutoPublisher(service, pairedRules())
	p.retryBackoff = func(int) time.Duration { return time.Millisecond }
	p.ObserveIngest(pairedObservation("retry"))
	// Drive only batch attempts, without the independent snapshot.
	for i := 0; i < autoPublishMaxAttempts+2; i++ {
		p.queueReadyPairedBatches()
		req := <-p.queue
		p.publishPairedBatch(context.Background(), req)
		p.mu.Lock()
		for _, s := range p.pairedPending {
			s.due = time.Time{}
		}
		p.mu.Unlock()
	}
	service.err = nil
	p.queueReadyPairedBatches()
	p.publishPairedBatch(context.Background(), <-p.queue)
	if len(p.pairedPending) != 0 || p.Stats().Published != 1 || p.Stats().Retrying != 0 {
		t.Fatal("retry did not complete")
	}
}

func TestPairedPublicationSingleRuleCompatibility(t *testing.T) {
	for _, scope := range []string{"", "batch", "source"} {
		t.Run(scope, func(t *testing.T) {
			rules := pairedRules()[:1]
			rules[0].PublishScope = scope
			rules[0].MinInterval = 0
			p := NewAutoPublisher(newRecordingPublicationService(), rules)
			p.seedSourcePublicationsLocked()
			p.ObserveIngest(pairedObservation("one"))
			p.ObserveIngest(pairedObservation("two"))
			if len(p.pairedPending) != 0 {
				t.Fatal("single rule entered paired scheduling")
			}
			if scope == "source" {
				if len(p.sources) != 1 || len(p.queue) != 0 {
					t.Fatal("source shape changed")
				}
				for _, s := range p.sources {
					if s.periodic || s.interval != autoPublishDefaultMinInterval {
						t.Fatal("single source policy changed")
					}
				}
			} else if len(p.sources) != 0 || len(p.queue) != 1 {
				t.Fatal("single batch policy changed")
			}
		})
	}
}

func TestPairedPublicationZeroInsertReplayAndNilResult(t *testing.T) {
	service := &pairedNilPublication{}
	p := NewAutoPublisher(service, pairedRules())
	batch := pairedObservation("already-committed")
	batch.Inserted = 0
	p.ObserveIngest(batch)
	if len(p.queue) != 1 {
		t.Fatal("zero-insert replay was ignored")
	}
	p.publishPairedBatch(context.Background(), <-p.queue)
	if len(p.pairedPending) != 1 || p.Stats().Published != 0 {
		t.Fatal("nil result advanced progress")
	}
}

type pairedNilPublication struct{}

func (*pairedNilPublication) PublishDatasetUpdate(context.Context, DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	return nil, nil
}

func TestPairedPublicationIngestDuringAttemptIsRetained(t *testing.T) {
	service := &blockingPublicationService{release: make(chan struct{}), entered: make(chan struct{}, 1)}
	p := NewAutoPublisher(service, pairedRules())
	now := time.Now()
	p.now = func() time.Time { return now }
	p.ObserveIngest(pairedObservation("parts"))
	request := <-p.queue
	done := make(chan struct{})
	go func() { p.publishPairedBatch(context.Background(), request); close(done) }()
	<-service.entered
	p.ObserveIngest(pairedObservation("parts"))
	if len(p.queue) != 0 {
		t.Fatal("in-flight batch queued twice")
	}
	close(service.release)
	<-done
	if len(p.pairedPending) != 1 {
		t.Fatal("ingest during attempt was erased by completion")
	}
	p.queueReadyPairedBatches()
	if len(p.queue) != 0 {
		t.Fatal("continuation bypassed the publication interval")
	}
	now = now.Add(time.Hour)
	p.queueReadyPairedBatches()
	if len(p.queue) != 1 {
		t.Fatal("later ingest has no queued continuation")
	}
}

func TestPairedPublicationRecoveryFiltersConfiguredSource(t *testing.T) {
	dir := t.TempDir()
	store := pairedStore(t, filepath.Join(dir, "store"))
	defer store.Close()
	pairedCommit(t, store, "one", 900001)
	service := inertPairedService(t, dir, store)
	rules := pairedRules()
	rules[0].ProviderID = ""
	rules[0].SourceName = ""
	p := NewAutoPublisher(service, rules)
	p.recoverPairedBatches(context.Background())
	if len(p.queue) != 1 {
		t.Fatal("wildcard batch rule did not pair with exact source")
	}
	rules[1].ProviderID = "another-provider"
	p = NewAutoPublisher(service, rules)
	p.recoverPairedBatches(context.Background())
	if len(p.queue) != 0 {
		t.Fatal("recovery published an unconfigured source")
	}
}

func TestPairedPublicationTwoRulesRequireOptIn(t *testing.T) {
	for _, sourceFirst := range []bool{false, true} {
		rules := pairedRules()
		rules[0].PairedPublication = false
		rules[1].PairedPublication = false
		if sourceFirst {
			rules[0], rules[1] = rules[1], rules[0]
		}
		p := &AutoPublisher{lanes: rules, sources: map[sourcePublicationKey]*sourcePublicationState{}}
		if p.isPairedSource("CAT.fbs", "synthetic-node", "catalog-index") {
			t.Fatal("existing rules opted in implicitly")
		}
		first, ok := p.matchLane("CAT.fbs", "synthetic-node", "catalog-index")
		if !ok || first.PublishScope != rules[0].PublishScope {
			t.Fatal("first matching rule changed")
		}
		p.seedSourcePublicationsLocked()
		if len(p.sources) != map[bool]int{false: 0, true: 1}[sourceFirst] {
			t.Fatal("startup differs from first-rule behavior")
		}
		for _, state := range p.sources {
			if state.interval != autoPublishDefaultMinInterval || state.periodic {
				t.Fatal("legacy default changed")
			}
		}
	}
}
