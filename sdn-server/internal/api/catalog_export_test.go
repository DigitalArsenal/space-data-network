package api

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	cidlib "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spacedatanetwork/sdn-server/internal/config"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type catalogExportEvent struct {
	Request DatasetPublicationRequest
	Export  *storage.DatasetExport
}
type catalogExportService struct {
	*ConcreteDatasetPublicationService
	events []catalogExportEvent
	dir    string
}

func catalogActualExport(store *storage.FlatSQLStore, dir string, req DatasetPublicationRequest) (*storage.DatasetExport, error) {
	filter := datasetPublicationExportFilter(req, req.Schema, unlimitedDatasetPublicationLimit(), 0)
	filter.OrderByCID = true
	return store.ExportDatasetWindow(dir, filter)
}
func (s *catalogExportService) PublishDatasetUpdate(ctx context.Context, req DatasetPublicationRequest) (*DatasetPublicationResult, error) {
	ex, err := catalogActualExport(s.store, s.dir, req)
	if err != nil {
		return nil, err
	}
	fingerprint, count, err := s.store.PagedDatasetPublicationSetFingerprint(req.Schema, req.ProviderID, req.SourceName, req.BatchID)
	if err != nil {
		return nil, err
	}
	// The shard is the real production export. Only transport/signing are inert.
	s.events = append(s.events, catalogExportEvent{req, ex})
	err = s.store.RecordDatasetPublicationSeries(storage.DatasetPublicationSeries{SchemaName: req.Schema, ProviderID: req.ProviderID, SourceName: req.SourceName, BatchID: req.BatchID, SeriesID: ex.ShardCID, ManifestCID: ex.ShardCID, Fingerprint: fingerprint, RecordCount: count, PublishedAt: time.Now()}, "synthetic")
	if err != nil {
		return nil, err
	}
	return &DatasetPublicationResult{RecordCount: ex.RecordCount, ManifestCID: ex.ShardCID}, nil
}
func catalogReadShard(t *testing.T, path string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]byte
	for len(raw) > 0 {
		if len(raw) < 4 {
			t.Fatal("short shard")
		}
		n := int(binary.LittleEndian.Uint32(raw))
		if n < 8 || n+4 > len(raw) {
			t.Fatal("invalid shard frame")
		}
		rows = append(rows, raw[:n+4])
		raw = raw[n+4:]
	}
	return rows
}

type catalogExportSelection struct {
	AsOf, CID string
	Kind      byte
}
type catalogConsumer struct {
	selected map[uint32]catalogExportSelection
	products map[string][]byte
}

