package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/CAT"
	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/OCM"
	flatbuffers "github.com/google/flatbuffers/go"
	cidlib "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"github.com/spacedatanetwork/sdn-server/internal/pinpolicy"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// One product a catalog row lists, as the catalog builder writes a row with
// catalog.object_product_set gp-xp-ap: kind, record and source side by side.
type catalogRowProduct struct {
	kind   int8
	schema string
	source string
	record []byte // size-prefixed, the bytes its RECORD_CID names
}

func catalogRecordCID(t *testing.T, framed []byte) string {
	t.Helper()
	h, err := mh.Sum(framed, mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cidlib.NewCidV1(cidlib.Raw, h).String()
}

func catalogRow(t *testing.T, norad uint32, asOf string, products []catalogRowProduct) []byte {
	t.Helper()
	b := flatbuffers.NewBuilder(512)
	offsets := make([]flatbuffers.UOffsetT, len(products))
	for i, p := range products {
		cid, source, epoch := b.CreateString(catalogRecordCID(t, p.record)), b.CreateString(p.source), b.CreateString(asOf)
		reason := b.CreateString(fmt.Sprintf("data-source=%s; data-kind=fixture;", p.source))
		CAT.CATProductStart(b)
		switch p.kind { // the generated enum type is unexported: name its values
		case 1:
			CAT.CATProductAddKIND(b, 1) // Sgp4Elements
		case 2:
			CAT.CATProductAddKIND(b, 2) // Sgp4XpElements
		case 4:
			CAT.CATProductAddKIND(b, 4) // ApElements
		default:
			t.Fatalf("fixture kind %d", p.kind)
		}
		CAT.CATProductAddRECORD_CID(b, cid)
		CAT.CATProductAddSOURCE_ID(b, source)
		CAT.CATProductAddEPOCH(b, epoch)
		CAT.CATProductAddSELECTION_REASON(b, reason)
		offsets[i] = CAT.CATProductEnd(b)
	}
	CAT.CATStartPRODUCTSVector(b, len(offsets))
	for i := len(offsets) - 1; i >= 0; i-- {
		b.PrependUOffsetT(offsets[i])
	}
	list := b.EndVector(len(offsets))
	name, as := b.CreateString(fmt.Sprintf("FIXTURE %d", norad)), b.CreateString(asOf)
	CAT.CATStart(b)
	CAT.CATAddOBJECT_NAME(b, name)
	CAT.CATAddNORAD_CAT_ID(b, norad)
	CAT.CATAddPRODUCTS(b, list)
	CAT.CATAddPRODUCTS_AS_OF(b, as)
	CAT.FinishSizePrefixedCATBuffer(b, CAT.CATEnd(b))
	return bytes.Clone(b.FinishedBytes())
}

// The fixture catalog: every object lists a GP OMM (an $OMM, SGP4 theory), an
// SGP4-XP set (an $OCM whose MEAN_ELEMENTS theory is SGP4XP) and an SDN-AP
// record (an $OCM whose MEAN_ELEMENTS theory is AP), in that order.
func catalogThreeProductRows(t *testing.T, objects int) (rows [][]byte, products map[string][]catalogRowProduct) {
	t.Helper()
	seed, err := os.ReadFile(filepath.Join("testdata", "catalog-history", "seed-OCM.sds"))
	if err != nil {
		t.Fatal(err)
	}
	const asOf = "2026-11-20T00:00:00.000Z"
	products = map[string][]catalogRowProduct{}
	for i := 0; i < objects; i++ {
		norad := uint32(920000 + i)
		gp := sds.NewOMMBuilder().WithNoradCatID(norad).WithObjectName(fmt.Sprintf("FIXTURE %d", norad)).WithEpoch(asOf).WithMeanMotion(15.5).WithInclination(51.6).Build()
		// The seed is an SDN-AP set; its SGP4-XP twin differs in theory only.
		ap, xp := bytes.Clone(seed), bytes.Clone(seed)
		for _, record := range [][]byte{ap, xp} {
			if !OCM.GetRootAsOCM(record, 4).METADATA(nil).MutateNORAD_CAT_ID(norad) {
				t.Fatal("missing synthetic identifier")
			}
		}
		elements := OCM.GetRootAsOCM(xp, 4).MEAN_ELEMENTS(nil)
		if elements == nil || OCM.EnumNamesmeanElementSource[elements.MEAN_ELEMENT_THEORY()] != "AP" || !elements.MutateMEAN_ELEMENT_THEORY(1) || OCM.EnumNamesmeanElementSource[elements.MEAN_ELEMENT_THEORY()] != "SGP4XP" {
			t.Fatal("the seed OCM must store an AP theory to make its SGP4-XP twin")
		}
		row := []catalogRowProduct{
			{kind: 1, schema: "OMM.fbs", source: "public-gp", record: gp},
			{kind: 2, schema: "OCM.fbs", source: "operator-ephemeris-xp", record: xp},
			{kind: 4, schema: "OCM.fbs", source: "operator-ephemeris-ap", record: ap},
		}
		for _, p := range row {
			products[p.schema] = append(products[p.schema], p)
		}
		rows = append(rows, catalogRow(t, norad, asOf, row))
	}
	return rows, products
}

// TestCatalogThreeProductRowsPublishAndReadBack: a whole catalog whose rows
// each list three products of two standards publishes through the production
// signer and exporter, one series per product standard before the index, and
// a separate consumer reads back every row with its products in order, each
// one resolved in its own standard. The object limit is the catalog's own
// size, so the three products per object pass only on the per-product
// allowance.
func TestCatalogThreeProductRowsPublishAndReadBack(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	const objects = 32
	ctx := context.Background()
	root := t.TempDir()
	producer := pairedStore(t, filepath.Join(root, "producer"))
	defer producer.Close()
	if err := producer.ConfigurePublicationSources([]storage.PublicationSource{
		{Schema: "CAT.fbs", Provider: "synthetic-node", Source: "catalog-index", ProductSource: "catalog-products", CatalogObjectLimit: objects},
		{Schema: "OCM.fbs", Provider: "synthetic-node", Source: "catalog-products"},
		{Schema: "OMM.fbs", Provider: "synthetic-node", Source: "catalog-products"},
	}); err != nil {
		t.Fatal(err)
	}
	consumer := pairedStore(t, filepath.Join(root, "consumer"))
	defer consumer.Close()
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	trusted := mustLibp2pEd25519(t, public)
	kubo := newDatasetPublicationKuboTestServer(t, map[string][]byte{})
	defer kubo.Close()
	fetch := func(ctx context.Context, id string) ([]byte, error) {
		return storage.FetchIPFSBlockByCID(ctx, kubo.URL, id)
	}
	var received []string
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err == nil {
			var result *storage.DatasetPublicationReplayResult
			result, err = storage.MaterializeDatasetPublication(r.Context(), consumer, storage.DatasetPublicationReplayOptions{
				PNM: body, ProviderPublicKey: trusted, FetchByCID: fetch, WorkDir: filepath.Join(root, "replay"),
			})
			if err == nil {
				received = append(received, result.SchemaName)
			}
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer receiver.Close()
	service := NewConcreteDatasetPublicationService(producer, &signedCatalogLoopback{url: receiver.URL}, private, "synthetic-node", "bafy-provider-epm", kubo.URL, filepath.Join(root, "exports"))
	pins, err := pinpolicy.New(kubo.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	service.SetPinPolicy(pins)

	rows, products := catalogThreeProductRows(t, objects)
	// Products of each standard first, then the terminal index batch, as the
	// builder stores a generation.
	for _, schema := range []string{"OMM.fbs", "OCM.fbs"} {
		var records [][]byte
		for _, p := range products[schema] {
			records = append(records, p.record[4:])
		}
		if _, err := producer.StoreBatchWithSourceTags(schema, records, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: "catalog-products", BatchID: "catalog-products-" + schema}); err != nil {
			t.Fatalf("store %s products: %v", schema, err)
		}
	}
	index := make([][]byte, len(rows))
	for i, row := range rows {
		index[i] = row[4:]
	}
	if _, err := producer.StoreBatchWithSourceTags("CAT.fbs", index, "synthetic", nil, storage.SourceTags{ProviderID: "synthetic-node", SourceName: "catalog-index", BatchID: "catalog-index:complete"}); err != nil {
		t.Fatalf("store the terminal index batch: %v", err)
	}
	if _, err := service.PublishDatasetUpdate(ctx, DatasetPublicationRequest{Schema: "CAT.fbs", ProviderID: "synthetic-node", SourceName: "catalog-index", FullCatalog: true}); err != nil {
		t.Fatalf("publish the catalog snapshot: %v", err)
	}
	if fmt.Sprint(received) != "[OCM.fbs OMM.fbs CAT.fbs]" {
		t.Fatalf("consumer received %v, want each product standard before the index", received)
	}

	// Read back from the consumer: every record of every standard, by CID.
	readBack := map[string]map[string][]byte{}
	for _, schema := range []string{"CAT.fbs", "OCM.fbs", "OMM.fbs"} {
		source := "catalog-products"
		if schema == "CAT.fbs" {
			source = "catalog-index"
		}
		ex, err := consumer.ExportDatasetWindow(t.TempDir(), storage.IndexedRecordQuery{SchemaName: schema, ProviderID: "synthetic-node", SourceName: source, Limit: 1000})
		if err != nil {
			t.Fatalf("read back %s: %v", schema, err)
		}
		readBack[schema] = map[string][]byte{}
		for _, framed := range catalogReadShard(t, ex.ShardPath) {
			readBack[schema][catalogRecordCID(t, framed)] = framed
		}
	}
	if len(readBack["CAT.fbs"]) != objects || len(readBack["OCM.fbs"]) != 2*objects || len(readBack["OMM.fbs"]) != objects {
		t.Fatalf("read back %d rows, %d OCM and %d OMM products; want %d, %d, %d", len(readBack["CAT.fbs"]), len(readBack["OCM.fbs"]), len(readBack["OMM.fbs"]), objects, 2*objects, objects)
	}
	for _, row := range rows {
		got := readBack["CAT.fbs"][catalogRecordCID(t, row)]
		if !bytes.Equal(got, row) {
			t.Fatal("a catalog row read back differs from the row the producer wrote")
		}
		cat := CAT.GetSizePrefixedRootAsCAT(got, 0)
		if cat.PRODUCTSLength() != 3 {
			t.Fatalf("row %d lists %d products, want 3", cat.NORAD_CAT_ID(), cat.PRODUCTSLength())
		}
		for i, want := range []struct {
			kind           int8
			schema, source string
		}{{1, "OMM.fbs", "public-gp"}, {2, "OCM.fbs", "operator-ephemeris-xp"}, {4, "OCM.fbs", "operator-ephemeris-ap"}} {
			var p CAT.CATProduct
			cat.PRODUCTS(&p, i)
			if int8(p.KIND()) != want.kind || string(p.SOURCE_ID()) != want.source {
				t.Fatalf("row %d product %d: kind %d source %q, want %d %q", cat.NORAD_CAT_ID(), i, p.KIND(), p.SOURCE_ID(), want.kind, want.source)
			}
			record := readBack[want.schema][string(p.RECORD_CID())]
			if record == nil {
				t.Fatalf("row %d product %d (%s) does not resolve in the consumer's %s records", cat.NORAD_CAT_ID(), i, p.RECORD_CID(), want.schema)
			}
			if string(record[8:12]) != "$"+want.schema[:3] {
				t.Fatalf("row %d product %d resolves to a %q record, want $%s", cat.NORAD_CAT_ID(), i, record[8:12], want.schema[:3])
			}
		}
	}
}
