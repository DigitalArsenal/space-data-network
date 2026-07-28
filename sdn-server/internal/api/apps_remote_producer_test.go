package api

import (
	"testing"
	"time"

	"github.com/spacedatanetwork/sdn-server/internal/sourcemetrics"
	"github.com/spacedatanetwork/sdn-server/internal/storage"
)

// fakeProducerProgress stands in for the record store's per-producer read.
type fakeProducerProgress struct {
	rows  []storage.ProducerSourceProgress
	err   error
	delay time.Duration
}

func (f *fakeProducerProgress) ProducerSourceProgress() ([]storage.ProducerSourceProgress, error) {
	if f.delay > 0 {
		time.Sleep(f.delay)
	}
	return f.rows, f.err
}

const (
	remoteProducerPeer = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
	selfProducerPeer   = "12D3KooWKh3diobFtzBk2RvdwR4TuFB8nkU31th8Mc2iKb7bZBWs"
)

// A node that RECEIVES a catalog over pubsub is doing its job, and its board
// must say so. Before this, the feed reported only what the node pulled itself:
// a receiving node showed an empty board while a peer filled its store, which
// reads exactly like a broken node.
func TestAppsFeedReportsDataReceivedFromRemoteProducers(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	h := NewAppsHandler(nil, ledger.Sources, nil, nil).WithSelfPeerID(selfProducerPeer)
	h.producers = &fakeProducerProgress{rows: []storage.ProducerSourceProgress{{
		ProducerPeerID: remoteProducerPeer,
		SchemaName:     "CAT.fbs",
		ProviderID:     "celestrak",
		SourceName:     "celestrak-satcat",
		LastBatchID:    "b-42",
		BatchCount:     3,
		Count:          138126,
		TotalBytes:     41_000_000,
		FirstSeenUnix:  1785000000,
		LastSeenUnix:   1785003600,
	}}}

	feed := appsFeed(t, h)
	srcs, _ := feed["sources"].([]interface{})
	if len(srcs) != 1 {
		t.Fatalf("sources = %d, want 1 received row (the ledger is empty; the store is not)", len(srcs))
	}
	src := srcs[0].(map[string]interface{})

	if src["via"] != viaPubsub {
		t.Fatalf("via = %v, want %q — a received row must be distinguishable from a local pull", src["via"], viaPubsub)
	}
	if src["producer_peer_id"] != remoteProducerPeer {
		t.Fatalf("producer_peer_id = %v, want the producing peer %q", src["producer_peer_id"], remoteProducerPeer)
	}
	if src["record_count"] != float64(138126) {
		t.Fatalf("record_count = %v, want the cumulative received count", src["record_count"])
	}
	if src["schema_name"] != "CAT.fbs" {
		t.Fatalf("schema_name = %v, want the received record type", src["schema_name"])
	}
	if src["last_seen_at"] == nil || src["last_seen_at"] == "" {
		t.Fatalf("last_seen_at missing: %v — 'when did data last arrive' is the whole question", src)
	}
}