func newCatalogConsumer() *catalogConsumer {
	return &catalogConsumer{map[uint32]catalogExportSelection{}, map[string][]byte{}}
}
func (c *catalogConsumer) apply(t *testing.T, ex *storage.DatasetExport) {
	t.Helper()
	rows := catalogReadShard(t, ex.ShardPath)
	if len(rows) != ex.RecordCount {
		t.Fatal("export count mismatch")
	}
	for _, b := range rows {
		if ex.SchemaName == "OCM.fbs" {
			h, _ := mh.Sum(b, mh.SHA2_256, -1)
			c.products[cidlib.NewCidV1(cidlib.Raw, h).String()] = b
			continue
		}
		cat := CAT.GetRootAsCAT(b, 4)
		s := catalogExportSelection{AsOf: string(cat.PRODUCTS_AS_OF())}
		if cat.PRODUCTSLength() > 1 {
			t.Fatal("multiple selection")
		}
		if cat.PRODUCTSLength() == 1 {
			var p CAT.CATProduct
			cat.PRODUCTS(&p, 0)
			s.CID = string(p.RECORD_CID())
			s.Kind = byte(p.KIND())
		}
		old, ok := c.selected[cat.NORAD_CAT_ID()]
		if !ok || s.AsOf > old.AsOf {
			c.selected[cat.NORAD_CAT_ID()] = s
		} else if s.AsOf == old.AsOf && s != old {
			t.Fatal("ambiguous equal-time selections")
		}
	}
}
func (c *catalogConsumer) validate(t *testing.T) {
	t.Helper()
	for _, s := range c.selected {
		if s.CID != "" && c.products[s.CID] == nil {
			t.Fatal("selected exact product missing from exported shards")
		}
	}
}
func catalogRules() []config.AutoPublishLane {
	var r []config.AutoPublishLane
	for _, x := range []struct{ schema, source string }{{"OCM.fbs", "catalog-products"}, {"CAT.fbs", "catalog-index"}} {
		for _, scope := range []string{"batch", "source"} {
			r = append(r, config.AutoPublishLane{Schema: x.schema, ProviderID: "synthetic-node", SourceName: x.source, PublishScope: scope, MinInterval: time.Millisecond})
		}
	}
	return r
}
func catalogDrain(t *testing.T, p *AutoPublisher) {
	t.Helper()
	now := time.Now().Add(time.Hour)
	p.now = func() time.Time { return now }
	p.recoverPairedBatches(context.Background())
	for round := 0; round < 100 && len(p.pairedPending) > 0; round++ {
		p.queueReadyPairedBatches()
		for len(p.queue) > 0 {
			p.publishPairedBatch(context.Background(), <-p.queue)
		}
		now = now.Add(time.Hour)
	}
	if len(p.pairedPending) != 0 {
		t.Fatal("pending batches did not drain")
	}
}
func catalogSave(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: %s", name, raw)
}
func catalogEnableRetention(t *testing.T, store *storage.FlatSQLStore) {
	t.Helper()
	if err := store.ConfigurePublicationSources([]storage.PublicationSource{
		{Schema: "CAT.fbs", Provider: "synthetic-node", Source: "catalog-index", ProductSource: "catalog-products"},
		{Schema: "OCM.fbs", Provider: "synthetic-node", Source: "catalog-products"},
	}); err != nil {
		t.Fatal(err)
	}
}
func TestCatalogCompactSnapshot(t *testing.T) {
	root := t.TempDir()
	store := pairedStore(t, filepath.Join(root, "session6-history-store"))
	defer store.Close()
	catalogEnableRetention(t, store)
	s := &catalogExportService{ConcreteDatasetPublicationService: &ConcreteDatasetPublicationService{store: store}, dir: filepath.Join(root, "session6-history-exports")}
	var measurements []map[string]any
	for day := 0; day <= 3; day++ {
		for _, item := range []struct{ schema, source string }{{"OCM", "catalog-products"}, {"CAT", "catalog-index"}} {
			rows := catalogReadShard(t, filepath.Join("testdata", "catalog-history", fmt.Sprintf("day%d-%s.sds", day, item.schema)))
			for i := range rows {
				rows[i] = rows[i][4:]
			}
			if _, err := store.StoreBatchWithSourceTags(item.schema+".fbs", rows, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: item.source, BatchID: fmt.Sprintf("day%d", day)}); err != nil {
				t.Fatal(err)
			}
		}
		p := NewAutoPublisher(s, catalogRules())
		catalogDrain(t, p)
		start := len(s.events)
		p.onPublished = func(_ DatasetPublicationRequest, _ *DatasetPublicationResult, err error) {
			if err != nil {
				t.Fatal(err)
			}
		}
		p.seedSourcePublicationsLocked()
		p.publishReadySource(context.Background())
		p.publishReadySource(context.Background())
		if len(s.events) != start+2 {
			t.Fatal("two source snapshots required")
		}
		c := newCatalogConsumer()
		row := map[string]any{"day": day}
		for _, e := range s.events[start:] {
			c.apply(t, e.Export)
			row[e.Export.SchemaName] = map[string]any{"records": e.Export.RecordCount, "shardBytes": e.Export.ShardBytes, "indexBytes": e.Export.IndexBytes}
		}
		c.validate(t)
		selectedBytes := 0
		for _, v := range c.selected {
			selectedBytes += len(c.products[v.CID])
		}
		row["selectedObjects"] = len(c.selected)
		row["selectedProductBytes"] = selectedBytes
		measurements = append(measurements, row)
	}
	// Control: a separately retained current source compacts, but replacement of
	// its two standards is not an atomic snapshot publication boundary.
	putMirror := func(day int, schema, source, batch string) {
		rows := catalogReadShard(t, filepath.Join("testdata", "catalog-history", fmt.Sprintf("day%d-%s.sds", day, schema)))
		for i := range rows {
			rows[i] = rows[i][4:]
		}
		if _, err := store.StoreBatchWithSourceTags(schema+".fbs", rows, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: source, BatchID: batch}); err != nil {
			t.Fatal(err)
		}
	}
	putMirror(0, "OCM", "compact-products", "old")
	putMirror(0, "CAT", "compact-index", "old")
	putMirror(1, "OCM", "compact-products", "new")
	if _, err := store.ReconcileSourceBatch("OCM.fbs", "synthetic-node", "compact-products", "new", true); err != nil {
		t.Fatal(err)
	}
	torn := newCatalogConsumer()
	for _, item := range []struct{ schema, source string }{{"OCM.fbs", "compact-products"}, {"CAT.fbs", "compact-index"}} {
		ex, err := catalogActualExport(store, filepath.Join(root, "session6-mirror-control"), DatasetPublicationRequest{Schema: item.schema, ProviderID: "synthetic-node", SourceName: item.source, FullCatalog: true})
		if err != nil {
			t.Fatal(err)
		}
		torn.apply(t, ex)
	}
	missing := 0
	for _, v := range torn.selected {
		if torn.products[v.CID] == nil {
			missing++
		}
	}
	if missing != 32 {
		t.Fatal("separate-source interruption control did not expose the expected missing selected products")
	}
	var retention []map[string]any
	for _, item := range []struct{ schema, source string }{{"OCM.fbs", "catalog-products"}, {"CAT.fbs", "catalog-index"}} {
		plan, err := store.PlanDatasetPublicationRetention(storage.DatasetPublicationLane{SchemaName: item.schema, ProviderID: "synthetic-node", SourceName: item.source}, "synthetic", storage.DatasetPublicationRetentionPolicy{KeepSeries: 0})
		if err != nil {
			t.Fatal(err)
		}
		retention = append(retention, map[string]any{"schema": item.schema, "retiredSeries": len(plan.RetiredSeries), "retiredRows": len(plan.RetireRows)})
	}
	catalogSave(t, "session6-compact-snapshot.json", map[string]any{"synthetic": true, "objects": 32, "dailyUpdatesAfterInitial": 3, "days": measurements, "compact": true, "separateCurrentSourceInterruptedMissingProducts": missing, "keepSeriesZeroRetention": retention})
	last := measurements[3]["OCM.fbs"].(map[string]any)["records"].(int)
	if last != 32 {
		t.Fatalf("compact snapshot gate: %d exported products for 32 selected objects after three daily updates", last)
	}
}

