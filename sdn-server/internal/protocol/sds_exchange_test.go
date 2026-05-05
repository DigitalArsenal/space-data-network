package protocol

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/DigitalArsenal/spacedatastandards.org/lib/go/PNM"
	flatbuffers "github.com/google/flatbuffers/go"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

func TestHandlePubSubMessageStoresPNMAnnouncementsFromDatasetSchemaTopic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "sdn-protocol-pnm-*")
	if err != nil {
		t.Fatalf("MkdirTemp failed: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("NewValidator failed: %v", err)
	}
	store, err := storage.NewFlatSQLStore(filepath.Join(tmpDir, "db"), validator)
	if err != nil {
		t.Fatalf("NewFlatSQLStore failed: %v", err)
	}
	defer store.Close()

	pnmBytes := buildProtocolTestPNM(t, "bafymanifest", "DPM")
	handler := NewSDSExchangeHandler(store, validator)
	if err := handler.HandlePubSubMessage("OMM.fbs", pnmBytes, peer.ID("12D3KooWDatasetPublisher")); err != nil {
		t.Fatalf("HandlePubSubMessage failed: %v", err)
	}

	pnmRecords, err := store.QueryAll("PNM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll PNM failed: %v", err)
	}
	if len(pnmRecords) != 1 || !reflect.DeepEqual(pnmRecords[0], pnmBytes) {
		t.Fatalf("PNM records = %d, first matches original: %v", len(pnmRecords), len(pnmRecords) == 1 && reflect.DeepEqual(pnmRecords[0], pnmBytes))
	}

	ommRecords, err := store.QueryAll("OMM.fbs", 10)
	if err != nil {
		t.Fatalf("QueryAll OMM failed: %v", err)
	}
	if len(ommRecords) != 0 {
		t.Fatalf("PNM announcement should not be stored as OMM data, got %d OMM records", len(ommRecords))
	}
}

func buildProtocolTestPNM(t *testing.T, cid, fileID string) []byte {
	t.Helper()

	builder := flatbuffers.NewBuilder(256)
	cidOffset := builder.CreateString(cid)
	fileIDOffset := builder.CreateString(fileID)
	timestampOffset := builder.CreateString(time.Now().UTC().Format(time.RFC3339))

	PNM.PNMStart(builder)
	PNM.PNMAddCID(builder, cidOffset)
	PNM.PNMAddFILE_ID(builder, fileIDOffset)
	PNM.PNMAddPUBLISH_TIMESTAMP(builder, timestampOffset)
	pnm := PNM.PNMEnd(builder)
	PNM.FinishSizePrefixedPNMBuffer(builder, pnm)
	return append([]byte(nil), builder.FinishedBytes()...)
}
