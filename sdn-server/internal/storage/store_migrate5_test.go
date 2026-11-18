package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4"
	"github.com/spacedatanetwork/sdn-server/internal/storage/format4/marker"
)

// The fixture and its expected answers were written and read by the real
// FlatSQL 3.7.2 engine. Its provenance and generator are beside the fixture.
func TestFormat5FirstBootPreservesFormat4(t *testing.T) {
	wasm := format4TestWasm(t)
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	cache := format4SharedAOTDir(t, wasm)
	previous := format4OpenEngine
	format4OpenEngine = func(ctx context.Context, opt format4.Options) (format4.API, error) {
		opt.Wasm, opt.CompileOnMiss, opt.AOTCacheDir, opt.Cores = wasm, true, cache, 2
		return previous(ctx, opt)
	}
	t.Cleanup(func() { format4OpenEngine = previous })
	t.Setenv(format4.FormatEnv, "5")
	t.Setenv(checkpointIntervalEnv, "0")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	fixture := filepath.Join("testdata", "format4-3.7.2")
	root := t.TempDir()
	if err := os.CopyFS(root, os.DirFS(filepath.Join(fixture, "store"))); err != nil {
		t.Fatal(err)
	}
	before := map[string][32]byte{}
	if err := filepath.WalkDir(filepath.Join(root, marker.LegacyDir), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil {
			before[path] = sha256.Sum256(data)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
	var expected struct {
		OMM        []format4.Rec              `json:"records_OMM"`
		IQC        []format4.Rec              `json:"records_IQC"`
		OMMHead    format4.Head               `json:"head_OMM"`
		IQCHead    format4.Head               `json:"head_IQC"`
		OMMTags    []format4.TagRow           `json:"tags_OMM"`
		IQCTags    []format4.TagRow           `json:"tags_IQC"`
		Types      []format4.TypeSummary      `json:"types"`
		Partitions []format4.PartitionSummary `json:"partitions"`
		Lanes      []format4.Lane             `json:"lanes"`
	}
	data, err := os.ReadFile(filepath.Join(fixture, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	pub := DatasetShardPublication{
		SchemaName: "OMM.fbs", ProviderID: "fixture-provider", SourceName: "fixture-source", BatchID: "batch-a",
		QueryProfile: DatasetPublicationQueryProfile, Limit: 3, RecordCount: 3, ByteCount: 417,
		ShardCID: "bafkshard", IndexCID: "bafkindex", ManifestCID: "bafkmanifest", PNMCID: "fixture-pnm",
		ShardSHA256: "fixture-shard", IndexSHA256: "fixture-index", QuerySHA256: "fixture-query", ResultSHA256: "fixture-result",
		FeedSequence: 7, PreviousHead: "fixture-previous", FeedHead: "fixture-head", PublishedAt: time.Unix(1700000040, 0).UTC(),
	}
	auxPath := filepath.Join(root, auxiliaryMetadataFileName)
	aux, err := openAuxiliaryMetadataStore(auxPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := aux.Append(auxiliaryMetadataEvent{Kind: auxiliaryEventDatasetShardPublicationUpsert, DatasetShardPublication: &pub}); err != nil {
		t.Fatal(err)
	}
	if err := aux.Close(); err != nil {
		t.Fatal(err)
	}
	auxBefore, err := os.ReadFile(auxPath)
	if err != nil {
		t.Fatal(err)
	}

	// A corrupt source catalog never publishes fsql5 and retains format 4.
	broken := t.TempDir()
	if err := os.CopyFS(broken, os.DirFS(filepath.Join(fixture, "store"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, marker.LegacyDir, "T", "TYPES"), []byte{1, 2, 3}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStore5(ctx, broken); err == nil {
		t.Fatal("corrupt catalog was activated")
	}
	if _, err := os.Stat(filepath.Join(broken, marker.Dir)); !os.IsNotExist(err) {
		t.Fatalf("failed conversion published a target: %v", err)
	}
	if m, err := marker.ReadLegacy(broken); err != nil || !m.ActivatedAs(4) {
		t.Fatalf("failed conversion changed source markers: %+v %v", m, err)
	}
	// A process killed while copying leaves an owned unpublished candidate.
	// First boot must reclaim it and convert from the unchanged source.
	legacy, err := marker.ReadLegacy(root)
	if err != nil {
		t.Fatal(err)
	}
	pending := filepath.Join(root, ".store-format5-pending")
	if err := os.Mkdir(pending, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "OWNER"), []byte(fmt.Sprintf("SDN format-5 candidate v1\nsource=%x\n", legacy.UUID)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pending, "partial-copy"), []byte{1, 2, 3}, 0o600); err != nil {
		t.Fatal(err)
	}

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewFlatSQLStore(root, validator, WithDeferredBootRebuilds())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if m, err := marker.Read(root); err != nil || !m.Activated() {
		t.Fatalf("first boot did not activate format 5: %+v %v", m, err)
	} else if m.UUID != legacy.UUID || m.CreatedMs != legacy.CreatedMs || m.GseqFloor != legacy.GseqFloor || m.MigratedFrom != 4 {
		t.Fatalf("conversion changed store identity, creation time or sequence floor: got %+v, original %+v", m, legacy)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Fatalf("completed first boot left a candidate directory: %v", err)
	}
	engine := store.f4.api()
	for _, check := range []struct {
		typ     string
		records []format4.Rec
		tags    []format4.TagRow
		head    format4.Head
	}{
		{"OMM", expected.OMM, expected.OMMTags, expected.OMMHead}, {"IQC", expected.IQC, expected.IQCTags, expected.IQCHead},
	} {
		seen := map[string]bool{}
		var cids []string
		for _, record := range check.records {
			if !seen[record.CID] {
				cids = append(cids, record.CID)
				seen[record.CID] = true
			}
		}
		records, err := engine.Get(ctx, check.typ, cids, true, true)
		if err != nil {
			t.Fatal(err)
		}
		migration5EqualRows(t, check.typ+" record fields and payloads", records, check.records)
		tags, err := engine.Tags(ctx, check.typ, cids)
		if err != nil {
			t.Fatal(err)
		}
		migration5EqualRows(t, check.typ+" provenance", tags, check.tags)
		head, err := engine.Head(ctx, format4.Query{Type: check.typ})
		if err != nil || head != check.head {
			t.Fatalf("%s head: got %+v, want %+v: %v", check.typ, head, check.head, err)
		}
	}
	types, err := engine.Types(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var held []format4.TypeSummary
	for _, typ := range types {
		if typ.Records > 0 {
			held = append(held, typ)
		}
	}
	migration5EqualRows(t, "type counters and sequence highs", held, expected.Types)
	parts, err := engine.Partitions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	migration5EqualRows(t, "producer counters", parts, expected.Partitions)
	lanes, err := engine.Lanes(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	migration5EqualRows(t, "historical instance counters and timestamps", lanes, expected.Lanes)
	got, found, err := store.FindDatasetShardPublication(DatasetShardPublicationQuery{
		SchemaName: pub.SchemaName, ProviderID: pub.ProviderID, SourceName: pub.SourceName, BatchID: pub.BatchID,
		QueryProfile: pub.QueryProfile, Limit: pub.Limit, RecordCount: pub.RecordCount,
	})
	if err != nil || !found || !reflect.DeepEqual(got, pub) {
		t.Fatalf("publication catalog: got %+v, want %+v, found=%v: %v", got, pub, found, err)
	}
	for path, digest := range before {
		data, err := os.ReadFile(path)
		if err != nil || sha256.Sum256(data) != digest {
			t.Fatalf("rollback source changed: %s: %v", path, err)
		}
	}
	backup, err := os.ReadFile(filepath.Join(root, marker.LegacyDir, "rollback-auxiliary.flatsqlmeta"))
	if err != nil || !bytes.Equal(backup, auxBefore) {
		t.Fatalf("auxiliary rollback copy differs: %v", err)
	}
	var identity [32]byte
	for i := range identity {
		identity[i] = 0x5a
	}
	record := expected.IQC[0]
	// The engine reports IDENT_DUP when another CID carries the held identity.
	// Repeating the same CID is an ordinary DUP even when its identity exists.
	repeatedCapture := bytes.Replace(record.Data, []byte("capture-1"), []byte("capture-2"), 1)
	if bytes.Equal(repeatedCapture, record.Data) {
		t.Fatal("capture fixture did not contain its synthetic identifier")
	}
	out, err := engine.Put(ctx, format4.Batch{Type: "IQC", Peer: record.Peer, Tags: []format4.Tag{expected.IQCTags[0].Tag},
		Records: []format4.In{{CID: ComputeCID(repeatedCapture), Plain: repeatedCapture, TS: record.TS, Ident: &identity}}})
	if err != nil || len(out) != 1 || out[0].Action != format4.ActIdentDup || out[0].Seq != 211 {
		t.Fatalf("ingest identity was not preserved: %+v %v", out, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := MigrateStore5(ctx, root); err != nil {
		t.Fatalf("completed conversion is not idempotent: %v", err)
	}
	// Normal boot registers unused standard schemas too. The converted store
	// must reopen with those specs even when no feed/index was ever created.
	// Remove only GJN's empty registry to reproduce that historical layout.
	unused := filepath.Join(root, marker.Dir, "P", "GJN")
	if entries, err := os.ReadDir(unused); err == nil {
		if len(entries) != 0 {
			t.Fatal("unused schema unexpectedly has feed data")
		}
		if err := os.Remove(unused); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(root, marker.Dir, "T", "GJN.idx") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	reopened, err := NewFlatSQLStore(root, validator, WithDeferredBootRebuilds())
	if err != nil {
		t.Fatalf("reopen converted store with registered unused schemas: %v", err)
	}
	defer reopened.Close()
	for typ, want := range map[string]format4.Head{"OMM": expected.OMMHead, "IQC": expected.IQCHead} {
		got, err := reopened.f4.api().Head(ctx, format4.Query{Type: typ})
		if err != nil || got != want {
			t.Fatalf("%s head after reopen: got %+v, want %+v: %v", typ, got, want, err)
		}
	}
}

func migration5EqualRows(t *testing.T, label string, got, want any) {
	t.Helper()
	canonical := func(value any) []string {
		v := reflect.ValueOf(value)
		rows := make([]string, v.Len())
		for i := range rows {
			encoded, err := json.Marshal(v.Index(i).Interface())
			if err != nil {
				t.Fatal(err)
			}
			rows[i] = string(encoded)
		}
		sort.Strings(rows)
		return rows
	}
	if !reflect.DeepEqual(canonical(got), canonical(want)) {
		t.Fatalf("%s changed: got %+v; want %+v", label, got, want)
	}
}