func TestCatalogSupersededBatchRecovery(t *testing.T) {
	root := t.TempDir()
	store := pairedStore(t, filepath.Join(root, "session6-supersede-store"))
	defer store.Close()
	catalogEnableRetention(t, store)
	s := &catalogExportService{ConcreteDatasetPublicationService: &ConcreteDatasetPublicationService{store: store}, dir: filepath.Join(root, "session6-supersede-exports")}
	observed := NewAutoPublisher(s, catalogRules())
	committed := 0
	for day := 0; day < 2; day++ {
		for _, item := range []struct{ schema, source string }{{"OCM", "catalog-products"}, {"CAT", "catalog-index"}} {
			rows := catalogReadShard(t, filepath.Join("testdata", "catalog-history", fmt.Sprintf("day%d-%s.sds", day, item.schema)))
			for i := range rows {
				rows[i] = rows[i][4:]
			}
			batch := fmt.Sprintf("day%d", day)
			if _, err := store.StoreBatchWithSourceTags(item.schema+".fbs", rows, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: item.source, BatchID: batch}); err != nil {
				t.Fatal(err)
			}
			committed++
			observed.ObserveIngest(IngestedBatch{Schema: item.schema + ".fbs", ProviderID: "synthetic-node", SourceName: item.source, BatchID: batch, Inserted: 32})
		}
	}
	// Restart before export. The two schemas have identical ingest ordering and
	// no explicit deletion/reconciliation: only the storage schema differs.
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store = pairedStore(t, filepath.Join(root, "session6-supersede-store"))
	defer store.Close()
	s.ConcreteDatasetPublicationService = &ConcreteDatasetPublicationService{store: store}
	restarted := NewAutoPublisher(s, catalogRules())
	catalogDrain(t, restarted)
	catBatches, ocmBatches := 0, 0
	for _, e := range s.events {
		if e.Request.Schema == "CAT.fbs" {
			catBatches++
		} else {
			ocmBatches++
		}
	}
	_, oldCATError := catalogActualExport(store, filepath.Join(root, "session6-supersede-old"), DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "day0"})
	_, oldOCMError := catalogActualExport(store, filepath.Join(root, "session6-supersede-old"), DatasetPublicationRequest{Schema: "OCM.fbs", ProviderID: "synthetic-node", SourceName: "catalog-products", BatchID: "day0"})
	catalogSave(t, "session6-supersede-recovery.json", map[string]any{"committedBatches": committed, "recoveredBatchExports": len(s.events), "catalogBatches": catBatches, "productBatches": ocmBatches, "oldCatalogExportAvailable": oldCATError == nil, "oldProductExportAvailable": oldOCMError == nil, "objects": 32})
	if oldOCMError != nil {
		t.Fatal("append-retained product control failed")
	}
	if catBatches != 2 || len(s.events) != committed {
		t.Fatalf("publication completeness gate: committed %d batches, exported %d after restart (CAT %d of 2; OCM %d of 2); old CAT export: %v", committed, len(s.events), catBatches, ocmBatches, oldCATError)
	}
}
