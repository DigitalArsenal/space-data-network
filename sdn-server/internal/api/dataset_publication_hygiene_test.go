package api

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/IQC"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/ipfs/go-cid"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// dataset_publication_hygiene_test.go — graph: sdn-publication-hygiene-20260928.
// Computable outcomes only: records landed, kubo calls made, CIDs and bytes
// pinned, rows advertised, the value an IPNS name points at.

const (
	hygieneProvider = "space-data-network-02"
	hygieneSource   = "IQEngine"
	hygienePeerID   = "16Uiu2HAmV963F8WEK6V1jTMNWrjFBkrKodB53RqsDA3qTsFcz3y4"
)

// hygieneKubo is a fake Kubo RPC that tracks pins, command counts and IPNS
// names.
type hygieneKubo struct {
	t      *testing.T
	mu     sync.Mutex
	pinned map[string][]byte
	calls  map[string]int
	names  map[string]string
	server *httptest.Server
}

func newHygieneKubo(t *testing.T) *hygieneKubo {
	t.Helper()
	k := &hygieneKubo{t: t, pinned: map[string][]byte{}, calls: map[string]int{}, names: map[string]string{}}
	k.server = httptest.NewServer(http.HandlerFunc(k.serve))
	t.Cleanup(k.server.Close)
	return k
}

