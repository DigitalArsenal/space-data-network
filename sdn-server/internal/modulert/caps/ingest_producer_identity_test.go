package caps

// The producer of a record is the NODE that pulled it.
//
// Before this contract existed, storage.ingest_with_source wrote no producer
// identity at all: the store back-filled producer_peer_id with provider_id
// (storage/flatsql.go:1984), so every flow-ingested record claimed
// "space-data-network-02" — a provider NAME — as its producer PEER. Two
// consequences, both fatal to receipt: a peer importing the shard could not
// tell the records apart from its own, and the $APPS feed's remote-producer
// view correctly refused the row (apps.go:435 skips producer == provider_id),
// so a node whose store was being filled by a peer still displayed nothing
// under via:"pubsub".

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

const testNodePeerID = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"

func ingestOneRecord(t *testing.T, handler func(string, []byte) ([]byte, error), extra map[string]interface{}) map[string]interface{} {
	t.Helper()
	stream := sizePrefixedStream([][]byte{buildIngestTestOMM(t, 9101, 1700000000)})
	payload := map[string]interface{}{
		"schema":      "OMM.fbs",
		"provider_id": "space-data-network-02",
		"source_name": "provider-gp",
		"batch_id":    "batch-producer-1",
		"records":     base64.StdEncoding.EncodeToString(stream),
	}
	for k, v := range extra {
		payload[k] = v
	}
	body, _ := json.Marshal(payload)
	resp, err := handler("storage.ingest_with_source", body)
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	meta := decodeCapMeta(t, resp)
	if ok, _ := meta["ok"].(bool); !ok {
		t.Fatalf("ingest failed: %v", meta)
	}
	return meta
}

func TestIngestStampsTheNodesOwnPeerIDAsProducer(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{
		MinFreeDiskBytes: 1,
		NodePeerID:       testNodePeerID,
	})
	ingestOneRecord(t, handler, nil)

	rows, err := store.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no producer progress rows after ingest")
	}
	for _, row := range rows {
		if row.ProducerPeerID != testNodePeerID {
			t.Fatalf("producer_peer_id = %q, want this node's peer id %q", row.ProducerPeerID, testNodePeerID)
		}
		if row.ProducerPeerID == row.ProviderID {
			t.Fatalf("producer_peer_id fell back to provider_id (%q); the $APPS feed drops such rows", row.ProviderID)
		}
	}
}

// A module must not be able to attribute its writes to another node: the host
// supplies the identity and the payload is not consulted for it.
func TestIngestIgnoresAModuleSuppliedProducerPeerID(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{
		MinFreeDiskBytes: 1,
		NodePeerID:       testNodePeerID,
	})
	ingestOneRecord(t, handler, map[string]interface{}{
		"producer_peer_id": "16Uiu2HAmIMPERSONATINGSOMEONEELSE00000000000000000000",
	})

	rows, err := store.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("no producer progress rows after ingest")
	}
	for _, row := range rows {
		if row.ProducerPeerID != testNodePeerID {
			t.Fatalf("producer_peer_id = %q; a module forged its producer identity", row.ProducerPeerID)
		}
	}
}

// An unwired NodePeerID degrades to the pre-existing behaviour rather than
// failing the ingest — attribution is worth less than the data.
func TestIngestWithoutNodePeerIDStillStores(t *testing.T) {
	handler, store := newIngestTestHandler(t, StorageCapOptions{MinFreeDiskBytes: 1})
	ingestOneRecord(t, handler, nil)

	rows, err := store.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("ingest without a node peer id stored nothing")
	}
}
