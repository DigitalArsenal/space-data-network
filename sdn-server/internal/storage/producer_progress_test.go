package storage

import (
	"os"
	"testing"

	"github.com/spacedatanetwork/sdn-server/internal/sds"
)

// SourceBatchProgress answers "how much data is here" and deliberately merges
// producers. This proves the other question is answerable too: WHO sent it.
// Without that, a node receiving a peer's catalog over pubsub can only report
// what it pulled itself, and looks idle while its store fills.
func TestFlatSQLStoreProducerSourceProgressSeparatesProducers(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "flatsql-producer-progress-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	validator, err := sds.NewValidator(nil)
	if err != nil {
		t.Fatalf("Failed to create validator: %v", err)
	}
	store, err := NewFlatSQLStore(tmpDir, validator)
	if err != nil {
		t.Fatalf("Failed to create store: %v", err)
	}
	defer store.Close()

	const (
		remotePeer = "16Uiu2HAmGjaPxkWFSXBbmhs9K5x1Zo6euJw95VjS6Jj2bcPpYr2U"
		localPeer  = "12D3KooWKh3diobFtzBk2RvdwR4TuFB8nkU31th8Mc2iKb7bZBWs"
	)

	// The SAME lane, filled by two producers: one record this node published,
	// two that arrived from a peer. SourceBatchProgress would report "3" and
	// lose the distinction entirely.
	mine := sds.NewOMMBuilder().WithNoradCatID(25544).WithObjectName("MINE").Build()
	if _, err := store.StoreWithSourceTags("OMM.fbs", mine, localPeer, nil, SourceTags{
		ProviderID: "celestrak", SourceName: "celestrak-gp", BatchID: "batch-local",
		ProducerPeerID: localPeer, ContentKeyID: "k1",
	}); err != nil {
		t.Fatalf("store local record: %v", err)
	}
	for i, name := range []string{"THEIRS-A", "THEIRS-B"} {
		rec := sds.NewOMMBuilder().WithNoradCatID(uint32(40000 + i)).WithObjectName(name).Build()
		if _, err := store.StoreWithSourceTags("OMM.fbs", rec, remotePeer, nil, SourceTags{
			ProviderID: "celestrak", SourceName: "celestrak-gp", BatchID: "batch-remote",
			ProducerPeerID: remotePeer, ContentKeyID: "k" + name,
		}); err != nil {
			t.Fatalf("store received record %s: %v", name, err)
		}
	}

	rows, err := store.ProducerSourceProgress()
	if err != nil {
		t.Fatalf("ProducerSourceProgress failed: %v", err)
	}

	byProducer := map[string]ProducerSourceProgress{}
	for _, row := range rows {
		if row.SchemaName == "OMM.fbs" && row.SourceName == "celestrak-gp" {
			byProducer[row.ProducerPeerID] = row
		}
	}
	local, ok := byProducer[localPeer]
	if !ok {
		t.Fatalf("this node's own contribution is missing: %+v", rows)
	}
	if local.Count != 1 {
		t.Fatalf("local count = %d, want 1", local.Count)
	}
	remote, ok := byProducer[remotePeer]
	if !ok {
		t.Fatalf("the peer's contribution is missing — a receiving node would report nothing: %+v", rows)
	}
	if remote.Count != 2 {
		t.Fatalf("received count = %d, want 2", remote.Count)
	}
	if remote.LastBatchID != "batch-remote" {
		t.Fatalf("received last batch = %q, want the peer's batch", remote.LastBatchID)
	}
	if remote.TotalBytes <= 0 {
		t.Fatalf("received total bytes = %d, want the real byte total", remote.TotalBytes)
	}
	if remote.LastSeenUnix <= 0 {
		t.Fatalf("received last_seen missing — 'when did data last arrive' is the whole question")
	}
}