func (k *hygieneKubo) serve(w http.ResponseWriter, r *http.Request) {
	k.mu.Lock()
	k.calls[r.URL.Path]++
	k.mu.Unlock()
	switch r.URL.Path {
	case "/api/v0/add", "/api/v0/block/put":
		reader, err := r.MultipartReader()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		body, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		value := cidV1RawSHA256ForTest(k.t, body)
		k.mu.Lock()
		k.pinned[value] = body
		k.mu.Unlock()
		key := "Hash"
		if r.URL.Path == "/api/v0/block/put" {
			key = "Key"
		}
		_, _ = fmt.Fprintf(w, `{"%s":"%s"}`+"\n", key, value)
	case "/api/v0/dag/export", "/api/v0/cat":
		value := r.URL.Query().Get("arg")
		k.mu.Lock()
		body, ok := k.pinned[value]
		k.mu.Unlock()
		if !ok {
			http.Error(w, "block not found "+value, http.StatusInternalServerError)
			return
		}
		if r.URL.Path == "/api/v0/cat" {
			_, _ = w.Write(body)
			return
		}
		root, err := cid.Decode(value)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeSingleBlockCARForTest(k.t, w, root, body)
	case "/api/v0/pin/rm":
		value := r.URL.Query().Get("arg")
		k.mu.Lock()
		_, ok := k.pinned[value]
		delete(k.pinned, value)
		k.mu.Unlock()
		if !ok {
			http.Error(w, `{"Message":"not pinned or pinned indirectly"}`, http.StatusInternalServerError)
			return
		}
		_, _ = fmt.Fprintf(w, `{"Pins":["%s"]}`+"\n", value)
	case "/api/v0/name/publish":
		key := r.URL.Query().Get("key")
		value := r.URL.Query().Get("arg")
		k.mu.Lock()
		k.names[key] = value
		k.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"Name":"k51-fake-%s","Value":"%s"}`+"\n", key, value)
	default:
		http.Error(w, "unexpected "+r.URL.Path, http.StatusNotFound)
	}
}

func (k *hygieneKubo) pinnedSet() map[string]int {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]int, len(k.pinned))
	for value, body := range k.pinned {
		out[value] = len(body)
	}
	return out
}

func (k *hygieneKubo) callCount(path string) int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.calls[path]
}

func (k *hygieneKubo) name(key string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.names[key]
}

// hygieneIQC builds a ~1 KB $IQC record carrying the parser's fetch stamps.
func hygieneIQC(seq int, stamp string) []byte {
	b := flatbuffers.NewBuilder(1024)
	id := b.CreateString(fmt.Sprintf("iqengine:local/local/capture-%06d", seq))
	capture := b.CreateString(fmt.Sprintf("capture-%06d", seq))
	source := b.CreateString(hygieneSource)
	desc := b.CreateString(strings.Repeat(fmt.Sprintf("SigMF capture %d. ", seq), 40))
	retrieved := b.CreateString(stamp)
	created := b.CreateString(stamp)
	IQC.IQCStart(b)
	IQC.IQCAddID(b, id)
	IQC.IQCAddCAPTURE_ID(b, capture)
	IQC.IQCAddSOURCE_NAME(b, source)
	IQC.IQCAddRETRIEVED_AT(b, retrieved)
	IQC.IQCAddDESCRIPTION(b, desc)
	IQC.IQCAddSAMPLE_RATE_HZ(b, 2.4e6)
	IQC.IQCAddCREATED_AT(b, created)
	IQC.IQCAddUPDATED_AT(b, created)
	root := IQC.IQCEnd(b)
	b.FinishWithFileIdentifier(root, []byte(IQC.IQCIdentifier))
	return append([]byte(nil), b.FinishedBytes()...)
}

func hygieneIngest(t *testing.T, store *storage.FlatSQLStore, batch string, from, to int, stamp string) int {
	t.Helper()
	records := make([][]byte, 0, to-from)
	for seq := from; seq < to; seq++ {
		records = append(records, hygieneIQC(seq, stamp))
	}
	inserted, err := store.StoreBatchWithSourceTags("IQC.fbs", records, "module:sigmf", nil,
		storage.SourceTags{ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch, ContentKeyID: "public"})
	if err != nil {
		t.Fatalf("ingest %s [%d,%d): %v", batch, from, to, err)
	}
	return inserted
}

type hygieneFixture struct {
	store     *storage.FlatSQLStore
	kubo      *hygieneKubo
	service   *ConcreteDatasetPublicationService
	announcer *fakeDatasetUpdatePublisher
	outputDir string
}

func newHygieneFixture(t *testing.T) *hygieneFixture {
	t.Helper()
	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	store, err := storage.NewFlatSQLStore(filepath.Join(dir, "store"), validator)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	_, signingKey, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	kubo := newHygieneKubo(t)
	announcer := &fakeDatasetUpdatePublisher{}
	outputDir := filepath.Join(dir, "dataset-publications")
	service := NewConcreteDatasetPublicationService(store, announcer, signingKey, hygienePeerID, "bafy-provider-epm", kubo.server.URL, outputDir)
	return &hygieneFixture{store: store, kubo: kubo, service: service, announcer: announcer, outputDir: outputDir}
}

func (f *hygieneFixture) publishBatch(t *testing.T, batch string, maxShardBytes int64) *DatasetPublicationResult {
	t.Helper()
	result, err := f.service.PublishDatasetUpdate(context.Background(), DatasetPublicationRequest{
		Schema: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch, MaxShardBytes: maxShardBytes,
	})
	if err != nil {
		t.Fatalf("publish %s: %v", batch, err)
	}
	return result
}

// seriesPins is what one series pinned: every window's shard, index and DPM
// plus the lane's current CAR bundle(s).
func (f *hygieneFixture) seriesPins(t *testing.T, result *DatasetPublicationResult) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	parts := result.Publications
	if len(parts) == 0 {
		parts = []DatasetPublicationResult{*result}
	}
	for _, part := range parts {
		out[part.ShardCID], out[part.IndexCID], out[part.ManifestCID] = true, true, true
	}
	series, err := f.store.ListDatasetPublicationSeries(storage.DatasetPublicationLane{SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource})
	if err != nil || len(series) == 0 {
		t.Fatalf("list series: %v (%d)", err, len(series))
	}
	for _, member := range series[0].Members {
		if member.Role == storage.PinLedgerRoleShardGroupCAR {
			out[member.CID] = true
		}
	}
	return out
}

func (f *hygieneFixture) advertisedRows(t *testing.T) []storage.DatasetShardPublication {
	t.Helper()
	rows, err := f.store.ListDatasetShardPublications(storage.DatasetShardPublicationQuery{
		SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource,
		QueryProfile: storage.DatasetPublicationQueryProfile,
	})
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func dirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	sort.Strings(out)
	return out
}

// TestAutoPublishOfUnchangedSetIsNoOp: after a lane is published, neither a
// restart's source catch-up nor a replayed batch that landed nothing new
// exports, pins or announces anything.
func TestAutoPublishOfUnchangedSetIsNoOp(t *testing.T) {
	f := newHygieneFixture(t)
	const batch = "60cc9680"
	if n := hygieneIngest(t, f.store, batch, 0, 120, "2026-09-15T02:11:22Z"); n != 120 {
		t.Fatalf("ingest landed %d", n)
	}

	run := func(lane config.AutoPublishLane, observe *IngestedBatch) (published, unchanged int64) {
		t.Helper()
		publisher := NewAutoPublisher(f.service, []config.AutoPublishLane{lane})
		done := make(chan error, 4)
		publisher.onPublished = func(_ DatasetPublicationRequest, _ *DatasetPublicationResult, err error) { done <- err }
		publisher.Start(context.Background())
		defer publisher.Stop()
		if observe != nil {
			publisher.ObserveIngest(*observe)
		}
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("auto-publish: %v", err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("auto-publish did not run")
		}
		stats := publisher.Stats()
		return stats.Published, stats.Unchanged
	}

	sourceLane := config.AutoPublishLane{Schema: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, PublishScope: "source", MinInterval: time.Hour}
	// First start: the lane was never published.
	if published, unchanged := run(sourceLane, nil); published != 1 || unchanged != 0 {
		t.Fatalf("first start: published %d unchanged %d, want 1/0", published, unchanged)
	}
	adds, puts, announced := f.kubo.callCount("/api/v0/add"), f.kubo.callCount("/api/v0/block/put"), len(f.announcer.announcements)
	if adds == 0 || puts == 0 || announced == 0 {
		t.Fatalf("first publication made no kubo writes / announcements (%d/%d/%d)", adds, puts, announced)
	}

	// The daemon restarts; the flow re-fetches the same payload (0 new).
	if n := hygieneIngest(t, f.store, batch, 0, 120, "2026-09-28T19:15:04Z"); n != 0 {
		t.Fatalf("replay landed %d records, want 0", n)
	}
	if published, unchanged := run(sourceLane, nil); published != 0 || unchanged != 1 {
		t.Fatalf("restart: published %d unchanged %d, want 0/1", published, unchanged)
	}
	// Batch scope, told a batch landed: its set is unchanged too.
	batchLane := config.AutoPublishLane{Schema: "IQC.fbs", SourceName: hygieneSource, MinInterval: time.Minute}
	observe := &IngestedBatch{Schema: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, BatchID: batch, Inserted: 1}
	if n := hygieneIngest(t, f.store, batch, 0, 120, "2026-09-29T00:00:00Z"); n != 0 {
		t.Fatalf("replay landed %d records, want 0", n)
	}
	// A batch-scoped publication of the batch is a different scope than the
	// source publication; publish it once, then the same observation is a no-op.
	if published, _ := run(batchLane, observe); published != 1 {
		t.Fatalf("batch scope first publication: published %d, want 1", published)
	}
	adds, puts, announced = f.kubo.callCount("/api/v0/add"), f.kubo.callCount("/api/v0/block/put"), len(f.announcer.announcements)
	if published, unchanged := run(batchLane, observe); published != 0 || unchanged != 1 {
		t.Fatalf("batch scope replay: published %d unchanged %d, want 0/1", published, unchanged)
	}
	if got := f.kubo.callCount("/api/v0/add"); got != adds {
		t.Fatalf("unchanged set added %d kubo objects", got-adds)
	}
	if got := f.kubo.callCount("/api/v0/block/put"); got != puts {
		t.Fatalf("unchanged set put %d kubo blocks", got-puts)
	}
	if got := len(f.announcer.announcements); got != announced {
		t.Fatalf("unchanged set announced %d times", got-announced)
	}

	// A changed set publishes again.
	if n := hygieneIngest(t, f.store, batch, 120, 130, "2026-09-29T01:00:00Z"); n != 10 {
		t.Fatalf("new captures landed %d, want 10", n)
	}
	if published, unchanged := run(batchLane, observe); published != 1 || unchanged != 0 {
		t.Fatalf("changed set: published %d unchanged %d, want 1/0", published, unchanged)
	}
}

// TestPublicationRetentionBoundsPinsUnderRepeatedSeries is the IQC shape: one
// batch republished as it grows, byte-cut windows drifting each time. Pins
// stay bounded to the newest two series, only the newest is advertised, and
// no stale window file or CAR staging file survives.
func TestPublicationRetentionBoundsPinsUnderRepeatedSeries(t *testing.T) {
	f := newHygieneFixture(t)
	const batch = "60cc9680"
	const shardBytes = 64 << 10
	var previous map[string]bool
	var previousBytes, maxSeriesBytes int
	for round := 0; round < 8; round++ {
		// Each round the SAME batch grows by 25 captures (the pre-fix replay).
		if n := hygieneIngest(t, f.store, batch, round*25, (round+1)*25+100, fmt.Sprintf("2026-09-%02dT00:00:00Z", 10+round)); n <= 0 {
			t.Fatalf("round %d landed nothing", round)
		}
		result := f.publishBatch(t, batch, shardBytes)
		if len(result.Publications) < 2 {
			t.Fatalf("round %d: %d windows, want the byte budget to cut several", round, len(result.Publications))
		}
		current := f.seriesPins(t, result)
		pinned := f.kubo.pinnedSet()
		currentBytes := 0
		for value := range current {
			if _, ok := pinned[value]; !ok {
				t.Fatalf("round %d: current series CID %s is not pinned", round, value)
			}
			currentBytes += pinned[value]
		}
		if currentBytes > maxSeriesBytes {
			maxSeriesBytes = currentBytes
		}
		want := map[string]bool{}
		for value := range current {
			want[value] = true
		}
		for value := range previous {
			want[value] = true
		}
		total := 0
		for value, size := range pinned {
			if !want[value] {
				t.Fatalf("round %d: %s stays pinned but belongs to no kept series", round, value)
			}
			total += size
		}
		if round > 0 && total > currentBytes+previousBytes {
			t.Fatalf("round %d: %d bytes pinned, bound is %d", round, total, currentBytes+previousBytes)
		}
		// Only the newest series is advertised, and only its files remain.
		rows := f.advertisedRows(t)
		if len(rows) != len(result.Publications) {
			t.Fatalf("round %d: %d rows advertised, want the %d windows of the newest series", round, len(rows), len(result.Publications))
		}
		shards := dirFiles(t, filepath.Join(f.outputDir, "IQC", "shards"))
		indexes := dirFiles(t, filepath.Join(f.outputDir, "IQC", "indexes"))
		if len(shards) != len(rows) || len(indexes) != len(rows) {
			t.Fatalf("round %d: %d shard / %d index files for %d rows", round, len(shards), len(indexes), len(rows))
		}
		if cars := dirFiles(t, filepath.Join(f.outputDir, "IQC", "car")); len(cars) != 0 {
			t.Fatalf("round %d: CAR staging files left on disk: %v", round, cars)
		}
		previous, previousBytes = current, currentBytes
	}
	if total := len(f.kubo.pinnedSet()); total == 0 {
		t.Fatal("nothing pinned")
	}
	// Retired pins are marked in the ledger.
	retired, err := f.store.ListPinLedgerEntries(storage.PinLedgerQuery{SchemaName: "IQC.fbs", ProviderID: hygieneProvider, SourceName: hygieneSource, VerificationState: storage.PinLedgerStateRetired})
	if err != nil || len(retired) == 0 {
		t.Fatalf("retired ledger entries: %d (err %v)", len(retired), err)
	}
	for _, entry := range retired {
		if _, pinned := f.kubo.pinnedSet()[entry.CID]; pinned {
			t.Fatalf("ledger says %s %s is retired but kubo still pins it", entry.Role, entry.CID)
		}
	}
	_ = maxSeriesBytes
}

// TestPublicationRetentionRespectsBatchSemantics: a lane whose older batches
// the store still holds keeps them published (parts are never dropped); the
// same lane declared snapshot_batches, or one whose store dropped the old
// batches, keeps only the newest series.
func TestPublicationRetentionRespectsBatchSemantics(t *testing.T) {
	lanePins := func(f *hygieneFixture) int { return len(f.kubo.pinnedSet()) }
	publishBatches := func(t *testing.T, f *hygieneFixture, dropOld bool) []int {
		t.Helper()
		var counts []int
		for round := 0; round < 5; round++ {
			batch := fmt.Sprintf("batch-%d", round)
			hygieneIngest(t, f.store, batch, round*40, round*40+40, "2026-09-15T00:00:00Z")
			if dropOld {
				// A "current" snapshot lane: the store keeps only the newest batch.
				if _, err := f.store.ReconcileSourceBatch("IQC.fbs", hygieneProvider, hygieneSource, batch, true); err != nil {
					t.Fatal(err)
				}
			}
			f.publishBatch(t, batch, 0)
			counts = append(counts, len(f.advertisedRows(t)))
		}
		return counts
	}

	t.Run("parts stay published", func(t *testing.T) {
		f := newHygieneFixture(t)
		rows := publishBatches(t, f, false)
		if rows[len(rows)-1] != 5 {
			t.Fatalf("advertised rows %v, want every part (5) still published", rows)
		}
		if got := lanePins(f); got < 5*3 {
			t.Fatalf("%d pins, want every part's shard/index/DPM pinned", got)
		}
	})
	t.Run("snapshot batches keep the newest two", func(t *testing.T) {
		f := newHygieneFixture(t)
		f.service.SetPublicationPolicy(config.PublicationRetentionConfig{
			KeepSeries: 2,
			Lanes:      []config.PublicationRetentionLane{{Schema: "IQC", SourceName: hygieneSource, KeepSeries: 2, SnapshotBatches: true}},
		}, nil)
		rows := publishBatches(t, f, false)
		if rows[len(rows)-1] != 2 {
			t.Fatalf("advertised rows %v, want the newest 2 series", rows)
		}
		if got := lanePins(f); got > 2*4 {
			t.Fatalf("%d pins, want at most 2 series x (shard, index, DPM, CAR)", got)
		}
	})
	t.Run("dropped batches are retired", func(t *testing.T) {
		f := newHygieneFixture(t)
		rows := publishBatches(t, f, true)
		if rows[len(rows)-1] != 2 {
			t.Fatalf("advertised rows %v, want the newest 2 series", rows)
		}
		if got := lanePins(f); got > 2*4 {
			t.Fatalf("%d pins, want at most 2 series x (shard, index, DPM, CAR)", got)
		}
	})
	t.Run("keep_series 0 archives everything", func(t *testing.T) {
		f := newHygieneFixture(t)
		f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 0}, nil)
		rows := publishBatches(t, f, true)
		if rows[len(rows)-1] != 5 {
			t.Fatalf("advertised rows %v, want all 5 series kept", rows)
		}
	})
}

// TestIPNSPointerTracksLatestDPM: a configured lane's IPNS name ends on the
// newest series' DPM; an unconfigured lane publishes no name.
func TestIPNSPointerTracksLatestDPM(t *testing.T) {
	f := newHygieneFixture(t)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 2}, []config.IPNSPointerConfig{
		{Schema: "IQC", SourceName: hygieneSource, Key: "self"},
		{Schema: "OMM.fbs", SourceName: "celestrak-gp", Key: "omm-current"},
	})
	for round := 0; round < 3; round++ {
		batch := fmt.Sprintf("edition-%d", round)
		hygieneIngest(t, f.store, batch, round*30, round*30+30, "2026-09-15T00:00:00Z")
		result := f.publishBatch(t, batch, 0)
		f.service.hygiene.ipns.wait()
		if got, want := f.kubo.name("self"), "/ipfs/"+result.ManifestCID; got != want {
			t.Fatalf("round %d: IPNS self -> %q, want %q", round, got, want)
		}
		status, ok := f.service.hygiene.ipns.lastStatus("self")
		if !ok || status.Err != nil || status.Name != "k51-fake-self" {
			t.Fatalf("round %d: pointer status %+v", round, status)
		}
	}
	if got := f.kubo.name("omm-current"); got != "" {
		t.Fatalf("a lane that published nothing has an IPNS value %q", got)
	}
	if got := f.kubo.callCount("/api/v0/name/publish"); got != 3 {
		t.Fatalf("name/publish called %d times, want 3", got)
	}
}

// TestIPNSPointerIsOffUnlessConfigured: no ipns_pointers, no name/publish.
func TestIPNSPointerIsOffUnlessConfigured(t *testing.T) {
	f := newHygieneFixture(t)
	hygieneIngest(t, f.store, "b1", 0, 20, "2026-09-15T00:00:00Z")
	f.publishBatch(t, "b1", 0)
	f.service.hygiene.ipns.wait()
	if got := f.kubo.callCount("/api/v0/name/publish"); got != 0 {
		t.Fatalf("name/publish called %d times without configuration", got)
	}
}

// TestIPNSPointerWorkerKeepsOnlyTheLatestPath: a burst of pointers for one
// key while a publish is in flight ends on the last path, never an older one.
func TestIPNSPointerWorkerKeepsOnlyTheLatestPath(t *testing.T) {
	w := newIPNSPointerWorker()
	started := make(chan struct{})
	release := make(chan struct{})
	var mu sync.Mutex
	var published []string
	w.publish = func(ctx context.Context, job ipnsPointerJob) (string, error) {
		if job.Path == "/ipfs/first" {
			close(started)
			<-release
		}
		mu.Lock()
		published = append(published, job.Path)
		mu.Unlock()
		return "k51-" + job.Key, nil
	}
	w.enqueue(ipnsPointerJob{Key: "self", Path: "/ipfs/first"})
	<-started
	for i := 0; i < 5; i++ {
		w.enqueue(ipnsPointerJob{Key: "self", Path: fmt.Sprintf("/ipfs/next-%d", i)})
	}
	close(release)
	w.wait()
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 2 || published[0] != "/ipfs/first" || published[1] != "/ipfs/next-4" {
		t.Fatalf("published %v, want [/ipfs/first /ipfs/next-4]", published)
	}
	if status, _ := w.lastStatus("self"); status.Path != "/ipfs/next-4" || status.Count != 2 {
		t.Fatalf("status %+v", status)
	}
}

func TestPublishIPNSNameRequestShape(t *testing.T) {
	var query map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v0/name/publish" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		query = r.URL.Query()
		_ = json.NewEncoder(w).Encode(map[string]string{"Name": "k51self", "Value": r.URL.Query().Get("arg")})
	}))
	defer server.Close()
	manifest := "bafkreidr46gadotnxmcmx2wcrqevo5xgscv7qvwq2zmrbnzochjc3xf7yi"
	name, value, err := storage.PublishIPNSName(context.Background(), server.URL, "self", "/ipfs/"+manifest, 48*time.Hour, 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if name != "k51self" || value != "/ipfs/"+manifest {
		t.Fatalf("name %q value %q", name, value)
	}
	want := map[string]string{"arg": "/ipfs/" + manifest, "key": "self", "allow-offline": "true", "resolve": "false", "lifetime": "48h0m0s", "ttl": "5m0s"}
	for k, v := range want {
		if got := query[k]; len(got) != 1 || got[0] != v {
			t.Fatalf("query %s = %v, want %q", k, got, v)
		}
	}
	if _, _, err := storage.PublishIPNSName(context.Background(), server.URL, "self", "/ipfs/not-a-cid", 0, 0); err == nil {
		t.Fatal("a non-CID value was accepted")
	}
}

// TestRetentionPassOnDemandReleasesWhatNoSeriesKeeps: a lane published with
// retention off (keep_series 0, the pre-change behaviour) is cleaned by one
// on-demand pass through the loopback admin route; the dry run reports
// exactly what the applied pass releases and changes nothing.
func TestRetentionPassOnDemandReleasesWhatNoSeriesKeeps(t *testing.T) {
	f := newHygieneFixture(t)
	f.service.SetPublicationPolicy(config.PublicationRetentionConfig{KeepSeries: 0}, nil)
	const batch = "60cc9680"
	var results []*DatasetPublicationResult
	for round := 0; round < 5; round++ {
		hygieneIngest(t, f.store, batch, round*25, round*25+100, fmt.Sprintf("2026-09-%02dT00:00:00Z", 10+round))
		results = append(results, f.publishBatch(t, batch, 64<<10))
	}
	before := f.kubo.pinnedSet()

	handler := NewDatasetPublicationHandler(f.service)
	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)
	call := func(body string) DatasetPublicationRetentionReport {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/retention", strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:40000"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("retention route: %d %s", rec.Code, rec.Body.String())
		}
		var report DatasetPublicationRetentionReport
		if err := json.Unmarshal(rec.Body.Bytes(), &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	body := `{"schema":"IQC","providerId":"` + hygieneProvider + `","sourceName":"` + hygieneSource + `","keepSeries":2%s}`

	dry := call(fmt.Sprintf(body, ""))
	if dry.Applied || len(dry.Unpin) == 0 || len(dry.KeptSeries) != 2 || len(dry.RetiredSeries) != 3 {
		t.Fatalf("dry run: applied %v, %d unpins, kept %d, retired %d", dry.Applied, len(dry.Unpin), len(dry.KeptSeries), len(dry.RetiredSeries))
	}
	if after := f.kubo.pinnedSet(); len(after) != len(before) {
		t.Fatalf("dry run changed kubo: %d pins, was %d", len(after), len(before))
	}

	applied := call(fmt.Sprintf(body, `,"apply":true`))
	if !applied.Applied || applied.Result == nil || applied.Result.Unpinned != len(dry.Unpin) || applied.Result.UnpinFailed != 0 {
		t.Fatalf("applied pass: %+v (dry run planned %d unpins)", applied.Result, len(dry.Unpin))
	}
	pinned := f.kubo.pinnedSet()
	for _, pin := range dry.Unpin {
		if _, ok := pinned[pin.CID]; ok {
			t.Fatalf("%s %s is still pinned after the pass", pin.Role, pin.CID)
		}
	}
	// The newest two series stay whole (bundles follow the head only).
	for _, result := range results[len(results)-2:] {
		for _, part := range result.Publications {
			for _, value := range []string{part.ShardCID, part.IndexCID, part.ManifestCID} {
				if _, ok := pinned[value]; !ok {
					t.Fatalf("kept series CID %s was unpinned", value)
				}
			}
		}
	}
	if again := call(fmt.Sprintf(body, `,"apply":true`)); len(again.Unpin) != 0 || len(again.RetireRows) != 0 {
		t.Fatalf("a second pass still releases %d pins / %d rows", len(again.Unpin), len(again.RetireRows))
	}

	// Loopback only.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/dataset-updates/retention", strings.NewReader(fmt.Sprintf(body, "")))
	req.RemoteAddr = "203.0.113.9:40000"
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback retention request: %d, want 403", rec.Code)
	}
}
