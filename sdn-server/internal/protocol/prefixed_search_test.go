package protocol_test

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/spacedatanetwork/sdn-server/internal/modulert"
	"github.com/spacedatanetwork/sdn-server/internal/modulert/caps"
	"github.com/spacedatanetwork/sdn-server/internal/protocol"
	"github.com/spacedatanetwork/sdn-server/internal/sds"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// Each admission path passes one complete size-prefixed record to format 5.
// Search must index that envelope without changing the stored bytes or CID.
func TestFormat5PrefixedIngressSearch(t *testing.T) {
	t.Setenv("SDN_STORE_FORMAT", "5")
	for _, path := range []string{"pubsub", "module-write", "node-builder"} {
		t.Run(path, func(t *testing.T) {
			validator, err := sds.NewValidator(nil)
			if err != nil {
				t.Fatal(err)
			}
			store, err := storage.NewFlatSQLStore(t.TempDir(), validator)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			raw := sds.NewCATBuilder().WithNoradCatID(999991).WithObjectName("Synthetic prefixprobe").Build()
			if int(binary.LittleEndian.Uint32(raw)) != len(raw)-4 {
				t.Fatal("fixture has no size prefix")
			}
			wantCID := storage.ComputeCID(raw)
			var gotCID string
			handler := protocol.NewSDSExchangeHandler(store, validator)
			handler.SetPushAuthorizer(protocol.AllowAnyPush)
			switch path {
			case "pubsub":
				if err := handler.HandlePubSubMessage("CAT.fbs", raw, peer.ID("synthetic-peer")); err != nil {
					t.Fatal(err)
				}
				gotCID = wantCID
			case "module-write":
				cap := caps.NewStorageCapFactory(store)(nil, modulert.NewHostBridge(nil, []string{"storage_write"}))
				request, err := json.Marshal(map[string]string{"schema": "CAT.fbs", "data": base64.StdEncoding.EncodeToString(raw)})
				if err != nil {
					t.Fatal(err)
				}
				response, err := cap("storage.write", request)
				if err != nil {
					t.Fatal(err)
				}
				var result struct {
					OK   bool `json:"ok"`
					Data struct {
						CID string `json:"cid"`
					} `json:"result"`
				}
				if err := json.Unmarshal(response, &result); err != nil || !result.OK {
					t.Fatalf("storage.write: %s (%v)", response, err)
				}
				gotCID = result.Data.CID
			case "node-builder":
				gotCID, err = store.Store("CAT.fbs", raw, "synthetic-builder", nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			if gotCID != wantCID {
				t.Fatalf("CID changed: got %q want %q", gotCID, wantCID)
			}
			stored, err := store.Get("CAT.fbs", wantCID)
			if err != nil || !bytes.Equal(stored, raw) {
				t.Fatalf("stored bytes changed: %v", err)
			}
			deadline := time.Now().Add(time.Minute)
			for {
				records, err := store.QueryRawRecords(storage.RawRecordQuery{SchemaName: "CAT.fbs", Search: "prefixprobe", Limit: 10})
				if errors.Is(err, storage.ErrSearchIndexBuilding) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				if err != nil {
					t.Fatal(err)
				}
				if len(records) != 1 || records[0].CID != wantCID || !bytes.Equal(records[0].Data, raw) {
					t.Fatalf("prefixed search returned %d records; want the original record", len(records))
				}
				break
			}
		})
	}
}