// Only a real, foreign producer earns a received row. This node's own
// publications, the store's provider-id back-fill for records that carry no
// producer, and locally synthesized rows are all NOT peers — reporting any of
// them as one would invent a node that does not exist.
//
// A lane this node also pulls itself is NOT excluded: two nodes retrieving the
// same public source is normal, and the peer's contribution is a separate fact
// that survives this node dropping the source.
func TestAppsFeedNeverInventsARemoteProducer(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID:      "com.digitalarsenal.flows.celestrak-satcat-ingest",
		ProviderID: "celestrak", SourceName: "celestrak-satcat",
		SourceURL: "https://celestrak.org/pub/satcat.csv", Schema: "CAT.fbs",
		BatchID: "local-1", Records: 138126, Inserted: 138126,
	})

	h := NewAppsHandler(nil, ledger.Sources, nil, nil).WithSelfPeerID(selfProducerPeer)
	h.producers = &fakeProducerProgress{rows: []storage.ProducerSourceProgress{
		// Same lane, seen under a peer's identity: this node pulled it.
		{ProducerPeerID: remoteProducerPeer, SchemaName: "CAT.fbs",
			ProviderID: "celestrak", SourceName: "celestrak-satcat", Count: 138126},
		// This node's own publication of a different lane: also not "received".
		{ProducerPeerID: selfProducerPeer, SchemaName: "OMM.fbs",
			ProviderID: "celestrak", SourceName: "celestrak-gp", Count: 10847},
		// Store back-fill: producer defaults to the provider when unknown.
		{ProducerPeerID: "celestrak", SchemaName: "SPW.fbs",
			ProviderID: "celestrak", SourceName: "celestrak-spw", Count: 25363},
		// Locally synthesized rows are marked, not attributed to a peer.
		{ProducerPeerID: localProducerMarker, SchemaName: "EPM.fbs",
			ProviderID: "self", SourceName: "epm", Count: 1},
	}}

	feed := appsFeed(t, h)
	srcs, _ := feed["sources"].([]interface{})
	if len(srcs) != 2 {
		t.Fatalf("sources = %d, want 2: this node's ledger row plus the one genuine peer contribution", len(srcs))
	}

	byVia := map[string]map[string]interface{}{}
	for _, raw := range srcs {
		src := raw.(map[string]interface{})
		via, _ := src["via"].(string)
		byVia[via] = src
	}
	local, ok := byVia[viaLocal]
	if !ok {
		t.Fatalf("no local row served: %v", srcs)
	}
	if local["source_name"] != "celestrak-satcat" {
		t.Fatalf("local row = %v, want this node's own pull of celestrak-satcat", local)
	}
	remote, ok := byVia[viaPubsub]
	if !ok {
		t.Fatalf("the peer's contribution to a lane this node also pulls was dropped: %v", srcs)
	}
	if remote["producer_peer_id"] != remoteProducerPeer {
		t.Fatalf("received row attributed to %v, want the real peer %q", remote["producer_peer_id"], remoteProducerPeer)
	}
	// The self row, the provider-id back-fill and the local marker must all be
	// absent: no invented peers.
	for _, raw := range srcs {
		src := raw.(map[string]interface{})
		producer, _ := src["producer_peer_id"].(string)
		if producer == selfProducerPeer || producer == "celestrak" || producer == localProducerMarker {
			t.Fatalf("invented a remote producer from %q: %v", producer, src)
		}
	}
}

// The received-data read touches the same single-writer store an ingest
// occupies for tens of minutes. It gets a budget for the same reason the PNM
// refresh does: this endpoint is asked whether the node is working PRECISELY
// when the node is busiest, and it must answer.
func TestAppsFeedAnswersWhenTheProducerReadIsSlow(t *testing.T) {
	ledger, err := sourcemetrics.Open(t.TempDir())
	if err != nil {
		t.Fatalf("sourcemetrics.Open: %v", err)
	}
	defer ledger.Close()

	ledger.RecordIngest(sourcemetrics.Ingest{
		AppID: "app", ProviderID: "p", SourceName: "s",
		Schema: "OMM.fbs", BatchID: "b", Records: 1, Inserted: 1,
	})

	h := NewAppsHandler(nil, ledger.Sources, nil, nil)
	h.producers = &fakeProducerProgress{delay: 5 * time.Second, rows: []storage.ProducerSourceProgress{{
		ProducerPeerID: remoteProducerPeer, SchemaName: "CAT.fbs",
		ProviderID: "celestrak", SourceName: "celestrak-satcat", Count: 1,
	}}}

	start := time.Now()
	feed := appsFeed(t, h)
	elapsed := time.Since(start)

	if elapsed > 3*time.Second {
		t.Fatalf("feed took %s with a blocked store; the received-data read must be abandoned at its budget", elapsed)
	}
	srcs, _ := feed["sources"].([]interface{})
	if len(srcs) != 1 {
		t.Fatalf("sources = %d, want the local ledger row served without the abandoned read", len(srcs))
	}
}
