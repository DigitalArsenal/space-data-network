package protocol

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"path/filepath"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
	"github.com/spacedatanetwork/sdn-server/internal/syncwire"
)

func TestListPublishedAnnouncementContent(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "4")
	v, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatal(err)
	}
	s, err := storage.NewFlatSQLStore(t.TempDir(), v)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tags := storage.SourceTags{ProviderID: "synthetic", SourceName: "index", BatchID: "snapshot"}
	if _, err = s.StoreWithSourceTags("CAT.fbs", sds.NewCATBuilder().WithNoradCatID(900001).Build(), "synthetic", nil, tags); err != nil {
		t.Fatal(err)
	}
	ex, err := s.ExportDatasetWindow(t.TempDir(), storage.IndexedRecordQuery{SchemaName: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName})
	if err != nil {
		t.Fatal(err)
	}
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	m := &storage.DatasetPublicationManifest{Path: filepath.Join(t.TempDir(), "synthetic.dpm"), CID: ex.ShardCID, FileID: "synthetic-announcement"}
	pnm, err := storage.BuildDatasetPublicationPNM(m, storage.DatasetPublicationPNMOptions{SigningKey: key, PublishedAt: time.Unix(1700000000, 0)})
	if err != nil {
		t.Fatal(err)
	}
	pnmCID, err := s.Store("PNM.fbs", pnm, "synthetic", nil)
	if err != nil {
		t.Fatal(err)
	}
	pub := storage.DatasetShardPublication{SchemaName: "CAT.fbs", ProviderID: tags.ProviderID, SourceName: tags.SourceName, BatchID: tags.BatchID, QueryProfile: storage.DatasetPublicationQueryProfile, Limit: 1, RecordCount: 1, ByteCount: ex.ShardBytes, ShardCID: ex.ShardCID, IndexCID: ex.IndexCID, ManifestCID: m.CID, PNMCID: pnmCID, ShardSHA256: ex.ShardSHA256, IndexSHA256: ex.IndexSHA256, QuerySHA256: ex.QuerySHA256, ResultSHA256: ex.ResultSHA256, PublishedAt: time.Unix(1700000000, 0)}
	if err = s.UpsertDatasetShardPublication(pub); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = NewFlatSQLSyncHandler(s).handleListPublished(&out, syncRequest{schema: "CAT.fbs", providerID: tags.ProviderID, sourceName: tags.SourceName, limit: 1}); err != nil {
		t.Fatal(err)
	}
	page, _, err := syncwire.ReadPage(&out)
	if err != nil {
		t.Fatal(err)
	}
	columns, rows := syncwire.Rows(page)
	column, ok := columns["ANNOUNCEMENT_CID"]
	if !ok || len(rows) != 1 || rows[0][column].Text != pnmCID {
		t.Fatal("missing announcement content address")
	}
	legacy, err := syncwire.Segments(page)
	if err != nil || len(legacy) != 1 || legacy[0].PNMCID != pnmCID {
		t.Fatal("existing segment reader changed", err)
	}
	// The raw-record HTTP endpoint uses this exact lookup, without a scan.
	record, err := s.GetRawRecord("PNM.fbs", rows[0][column].Text)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(record.Data, pnm) {
		t.Fatal("content lookup changed signed bytes")
	}
	root := PNM.GetSizePrefixedRootAsPNM(record.Data, 0)
	signature, err := hex.DecodeString(string(root.SIGNATURE()))
	if err != nil {
		t.Fatal(err)
	}
	payload := append([]byte("SDN-DPM-PNM\x00"), root.FILE_ID()...)
	payload = append(payload, 0)
	payload = append(payload, root.CID()...)
	if string(root.SIGNATURE_TYPE()) != "Ed25519" || !ed25519.Verify(public, payload, signature) {
		t.Fatal("announcement does not verify")
	}
	payload[len(payload)-1] ^= 1
	if ed25519.Verify(public, payload, signature) {
		t.Fatal("tampered announcement verified")
	}
}
